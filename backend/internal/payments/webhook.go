package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/paypal"
)

// decodeWebhookBody parses a raw PayPal webhook into the reduced event shape.
func decodeWebhookBody(raw []byte) (*paypal.WebhookEvent, error) {
	return paypal.DecodeWebhook(raw)
}

// persistWebhook stores the inbound notification exactly once. A duplicate
// event id is treated as already-processed rather than reprocessed, which is what
// makes PayPal's at-least-once webhook delivery safe.
func (s *Service) persistWebhook(ctx context.Context, rec *models.WebhookEvent) error {
	if rec.EventID != "" {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM webhook_events WHERE event_id = ?`, rec.EventID).Scan(&n); err != nil {
			return fmt.Errorf("payments: check webhook: %w", err)
		}
		if n > 0 {
			rec.Processed = true
			rec.Status = rec.Status + "_DUPLICATE"
			return nil
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_events (
			id, provider, event_id, event_type, resource_type, resource_id,
			signature_verified, processed, status, payload, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Provider, rec.EventID, rec.EventType, rec.ResourceType, rec.ResourceID,
		boolToInt(rec.SignatureVerified), boolToInt(rec.Processed), rec.Status,
		rec.Payload, core.FormatTime(rec.CreatedAt))
	if err != nil {
		return fmt.Errorf("payments: persist webhook: %w", err)
	}
	return nil
}

func (s *Service) markWebhookProcessed(ctx context.Context, id string, processed bool, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_events SET processed = ?, status = ? WHERE id = ?`,
		boolToInt(processed), core.Truncate(status, 300), id)
	return err
}

// applyWebhook routes a verified event to its handler.
func (s *Service) applyWebhook(ctx context.Context, raw []byte) error {
	ev, err := decodeWebhookBody(raw)
	if err != nil {
		return err
	}

	switch ev.EventType {
	case "PAYMENT.CAPTURE.COMPLETED", "CHECKOUT.ORDER.COMPLETED":
		orderID := ev.Resource.SupplementaryData.RelatedIDs.OrderID
		if orderID == "" {
			orderID = ev.Resource.ID
		}
		d, derr := s.donations.ByOrderID(ctx, orderID)
		if derr != nil {
			return fmt.Errorf("payments: webhook references unknown order %s: %w", orderID, derr)
		}
		captureID := ev.Resource.SupplementaryData.RelatedIDs.CaptureID
		if captureID == "" {
			captureID = ev.Resource.ID
		}
		_, cerr := s.ApplyConfirmedCapture(ctx, d.ID, captureID, ev.Resource.Status, false)
		if cerr != nil && cerr != ErrAlreadyCaptured {
			return cerr
		}
		return nil

	case "PAYMENT.CAPTURE.DENIED", "PAYMENT.CAPTURE.REVERSED":
		d, derr := s.donations.ByOrderID(ctx, ev.Resource.SupplementaryData.RelatedIDs.OrderID)
		if derr != nil {
			return fmt.Errorf("payments: webhook references unknown order: %w", derr)
		}
		return s.db.RunInTx(ctx, func(tx *db.Tx) error {
			if err := s.donations.SetStatus(ctx, tx, d.ID, models.DonationFailed); err != nil {
				return err
			}
			return s.recordEvent(ctx, tx, models.PaymentEvent{
				DonationID: d.ID, ProjectID: d.ProjectID, Type: models.EventPaymentFailed,
				ProviderRef: ev.Resource.ID, Amount: d.Amount, Currency: d.Currency,
				Status: ev.Resource.Status, Detail: "reported by PayPal webhook: " + ev.EventType,
			})
		})

	case "PAYMENT.PAYOUTSBATCH.SUCCESS", "PAYMENT.PAYOUTSBATCH.DENIED":
		// Payout outcomes are recorded against the milestone payment that
		// requested them.
		status := "EXECUTED"
		if strings.HasSuffix(ev.EventType, "DENIED") {
			status = "FAILED"
		}
		var mpID string
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM milestone_payments WHERE paypal_ref = ? LIMIT 1`, ev.Resource.ID).Scan(&mpID)
		if err != nil {
			return nil // nothing we track; not an error
		}
		_, uerr := s.db.ExecContext(ctx, `
			UPDATE milestone_payments SET paypal_status = ?, status = ? WHERE id = ?`,
			ev.Resource.Status, status, mpID)
		return uerr

	default:
		// Unhandled event types are stored and audited, not failed.
		return nil
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func joinAnd(parts []string) string {
	return strings.Join(parts, " AND ")
}

func rawOrEmpty(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

// MarshalRaw is a small helper for persisting provider payloads.
func MarshalRaw(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
