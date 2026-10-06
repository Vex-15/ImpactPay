package paypal

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/core"
)

// ---------------------------------------------------------------------------
// Orders v2
// ---------------------------------------------------------------------------

// Money is a PayPal currency amount.
type Money struct {
	CurrencyCode string `json:"currency_code"`
	Value        string `json:"value"`
}

// AmountFromMinor renders a minor-unit amount as PayPal's decimal string.
func AmountFromMinor(minor int64, currency string) Money {
	return Money{
		CurrencyCode: strings.ToUpper(currency),
		Value:        fmt.Sprintf("%d.%02d", minor/100, abs(minor%100)),
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// PurchaseUnit is one funding unit inside an order.
type PurchaseUnit struct {
	ReferenceID string `json:"reference_id,omitempty"`
	CustomID    string `json:"custom_id,omitempty"`
	Description string `json:"description,omitempty"`
	Amount      Money  `json:"amount"`
	Payee       *Payee `json:"payee,omitempty"`
}

// Payee is the receiving account for a purchase unit.
type Payee struct {
	EmailAddress string `json:"email_address,omitempty"`
	MerchantID   string `json:"merchant_id,omitempty"`
}

// OrderRequest creates a PayPal order.
type OrderRequest struct {
	Intent             string         `json:"intent"`
	PurchaseUnits      []PurchaseUnit `json:"purchase_units"`
	ApplicationContext map[string]any `json:"application_context,omitempty"`
}

// Link is a HATEOAS link returned by PayPal.
type Link struct {
	Href   string `json:"href"`
	Rel    string `json:"rel"`
	Method string `json:"method"`
}

// Order is a PayPal order resource.
type Order struct {
	ID            string         `json:"id"`
	Status        string         `json:"status"`
	Intent        string         `json:"intent"`
	CreateTime    string         `json:"create_time"`
	PurchaseUnits []PurchaseUnit `json:"purchase_units"`
	Links         []Link         `json:"links"`
}

// ApproveURL returns the buyer-facing approval link.
func (o *Order) ApproveURL() string {
	for _, l := range o.Links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			return l.Href
		}
	}
	return ""
}

// CreateOrder creates an order for a donation.
func (c *Client) CreateOrder(ctx context.Context, req OrderRequest, idempotencyKey string) (*Order, error) {
	if req.Intent == "" {
		req.Intent = "CAPTURE"
	}
	var out Order
	if err := c.do(ctx, http.MethodPost, "/v2/checkout/orders", req, idempotencyKey, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetOrder retrieves an order, used to reconcile state and on webhook arrival.
func (c *Client) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	var out Order
	if err := c.do(ctx, http.MethodGet, "/v2/checkout/orders/"+urlPath(orderID), nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Capture is one capture inside a capture response.
type Capture struct {
	ID                        string `json:"id"`
	Status                    string `json:"status"`
	Amount                    Money  `json:"amount"`
	FinalCapture              bool   `json:"final_capture"`
	CreateTime                string `json:"create_time"`
	SellerReceivableBreakdown struct {
		GrossAmount Money `json:"gross_amount"`
		PayPalFee   Money `json:"paypal_fee"`
		NetAmount   Money `json:"net_amount"`
	} `json:"seller_receivable_breakdown"`
}

// CaptureResult is the outcome of capturing an order.
type CaptureResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	OrderID string `json:"-"`
	Payer   struct {
		PayerID      string `json:"payer_id"`
		EmailAddress string `json:"email_address"`
		Name         struct {
			GivenName string `json:"given_name"`
			Surname   string `json:"surname"`
		} `json:"name"`
	} `json:"payer"`
	PurchaseUnits []struct {
		ReferenceID string `json:"reference_id"`
		Payments    struct {
			Captures []Capture `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
}

// FirstCapture returns the first capture in a result, or nil.
func (r *CaptureResult) FirstCapture() *Capture {
	for _, pu := range r.PurchaseUnits {
		if len(pu.Payments.Captures) > 0 {
			return &pu.Payments.Captures[0]
		}
	}
	return nil
}

// CaptureOrder captures an approved order. The idempotency key makes a retried
// capture safe: PayPal returns the original result instead of double-charging.
func (c *Client) CaptureOrder(ctx context.Context, orderID, idempotencyKey string) (*CaptureResult, error) {
	var out CaptureResult
	if err := c.do(ctx, http.MethodPost,
		"/v2/checkout/orders/"+urlPath(orderID)+"/capture", struct{}{}, idempotencyKey, &out); err != nil {
		return nil, err
	}
	out.OrderID = orderID
	return &out, nil
}

// ---------------------------------------------------------------------------
// Refunds (Payments v2)
// ---------------------------------------------------------------------------

// RefundResult is the outcome of a capture refund.
type RefundResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Amount     Money  `json:"amount"`
	CreateTime string `json:"create_time"`
}

// Refund refunds a capture in full or in part.
func (c *Client) Refund(ctx context.Context, captureID string, amount *Money, note string, idempotencyKey string) (*RefundResult, error) {
	payload := map[string]any{}
	if amount != nil {
		payload["amount"] = *amount
	}
	if note != "" {
		payload["note_to_payer"] = core.Truncate(note, 255)
	}
	var out RefundResult
	if err := c.do(ctx, http.MethodPost,
		"/v2/payments/captures/"+urlPath(captureID)+"/refund", payload, idempotencyKey, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Payouts (Payments v1)
// ---------------------------------------------------------------------------

// PayoutItem is one payout recipient.
type PayoutItem struct {
	RecipientType string `json:"recipient_type"`
	Amount        Money  `json:"amount"`
	Receiver      string `json:"receiver"`
	Note          string `json:"note,omitempty"`
	SenderItemID  string `json:"sender_item_id,omitempty"`
	Purpose       string `json:"purpose,omitempty"`
}

// PayoutRequest creates a batch payout.
type PayoutRequest struct {
	SenderBatchHeader struct {
		SenderBatchID string `json:"sender_batch_id"`
		EmailSubject  string `json:"email_subject"`
		EmailMessage  string `json:"email_message,omitempty"`
	} `json:"sender_batch_header"`
	Items []PayoutItem `json:"items"`
}

// PayoutResult is the outcome of a batch payout.
type PayoutResult struct {
	BatchHeader struct {
		PayoutBatchID string `json:"payout_batch_id"`
		BatchStatus   string `json:"batch_status"`
	} `json:"batch_header"`
}

// CreatePayout moves the released milestone funding to the organization's
// sandbox PayPal account.
//
// The funding has already been captured by ImpactPay's own account, so the payout
// is what actually transfers it onward. PayPal payouts are not reversible by the
// sender, which is precisely why the release policy is validated in the database
// transaction before this call is made.
func (c *Client) CreatePayout(ctx context.Context, req PayoutRequest, idempotencyKey string) (*PayoutResult, error) {
	var out PayoutResult
	if err := c.do(ctx, http.MethodPost, "/v1/payments/payouts", req, idempotencyKey, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

// WebhookEvent is the subset of a webhook payload ImpactPay acts on.
type WebhookEvent struct {
	ID           string `json:"id"`
	EventType    string `json:"event_type"`
	CreateTime   string `json:"create_time"`
	ResourceType string `json:"resource_type"`
	Resource     struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		Amount            Money  `json:"amount"`
		CustomID          string `json:"custom_id"`
		SupplementaryData struct {
			RelatedIDs struct {
				OrderID   string `json:"order_id"`
				CaptureID string `json:"capture_id"`
				RefundID  string `json:"refund_id"`
			} `json:"related_ids"`
		} `json:"supplementary_data"`
	} `json:"resource"`
}

// VerificationResult is PayPal's verdict on a webhook signature.
type VerificationResult struct {
	VerificationStatus string `json:"verification_status"`
}

// VerifyWebhookSignature asks PayPal to validate the webhook signature headers.
// Returns true only when PayPal answers SUCCESS.
func (c *Client) VerifyWebhookSignature(ctx context.Context, headers http.Header, rawBody []byte) (bool, error) {
	if !c.Enabled() {
		return false, fmt.Errorf("paypal: credentials are not configured")
	}
	if c.cfg.WebhookID == "" {
		return false, fmt.Errorf("paypal: PAYPAL_WEBHOOK_ID is not configured, so webhook signatures cannot be verified")
	}

	get := func(name string) string { return headers.Get(name) }
	required := map[string]string{
		"auth_algo":         get("Paypal-Auth-Algo"),
		"cert_url":          get("Paypal-Cert-Url"),
		"transmission_id":   get("Paypal-Transmission-Id"),
		"transmission_sig":  get("Paypal-Transmission-Sig"),
		"transmission_time": get("Paypal-Transmission-Time"),
	}
	for k, v := range required {
		if strings.TrimSpace(v) == "" {
			return false, fmt.Errorf("paypal: webhook is missing the %s header", k)
		}
	}

	payload := map[string]any{
		"auth_algo":         required["auth_algo"],
		"cert_url":          required["cert_url"],
		"transmission_id":   required["transmission_id"],
		"transmission_sig":  required["transmission_sig"],
		"transmission_time": required["transmission_time"],
		"webhook_id":        c.cfg.WebhookID,
		"webhook_event":     json.RawMessage(rawBody),
	}

	var out VerificationResult
	if err := c.do(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", payload, "", &out); err != nil {
		return false, err
	}
	// Constant-time compare keeps the check uniform regardless of verdict.
	ok := subtle.ConstantTimeCompare([]byte(out.VerificationStatus), []byte("SUCCESS")) == 1
	return ok, nil
}

// DecodeWebhook parses a raw webhook body.
func DecodeWebhook(raw []byte) (*WebhookEvent, error) {
	var ev WebhookEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("paypal: decode webhook: %w", err)
	}
	return &ev, nil
}

// Health probes the sandbox and reports the round-trip latency.
func (c *Client) Health(ctx context.Context) (string, time.Duration, error) {
	started := time.Now()
	if !c.Enabled() {
		return "", 0, fmt.Errorf("paypal: credentials are not configured")
	}
	if _, err := c.token(ctx); err != nil {
		return "", 0, err
	}
	return "reachable", time.Since(started), nil
}

// urlPath escapes a resource id for a URL path segment.
func urlPath(id string) string {
	return strings.ReplaceAll(strings.TrimSpace(id), "/", "")
}

// MustReadBody is a test helper that drains a body safely.
func MustReadBody(r io.ReadCloser) string {
	defer func() { _ = r.Close() }()
	b, _ := io.ReadAll(r)
	return string(b)
}
