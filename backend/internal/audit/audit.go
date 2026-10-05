// Package audit writes and reads ImpactPay's append-only event log.
//
// Audit rows are never updated or deleted. A correction is a new event, which is
// what makes the Activity Timeline trustworthy: the same record that a user sees
// is the record the platform acted on, written in the same database transaction
// as the state change it describes.
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
)

// Service records and queries audit events.
type Service struct {
	db *db.DB
}

// New creates the audit service.
func New(database *db.DB) *Service { return &Service{db: database} }

// Event is the input for a recorded audit event.
type Event struct {
	Action      models.AuditAction
	EntityType  string
	EntityID    string
	ProjectID   string
	MilestoneID string
	DonationID  string
	Severity    string // info | warning | critical
	Summary     string
	Data        any
	Actor       *models.Actor // defaults to the context actor
}

// Record appends an audit event using the supplied executor. Pass the active
// transaction so the event commits atomically with the mutation it describes.
func (s *Service) Record(ctx context.Context, ex db.Executor, e Event) (string, error) {
	actor := models.ActorFrom(ctx)
	if e.Actor != nil {
		actor = *e.Actor
	}
	severity := e.Severity
	if severity == "" {
		severity = "info"
	}
	id := core.NewID()

	_, err := ex.ExecContext(ctx, `
		INSERT INTO audit_events (
			id, actor_id, actor_role, actor_label, action, entity_type, entity_id,
			project_id, milestone_id, donation_id, severity, summary, data, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, actor.ID, actor.AuditRole(), actor.AuditLabel(), string(e.Action),
		e.EntityType, e.EntityID, e.ProjectID, e.MilestoneID, e.DonationID,
		severity, core.Truncate(e.Summary, 500), core.JSONString(e.Data), core.NowStr(),
	)
	if err != nil {
		return "", fmt.Errorf("audit: record %s: %w", e.Action, err)
	}
	return id, nil
}

// RecordNoTx appends an event outside a transaction.
func (s *Service) RecordNoTx(ctx context.Context, e Event) error {
	_, err := s.Record(ctx, s.db, e)
	return err
}

// Filter narrows an audit query.
type Filter struct {
	ProjectID   string
	MilestoneID string
	DonationID  string
	Actions     []models.AuditAction
	Limit       int
	Before      time.Time
}

// List returns audit events newest first.
func (s *Service) List(ctx context.Context, f Filter) ([]models.AuditEvent, error) {
	limit := f.Limit
	if limit <= 0 || limit > 300 {
		limit = 100
	}

	var (
		where []string
		args  []any
	)
	if f.ProjectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.MilestoneID != "" {
		where = append(where, "milestone_id = ?")
		args = append(args, f.MilestoneID)
	}
	if f.DonationID != "" {
		where = append(where, "donation_id = ?")
		args = append(args, f.DonationID)
	}
	if len(f.Actions) > 0 {
		ph := make([]string, 0, len(f.Actions))
		for _, a := range f.Actions {
			ph = append(ph, "?")
			args = append(args, string(a))
		}
		where = append(where, "action IN ("+strings.Join(ph, ",")+")")
	}
	if !f.Before.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, core.FormatTime(f.Before))
	}

	q := `SELECT id, actor_id, actor_role, actor_label, action, entity_type, entity_id,
	             project_id, milestone_id, donation_id, severity, summary, data, created_at
	      FROM audit_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]models.AuditEvent, 0, limit)
	for rows.Next() {
		var (
			e         models.AuditEvent
			createdAt string
		)
		if err := rows.Scan(
			&e.ID, &e.ActorID, &e.ActorRole, &e.ActorLabel, &e.Action, &e.EntityType, &e.EntityID,
			&e.ProjectID, &e.MilestoneID, &e.DonationID, &e.Severity, &e.Summary, &e.Data, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		e.CreatedAt = core.MustParseTime(createdAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountForProject returns the number of events attached to a project.
func (s *Service) CountForProject(ctx context.Context, projectID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE project_id = ?`, projectID).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return n, nil
}
