package payments

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// MilestonePaymentFilter narrows a conditional-release query. Every field is
// optional; an empty filter lists the most recent attempts across the platform,
// which is what the admin transaction view needs.
type MilestonePaymentFilter struct {
	MilestoneID string
	ProjectID   string
	Kind        models.MilestonePaymentKind
	Status      models.MilestonePaymentStatus
	Limit       int
}

// QueryMilestonePayments returns conditional release, refund and payout records
// newest first.
func (s *Service) QueryMilestonePayments(ctx context.Context, f MilestonePaymentFilter) ([]models.MilestonePayment, error) {
	limit := f.Limit
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	var (
		where []string
		args  []any
	)
	if f.MilestoneID != "" {
		where = append(where, "milestone_id = ?")
		args = append(args, f.MilestoneID)
	}
	if f.ProjectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(f.Kind))
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}

	q := `SELECT id, milestone_id, project_id, org_id, donation_id, kind, amount, currency,
	             status, provider, paypal_ref, paypal_status, policy_checks, fx_note,
	             blocked_reason, executed_by, simulated, created_at, executed_at
	      FROM milestone_payments`
	if len(where) > 0 {
		q += " WHERE " + joinAnd(where)
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("payments: query milestone payments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []models.MilestonePayment{}
	for rows.Next() {
		var (
			mp           models.MilestonePayment
			kind, status string
			checks, fx   string
			sim          int
			createdAt    string
			executedAt   sql.NullString
		)
		if err := rows.Scan(&mp.ID, &mp.MilestoneID, &mp.ProjectID, &mp.OrgID, &mp.DonationID,
			&kind, &mp.Amount, &mp.Currency, &status, &mp.Provider, &mp.PayPalRef,
			&mp.PayPalStatus, &checks, &fx, &mp.BlockedReason, &mp.ExecutedBy, &sim,
			&createdAt, &executedAt); err != nil {
			return nil, err
		}
		mp.Kind = models.MilestonePaymentKind(kind)
		mp.Status = models.MilestonePaymentStatus(status)
		mp.Simulated = sim == 1
		mp.CreatedAt = core.MustParseTime(createdAt)
		if executedAt.Valid && executedAt.String != "" {
			t := core.MustParseTime(executedAt.String)
			mp.ExecutedAt = &t
		}
		mp.PolicyChecks = []models.PolicyCheck{}
		core.DecodeJSON(checks, &mp.PolicyChecks)
		core.DecodeJSON(fx, &mp.FX)
		out = append(out, mp)
	}
	return out, rows.Err()
}
