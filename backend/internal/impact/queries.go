package impact

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// ErrNotFound is returned when a referenced row does not exist.
var ErrNotFound = errors.New("not found")

func (s *Service) donation(ctx context.Context, id string) (*models.Donation, error) {
	var (
		d                      models.Donation
		status                 string
		createdAt, updatedAt   string
		capturedAt, refundedAt sql.NullString
		simulated              int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, donor_id, amount, currency, status, paypal_order_id,
		       paypal_capture_id, capture_status, provider, simulated, note,
		       created_at, updated_at, captured_at, refunded_at
		FROM donations WHERE id = ?`, id).
		Scan(&d.ID, &d.ProjectID, &d.DonorID, &d.Amount, &d.Currency, &status,
			&d.PayPalOrderID, &d.PayPalCaptureID, &d.CaptureStatus, &d.Provider,
			&simulated, &d.Note, &createdAt, &updatedAt, &capturedAt, &refundedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("contribution %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	d.Status = models.DonationStatus(status)
	d.Simulated = simulated == 1
	d.CreatedAt = core.MustParseTime(createdAt)
	d.UpdatedAt = core.MustParseTime(updatedAt)
	if capturedAt.Valid {
		t := core.MustParseTime(capturedAt.String)
		d.CapturedAt = &t
	}
	if refundedAt.Valid {
		t := core.MustParseTime(refundedAt.String)
		d.RefundedAt = &t
	}
	return &d, nil
}

func (s *Service) project(ctx context.Context, id string) (*models.Project, error) {
	var (
		p                        models.Project
		status                   string
		createdAt, updatedAt     string
		publishedAt, completedAt sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, org_id, title, slug, category, summary, problem, approach, location,
		       currency, goal_amount, funded_amount, reserved_amount, released_amount,
		       beneficiary_count, beneficiary_description, outcome_description, status,
		       cover_seed, created_by, created_at, updated_at, published_at, completed_at
		FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.OrgID, &p.Title, &p.Slug, &p.Category, &p.Summary, &p.Problem,
			&p.Approach, &p.Location, &p.Currency, &p.GoalAmount, &p.FundedAmount,
			&p.ReservedAmount, &p.ReleasedAmount, &p.BeneficiaryCount,
			&p.BeneficiaryDescription, &p.OutcomeDescription, &status, &p.CoverSeed,
			&p.CreatedBy, &createdAt, &updatedAt, &publishedAt, &completedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("project %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	p.Status = models.ProjectStatus(status)
	p.CreatedAt = core.MustParseTime(createdAt)
	p.UpdatedAt = core.MustParseTime(updatedAt)
	if publishedAt.Valid {
		t := core.MustParseTime(publishedAt.String)
		p.PublishedAt = &t
	}
	if completedAt.Valid {
		t := core.MustParseTime(completedAt.String)
		p.CompletedAt = &t
	}
	return &p, nil
}

func (s *Service) orgSummary(ctx context.Context, id string) (*OrgSummary, error) {
	var (
		o        OrgSummary
		verified int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, verified, country FROM organizations WHERE id = ?`, id).
		Scan(&o.ID, &o.Name, &o.Slug, &verified, &o.Country)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("organization %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	o.Verified = verified == 1
	return &o, nil
}

// fundingsWithMilestones returns a donation's attribution rows joined to their
// milestones, in milestone order.
func (s *Service) fundingsWithMilestones(ctx context.Context, donationID string) ([]struct {
	Funding   models.MilestoneFunding
	Milestone models.Milestone
}, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.milestone_id, f.project_id, f.donation_id, f.amount, f.created_at,
		       m.id, m.project_id, m.ordinal, m.title, m.description, m.requirement,
		       m.unit_label, m.required_quantity, m.budget_amount, m.evidence_requirements,
		       m.status, m.verified_quantity, m.created_at, m.updated_at
		FROM milestone_fundings f
		JOIN milestones m ON m.id = f.milestone_id
		WHERE f.donation_id = ?
		ORDER BY m.ordinal ASC`, donationID)
	if err != nil {
		return nil, fmt.Errorf("impact: fundings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []struct {
		Funding   models.MilestoneFunding
		Milestone models.Milestone
	}{}
	for rows.Next() {
		var (
			entry struct {
				Funding   models.MilestoneFunding
				Milestone models.Milestone
			}
			createdAt            string
			ev                   string
			msStatus             string
			msCreated, msUpdated string
		)
		if err := rows.Scan(
			&entry.Funding.ID, &entry.Funding.MilestoneID, &entry.Funding.ProjectID,
			&entry.Funding.DonationID, &entry.Funding.Amount, &createdAt,
			&entry.Milestone.ID, &entry.Milestone.ProjectID, &entry.Milestone.Ordinal,
			&entry.Milestone.Title, &entry.Milestone.Description, &entry.Milestone.Requirement,
			&entry.Milestone.UnitLabel, &entry.Milestone.RequiredQuantity, &entry.Milestone.BudgetAmount,
			&ev, &msStatus, &entry.Milestone.VerifiedQuantity, &msCreated, &msUpdated,
		); err != nil {
			return nil, err
		}
		entry.Funding.CreatedAt = core.MustParseTime(createdAt)
		entry.Milestone.Status = models.MilestoneStatus(msStatus)
		entry.Milestone.CreatedAt = core.MustParseTime(msCreated)
		entry.Milestone.UpdatedAt = core.MustParseTime(msUpdated)
		entry.Milestone.EvidenceRequirements = []models.EvidenceRequirementSpec{}
		core.DecodeJSON(ev, &entry.Milestone.EvidenceRequirements)
		out = append(out, entry)
	}
	return out, rows.Err()
}

// evidenceRefs loads the inspectable evidence attached to a milestone.
func (s *Service) evidenceRefs(ctx context.Context, milestoneID string) ([]EvidenceRef, int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, filename, kind, description, size_bytes, mime_type, extracted, created_at
		FROM evidence WHERE milestone_id = ?
		ORDER BY created_at ASC, id ASC`, milestoneID)
	if err != nil {
		return nil, 0, fmt.Errorf("impact: evidence refs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	refs := []EvidenceRef{}
	total := 0
	for rows.Next() {
		var (
			r         EvidenceRef
			extracted string
			createdAt string
		)
		if err := rows.Scan(&r.ID, &r.Filename, &r.Kind, &r.Description, &r.SizeBytes,
			&r.MimeType, &extracted, &createdAt); err != nil {
			return nil, 0, err
		}
		var facts models.ExtractedFacts
		core.DecodeJSON(extracted, &facts)
		r.UnitIDs = facts.UnitIDs
		r.AmountMinor = facts.AmountMinor
		r.Vendor = facts.Vendor
		r.InvoiceNo = facts.InvoiceNumber
		r.Location = facts.Location
		r.Quality = facts.Quality
		r.CreatedAt = core.MustParseTime(createdAt).Format("2 Jan 2006")
		r.URL = "/api/evidence/" + r.ID + "/file"
		refs = append(refs, r)
		total++
	}
	return refs, total, rows.Err()
}

func (s *Service) latestVerification(ctx context.Context, milestoneID string) (*models.Verification, error) {
	var (
		v                      models.Verification
		status, recommendation string
		claimed, found, inc    string
		missing, checks        string
		resultJSON, summary    string
		isOverride             int
		// NULL until a human override happens, so all three are nullable scans.
		humanDecision sql.NullString
		humanNote     sql.NullString
		decidedBy     sql.NullString
		decidedAt     sql.NullString
		createdAt     string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, milestone_id, project_id, submission_id, status, recommendation,
		       verified_quantity, required_quantity, unit_label, confidence, claimed,
		       evidence_found, inconsistencies, missing_evidence, checks, result,
		       summary, engine, model, is_override, human_decision, human_note,
		       decided_by, decided_at, created_at
		FROM verifications WHERE milestone_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, milestoneID).
		Scan(&v.ID, &v.MilestoneID, &v.ProjectID, &v.SubmissionID, &status, &recommendation,
			&v.VerifiedQuantity, &v.RequiredQuantity, &v.UnitLabel, &v.Confidence, &claimed,
			&found, &inc, &missing, &checks, &resultJSON, &summary, &v.Engine, &v.Model,
			&isOverride, &humanDecision, &humanNote, &decidedBy, &decidedAt, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("impact: latest verification: %w", err)
	}
	v.HumanNote = humanNote.String
	v.DecidedBy = decidedBy.String
	v.Status = models.VerificationDecision(status)
	v.Recommendation = models.VerificationDecision(recommendation)
	v.Summary = summary
	v.IsOverride = isOverride == 1
	v.HumanDecision = humanDecision.String
	if decidedAt.Valid {
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

// MilestoneEvidenceStat reports, for one milestone, how much of its required
// evidence has actually been supplied. Impact Pay uses this to publish an
// artefact-level completeness figure instead of a vague trust score.
type MilestoneEvidenceStat struct {
	MilestoneID       string
	RequiredArtefacts int
	DistinctArtefacts int
	Duplicates        int
}

// Completeness returns the fraction of required artefacts supplied, capped at 1.
func (s MilestoneEvidenceStat) Completeness() float64 {
	if s.RequiredArtefacts <= 0 {
		return 1
	}
	return core.Clamp(core.Ratio(float64(s.DistinctArtefacts), float64(s.RequiredArtefacts)), 0, 1)
}

// evidenceStatsForProject returns per-milestone evidence statistics. Duplicate
// artefacts are counted separately and excluded from the distinct total, so
// re-uploading the same file cannot inflate completeness.
func (s *Service) evidenceStatsForProject(ctx context.Context, projectID string) (map[string]MilestoneEvidenceStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.evidence_requirements,
		       COALESCE(ev.total, 0), COALESCE(ev.distinct_hashes, 0)
		FROM milestones m
		LEFT JOIN (
		    SELECT milestone_id, COUNT(*) AS total, COUNT(DISTINCT content_hash) AS distinct_hashes
		    FROM evidence GROUP BY milestone_id
		) ev ON ev.milestone_id = m.id
		WHERE m.project_id = ?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("impact: evidence stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]MilestoneEvidenceStat{}
	for rows.Next() {
		var (
			id                    string
			reqJSON               string
			total, distinctHashes int
		)
		if err := rows.Scan(&id, &reqJSON, &total, &distinctHashes); err != nil {
			return nil, err
		}
		reqs := []models.EvidenceRequirementSpec{}
		core.DecodeJSON(reqJSON, &reqs)
		stat := MilestoneEvidenceStat{MilestoneID: id}
		for _, r := range reqs {
			if !r.Required {
				continue
			}
			n := r.MinCount
			if n <= 0 {
				n = 1
			}
			stat.RequiredArtefacts += n
		}
		stat.DistinctArtefacts = distinctHashes
		stat.Duplicates = total - distinctHashes
		out[id] = stat
	}
	return out, rows.Err()
}

// countInconsistenciesForProject counts un-resolved findings in the latest
// verification of every milestone in a project.
func (s *Service) countInconsistenciesForProject(ctx context.Context, projectID string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.inconsistencies
		FROM milestones m
		JOIN verifications v ON v.id = (
			SELECT id FROM verifications
			WHERE milestone_id = m.id
			ORDER BY created_at DESC, id DESC LIMIT 1
		)
		WHERE m.project_id = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("impact: inconsistencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	n := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		items := []models.Inconsistency{}
		core.DecodeJSON(raw, &items)
		for _, i := range items {
			if i.Severity != "info" {
				n++
			}
		}
	}
	return n, rows.Err()
}

func joinAnd(parts []string) string { return strings.Join(parts, " AND ") }
