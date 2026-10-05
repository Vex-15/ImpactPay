// Package donations owns donor contributions and, crucially, the attribution of
// each contribution to the milestones it funds.
//
// Attribution is what makes "Trace My Impact" answerable by query rather than by
// assertion. Whenever a donation is captured, its amount is allocated across the
// project's milestones in ordinal order — filling each milestone's remaining
// budget before spilling into the next. The resulting rows in milestone_fundings
// are the ledger that connects a rupee to a filtration unit.
package donations

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
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
)

// Sentinel errors.
var (
	ErrNotFound       = errors.New("donation not found")
	ErrAmountInvalid  = errors.New("contribution must be greater than zero")
	ErrAmountTooSmall = errors.New("the minimum contribution is 1.00")
	ErrAmountTooLarge = errors.New("the maximum single contribution is 10,000,000.00")
	ErrProjectClosed  = errors.New("this project is not currently accepting contributions")
	ErrSelfDonation   = errors.New("you cannot contribute to your own organization's project")
)

// MinContributionMinor and MaxContributionMinor bound a single contribution.
const (
	MinContributionMinor int64 = 100        // 1.00
	MaxContributionMinor int64 = 1000000000 // 10,000,000.00
)

// Service provides donation operations.
type Service struct {
	db         *db.DB
	audit      *audit.Service
	milestones *milestones.Service
}

// New creates the donations service.
func New(database *db.DB, auditSvc *audit.Service, ms *milestones.Service) *Service {
	return &Service{db: database, audit: auditSvc, milestones: ms}
}

// CreateInput initiates a contribution.
type CreateInput struct {
	ProjectID string
	DonorID   string
	Amount    int64
	Currency  string
	Note      string
	Simulated bool
	// OrgOwnerUserID is the project owner's user id, used to reject self-donation.
	OrgOwnerUserID string
}

// Create records a pending donation. No money has moved yet; the PayPal order is
// created by the payments service immediately afterwards.
func (s *Service) Create(ctx context.Context, in CreateInput) (*models.Donation, error) {
	if in.Amount < MinContributionMinor {
		return nil, ErrAmountTooSmall
	}
	if in.Amount > MaxContributionMinor {
		return nil, ErrAmountTooLarge
	}
	if in.OrgOwnerUserID != "" && in.OrgOwnerUserID == in.DonorID {
		return nil, ErrSelfDonation
	}
	if in.Currency == "" {
		in.Currency = "INR"
	}

	d := &models.Donation{
		ID:        core.NewID(),
		ProjectID: in.ProjectID,
		DonorID:   in.DonorID,
		Amount:    in.Amount,
		Currency:  in.Currency,
		Status:    models.DonationPending,
		Provider:  "paypal",
		Simulated: in.Simulated,
		Note:      core.Truncate(strings.TrimSpace(in.Note), 500),
		CreatedAt: core.Now(),
		UpdatedAt: core.Now(),
	}

	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO donations (
				id, project_id, donor_id, amount, currency, status, paypal_order_id,
				paypal_capture_id, capture_status, provider, simulated, note,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, '', '', '', ?, ?, ?, ?, ?)`,
			d.ID, d.ProjectID, d.DonorID, d.Amount, d.Currency, string(d.Status),
			d.Provider, boolToInt(d.Simulated), d.Note,
			core.FormatTime(d.CreatedAt), core.FormatTime(d.UpdatedAt))
		if err != nil {
			return err
		}
		_, err = s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditDonationCreated,
			EntityType: "donation",
			EntityID:   d.ID,
			ProjectID:  d.ProjectID,
			DonationID: d.ID,
			Summary: fmt.Sprintf("Contribution of %s initiated",
				core.FormatMoney(core.Money(d.Amount), d.Currency)),
			Data: map[string]any{
				"amount_minor": d.Amount,
				"currency":     d.Currency,
				"simulated":    d.Simulated,
			},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SetOrder records the provider order attached to a donation.
func (s *Service) SetOrder(ctx context.Context, ex db.Executor, donationID, orderID, provider string, simulated bool) error {
	_, err := ex.ExecContext(ctx, `
		UPDATE donations SET paypal_order_id = ?, provider = ?, simulated = ?,
			status = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		orderID, provider, boolToInt(simulated), string(models.DonationOrderCreated),
		core.NowStr(), donationID,
		string(models.DonationPending), string(models.DonationOrderCreated))
	if err != nil {
		return fmt.Errorf("donations: set order: %w", err)
	}
	return nil
}

// SetCapture marks a donation as successfully captured.
func (s *Service) SetCapture(ctx context.Context, ex db.Executor, donationID, captureID, captureStatus string, simulated bool) error {
	now := core.Now()
	res, err := ex.ExecContext(ctx, `
		UPDATE donations SET paypal_capture_id = ?, capture_status = ?, status = ?,
			simulated = ?, captured_at = ?, updated_at = ?
		WHERE id = ? AND status <> ?`,
		captureID, captureStatus, string(models.DonationCaptured), boolToInt(simulated),
		core.FormatTime(now), core.FormatTime(now), donationID, string(models.DonationCaptured))
	if err != nil {
		return fmt.Errorf("donations: set capture: %w", err)
	}
	// Already captured is not an error: webhook and manual capture can race, and
	// the idempotent path must succeed quietly.
	if n, _ := res.RowsAffected(); n == 0 {
		var status string
		if err := ex.QueryRowContext(ctx, `SELECT status FROM donations WHERE id = ?`, donationID).Scan(&status); err != nil {
			return fmt.Errorf("donations: verify capture: %w", err)
		}
		if status == string(models.DonationCaptured) {
			return nil
		}
	}
	return nil
}

// SetStatus updates the donation's funding state.
func (s *Service) SetStatus(ctx context.Context, ex db.Executor, donationID string, status models.DonationStatus) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE donations SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), core.NowStr(), donationID)
	return err
}

// SetRefunded records a completed refund.
func (s *Service) SetRefunded(ctx context.Context, ex db.Executor, donationID string) error {
	now := core.Now()
	_, err := ex.ExecContext(ctx,
		`UPDATE donations SET status = ?, refunded_at = ?, updated_at = ? WHERE id = ?`,
		string(models.DonationRefunded), core.FormatTime(now), core.FormatTime(now), donationID)
	if err != nil {
		return fmt.Errorf("donations: set refunded: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Allocation
// ---------------------------------------------------------------------------

// Allocation is one milestone's share of a donation.
type Allocation struct {
	MilestoneID  string
	MilestoneOrd int
	Amount       int64
}

// Allocate distributes a captured donation across the project's milestones in
// ordinal order, filling each milestone's remaining budget before moving on.
//
// Allocation is an attribution ledger, not a movement of money: it records which
// milestone a contribution was raised for. Releasing that funding still requires
// the milestone to pass verification and backend policy.
func (s *Service) Allocate(ctx context.Context, ex db.Executor, projectID, donationID string, amount int64) ([]Allocation, error) {
	if amount <= 0 {
		return nil, nil
	}
	ms, err := s.milestones.ListByProjectTx(ctx, ex, projectID)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, nil
	}

	allocated, err := existingAllocations(ctx, ex, projectID)
	if err != nil {
		return nil, err
	}

	remaining := amount
	out := []Allocation{}
	for _, m := range ms {
		if remaining <= 0 {
			break
		}
		need := m.BudgetAmount - allocated[m.ID]
		if need <= 0 {
			continue
		}
		take := need
		if take > remaining {
			take = remaining
		}
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO milestone_fundings (id, milestone_id, project_id, donation_id, amount, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			core.NewID(), m.ID, projectID, donationID, take, core.NowStr()); err != nil {
			return nil, fmt.Errorf("donations: allocate: %w", err)
		}
		out = append(out, Allocation{MilestoneID: m.ID, MilestoneOrd: m.Ordinal, Amount: take})
		remaining -= take
	}

	// Any residual (a fully funded project receiving an extra contribution) is
	// attributed to the last milestone so no rupee becomes untraceable.
	if remaining > 0 && len(ms) > 0 {
		last := ms[len(ms)-1]
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO milestone_fundings (id, milestone_id, project_id, donation_id, amount, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			core.NewID(), last.ID, projectID, donationID, remaining, core.NowStr()); err != nil {
			return nil, fmt.Errorf("donations: allocate residual: %w", err)
		}
		out = append(out, Allocation{MilestoneID: last.ID, MilestoneOrd: last.Ordinal, Amount: remaining})
	}

	if len(out) > 0 {
		_, err = s.audit.Record(ctx, ex, audit.Event{
			Action:     models.AuditDonationCreated,
			EntityType: "donation",
			EntityID:   donationID,
			ProjectID:  projectID,
			DonationID: donationID,
			Summary: fmt.Sprintf("Contribution allocated across %d milestone(s)", len(out)),
			Data: map[string]any{
				"allocations": out,
				"amount":      amount,
			},
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func existingAllocations(ctx context.Context, ex db.Executor, projectID string) (map[string]int64, error) {
	rows, err := ex.QueryContext(ctx, `
		SELECT milestone_id, SUM(amount) FROM milestone_fundings
		WHERE project_id = ? GROUP BY milestone_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("donations: existing allocations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var (
			id string
			n  int64
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// AllocatedToMilestone returns the total funding attributed to a milestone.
func (s *Service) AllocatedToMilestone(ctx context.Context, ex db.Executor, milestoneID string) (int64, error) {
	var n sql.NullInt64
	if err := ex.QueryRowContext(ctx,
		`SELECT SUM(amount) FROM milestone_fundings WHERE milestone_id = ?`, milestoneID).Scan(&n); err != nil {
		return 0, fmt.Errorf("donations: allocated to milestone: %w", err)
	}
	return n.Int64, nil
}

// Funding is a donation's share of one milestone, resolved with the milestone.
type Funding struct {
	MilestoneFunding models.MilestoneFunding `json:"funding"`
	Milestone        models.Milestone        `json:"milestone"`
}

// FundingsForDonation returns every milestone a donation was attributed to.
func (s *Service) FundingsForDonation(ctx context.Context, donationID string) ([]Funding, error) {
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
		return nil, fmt.Errorf("donations: fundings for donation: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Funding{}
	for rows.Next() {
		var (
			f        Funding
			created  string
			ev       string
			msStatus string
			msCreated, msUpdated string
		)
		if err := rows.Scan(
			&f.MilestoneFunding.ID, &f.MilestoneFunding.MilestoneID, &f.MilestoneFunding.ProjectID,
			&f.MilestoneFunding.DonationID, &f.MilestoneFunding.Amount, &created,
			&f.Milestone.ID, &f.Milestone.ProjectID, &f.Milestone.Ordinal, &f.Milestone.Title,
			&f.Milestone.Description, &f.Milestone.Requirement, &f.Milestone.UnitLabel,
			&f.Milestone.RequiredQuantity, &f.Milestone.BudgetAmount, &ev,
			&msStatus, &f.Milestone.VerifiedQuantity, &msCreated, &msUpdated,
		); err != nil {
			return nil, err
		}
		f.MilestoneFunding.CreatedAt = core.MustParseTime(created)
		f.Milestone.Status = models.MilestoneStatus(msStatus)
		f.Milestone.CreatedAt = core.MustParseTime(msCreated)
		f.Milestone.UpdatedAt = core.MustParseTime(msUpdated)
		f.Milestone.EvidenceRequirements = []models.EvidenceRequirementSpec{}
		core.DecodeJSON(ev, &f.Milestone.EvidenceRequirements)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// ByID loads a donation.
func (s *Service) ByID(ctx context.Context, id string) (*models.Donation, error) {
	return s.scan(s.db.QueryRowContext(ctx, donationSelect+" WHERE id = ?", id))
}

// ByIDTx loads a donation inside a transaction.
func (s *Service) ByIDTx(ctx context.Context, ex db.Executor, id string) (*models.Donation, error) {
	return s.scan(ex.QueryRowContext(ctx, donationSelect+" WHERE id = ?", id))
}

// ByOrderID loads a donation by its provider order id.
func (s *Service) ByOrderID(ctx context.Context, orderID string) (*models.Donation, error) {
	return s.scan(s.db.QueryRowContext(ctx, donationSelect+" WHERE paypal_order_id = ?", orderID))
}

// ListByDonor returns a donor's contributions, newest first.
func (s *Service) ListByDonor(ctx context.Context, donorID string, limit int) ([]models.Donation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, donationSelect+" WHERE donor_id = ? ORDER BY created_at DESC LIMIT ?", donorID, limit)
	if err != nil {
		return nil, fmt.Errorf("donations: list by donor: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// ListByProject returns a project's contributions, newest first.
func (s *Service) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Donation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		donationSelect+" WHERE project_id = ? AND status = ? ORDER BY created_at DESC LIMIT ?",
		projectID, string(models.DonationCaptured), limit)
	if err != nil {
		return nil, fmt.Errorf("donations: list by project: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// WithDonor pairs a donation with the donor's display name for the funding feed.
type WithDonor struct {
	Donation   models.Donation `json:"donation"`
	DonorName  string          `json:"donor_name"`
	DonorEmail string          `json:"donor_email"`
}

// FundingHistory returns a project's contributions with donor labels.
func (s *Service) FundingHistory(ctx context.Context, projectID string, limit int) ([]WithDonor, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.project_id, d.donor_id, d.amount, d.currency, d.status,
		       d.paypal_order_id, d.paypal_capture_id, d.capture_status, d.provider,
		       d.simulated, d.note, d.created_at, d.updated_at, d.captured_at, d.refunded_at,
		       u.full_name, u.email
		FROM donations d
		JOIN users u ON u.id = d.donor_id
		WHERE d.project_id = ? AND d.status = ?
		ORDER BY d.created_at DESC LIMIT ?`,
		projectID, string(models.DonationCaptured), limit)
	if err != nil {
		return nil, fmt.Errorf("donations: funding history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []WithDonor{}
	for rows.Next() {
		var (
			wd                                          WithDonor
			d                                           models.Donation
			status                                      string
			createdAt, updatedAt                        string
			capturedAt, refundedAt                      sql.NullString
			simulated                                   int
		)
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.DonorID, &d.Amount, &d.Currency, &status,
			&d.PayPalOrderID, &d.PayPalCaptureID, &d.CaptureStatus, &d.Provider,
			&simulated, &d.Note, &createdAt, &updatedAt, &capturedAt, &refundedAt,
			&wd.DonorName, &wd.DonorEmail,
		); err != nil {
			return nil, err
		}
		d.Status = models.DonationStatus(status)
		d.Simulated = simulated == 1
		d.CreatedAt = core.MustParseTime(createdAt)
		d.UpdatedAt = core.MustParseTime(updatedAt)
		d.CapturedAt = nullTime(capturedAt)
		d.RefundedAt = nullTime(refundedAt)
		wd.Donation = d
		out = append(out, wd)
	}
	return out, rows.Err()
}

// DonorStats summarises a donor's giving and verified impact.
type DonorStats struct {
	TotalContributedMinor int64 `json:"total_contributed_minor"`
	ContributionCount     int   `json:"contribution_count"`
	ProjectsSupported     int   `json:"projects_supported"`
	VerifiedImpactMinor   int64 `json:"verified_impact_minor"`
	PendingImpactMinor    int64 `json:"pending_impact_minor"`
}

// StatsForDonor aggregates a donor's contributions.
func (s *Service) StatsForDonor(ctx context.Context, donorID string) (DonorStats, error) {
	var st DonorStats
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0), COUNT(*), COUNT(DISTINCT project_id)
		FROM donations WHERE donor_id = ? AND status = ?`,
		donorID, string(models.DonationCaptured)).Scan(
		&st.TotalContributedMinor, &st.ContributionCount, &st.ProjectsSupported)
	if err != nil {
		return st, fmt.Errorf("donations: donor stats: %w", err)
	}

	// Verified impact: the donor's share of milestones that are fully verified.
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(f.amount), 0)
		FROM milestone_fundings f
		JOIN milestones m ON m.id = f.milestone_id
		JOIN donations d ON d.id = f.donation_id
		WHERE d.donor_id = ? AND (m.status = ? OR m.status = ?)`,
		donorID, string(models.MilestoneCompleted), string(models.MilestonePaymentReleased)).Scan(&st.VerifiedImpactMinor)
	if err != nil {
		return st, fmt.Errorf("donations: verified impact: %w", err)
	}
	st.PendingImpactMinor = st.TotalContributedMinor - st.VerifiedImpactMinor
	if st.PendingImpactMinor < 0 {
		st.PendingImpactMinor = 0
	}
	return st, nil
}

const donationSelect = `SELECT id, project_id, donor_id, amount, currency, status,
	paypal_order_id, paypal_capture_id, capture_status, provider, simulated, note,
	created_at, updated_at, captured_at, refunded_at FROM donations`

type rowScanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row rowScanner) (*models.Donation, error) {
	var (
		d                          models.Donation
		status                     string
		createdAt, updatedAt       string
		capturedAt, refundedAt     sql.NullString
		simulated                  int
	)
	if err := row.Scan(
		&d.ID, &d.ProjectID, &d.DonorID, &d.Amount, &d.Currency, &status,
		&d.PayPalOrderID, &d.PayPalCaptureID, &d.CaptureStatus, &d.Provider,
		&simulated, &d.Note, &createdAt, &updatedAt, &capturedAt, &refundedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.Status = models.DonationStatus(status)
	d.Simulated = simulated == 1
	d.CreatedAt = core.MustParseTime(createdAt)
	d.UpdatedAt = core.MustParseTime(updatedAt)
	d.CapturedAt = nullTime(capturedAt)
	d.RefundedAt = nullTime(refundedAt)
	return &d, nil
}

func (s *Service) scanAll(rows *sql.Rows) ([]models.Donation, error) {
	out := []models.Donation{}
	for rows.Next() {
		d, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := core.MustParseTime(ns.String)
	return &t
}
