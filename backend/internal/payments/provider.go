// Package payments owns ImpactPay's funding state machine.
//
// It is the only package allowed to move money or change financial state, and it
// always does so inside a database transaction that also writes the payment event
// and the audit record.
//
// Two provider implementations sit behind one interface:
//
//	paypalProvider  — real PayPal Sandbox REST calls (orders, captures, refunds, payouts)
//	simulatedProvider — a deterministic stand-in used when sandbox credentials are absent
//
// The simulated provider is never presented as a real payment. Every row it
// writes is marked simulated=1, every API response carries simulated:true, and
// the UI labels it "Sandbox simulation". That distinction is deliberate: a demo
// that quietly fakes a payment is worse than one that says it is simulating.
package payments

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/paypal"
)

// Amount is a currency amount in minor units.
type Amount struct {
	Minor    int64
	Currency string
}

// OrderRequest creates a funding order for a donation.
type OrderRequest struct {
	DonationID string
	Amount     Amount
	ProjectRef string
	Description string
	ReturnURL  string
	CancelURL  string
}

// Order is a provider order.
type Order struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ApproveURL string `json:"approve_url"`
	Simulated  bool   `json:"simulated"`
	Raw        any    `json:"-"`
}

// CaptureOutcome is the result of capturing an approved order.
type CaptureOutcome struct {
	CaptureID string `json:"capture_id"`
	OrderID   string `json:"order_id"`
	Status    string `json:"status"`
	Amount    Amount `json:"amount"`
	PayerRef  string `json:"payer_ref"`
	Simulated bool   `json:"simulated"`
	Raw       any    `json:"-"`
}

// RefundOutcome is the result of a refund.
type RefundOutcome struct {
	RefundID  string `json:"refund_id"`
	CaptureID string `json:"capture_id"`
	Status    string `json:"status"`
	Amount    Amount `json:"amount"`
	Simulated bool   `json:"simulated"`
	Raw       any    `json:"-"`
}

// PayoutRequest moves released funding to the organization.
type PayoutRequest struct {
	MilestoneID string
	Amount      Amount
	Receiver    string // organization's sandbox PayPal email
	Note        string
}

// PayoutOutcome is the result of a payout.
type PayoutOutcome struct {
	PayoutID  string `json:"payout_id"`
	Status    string `json:"status"`
	Simulated bool   `json:"simulated"`
	Raw       any    `json:"-"`
}

// Provider abstracts the payment rail.
type Provider interface {
	// Name identifies the rail, e.g. "paypal_sandbox" or "simulated_sandbox".
	Name() string
	// Simulated reports whether results are simulated rather than real sandbox traffic.
	Simulated() bool
	// Live reports whether the provider can actually talk to PayPal.
	Live() bool

	CreateOrder(ctx context.Context, req OrderRequest, idempotencyKey string) (*Order, error)
	CaptureOrder(ctx context.Context, orderID, idempotencyKey string) (*CaptureOutcome, error)
	GetOrder(ctx context.Context, orderID string) (*Order, error)
	Refund(ctx context.Context, captureID string, amount Amount, note, idempotencyKey string) (*RefundOutcome, error)
	Payout(ctx context.Context, req PayoutRequest, idempotencyKey string) (*PayoutOutcome, error)
	VerifyWebhook(ctx context.Context, headers http.Header, body []byte) (bool, error)
}

// ---------------------------------------------------------------------------
// PayPal sandbox provider
// ---------------------------------------------------------------------------

// PayPalProvider performs real PayPal Sandbox REST calls.
type PayPalProvider struct {
	client *paypal.Client
}

// NewPayPalProvider wraps a PayPal sandbox client.
func NewPayPalProvider(client *paypal.Client) *PayPalProvider {
	return &PayPalProvider{client: client}
}

// Name implements Provider.
func (p *PayPalProvider) Name() string { return "paypal_sandbox" }

// Simulated implements Provider.
func (p *PayPalProvider) Simulated() bool { return false }

// Live implements Provider.
func (p *PayPalProvider) Live() bool { return p.client != nil && p.client.Enabled() }

// CreateOrder implements Provider.
func (p *PayPalProvider) CreateOrder(ctx context.Context, req OrderRequest, idempotencyKey string) (*Order, error) {
	appCtx := map[string]any{
		"brand_name":          "ImpactPay",
		"landing_page":        "LOGIN",
		"shipping_preference": "NO_SHIPPING",
		"user_action":         "PAY_NOW",
	}
	if req.ReturnURL != "" {
		appCtx["return_url"] = req.ReturnURL
	}
	if req.CancelURL != "" {
		appCtx["cancel_url"] = req.CancelURL
	}

	order, err := p.client.CreateOrder(ctx, paypal.OrderRequest{
		Intent: "CAPTURE",
		PurchaseUnits: []paypal.PurchaseUnit{{
			ReferenceID: shortRef(req.DonationID),
			CustomID:    req.DonationID,
			Description: core.Truncate(req.Description, 127),
			Amount:      paypal.AmountFromMinor(req.Amount.Minor, req.Amount.Currency),
		}},
		ApplicationContext: appCtx,
	}, idempotencyKey)
	if err != nil {
		return nil, err
	}
	return &Order{
		ID:         order.ID,
		Status:     order.Status,
		ApproveURL: order.ApproveURL(),
		Simulated:  false,
		Raw:        order,
	}, nil
}

// CaptureOrder implements Provider.
func (p *PayPalProvider) CaptureOrder(ctx context.Context, orderID, idempotencyKey string) (*CaptureOutcome, error) {
	res, err := p.client.CaptureOrder(ctx, orderID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	out := &CaptureOutcome{
		OrderID:   orderID,
		Status:    res.Status,
		Simulated: false,
		Raw:       res,
	}
	if c := res.FirstCapture(); c != nil {
		out.CaptureID = c.ID
		out.Status = c.Status
		out.Amount = Amount{Minor: minorFromDecimal(c.Amount.Value), Currency: c.Amount.CurrencyCode}
	}
	out.PayerRef = res.Payer.PayerID
	return out, nil
}

// GetOrder implements Provider.
func (p *PayPalProvider) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	order, err := p.client.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return &Order{ID: order.ID, Status: order.Status, ApproveURL: order.ApproveURL(), Raw: order}, nil
}

// Refund implements Provider.
func (p *PayPalProvider) Refund(ctx context.Context, captureID string, amount Amount, note, idempotencyKey string) (*RefundOutcome, error) {
	m := paypal.AmountFromMinor(amount.Minor, amount.Currency)
	res, err := p.client.Refund(ctx, captureID, &m, note, idempotencyKey)
	if err != nil {
		return nil, err
	}
	return &RefundOutcome{
		RefundID:  res.ID,
		CaptureID: captureID,
		Status:    res.Status,
		Amount:    amount,
		Raw:       res,
	}, nil
}

// Payout implements Provider.
func (p *PayPalProvider) Payout(ctx context.Context, req PayoutRequest, idempotencyKey string) (*PayoutOutcome, error) {
	pr := paypal.PayoutRequest{
		Items: []paypal.PayoutItem{{
			RecipientType: "EMAIL",
			Amount:        paypal.AmountFromMinor(req.Amount.Minor, req.Amount.Currency),
			Receiver:      req.Receiver,
			Note:          core.Truncate(req.Note, 255),
			SenderItemID:  req.MilestoneID,
			Purpose:       "GOODS",
		}},
	}
	pr.SenderBatchHeader.SenderBatchID = "impactpay-" + shortRef(req.MilestoneID)
	pr.SenderBatchHeader.EmailSubject = "ImpactPay milestone funding released"

	res, err := p.client.CreatePayout(ctx, pr, idempotencyKey)
	if err != nil {
		return nil, err
	}
	return &PayoutOutcome{
		PayoutID: res.BatchHeader.PayoutBatchID,
		Status:   res.BatchHeader.BatchStatus,
		Raw:      res,
	}, nil
}

// VerifyWebhook implements Provider.
func (p *PayPalProvider) VerifyWebhook(ctx context.Context, headers http.Header, body []byte) (bool, error) {
	return p.client.VerifyWebhookSignature(ctx, headers, body)
}

// ---------------------------------------------------------------------------
// Simulated provider
// ---------------------------------------------------------------------------

// SimulatedProvider stands in for PayPal when sandbox credentials are not
// configured. It exists so DEMO_MODE is reproducible offline. It produces
// deterministic identifiers and every artefact it touches is flagged simulated.
type SimulatedProvider struct {
	mu     sync.Mutex
	issued int
}

// NewSimulatedProvider creates the offline stand-in.
func NewSimulatedProvider() *SimulatedProvider { return &SimulatedProvider{} }

// Name implements Provider.
func (s *SimulatedProvider) Name() string { return "simulated_sandbox" }

// Simulated implements Provider.
func (s *SimulatedProvider) Simulated() bool { return true }

// Live implements Provider.
func (s *SimulatedProvider) Live() bool { return false }

func (s *SimulatedProvider) next(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issued++
	return fmt.Sprintf("SIM-%s-%s-%03d", prefix, time.Now().UTC().Format("20060102"), s.issued)
}

// CreateOrder implements Provider.
func (s *SimulatedProvider) CreateOrder(_ context.Context, req OrderRequest, _ string) (*Order, error) {
	if req.Amount.Minor <= 0 {
		return nil, fmt.Errorf("payments: simulated order requires a positive amount")
	}
	return &Order{
		ID:         s.next("ORDER"),
		Status:     "CREATED",
		ApproveURL: "",
		Simulated:  true,
	}, nil
}

// CaptureOrder implements Provider.
func (s *SimulatedProvider) CaptureOrder(_ context.Context, orderID, _ string) (*CaptureOutcome, error) {
	if strings.TrimSpace(orderID) == "" {
		return nil, fmt.Errorf("payments: simulated capture requires an order id")
	}
	return &CaptureOutcome{
		CaptureID: s.next("CAPTURE"),
		OrderID:   orderID,
		Status:    "COMPLETED",
		Simulated: true,
	}, nil
}

// GetOrder implements Provider.
func (s *SimulatedProvider) GetOrder(_ context.Context, orderID string) (*Order, error) {
	return &Order{ID: orderID, Status: "CREATED", Simulated: true}, nil
}

// Refund implements Provider.
func (s *SimulatedProvider) Refund(_ context.Context, captureID string, amount Amount, _ string, _ string) (*RefundOutcome, error) {
	return &RefundOutcome{
		RefundID:  s.next("REFUND"),
		CaptureID: captureID,
		Status:    "COMPLETED",
		Amount:    amount,
		Simulated: true,
	}, nil
}

// Payout implements Provider.
func (s *SimulatedProvider) Payout(_ context.Context, _ PayoutRequest, _ string) (*PayoutOutcome, error) {
	return &PayoutOutcome{PayoutID: s.next("PAYOUT"), Status: "SUCCESS", Simulated: true}, nil
}

// VerifyWebhook implements Provider. The simulation cannot verify a signature
// cryptographically, so it refuses rather than pretending to.
func (s *SimulatedProvider) VerifyWebhook(_ context.Context, _ http.Header, _ []byte) (bool, error) {
	return false, fmt.Errorf("payments: webhook signature verification requires live PayPal sandbox credentials")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// minorFromDecimal parses "10.00" into 1000 minor units.
func minorFromDecimal(v string) int64 {
	amt, ok := core.ParseAmount(v)
	if !ok {
		return 0
	}
	return int64(amt)
}

func shortRef(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
