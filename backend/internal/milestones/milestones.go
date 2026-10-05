// Package milestones owns the units of work that funding is tied to: their
// definitions, their lifecycle transitions and their progress summaries.
//
// A milestone moves through PENDING -> IN_PROGRESS -> EVIDENCE_SUBMITTED ->
// VERIFYING -> APPROVED/HELD/MORE_EVIDENCE_REQUESTED -> PAYMENT_RELEASED ->
// COMPLETED. Transitions are only ever driven by this package so the workflow
// stays inspectable in one place.
package milestones

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
)

// Sentinel errors.
var (
	ErrNotFound          = errors.New("milestone not found")
	ErrTitleRequired     = errors.New("milestone title is required")
	ErrBudgetRequired    = errors.New("milestone budget must be greater than zero")
	ErrInvalidTransition = errors.New("that milestone status transition is not allowed")
)

// Service provides milestone operations.
type Service struct {
	db    *db.DB
	audit *audit.Service
}

// New creates the milestones service.
func New(database *db.DB, auditSvc *audit.Service) *Service {
	return &Service{db: database, audit: auditSvc}
}

// Input describes a milestone created by the project wizard.
type Input struct {
	Title                string
	Description          string
	Requirement          string
	UnitLabel            string
	RequiredQuantity     int
	BudgetAmount         int64
	EvidenceRequirements []models.EvidenceRequirementSpec
}

// Validate checks the milestone definition.
func (in *Input) Validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return ErrTitleRequired
	}
	if in.BudgetAmount <= 0 {
		return ErrBudgetRequired
	}
	if in.UnitLabel == "" {
		in.UnitLabel = "unit"
	}
	if in.RequiredQuantity < 0 {
		in.RequiredQuantity = 0
	}
	for i := range in.EvidenceRequirements {
		r := &in.EvidenceRequirements[i]
		r.Label = strings.TrimSpace(r.Label)
		if r.Key == "" {
			r.Key = core.Slugify(r.Label)
		}
		if r.Key == "" {
			r.Key = fmt.Sprintf("requirement-%d", i+1)
		}
		if r.MinCount <= 0 {
			r.MinCount = 1
		}
		if r.Kind == "" {
			r.Kind = string(models.EvidenceDocument)
		}
	}
	return nil
}

// Create inserts a milestone inside an existing transaction.
func (s *Service) Create(ctx context.Context, ex db.Executor, projectID string, ordinal int, in Input) (*models.Milestone, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	m := &models.Milestone{
		ID:                   core.NewID(),
		ProjectID:            projectID,
		Ordinal:              ordinal,
		Title:                in.Title,
		Description:          strings.TrimSpace(in.Description),
		Requirement:          strings.TrimSpace(in.Requirement),
		UnitLabel:            in.UnitLabel,
		RequiredQuantity:     in.RequiredQuantity,
		BudgetAmount:         in.BudgetAmount,
		EvidenceRequirements: in.EvidenceRequirements,
		Status:               models.MilestonePending,
		CreatedAt:            core.Now(),
		UpdatedAt:            core.Now(),
	}
	if m.EvidenceRequirements == nil {
		m.EvidenceRequirements = []models.EvidenceRequirementSpec{}
	}

	_, err := ex.ExecContext(ctx, `
		INSERT INTO milestones (
			id, project_id, ordinal, title, description, requirement, unit_label,
			required_quantity, budget_amount, evidence_requirements, status,
			verified_quantity, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		m.ID, m.ProjectID, m.Ordinal, m.Title, m.Description, m.Requirement,
		m.UnitLabel, m.RequiredQuantity, m.BudgetAmount,
		core.JSONString(m.EvidenceRequirements), string(m.Status),
		core.FormatTime(m.CreatedAt), core.FormatTime(m.UpdatedAt))
	if err != nil {
		return nil, fmt.Errorf("milestones: create: %w", err)
	}
	return m, nil
}

// CreateMany inserts an ordered set of milestones.
func (s *Service) CreateMany(ctx context.Context, ex db.Executor, projectID string, inputs []Input) ([]models.Milestone, error) {
	out := make([]models.Milestone, 0, len(inputs))
	for i := range inputs {
		m, err := s.Create(ctx, ex, projectID, i+1, inputs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, nil
}

// ByID loads a milestone.
func (s *Service) ByID(ctx context.Context, id string) (*models.Milestone, error) {
	return s.scan(s.db.QueryRowContext(ctx, milestoneSelect+" WHERE id = ?", id))
}

// ByIDTx loads a milestone inside a transaction, for consistent reads before a
// financial decision.
func (s *Service) ByIDTx(ctx context.Context, ex db.Executor, id string) (*models.Milestone, error) {
	return s.scan(ex.QueryRowContext(ctx, milestoneSelect+" WHERE id = ?", id))
}

// ListByProject returns a project's milestones in order.
func (s *Service) ListByProject(ctx context.Context, projectID string) ([]models.Milestone, error) {
	rows, err := s.db.QueryContext(ctx, milestoneSelect+" WHERE project_id = ? ORDER BY ordinal ASC", projectID)
	if err != nil {
		return nil, fmt.Errorf("milestones: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// ListByProjectTx is the transactional variant used during verification.
func (s *Service) ListByProjectTx(ctx context.Context, ex db.Executor, projectID string) ([]models.Milestone, error) {
	rows, err := ex.QueryContext(ctx, milestoneSelect+" WHERE project_id = ? ORDER BY ordinal ASC", projectID)
	if err != nil {
		return nil, fmt.Errorf("milestones: list tx: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// NextIncomplete returns the lowest-ordered milestone that has not completed.
func (s *Service) NextIncomplete(ctx context.Context, ex db.Executor, projectID string) (*models.Milestone, error) {
	ms, err := s.ListByProjectTx(ctx, ex, projectID)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		if !ms[i].Status.Terminal() {
			return &ms[i], nil
		}
	}
	return nil, ErrNotFound
}

// SetStatus applies a lifecycle transition, enforcing which moves are legal.
func (s *Service) SetStatus(ctx context.Context, milestoneID string, to models.MilestoneStatus, actor models.Actor, extra map[string]any) error {
	return s.db.RunInTx(ctx, func(tx *db.Tx) error {
		return s.SetStatusTx(ctx, tx, milestoneID, to, actor, extra)
	})
}

// SetStatusTx applies a lifecycle transition inside a caller-owned transaction.
func (s *Service) SetStatusTx(ctx context.Context, tx db.Executor, milestoneID string, to models.MilestoneStatus, actor models.Actor, extra map[string]any) error {
	current, err := s.ByIDTx(ctx, tx, milestoneID)
	if err != nil {
		return err
	}
	if !allowedTransition(current.Status, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, to)
	}

	now := core.Now()
	sets := []string{"status = ?", "updated_at = ?"}
	args := []any{string(to), core.FormatTime(now)}

	switch to {
	case models.MilestoneInProgress:
		sets = append(sets, "started_at = COALESCE(started_at, ?)")
		args = append(args, core.FormatTime(now))
	case models.MilestoneEvidenceSubmitted, models.MilestoneVerifying:
		sets = append(sets, "submitted_at = COALESCE(submitted_at, ?)")
		args = append(args, core.FormatTime(now))
	case models.MilestoneApproved, models.MilestoneHeld, models.MilestoneMoreEvidenceRequested:
		sets = append(sets, "decided_at = ?")
		args = append(args, core.FormatTime(now))
	case models.MilestoneCompleted:
		sets = append(sets, "completed_at = ?")
		args = append(args, core.FormatTime(now))
	}
	args = append(args, milestoneID)

	if _, err := tx.ExecContext(ctx,
		"UPDATE milestones SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		return fmt.Errorf("milestones: set status: %w", err)
	}

	action, severity, summary := transitionAudit(current, to)
	if action != "" {
		data := map[string]any{"from": string(current.Status), "to": string(to), "ordinal": current.Ordinal}
		for k, v := range extra {
			data[k] = v
		}
		var a *models.Actor
		if actor.ID != "" || actor.Role != "" {
			a = &actor
		}
		if _, err := s.audit.Record(ctx, tx, audit.Event{
			Action:      action,
			EntityType:  "milestone",
			EntityID:    milestoneID,
			ProjectID:   current.ProjectID,
			MilestoneID: milestoneID,
			Severity:    severity,
			Summary:     summary,
			Data:        data,
			Actor:       a,
		}); err != nil {
			return err
		}
	}
	return nil
}

// SetVerifiedQuantity updates the verified unit count for a milestone.
func (s *Service) SetVerifiedQuantity(ctx context.Context, ex db.Executor, milestoneID string, quantity int) error {
	if quantity < 0 {
		quantity = 0
	}
	_, err := ex.ExecContext(ctx,
		`UPDATE milestones SET verified_quantity = ?, updated_at = ? WHERE id = ?`,
		quantity, core.NowStr(), milestoneID)
	if err != nil {
		return fmt.Errorf("milestones: set verified quantity: %w", err)
	}
	return nil
}

// Progress is a compact milestone summary used by project cards and dashboards.
type Progress struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	Verified   int `json:"verified"`
	InProgress int `json:"in_progress"`
	Held       int `json:"held"`
	Approved   int `json:"approved"`
	Released   int `json:"released"`
}

// ProgressFor aggregates milestone states for a project.
func (s *Service) ProgressFor(ctx context.Context, projectID string) (Progress, error) {
	ms, err := s.ListByProject(ctx, projectID)
	if err != nil {
		return Progress{}, err
	}
	return Summarize(ms), nil
}

// Summarize collapses a milestone list into counts.
func Summarize(ms []models.Milestone) Progress {
	var p Progress
	p.Total = len(ms)
	for _, m := range ms {
		switch m.Status {
		case models.MilestoneCompleted, models.MilestonePaymentReleased:
			p.Completed++
		case models.MilestoneApproved:
			p.Approved++
		case models.MilestoneInProgress, models.MilestoneEvidenceSubmitted, models.MilestoneVerifying:
			p.InProgress++
		case models.MilestoneHeld, models.MilestoneMoreEvidenceRequested:
			p.Held++
		}
		if m.FullyVerified() {
			p.Verified++
		}
	}
	return p
}

func allowedTransition(from, to models.MilestoneStatus) bool {
	if from == to {
		return true
	}
	switch to {
	case models.MilestoneInProgress:
		return from == models.MilestonePending
	case models.MilestoneEvidenceSubmitted:
		return from == models.MilestoneInProgress ||
			from == models.MilestoneMoreEvidenceRequested ||
			from == models.MilestoneHeld
	case models.MilestoneVerifying:
		return from == models.MilestoneEvidenceSubmitted || from == models.MilestoneInProgress
	case models.MilestoneApproved:
		return from == models.MilestoneVerifying || from == models.MilestoneEvidenceSubmitted ||
			from == models.MilestoneHeld || from == models.MilestoneMoreEvidenceRequested ||
			from == models.MilestoneInProgress
	case models.MilestoneHeld, models.MilestoneMoreEvidenceRequested:
		return from == models.MilestoneVerifying || from == models.MilestoneEvidenceSubmitted ||
			from == models.MilestoneInProgress
	case models.MilestonePaymentReleased:
		return from == models.MilestoneApproved
	case models.MilestoneCompleted:
		return from == models.MilestonePaymentReleased || from == models.MilestoneApproved
	case models.MilestoneRefunded:
		return from == models.MilestoneHeld || from == models.MilestoneMoreEvidenceRequested ||
			from == models.MilestoneInProgress || from == models.MilestoneEvidenceSubmitted
	case models.MilestonePending:
		return false
	}
	return false
}

func transitionAudit(m *models.Milestone, to models.MilestoneStatus) (models.AuditAction, string, string) {
	label := fmt.Sprintf("Milestone %d '%s'", m.Ordinal, m.Title)
	switch to {
	case models.MilestoneInProgress:
		return models.AuditMilestoneStarted, "info", label + " started"
	case models.MilestoneVerifying:
		return models.AuditVerificationStarted, "info", label + " submitted for verification"
	case models.MilestoneApproved:
		return models.AuditMilestoneApproved, "info", label + " approved"
	case models.MilestoneHeld:
		return models.AuditMilestoneHeld, "warning", label + " held"
	case models.MilestoneMoreEvidenceRequested:
		return models.AuditMoreEvidenceRequested, "warning", label + ": more evidence requested"
	case models.MilestonePaymentReleased:
		return models.AuditPaymentActionExecuted, "info", label + " funding released"
	case models.MilestoneCompleted:
		return models.AuditMilestoneCompleted, "info", label + " completed"
	}
	return "", "info", ""
}

const milestoneSelect = `SELECT id, project_id, ordinal, title, description, requirement,
	unit_label, required_quantity, budget_amount, evidence_requirements, status,
	verified_quantity, created_at, updated_at, started_at, submitted_at, decided_at,
	completed_at FROM milestones`

func (s *Service) scan(row rowScanner) (*models.Milestone, error) {
	m, err := s.scanRows(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *Service) scanRows(row rowScanner) (*models.Milestone, error) {
	var (
		m                                          models.Milestone
		evidenceJSON, status                       string
		createdAt, updatedAt                       string
		startedAt, submittedAt, decidedAt, doneAt  sql.NullString
	)
	if err := row.Scan(
		&m.ID, &m.ProjectID, &m.Ordinal, &m.Title, &m.Description, &m.Requirement,
		&m.UnitLabel, &m.RequiredQuantity, &m.BudgetAmount, &evidenceJSON, &status,
		&m.VerifiedQuantity, &createdAt, &updatedAt, &startedAt, &submittedAt,
		&decidedAt, &doneAt,
	); err != nil {
		return nil, err
	}
	m.Status = models.MilestoneStatus(status)
	m.EvidenceRequirements = []models.EvidenceRequirementSpec{}
	core.DecodeJSON(evidenceJSON, &m.EvidenceRequirements)
	if m.EvidenceRequirements == nil {
		m.EvidenceRequirements = []models.EvidenceRequirementSpec{}
	}
	m.CreatedAt = core.MustParseTime(createdAt)
	m.UpdatedAt = core.MustParseTime(updatedAt)
	m.StartedAt = nullTime(startedAt)
	m.SubmittedAt = nullTime(submittedAt)
	m.DecidedAt = nullTime(decidedAt)
	m.CompletedAt = nullTime(doneAt)
	return &m, nil
}

func (s *Service) scanAll(rows *sql.Rows) ([]models.Milestone, error) {
	out := make([]models.Milestone, 0, 8)
	for rows.Next() {
		m, err := s.scanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func nullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := core.MustParseTime(ns.String)
	return &t
}
