// Package payments — service wiring, funding state machine and event log.
package payments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/donations"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/notifications"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/verification"
)

// Sentinel errors.
var (
	ErrDonationNotFound  = errors.New("contribution not found")
	ErrOrderNotCreated   = errors.New("no payment order exists for this contribution")
	ErrAlreadyCaptured   = errors.New("this contribution has already been captured")
	ErrCaptureAmountMismatch = errors.New("the captured amount does not match the contribution")
	ErrNotCaptured       = errors.New("only a captured contribution can be refunded")
	ErrRefundNotAllowed  = errors.New("this contribution cannot be refunded while its milestone funding is released")
	ErrMilestoneNotFound = errors.New("milestone not found")
)

// Service owns the funding state machine.
type Service struct {
	db            *db.DB
	audit         *audit.Service
	provider      Provider
	donations     *donations.Service
	projects      *projects.Service
	milestones    *milestones.Service
	verifications *verification.Service
	orgs          *organizations.Service
	notifications *notifications.Service

	// accountCurrency is the PayPal account's settlement currency and
	// fxRateINRPerUSD is the configured indicative rate used to convert project
	// budgets into it. Neither is a live market rate; both are stored on every
	// release so a conversion is always visible.
	accountCurrency string
	fxRateINRPerUSD float64
}

// Deps wires the payments service.
type Deps struct {
	DB            *db.DB
	Audit         *audit.Service
	Provider      Provider
	Donations     *donations.Service
	Projects      *projects.Service
	Milestones    *milestones.Service
	Verifications *verification.Service
	Organizations *organizations.Service
	Notifications *notifications.Service
	// AccountCurrency defaults to USD when empty.
	AccountCurrency string
	// FXRateINRPerUSD defaults to 83.00 when zero.
	FXRateINRPerUSD float64
}

// New creates the payments service.
func New(d Deps) *Service {
	accountCurrency := d.AccountCurrency
	if accountCurrency == "" {
		accountCurrency = "USD"
	}
	rate := d.FXRateINRPerUSD
	if rate <= 0 {
		rate = 83.00
	}
	return &Service{
		db:              d.DB,
		audit:           d.Audit,
		provider:        d.Provider,
		donations:       d.Donations,
		projects:        d.Projects,
		milestones:      d.Milestones,
		verifications:   d.Verifications,
		orgs:            d.Organizations,
		notifications:   d.Notifications,
		accountCurrency: accountCurrency,
		fxRateINRPerUSD: rate,
	}
}

// Provider exposes the configured rail for diagnostics.
func (s *Service) Provider() Provider { return s.provider }

// AccountCurrency is the PayPal settlement currency.
func (s *Service) AccountCurrency() string { return s.accountCurrency }

// ConvertToAccount exposes the indicative conversion for UI display.
func (s *Service) ConvertToAccount(localMinor int64, localCurrency string) models.FXNote {
	return s.convertToAccount(localMinor, localCurrency)
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

// CreateDonationOrder opens a PayPal order for a pending contribution.
func (s *Service) CreateDonationOrder(ctx context.Context, donationID, returnURL, cancelURL string) (*models.Donation, *Order, error) {
	d, err := s.donations.ByID(ctx, donationID)
	if err != nil {
		return nil, nil, err
	}
	if d.Status == models.DonationCaptured {
		return nil, nil, ErrAlreadyCaptured
	}
	p, err := s.projects.ByID(ctx, d.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	if p.Status == models.ProjectSuspended || p.Status == models.ProjectArchived {
		return nil, nil, donations.ErrProjectClosed
	}

	order, err := s.provider.CreateOrder(ctx, OrderRequest{
		DonationID:  d.ID,
		Amount:      Amount{Minor: d.Amount, Currency: d.Currency},
		ProjectRef:  p.Slug,
		Description: "ImpactPay milestone funding: " + p.Title,
		ReturnURL:   returnURL,
		CancelURL:   cancelURL,
	}, "order-"+d.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("payments: create order: %w", err)
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if err := s.donations.SetOrder(ctx, tx, d.ID, order.ID, s.provider.Name(), order.Simulated); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, tx, models.PaymentEvent{
			DonationID:  d.ID,
			ProjectID:   p.ID,
			Type:        models.EventOrderCreated,
			ProviderRef: order.ID,
			Amount:      d.Amount,
			Currency:    d.Currency,
			Status:      order.Status,
			Detail:      fmt.Sprintf("PayPal sandbox order created for %s", core.FormatMoney(core.Money(d.Amount), d.Currency)),
			Simulated:   order.Simulated,
		}); err != nil {
			return err
		}
		_, err := s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditPayPalOrderCreated,
			EntityType: "donation",
			EntityID:   d.ID,
			ProjectID:  p.ID,
			DonationID: d.ID,
			Summary: fmt.Sprintf("Payment order created for %s contribution",
				core.FormatMoney(core.Money(d.Amount), d.Currency)),
			Data: map[string]any{
				"order_id":  order.ID,
				"provider":  s.provider.Name(),
				"simulated": order.Simulated,
			},
		})
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	updated, err := s.donations.ByID(ctx, d.ID)
	if err != nil {
		return nil, nil, err
	}
	return updated, order, nil
}

// ---------------------------------------------------------------------------
// Capture
// ---------------------------------------------------------------------------

// CaptureResult bundles the captured donation with its attribution.
type CaptureResult struct {
	Donation    models.Donation       `json:"donation"`
	Capture     CaptureOutcome        `json:"capture"`
	Allocations []donations.Allocation `json:"allocations"`
	Project     models.Project        `json:"project"`
}

// CaptureDonation captures an approved order and records the funding movement.
//
// Ordering matters: the provider call happens first, outside the transaction, and
// only a verified success is written. The captured amount is then validated
// against the expected amount before any ledger row is touched, so a mismatched
// or partial capture can never be recorded as funding.
func (s *Service) CaptureDonation(ctx context.Context, donationID string) (*CaptureResult, error) {
	d, err := s.donations.ByID(ctx, donationID)
	if err != nil {
		return nil, err
	}
	if d.Status == models.DonationCaptured {
		// Idempotent: re-capturing returns the existing, already-verified state.
		p, err := s.projects.ByID(ctx, d.ProjectID)
		if err != nil {
			return nil, err
		}
		return &CaptureResult{Donation: *d, Project: *p}, ErrAlreadyCaptured
	}
	if d.PayPalOrderID == "" {
		return nil, ErrOrderNotCreated
	}

	capture, err := s.provider.CaptureOrder(ctx, d.PayPalOrderID, "capture-"+d.ID)
	if err != nil {
		// Record the failure so the donation history is honest about it.
		_ = s.db.RunInTx(ctx, func(tx *db.Tx) error {
			_ = s.donations.SetStatus(ctx, tx, d.ID, models.DonationFailed)
			if err := s.recordEvent(ctx, tx, models.PaymentEvent{
				DonationID: d.ID, ProjectID: d.ProjectID, Type: models.EventPaymentFailed,
				ProviderRef: d.PayPalOrderID, Amount: d.Amount, Currency: d.Currency,
				Status: "FAILED", Detail: err.Error(), Simulated: s.provider.Simulated(),
			}); err != nil {
				return err
			}
			_, aerr := s.audit.Record(ctx, tx, audit.Event{
				Action: models.AuditPaymentFailed, EntityType: "donation", EntityID: d.ID,
				ProjectID: d.ProjectID, DonationID: d.ID, Severity: "critical",
				Summary: "Payment capture failed: " + core.Truncate(err.Error(), 200),
			})
			return aerr
		})
		return nil, fmt.Errorf("payments: capture order: %w", err)
	}

	// Deterministic validation: the captured amount must equal what was intended.
	if capture.Amount.Minor > 0 && capture.Amount.Minor != d.Amount {
		_ = s.db.RunInTx(ctx, func(tx *db.Tx) error {
			_ = s.donations.SetStatus(ctx, tx, d.ID, models.DonationFailed)
			return s.recordEvent(ctx, tx, models.PaymentEvent{
				DonationID: d.ID, ProjectID: d.ProjectID, Type: models.EventPaymentFailed,
				ProviderRef: capture.CaptureID, Amount: capture.Amount.Minor,
				Currency: capture.Amount.Currency, Status: "AMOUNT_MISMATCH",
				Detail: fmt.Sprintf("expected %d minor units, captured %d", d.Amount, capture.Amount.Minor),
				Simulated: capture.Simulated,
			})
		})
		return nil, fmt.Errorf("%w: expected %d, captured %d", ErrCaptureAmountMismatch, d.Amount, capture.Amount.Minor)
	}

	p, err := s.projects.ByID(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}

	return s.applyCapture(ctx, d, p, capture)
}

// ApplyConfirmedCapture records a capture that PayPal has already confirmed via a
// webhook, without re-invoking the capture endpoint. Webhooks are a
// reconciliation signal: if the browser already captured the order this is a
// no-op, and if the browser's capture call failed after PayPal succeeded, the
// ledger is repaired here.
func (s *Service) ApplyConfirmedCapture(ctx context.Context, donationID, captureID, captureStatus string, simulated bool) (*CaptureResult, error) {
	d, err := s.donations.ByID(ctx, donationID)
	if err != nil {
		return nil, err
	}
	if d.Status == models.DonationCaptured {
		p, perr := s.projects.ByID(ctx, d.ProjectID)
		if perr != nil {
			return nil, perr
		}
		return &CaptureResult{Donation: *d, Project: *p}, ErrAlreadyCaptured
	}
	p, err := s.projects.ByID(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}
	return s.applyCapture(ctx, d, p, &CaptureOutcome{
		CaptureID: captureID,
		OrderID:   d.PayPalOrderID,
		Status:    captureStatus,
		Amount:    Amount{Minor: d.Amount, Currency: d.Currency},
		Simulated: simulated,
	})
}

// applyCapture performs the ledger movement for a confirmed capture.
func (s *Service) applyCapture(ctx context.Context, d *models.Donation, p *models.Project, capture *CaptureOutcome) (*CaptureResult, error) {
	var allocs []donations.Allocation
	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if err := s.donations.SetCapture(ctx, tx, d.ID, capture.CaptureID, capture.Status, capture.Simulated); err != nil {
			return err
		}
		if err := s.projects.AddFunds(ctx, tx, p.ID, d.Amount); err != nil {
			return err
		}
		if err := s.projects.MarkInProgress(ctx, tx, p.ID); err != nil {
			return err
		}
		var aerr error
		allocs, aerr = s.donations.Allocate(ctx, tx, p.ID, d.ID, d.Amount)
		if aerr != nil {
			return aerr
		}
		if err := s.recordEvent(ctx, tx, models.PaymentEvent{
			DonationID: d.ID, ProjectID: p.ID, Type: models.EventPaymentCaptured,
			ProviderRef: capture.CaptureID, Amount: d.Amount, Currency: d.Currency,
			Status: capture.Status,
			Detail: fmt.Sprintf("Captured %s for '%s'",
				core.FormatMoney(core.Money(d.Amount), d.Currency), p.Title),
			Simulated: capture.Simulated,
		}); err != nil {
			return err
		}
		for _, a := range allocs {
			if err := s.recordEvent(ctx, tx, models.PaymentEvent{
				DonationID: d.ID, ProjectID: p.ID, MilestoneID: a.MilestoneID,
				Type: models.EventFundingAllocated, Amount: a.Amount, Currency: d.Currency,
				Status: "ALLOCATED",
				Detail: fmt.Sprintf("Attributed %s to milestone %d",
					core.FormatMoney(core.Money(a.Amount), d.Currency), a.MilestoneOrd),
				Simulated: capture.Simulated,
			}); err != nil {
				return err
			}
		}
		if _, err := s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditPaymentCompleted,
			EntityType: "donation",
			EntityID:   d.ID,
			ProjectID:  p.ID,
			DonationID: d.ID,
			Summary: fmt.Sprintf("%s contribution captured and attributed to %d milestone(s)",
				core.FormatMoney(core.Money(d.Amount), d.Currency), len(allocs)),
			Data: map[string]any{
				"capture_id":  capture.CaptureID,
				"order_id":    d.PayPalOrderID,
				"amount":      d.Amount,
				"simulated":   capture.Simulated,
				"allocations": allocs,
			},
		}); err != nil {
			return err
		}

		// Tell the organization's owner that funding arrived.
		org, err := s.orgs.ByID(ctx, p.OrgID)
		if err == nil {
			_ = s.notifications.Notify(ctx, tx, org.UserID, "funding",
				"New contribution received",
				fmt.Sprintf("%s was contributed to '%s'.",
					core.FormatMoney(core.Money(d.Amount), d.Currency), p.Title),
				"/dashboard/organization/projects/"+p.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	updatedDonation, err := s.donations.ByID(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	updatedProject, err := s.projects.ByID(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	_ = allocs
	return &CaptureResult{
		Donation: *updatedDonation,
		Capture:  *capture,
		Allocations: allocs,
		Project:  *updatedProject,
	}, nil
}

// ---------------------------------------------------------------------------
// Refunds
// ---------------------------------------------------------------------------

// RefundDonation refunds a captured contribution.
//
// Refunds are only permitted while the related milestone funding has not been
// released. Once a milestone is paid out the money has left ImpactPay's PayPal
// balance, and a silent refund would break the funding accounting — so the
// attempt is recorded as blocked instead of being quietly allowed.
func (s *Service) RefundDonation(ctx context.Context, donationID, reason string, actor models.Actor) (*models.Donation, error) {
	d, err := s.donations.ByID(ctx, donationID)
	if err != nil {
		return nil, err
	}
	if d.Status != models.DonationCaptured {
		return nil, ErrNotCaptured
	}
	if d.PayPalCaptureID == "" {
		return nil, fmt.Errorf("%w: the contribution has no capture reference", ErrNotCaptured)
	}

	fundings, err := s.donations.FundingsForDonation(ctx, donationID)
	if err != nil {
		return nil, err
	}
	for _, f := range fundings {
		if f.Milestone.Status == models.MilestonePaymentReleased || f.Milestone.Status == models.MilestoneCompleted {
			err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
				if _, aerr := s.audit.Record(ctx, tx, audit.Event{
					Action: models.AuditRefundExecuted, EntityType: "donation", EntityID: d.ID,
					ProjectID: d.ProjectID, DonationID: d.ID, Severity: "critical",
					Summary: fmt.Sprintf("Refund blocked: milestone %d '%s' funding was already released",
						f.Milestone.Ordinal, f.Milestone.Title),
					Actor: &actor,
				}); aerr != nil {
					return aerr
				}
				return s.recordEvent(ctx, tx, models.PaymentEvent{
					DonationID: d.ID, ProjectID: d.ProjectID, MilestoneID: f.Milestone.ID,
					Type: models.EventRefundRequested, Amount: d.Amount, Currency: d.Currency,
					Status: "BLOCKED", Detail: "milestone funding already released",
				})
			})
			if err != nil {
				return nil, err
			}
			return nil, ErrRefundNotAllowed
		}
	}

	refund, err := s.provider.Refund(ctx, d.PayPalCaptureID,
		Amount{Minor: d.Amount, Currency: d.Currency},
		core.Truncate(reason, 200), "refund-"+d.ID)
	if err != nil {
		return nil, fmt.Errorf("payments: refund: %w", err)
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if err := s.donations.SetRefunded(ctx, tx, d.ID); err != nil {
			return err
		}
		// Release the attribution so a refunded rupee stops counting as impact.
		if _, err := tx.ExecContext(ctx, `DELETE FROM milestone_fundings WHERE donation_id = ?`, d.ID); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, tx, models.PaymentEvent{
			DonationID: d.ID, ProjectID: d.ProjectID, Type: models.EventRefundCompleted,
			ProviderRef: refund.RefundID, Amount: d.Amount, Currency: d.Currency,
			Status: refund.Status, Detail: core.Truncate(reason, 300),
			Simulated: refund.Simulated,
		}); err != nil {
			return err
		}
		_, err := s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditRefundExecuted,
			EntityType: "donation",
			EntityID:   d.ID,
			ProjectID:  d.ProjectID,
			DonationID: d.ID,
			Severity:   "warning",
			Summary: fmt.Sprintf("Refund of %s executed: %s",
				core.FormatMoney(core.Money(d.Amount), d.Currency), core.Truncate(reason, 120)),
			Data: map[string]any{
				"refund_id":  refund.RefundID,
				"capture_id": d.PayPalCaptureID,
				"simulated":  refund.Simulated,
			},
			Actor: &actor,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.donations.ByID(ctx, donationID)
}

// ---------------------------------------------------------------------------
// Event log
// ---------------------------------------------------------------------------

// recordEvent appends to the append-only payment event log.
func (s *Service) recordEvent(ctx context.Context, ex db.Executor, e models.PaymentEvent) error {
	if e.Provider == "" {
		e.Provider = s.provider.Name()
	}
	if e.ActorID == "" {
		e.ActorID = models.ActorFrom(ctx).ID
	}
	_, err := ex.ExecContext(ctx, `
		INSERT INTO payment_events (
			id, donation_id, project_id, milestone_id, type, provider, provider_ref,
			amount, currency, status, detail, raw, actor_id, simulated, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		core.NewID(), e.DonationID, e.ProjectID, e.MilestoneID, string(e.Type),
		e.Provider, e.ProviderRef, e.Amount, e.Currency, e.Status,
		core.Truncate(e.Detail, 1000), rawOrEmpty(e.Raw), e.ActorID,
		boolToInt(e.Simulated || s.provider.Simulated()), core.NowStr())
	if err != nil {
		return fmt.Errorf("payments: record event %s: %w", e.Type, err)
	}
	return nil
}

// EventFilter narrows a payment event query.
type EventFilter struct {
	DonationID  string
	ProjectID   string
	MilestoneID string
	Limit       int
}

// PaymentEvents returns the append-only financial log, newest first.
func (s *Service) PaymentEvents(ctx context.Context, f EventFilter) ([]models.PaymentEvent, error) {
	limit := f.Limit
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	var (
		where []string
		args  []any
	)
	if f.DonationID != "" {
		where = append(where, "donation_id = ?")
		args = append(args, f.DonationID)
	}
	if f.ProjectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.MilestoneID != "" {
		where = append(where, "milestone_id = ?")
		args = append(args, f.MilestoneID)
	}
	q := `SELECT id, donation_id, project_id, milestone_id, type, provider, provider_ref,
	             amount, currency, status, detail, actor_id, simulated, created_at
	      FROM payment_events`
	if len(where) > 0 {
		q += " WHERE " + joinAnd(where)
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("payments: list events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []models.PaymentEvent{}
	for rows.Next() {
		var (
			e         models.PaymentEvent
			typ, cAt  string
			sim       int
		)
		if err := rows.Scan(&e.ID, &e.DonationID, &e.ProjectID, &e.MilestoneID, &typ,
			&e.Provider, &e.ProviderRef, &e.Amount, &e.Currency, &e.Status, &e.Detail,
			&e.ActorID, &sim, &cAt); err != nil {
			return nil, err
		}
		e.Type = models.PaymentEventType(typ)
		e.Simulated = sim == 1
		e.CreatedAt = core.MustParseTime(cAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MilestonePayments returns the conditional-release attempts for a milestone.
func (s *Service) MilestonePayments(ctx context.Context, milestoneID string, limit int) ([]models.MilestonePayment, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, milestone_id, project_id, org_id, donation_id, kind, amount, currency,
		       status, provider, paypal_ref, paypal_status, policy_checks, fx_note,
		       blocked_reason, executed_by, simulated, created_at, executed_at
		FROM milestone_payments WHERE milestone_id = ?
		ORDER BY created_at DESC LIMIT ?`, milestoneID, limit)
	if err != nil {
		return nil, fmt.Errorf("payments: milestone payments: %w", err)
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

// ProcessWebhook verifies and applies an inbound PayPal notification. Signature
// verification always happens before any state change.
func (s *Service) ProcessWebhook(ctx context.Context, headers http.Header, rawBody []byte) (*models.WebhookEvent, error) {
	verified, err := s.provider.VerifyWebhook(ctx, headers, rawBody)

	var (
		eventID, eventType, resType, resID, status string
	)
	rec := &models.WebhookEvent{
		ID:        core.NewID(),
		Provider:  s.provider.Name(),
		Payload:   string(rawBody),
		CreatedAt: core.Now(),
	}

	if err == nil {
		if ev, derr := decodeWebhookBody(rawBody); derr == nil {
			eventID = ev.ID
			eventType = ev.EventType
			resType = ev.ResourceType
			resID = ev.Resource.ID
		}
	}
	if !verified {
		status = "REJECTED"
		if err == nil {
			err = fmt.Errorf("payments: webhook signature verification failed")
		}
	} else {
		status = "VERIFIED"
	}

	rec.EventID = eventID
	rec.EventType = eventType
	rec.ResourceType = resType
	rec.ResourceID = resID
	rec.SignatureVerified = verified
	rec.Status = status

	if perr := s.persistWebhook(ctx, rec); perr != nil {
		return rec, perr
	}

	// Webhook receipt is audited either way; a rejected webhook is exactly the
	// kind of thing an admin needs to see.
	if _, aerr := s.audit.Record(ctx, s.db, audit.Event{
		Action:     models.AuditWebhookReceived,
		EntityType: "webhook",
		EntityID:   rec.ID,
		Severity:   severityForWebhook(verified),
		Summary: fmt.Sprintf("PayPal webhook %s: %s (%s)", status, eventType, resID),
		Data:       map[string]any{"event_type": eventType, "verified": verified, "resource_id": resID},
	}); aerr != nil {
		return rec, aerr
	}

	if !verified {
		return rec, err
	}

	if aerr := s.applyWebhook(ctx, rawBody); aerr != nil {
		_ = s.markWebhookProcessed(ctx, rec.ID, false, aerr.Error())
		return rec, aerr
	}
	_ = s.markWebhookProcessed(ctx, rec.ID, true, "")
	return rec, nil
}

func severityForWebhook(verified bool) string {
	if verified {
		return "info"
	}
	return "critical"
}
