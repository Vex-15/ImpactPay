package verification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/projects"
)

// Sentinel errors.
var (
	ErrNotFound        = errors.New("verification not found")
	ErrNoEvidence      = errors.New("this milestone has no evidence to verify")
	ErrUnknownDecision = errors.New("decision must be APPROVE, HOLD or REQUEST_MORE_EVIDENCE")
)

// Narrator optionally rewrites the summary of a computed verification result.
// It can never change the numbers, the checks or the recommendation — only the
// prose an admin reads.
type Narrator interface {
	Narrate(ctx context.Context, in Input, res models.VerificationResult) (summary, engine, model string)
}

type noopNarrator struct{}

func (noopNarrator) Narrate(_ context.Context, _ Input, res models.VerificationResult) (string, string, string) {
	return res.Summary, "deterministic", "impactpay-verifier-v1"
}

// Service runs and stores verifications.
type Service struct {
	db         *db.DB
	audit      *audit.Service
	evidence   *evidence.Service
	milestones *milestones.Service
	projects   *projects.Service
	narrator   Narrator
}

// New creates the verification service.
func New(database *db.DB, auditSvc *audit.Service, ev *evidence.Service, ms *milestones.Service, pr *projects.Service, narrator Narrator) *Service {
	if narrator == nil {
		narrator = noopNarrator{}
	}
	return &Service{db: database, audit: auditSvc, evidence: ev, milestones: ms, projects: pr, narrator: narrator}
}

// Result pairs a persisted verification with the full structured result.
type Result struct {
	Verification models.Verification       `json:"verification"`
	Result       models.VerificationResult `json:"result"`
}

// VerifyMilestone runs the pipeline over the cumulative evidence for a milestone
// and persists the outcome.
//
// The persisted verification is the *recommendation*. Turning that into a
// financial action is a separate, deterministic policy decision owned by the
// payments service — the AI never releases money on its own.
func (s *Service) VerifyMilestone(ctx context.Context, milestoneID string, actor models.Actor) (*Result, error) {
	m, err := s.milestones.ByID(ctx, milestoneID)
	if err != nil {
		return nil, err
	}
	p, err := s.projects.ByID(ctx, m.ProjectID)
	if err != nil {
		return nil, err
	}
	items, err := s.evidence.ListByMilestone(ctx, milestoneID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNoEvidence
	}

	actorCtx := models.WithActor(ctx, actor)

	// Mark the milestone as being verified and log the start, so the Activity
	// Timeline shows a verification in flight even if it later fails.
	if err := s.milestones.SetStatus(actorCtx, milestoneID, models.MilestoneVerifying, actor, nil); err != nil {
		// A milestone already in VERIFYING is fine; anything else is a real error.
		if !errors.Is(err, milestones.ErrInvalidTransition) {
			return nil, err
		}
	}
	if err := s.audit.RecordNoTx(actorCtx, audit.Event{
		Action:      models.AuditVerificationStarted,
		EntityType:  "milestone",
		EntityID:    milestoneID,
		ProjectID:   p.ID,
		MilestoneID: milestoneID,
		Summary: fmt.Sprintf("AI verification started for milestone %d '%s' over %d evidence artefact(s)",
			m.Ordinal, m.Title, len(items)),
		Data: map[string]any{"engine": "impactpay-verifier-v1", "evidence_count": len(items)},
	}); err != nil {
		return nil, err
	}

	in := Input{Project: p, Milestone: m, Evidence: items}
	res := Evaluate(in)

	summary, engine, model := s.narrator.Narrate(actorCtx, in, res)
	res.Summary = summary
	res.Engine = engine
	res.Model = model

	v := &models.Verification{
		ID:               core.NewID(),
		MilestoneID:      milestoneID,
		ProjectID:        p.ID,
		SubmissionID:     res.SubmissionID,
		Status:           res.Recommendation,
		Recommendation:   res.Recommendation,
		VerifiedQuantity: res.VerifiedQuantity,
		RequiredQuantity: res.RequiredQuantity,
		UnitLabel:        res.UnitLabel,
		Confidence:       res.Confidence,
		Claimed:          res.Claimed,
		EvidenceFound:    res.EvidenceFound,
		Inconsistencies:  res.Inconsistencies,
		MissingEvidence:  res.MissingEvidence,
		Checks:           res.Checks,
		Result:           res,
		Summary:          res.Summary,
		Engine:           engine,
		Model:            model,
		CreatedAt:        core.Now(),
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if err := s.insert(ctx, tx, v); err != nil {
			return err
		}
		if err := s.evidence.SetSubmissionStatus(ctx, tx, res.SubmissionID, submissionStatusFor(res.Recommendation), v.ID); err != nil {
			return err
		}
		if err := s.milestones.SetVerifiedQuantity(ctx, tx, milestoneID, res.VerifiedQuantity); err != nil {
			return err
		}
		if _, err := s.audit.Record(ctx, tx, audit.Event{
			Action:      models.AuditVerificationCompleted,
			EntityType:  "verification",
			EntityID:    v.ID,
			ProjectID:   p.ID,
			MilestoneID: milestoneID,
			Severity:    severityForDecision(res.Recommendation),
			Summary: fmt.Sprintf("AI verification completed: %s — VERIFIED %d/%d %s (confidence %d%%)",
				res.Recommendation, res.VerifiedQuantity, res.RequiredQuantity,
				res.UnitLabel, core.Percent(res.Confidence)),
			Data: map[string]any{
				"recommendation":    string(res.Recommendation),
				"verified_quantity": res.VerifiedQuantity,
				"required_quantity": res.RequiredQuantity,
				"confidence":        res.Confidence,
				"duplicates":        res.DuplicateCount,
				"missing":           len(res.MissingEvidence),
				"inconsistencies":   len(res.Inconsistencies),
				"engine":            engine,
			},
			Actor: &actor,
		}); err != nil {
			return err
		}
		if err := s.applyDecision(ctx, tx, m, res.Recommendation, actor); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// An approval reserves the milestone's budget against captured funding so the
	// payments service can later release exactly what was promised. Reservations
	// are recomputed for both AI and human approvals, so the two paths leave the
	// project's funding counters in the same state.
	if err := s.syncPaymentEligibility(ctx, m, res.Recommendation); err != nil {
		return nil, err
	}

	return &Result{Verification: *v, Result: res}, nil
}

// applyDecision moves the milestone to the status the recommendation implies.
func (s *Service) applyDecision(ctx context.Context, tx db.Executor, m *models.Milestone, decision models.VerificationDecision, actor models.Actor) error {
	var to models.MilestoneStatus
	switch decision {
	case models.DecisionApprove:
		to = models.MilestoneApproved
	case models.DecisionRequestMore:
		to = models.MilestoneMoreEvidenceRequested
	default:
		to = models.MilestoneHeld
	}
	// The milestone may already sit in the target state (for example a repeated
	// HOLD); SetStatusTx treats that as a no-op.
	if err := s.milestones.SetStatusTx(ctx, tx, m.ID, to, actor, map[string]any{
		"trigger": "ai_verification",
	}); err != nil && !errors.Is(err, milestones.ErrInvalidTransition) {
		return err
	}
	return nil
}

// Override records an explicit human decision over an AI recommendation. The AI
// output is preserved unchanged; the override is additive so the audit trail
// shows both what the agent said and what the human decided.
func (s *Service) Override(ctx context.Context, verificationID string, decision models.VerificationDecision, note string, actor models.Actor) (*models.Verification, error) {
	if decision != models.DecisionApprove && decision != models.DecisionHold && decision != models.DecisionRequestMore {
		return nil, ErrUnknownDecision
	}
	if len(note) < 10 {
		return nil, errors.New("a written justification of at least 10 characters is required to override the AI")
	}

	v, err := s.ByID(ctx, verificationID)
	if err != nil {
		return nil, err
	}
	m, err := s.milestones.ByID(ctx, v.MilestoneID)
	if err != nil {
		return nil, err
	}
	now := core.Now()

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE verifications
			SET is_override = 1, human_decision = ?, human_note = ?, decided_by = ?, decided_at = ?
			WHERE id = ?`,
			string(decision), note, actor.ID, core.FormatTime(now), verificationID); err != nil {
			return err
		}

		var to models.MilestoneStatus
		switch decision {
		case models.DecisionApprove:
			to = models.MilestoneApproved
		case models.DecisionRequestMore:
			to = models.MilestoneMoreEvidenceRequested
		default:
			to = models.MilestoneHeld
		}
		if err := s.milestones.SetStatusTx(ctx, tx, m.ID, to, actor, map[string]any{"trigger": "human_override"}); err != nil &&
			!errors.Is(err, milestones.ErrInvalidTransition) {
			return err
		}

		_, err := s.audit.Record(ctx, tx, audit.Event{
			Action:      models.AuditHumanOverride,
			EntityType:  "verification",
			EntityID:    verificationID,
			ProjectID:   v.ProjectID,
			MilestoneID: v.MilestoneID,
			Severity:    "warning",
			Summary:     fmt.Sprintf("Admin overrode AI recommendation %s with %s: %s", v.Recommendation, decision, core.Truncate(note, 160)),
			Data: map[string]any{
				"ai_recommendation": string(v.Recommendation),
				"human_decision":    string(decision),
				"note":              note,
			},
			Actor: &actor,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.syncPaymentEligibility(ctx, m, decision); err != nil {
		return nil, err
	}
	return s.ByID(ctx, verificationID)
}

// syncPaymentEligibility recalculates whether the milestone's funding may be
// released after a decision. It never executes a payment.
//
// When a milestone becomes APPROVED its budget is reserved against the project's
// captured funding, which is what later lets the payments service release it.
func (s *Service) syncPaymentEligibility(ctx context.Context, m *models.Milestone, decision models.VerificationDecision) error {
	if decision != models.DecisionApprove {
		return nil
	}
	p, err := s.projects.ByID(ctx, m.ProjectID)
	if err != nil {
		return err
	}
	if p.ReservedAmount >= m.BudgetAmount {
		return nil
	}
	amount := m.BudgetAmount
	if available := p.FundedAmount - p.ReservedAmount; available < amount {
		amount = available
	}
	if amount <= 0 {
		return nil
	}
	return s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if err := s.projects.Reserve(ctx, tx, p.ID, amount); err != nil {
			return err
		}
		_, err := s.audit.Record(ctx, tx, audit.Event{
			Action:      models.AuditProjectUpdated,
			EntityType:  "project",
			EntityID:    p.ID,
			ProjectID:   p.ID,
			MilestoneID: m.ID,
			Summary: fmt.Sprintf("Milestone %d '%s' approved; %s reserved against captured funding",
				m.Ordinal, m.Title, core.FormatMoney(core.Money(amount), p.Currency)),
			Data: map[string]any{"reserved_minor": amount, "milestone_id": m.ID},
		})
		return err
	})
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// ByID loads a verification.
func (s *Service) ByID(ctx context.Context, id string) (*models.Verification, error) {
	return s.scan(s.db.QueryRowContext(ctx, verificationSelect+" WHERE id = ?", id))
}

// LatestForMilestone returns the most recent verification, or nil.
func (s *Service) LatestForMilestone(ctx context.Context, milestoneID string) (*models.Verification, error) {
	v, err := s.scan(s.db.QueryRowContext(ctx,
		verificationSelect+" WHERE milestone_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", milestoneID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return v, nil
}

// ListForMilestone returns a milestone's verification history, newest first.
func (s *Service) ListForMilestone(ctx context.Context, milestoneID string) ([]models.Verification, error) {
	rows, err := s.db.QueryContext(ctx,
		verificationSelect+" WHERE milestone_id = ? ORDER BY created_at DESC, id DESC", milestoneID)
	if err != nil {
		return nil, fmt.Errorf("verification: list for milestone: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// ListForProject returns every verification attached to a project.
func (s *Service) ListForProject(ctx context.Context, projectID string, limit int) ([]models.Verification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		verificationSelect+" WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?", projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("verification: list for project: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// QueueItem is a milestone awaiting human review in the admin workspace.
type QueueItem struct {
	Verification models.Verification `json:"verification"`
	Milestone    models.Milestone    `json:"milestone"`
	Project      models.Project      `json:"project"`
	OrgName      string              `json:"org_name"`
}

// Queue returns non-approving verifications for the admin verification
// workspace, newest first.
func (s *Service) Queue(ctx context.Context, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.id,
		       m.id, m.project_id, m.ordinal, m.title, m.description, m.requirement,
		       m.unit_label, m.required_quantity, m.budget_amount, m.evidence_requirements,
		       m.status, m.verified_quantity, m.created_at, m.updated_at,
		       m.started_at, m.submitted_at, m.decided_at, m.completed_at,
		       p.id, p.org_id, p.title, p.slug, p.category, p.summary, p.problem, p.approach,
		       p.location, p.currency, p.goal_amount, p.funded_amount, p.reserved_amount,
		       p.released_amount, p.beneficiary_count, p.beneficiary_description,
		       p.outcome_description, p.status, p.cover_seed, p.created_by,
		       p.created_at, p.updated_at, p.published_at, p.completed_at,
		       o.name
		FROM verifications v
		JOIN milestones m ON m.id = v.milestone_id
		JOIN projects p ON p.id = v.project_id
		JOIN organizations o ON o.id = p.org_id
		WHERE v.status <> ? AND v.is_override = 0
		ORDER BY v.created_at DESC LIMIT ?`, string(models.DecisionApprove), limit)
	if err != nil {
		return nil, fmt.Errorf("verification: queue: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []QueueItem{}
	for rows.Next() {
		var (
			item QueueItem
			ev   string
			ms   models.Milestone
			pr   models.Project
		)
		if err := rows.Scan(
			&item.Verification.ID,
			&ms.ID, &ms.ProjectID, &ms.Ordinal, &ms.Title, &ms.Description, &ms.Requirement,
			&ms.UnitLabel, &ms.RequiredQuantity, &ms.BudgetAmount, &ev,
			&ms.Status, &ms.VerifiedQuantity, &ms.CreatedAt, &ms.UpdatedAt,
			&ms.StartedAt, &ms.SubmittedAt, &ms.DecidedAt, &ms.CompletedAt,
			&pr.ID, &pr.OrgID, &pr.Title, &pr.Slug, &pr.Category, &pr.Summary, &pr.Problem, &pr.Approach,
			&pr.Location, &pr.Currency, &pr.GoalAmount, &pr.FundedAmount, &pr.ReservedAmount,
			&pr.ReleasedAmount, &pr.BeneficiaryCount, &pr.BeneficiaryDescription,
			&pr.OutcomeDescription, &pr.Status, &pr.CoverSeed, &pr.CreatedBy,
			&pr.CreatedAt, &pr.UpdatedAt, &pr.PublishedAt, &pr.CompletedAt,
			&item.OrgName,
		); err != nil {
			return nil, err
		}
		core.DecodeJSON(ev, &ms.EvidenceRequirements)
		item.Milestone = ms
		item.Project = pr
		if err := s.fillVerification(ctx, &item.Verification, item.Verification.ID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// persistence helpers
// ---------------------------------------------------------------------------

func (s *Service) insert(ctx context.Context, ex db.Executor, v *models.Verification) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO verifications (
			id, milestone_id, project_id, submission_id, status, recommendation,
			verified_quantity, required_quantity, unit_label, confidence, claimed,
			evidence_found, inconsistencies, missing_evidence, checks, result, summary,
			engine, model, is_override, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		v.ID, v.MilestoneID, v.ProjectID, v.SubmissionID, string(v.Status),
		string(v.Recommendation), v.VerifiedQuantity, v.RequiredQuantity, v.UnitLabel,
		v.Confidence, core.JSONString(v.Claimed), core.JSONString(v.EvidenceFound),
		core.JSONString(v.Inconsistencies), core.JSONString(v.MissingEvidence),
		core.JSONString(v.Checks), core.JSONString(v.Result), core.Truncate(v.Summary, 2000),
		v.Engine, v.Model, core.FormatTime(v.CreatedAt))
	if err != nil {
		return fmt.Errorf("verification: insert: %w", err)
	}
	return nil
}

const verificationSelect = `SELECT id, milestone_id, project_id, submission_id, status,
	recommendation, verified_quantity, required_quantity, unit_label, confidence,
	claimed, evidence_found, inconsistencies, missing_evidence, checks, result,
	summary, engine, model, is_override, human_decision, human_note, decided_by,
	decided_at, created_at FROM verifications`

type rowScanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row rowScanner) (*models.Verification, error) {
	v, err := s.scanRows(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return v, nil
}

func (s *Service) scanRows(row rowScanner) (*models.Verification, error) {
	v, err := scanVerificationColumns(row)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func scanVerificationColumns(row rowScanner) (*models.Verification, error) {
	var (
		v                                                         models.Verification
		status, recommendation                                    string
		claimed, found, inc, missing, checks, resultJSON, summary string
		isOverride                                                int
		// human_decision, decided_by and decided_at are NULL until a human
		// override happens, so every one of them is scanned as a nullable column.
		humanDecision sql.NullString
		humanNote     sql.NullString
		decidedBy     sql.NullString
		decidedAt     sql.NullString
		createdAt     string
	)
	if err := row.Scan(
		&v.ID, &v.MilestoneID, &v.ProjectID, &v.SubmissionID, &status, &recommendation,
		&v.VerifiedQuantity, &v.RequiredQuantity, &v.UnitLabel, &v.Confidence, &claimed,
		&found, &inc, &missing, &checks, &resultJSON, &summary, &v.Engine, &v.Model,
		&isOverride, &humanDecision, &humanNote, &decidedBy, &decidedAt, &createdAt,
	); err != nil {
		return nil, err
	}
	v.HumanNote = humanNote.String
	v.DecidedBy = decidedBy.String
	v.Status = models.VerificationDecision(status)
	v.Recommendation = models.VerificationDecision(recommendation)
	v.Summary = summary
	v.IsOverride = isOverride == 1
	v.HumanDecision = humanDecision.String
	if decidedAt.Valid && decidedAt.String != "" {
		t := core.MustParseTime(decidedAt.String)
		v.DecidedAt = &t
	}
	v.CreatedAt = core.MustParseTime(createdAt)

	v.Claimed = []models.Claim{}
	v.EvidenceFound = []models.EvidenceFinding{}
	v.Inconsistencies = []models.Inconsistency{}
	v.MissingEvidence = []models.MissingEvidence{}
	v.Checks = []models.VerificationCheck{}
	core.DecodeJSON(claimed, &v.Claimed)
	core.DecodeJSON(found, &v.EvidenceFound)
	core.DecodeJSON(inc, &v.Inconsistencies)
	core.DecodeJSON(missing, &v.MissingEvidence)
	core.DecodeJSON(checks, &v.Checks)
	core.DecodeJSON(resultJSON, &v.Result)
	return &v, nil
}

func (s *Service) scanAll(rows *sql.Rows) ([]models.Verification, error) {
	out := []models.Verification{}
	for rows.Next() {
		v, err := s.scanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (s *Service) fillVerification(ctx context.Context, dst *models.Verification, id string) error {
	full, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	*dst = *full
	return nil
}

func submissionStatusFor(d models.VerificationDecision) models.SubmissionStatus {
	switch d {
	case models.DecisionApprove:
		return models.SubmissionVerified
	case models.DecisionRequestMore:
		return models.SubmissionMoreEvidenceRequested
	default:
		return models.SubmissionHeld
	}
}

func severityForDecision(d models.VerificationDecision) string {
	switch d {
	case models.DecisionApprove:
		return "info"
	case models.DecisionRequestMore:
		return "warning"
	default:
		return "warning"
	}
}
