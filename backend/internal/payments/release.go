package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
)

// ErrReleaseBlocked is returned alongside the recorded MilestonePayment when the
// deterministic policy denies the release. The record is still returned so the
// UI can show precisely which gate failed.
var ErrReleaseBlocked = errors.New("milestone funding release blocked by policy")

// FXNote is models.FXNote, re-exported for readability inside this package.
type FXNote = models.FXNote

// buildRelease assembles the policy context and the candidate payment record.
func (s *Service) buildRelease(ctx context.Context, milestoneID string) (*models.MilestonePayment, ReleaseDecision, FXNote, error) {
	m, err := s.milestones.ByID(ctx, milestoneID)
	if err != nil {
		return nil, ReleaseDecision{}, FXNote{}, err
	}
	p, err := s.projects.ByID(ctx, m.ProjectID)
	if err != nil {
		return nil, ReleaseDecision{}, FXNote{}, err
	}
	org, err := s.orgs.ByID(ctx, p.OrgID)
	if err != nil {
		return nil, ReleaseDecision{}, FXNote{}, err
	}
	v, err := s.verifications.LatestForMilestone(ctx, m.ID)
	if err != nil {
		return nil, ReleaseDecision{}, FXNote{}, err
	}
	allocated, err := s.donations.AllocatedToMilestone(ctx, s.db, m.ID)
	if err != nil {
		return nil, ReleaseDecision{}, FXNote{}, err
	}

	already, err := s.hasExecutedRelease(ctx, m.ID)
	if err != nil {
		return nil, ReleaseDecision{}, FXNote{}, err
	}

	decision := EvaluateRelease(ReleaseContext{
		Project:         p,
		Milestone:       m,
		Organization:    org,
		Verification:    v,
		AllocatedMinor:  allocated,
		AlreadyReleased: already,
	})

	fx := s.convertToAccount(decision.AmountMinor, p.Currency)

	record := &models.MilestonePayment{
		ID:            core.NewID(),
		MilestoneID:   m.ID,
		ProjectID:     p.ID,
		OrgID:         org.ID,
		Kind:          models.PaymentRelease,
		Amount:        decision.AmountMinor,
		Currency:      p.Currency,
		Status:        models.MsPaymentPending,
		Provider:      s.provider.Name(),
		PolicyChecks:  decision.Checks,
		Simulated:     s.provider.Simulated(),
		CreatedAt:     core.Now(),
	}
	if !decision.Allowed {
		record.Status = models.MsPaymentBlocked
		record.BlockedReason = decision.BlockedReason
	}
	return record, decision, fx, nil
}

// PreviewRelease evaluates every release gate without persisting anything. The
// UI uses it to show the admin which conditions are still outstanding.
func (s *Service) PreviewRelease(ctx context.Context, milestoneID string) (*models.MilestonePayment, error) {
	record, _, _, err := s.buildRelease(ctx, milestoneID)
	return record, err
}

// ReleaseMilestone conditionally releases a milestone's funding.
//
// Order of operations, deliberately:
//  1. Evaluate every deterministic policy gate.
//  2. If any gate fails, persist a BLOCKED record and stop. No provider call.
//  3. Only then ask PayPal to move the money.
//  4. Persist the successful release, move the project's funding counters and
//     advance the milestone, all in one transaction.
func (s *Service) ReleaseMilestone(ctx context.Context, milestoneID string, actor models.Actor) (*models.MilestonePayment, error) {
	record, decision, fx, err := s.buildRelease(ctx, milestoneID)
	if err != nil {
		return nil, err
	}
	actorCtx := models.WithActor(ctx, actor)

	if !decision.Allowed {
		err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
			if err := s.insertMilestonePayment(ctx, tx, record, fx); err != nil {
				return err
			}
			if err := s.recordEvent(ctx, tx, models.PaymentEvent{
				ProjectID: record.ProjectID, MilestoneID: record.MilestoneID,
				Type: models.EventReleaseBlocked, Amount: record.Amount, Currency: record.Currency,
				Status: string(models.MsPaymentBlocked), Detail: decision.BlockedReason,
			}); err != nil {
				return err
			}
			_, aerr := s.audit.Record(actorCtx, tx, audit.Event{
				Action:      models.AuditPaymentActionBlocked,
				EntityType:  "milestone",
				EntityID:    record.MilestoneID,
				ProjectID:   record.ProjectID,
				MilestoneID: record.MilestoneID,
				Severity:    "warning",
				Summary: fmt.Sprintf("Milestone funding release blocked: %s", decision.BlockedReason),
				Data: map[string]any{
					"checks":         decision.Checks,
					"blocked_reason": decision.BlockedReason,
					"attempted_minor": record.Amount,
				},
				Actor: &actor,
			})
			return aerr
		})
		if err != nil {
			return nil, err
		}
		return record, ErrReleaseBlocked
	}

	m, err := s.milestones.ByID(ctx, milestoneID)
	if err != nil {
		return nil, err
	}

	payout, err := s.provider.Payout(ctx, PayoutRequest{
		MilestoneID: m.ID,
		Amount:      Amount{Minor: fx.AccountMinor, Currency: fx.ToCurrency},
		Receiver:    s.payoutReceiver(ctx, record.OrgID),
		Note: fmt.Sprintf("ImpactPay milestone %d release: %s", m.Ordinal, core.Truncate(m.Title, 120)),
	}, "release-"+m.ID)
	if err != nil {
		record.Status = models.MsPaymentFailed
		record.BlockedReason = "provider rejected the payout: " + err.Error()
		rerr := s.db.RunInTx(ctx, func(tx *db.Tx) error {
			if err := s.insertMilestonePayment(ctx, tx, record, fx); err != nil {
				return err
			}
			if err := s.recordEvent(ctx, tx, models.PaymentEvent{
				ProjectID: record.ProjectID, MilestoneID: record.MilestoneID,
				Type: models.EventReleaseBlocked, Amount: record.Amount, Currency: record.Currency,
				Status: string(models.MsPaymentFailed), Detail: record.BlockedReason,
			}); err != nil {
				return err
			}
			_, aerr := s.audit.Record(actorCtx, tx, audit.Event{
				Action: models.AuditPaymentActionBlocked, EntityType: "milestone",
				EntityID: record.MilestoneID, ProjectID: record.ProjectID,
				MilestoneID: record.MilestoneID, Severity: "critical",
				Summary: "Milestone payout failed at the provider: " + core.Truncate(err.Error(), 180),
				Actor:   &actor,
			})
			return aerr
		})
		if rerr != nil {
			return nil, rerr
		}
		return record, fmt.Errorf("payments: release payout: %w", err)
	}

	record.PayPalRef = payout.PayoutID
	record.PayPalStatus = payout.Status
	record.ExecutedBy = actor.ID
	now := core.Now()
	record.ExecutedAt = &now
	record.Status = models.MsPaymentSimulated
	if !payout.Simulated {
		record.Status = models.MsPaymentExecuted
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if err := s.insertMilestonePayment(ctx, tx, record, fx); err != nil {
			return err
		}
		if err := s.projects.Release(ctx, tx, record.ProjectID, record.Amount); err != nil {
			return err
		}
		if err := s.milestones.SetStatusTx(ctx, tx, record.MilestoneID, models.MilestonePaymentReleased, actor, map[string]any{
			"amount_minor": record.Amount,
			"payout_ref":   payout.PayoutID,
		}); err != nil {
			return err
		}
		if err := s.milestones.SetStatusTx(ctx, tx, record.MilestoneID, models.MilestoneCompleted, actor, nil); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, tx, models.PaymentEvent{
			ProjectID: record.ProjectID, MilestoneID: record.MilestoneID,
			Type: models.EventReleaseCompleted, ProviderRef: payout.PayoutID,
			Amount: record.Amount, Currency: record.Currency, Status: payout.Status,
			Detail: fmt.Sprintf("Released %s to %s (%s %s at an indicative rate of %.2f)",
				core.FormatMoney(core.Money(record.Amount), record.Currency),
				s.payoutReceiver(ctx, record.OrgID),
				core.FormatMoney(core.Money(fx.AccountMinor), fx.ToCurrency),
				fx.ToCurrency, fx.Rate),
			Simulated: payout.Simulated,
		}); err != nil {
			return err
		}
		if _, err := s.audit.Record(actorCtx, tx, audit.Event{
			Action:      models.AuditPaymentActionExecuted,
			EntityType:  "milestone",
			EntityID:    record.MilestoneID,
			ProjectID:   record.ProjectID,
			MilestoneID: record.MilestoneID,
			Summary: fmt.Sprintf("Released %s for milestone '%s'",
				core.FormatMoney(core.Money(record.Amount), record.Currency), m.Title),
			Data: map[string]any{
				"amount_minor":      record.Amount,
				"account_amount":    fx.AccountMinor,
				"account_currency":  fx.ToCurrency,
				"rate":              fx.Rate,
				"payout_ref":        payout.PayoutID,
				"provider":          s.provider.Name(),
				"simulated":         payout.Simulated,
				"policy_checks":     decision.Checks,
			},
			Actor: &actor,
		}); err != nil {
			return err
		}

		org, oerr := s.orgs.ByID(ctx, record.OrgID)
		if oerr == nil {
			_ = s.notifications.Notify(ctx, tx, org.UserID, "payment",
				"Milestone funding released",
				fmt.Sprintf("%s was released for milestone '%s'.",
					core.FormatMoney(core.Money(record.Amount), record.Currency), m.Title),
				"/dashboard/organization/milestones")
		}

		// Tell every donor whose contribution funded this milestone.
		if derr := s.notifyFunders(ctx, tx, m, record); derr != nil {
			return derr
		}

		// Close the project if this was the last milestone.
		done, cerr := s.projects.MarkCompletedIfDone(ctx, tx, record.ProjectID)
		if cerr != nil {
			return cerr
		}
		if done {
			_, aerr := s.audit.Record(actorCtx, tx, audit.Event{
				Action: models.AuditProjectUpdated, EntityType: "project",
				EntityID: record.ProjectID, ProjectID: record.ProjectID,
				Summary: "All milestones completed; project marked as completed",
				Actor:   &actor,
			})
			return aerr
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

// notifyFunders tells the donors attributed to this milestone that their impact
// has been verified and released.
func (s *Service) notifyFunders(ctx context.Context, ex db.Executor, m *models.Milestone, record *models.MilestonePayment) error {
	rows, err := ex.QueryContext(ctx, `
		SELECT DISTINCT d.donor_id, SUM(f.amount)
		FROM milestone_fundings f
		JOIN donations d ON d.id = f.donation_id
		WHERE f.milestone_id = ?
		GROUP BY d.donor_id`, m.ID)
	if err != nil {
		return fmt.Errorf("payments: funders: %w", err)
	}
	type funder struct {
		userID string
		amount int64
	}
	funders := []funder{}
	for rows.Next() {
		var f funder
		if err := rows.Scan(&f.userID, &f.amount); err != nil {
			_ = rows.Close()
			return err
		}
		funders = append(funders, f)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, f := range funders {
		if err := s.notifications.Notify(ctx, ex, f.userID, "impact",
			"Verified impact: "+m.Title,
			fmt.Sprintf("%s of your contribution funded this milestone, which is now verified and released.",
				core.FormatMoney(core.Money(f.amount), record.Currency)),
			"/impact"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) hasExecutedRelease(ctx context.Context, milestoneID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM milestone_payments
		WHERE milestone_id = ? AND kind = ? AND status IN (?, ?)`,
		milestoneID, string(models.PaymentRelease),
		string(models.MsPaymentExecuted), string(models.MsPaymentSimulated)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("payments: check existing release: %w", err)
	}
	return n > 0, nil
}

// payoutReceiver resolves the organization's sandbox payout address.
func (s *Service) payoutReceiver(ctx context.Context, orgID string) string {
	org, err := s.orgs.ByID(ctx, orgID)
	if err != nil || org == nil {
		return ""
	}
	if org.PayoutEmail != "" {
		return org.PayoutEmail
	}
	if org.ContactEmail != "" {
		return org.ContactEmail
	}
	// The simulated provider does not need a real address; a labelled placeholder
	// keeps the ledger readable and makes the missing configuration obvious.
	return "unconfigured@" + org.Slug + ".invalid"
}

// insertMilestonePayment persists a release attempt together with the FX note.
func (s *Service) insertMilestonePayment(ctx context.Context, ex db.Executor, p *models.MilestonePayment, fx FXNote) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO milestone_payments (
			id, milestone_id, project_id, org_id, donation_id, kind, amount, currency,
			status, provider, paypal_ref, paypal_status, policy_checks, fx_note,
			blocked_reason, executed_by, simulated, created_at, executed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.MilestoneID, p.ProjectID, p.OrgID, p.DonationID, string(p.Kind),
		p.Amount, p.Currency, string(p.Status), p.Provider, p.PayPalRef, p.PayPalStatus,
		core.JSONString(p.PolicyChecks), core.JSONString(fx), core.Truncate(p.BlockedReason, 500),
		p.ExecutedBy, boolToInt(p.Simulated), core.FormatTime(p.CreatedAt), nullableTime(p.ExecutedAt))
	if err != nil {
		return fmt.Errorf("payments: insert milestone payment: %w", err)
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return core.FormatTime(*t)
}

// convertToAccount converts a project-currency amount into the PayPal account
// currency using the configured indicative rate.
//
// PayPal settles in the account's currency, so a project budgeted in INR is
// converted at a fixed, configured rate. This is deliberately *not* presented as
// a live exchange rate: the rate, its source and both amounts are stored on every
// release so the conversion is visible and auditable.
func (s *Service) convertToAccount(localMinor int64, localCurrency string) FXNote {
	note := FXNote{
		FromCurrency: localCurrency,
		ToCurrency:   s.accountCurrency,
		LocalMinor:   localMinor,
		AccountMinor: localMinor,
		Rate:         1,
		Source:       "identical currency, no conversion applied",
	}
	if localCurrency == "" || s.accountCurrency == "" || localCurrency == s.accountCurrency {
		return note
	}
	if localCurrency == "INR" && s.accountCurrency == "USD" && s.fxRateINRPerUSD > 0 {
		usd := float64(localMinor) / s.fxRateINRPerUSD
		note.AccountMinor = int64(usd + 0.5)
		note.Rate = s.fxRateINRPerUSD
		note.Source = fmt.Sprintf("configured indicative rate of %.2f INR per USD (no live FX feed)", s.fxRateINRPerUSD)
		return note
	}
	// An unsupported pair is recorded rather than silently mis-converted.
	note.Source = fmt.Sprintf("no configured conversion from %s to %s; amount forwarded unchanged",
		localCurrency, s.accountCurrency)
	return note
}
