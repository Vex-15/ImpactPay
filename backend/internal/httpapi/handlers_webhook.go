package httpapi

import (
	"io"
	"net/http"
)

// handlePayPalWebhook receives provider notifications.
//
// Signature verification happens inside the payments service before any state
// change, and a duplicate event id is treated as already-processed so PayPal's
// at-least-once delivery is safe. A rejected signature is answered with 400 so
// the provider records the failure rather than retrying an untrusted payload.
func (s *Server) handlePayPalWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", "The webhook body could not be read.")
		return
	}
	defer func() { _ = r.Body.Close() }()

	rec, err := s.Payments.ProcessWebhook(ctx, r.Header, body)
	if err != nil {
		if rec != nil && !rec.SignatureVerified {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"received":  true,
				"verified":  false,
				"event_id":  rec.EventID,
				"status":    rec.Status,
				"processed": false,
				"error": map[string]any{
					"code":    "webhook_rejected",
					"message": "The webhook signature could not be verified.",
				},
			})
			return
		}
		if rec == nil {
			s.handleServiceError(w, err, "paypal webhook")
			return
		}
		// The event was persisted but applying it failed; report it so the
		// provider retries and the admin can see the failure.
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"received":  true,
			"verified":  rec.SignatureVerified,
			"event_id":  rec.EventID,
			"status":    rec.Status,
			"processed": false,
			"error": map[string]any{
				"code":    "webhook_processing_failed",
				"message": "The webhook was recorded but could not be applied.",
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"received":   true,
		"verified":   rec.SignatureVerified,
		"event_id":   rec.EventID,
		"event_type": rec.EventType,
		"status":     rec.Status,
		"processed":  rec.Processed,
	})
}
