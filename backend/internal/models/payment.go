package models

import "time"

// DonationStatus tracks the funding state of a single donor contribution. These
// are deliberately named "funding states" rather than "escrow": ImpactPay pools
// contributions in its own ledger and releases milestone payments conditionally.
type DonationStatus string

const (
	DonationPending       DonationStatus = "PENDING"
	DonationOrderCreated  DonationStatus = "ORDER_CREATED"
	DonationCaptured      DonationStatus = "CAPTURED"
	DonationFailed        DonationStatus = "FAILED"
	DonationCancelled     DonationStatus = "CANCELLED"
	DonationRefunded      DonationStatus = "REFUNDED"
	DonationFullyReleased DonationStatus = "RELEASED"
)

// Donation is one donor contribution to one project.
type Donation struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"project_id"`
	DonorID         string         `json:"donor_id"`
	Amount          int64          `json:"amount"`
	Currency        string         `json:"currency"`
	Status          DonationStatus `json:"status"`
	PayPalOrderID   string         `json:"paypal_order_id"`
	PayPalCaptureID string         `json:"paypal_capture_id"`
	CaptureStatus   string         `json:"capture_status"`
	Provider        string         `json:"provider"`
	Simulated       bool           `json:"simulated"`
	Note            string         `json:"note"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	CapturedAt      *time.Time     `json:"captured_at,omitempty"`
	RefundedAt      *time.Time     `json:"refunded_at,omitempty"`
}

// MilestoneFunding attributes part of a captured donation to a milestone. It is
// the join that makes "where did my money go" answerable by query.
type MilestoneFunding struct {
	ID          string    `json:"id"`
	MilestoneID string    `json:"milestone_id"`
	ProjectID   string    `json:"project_id"`
	DonationID  string    `json:"donation_id"`
	Amount      int64     `json:"amount"`
	CreatedAt   time.Time `json:"created_at"`
}

// PaymentEventType enumerates every persisted financial state transition.
type PaymentEventType string

const (
	EventOrderCreated       PaymentEventType = "PAYPAL_ORDER_CREATED"
	EventOrderApproved      PaymentEventType = "PAYPAL_ORDER_APPROVED"
	EventPaymentCaptured    PaymentEventType = "PAYPAL_PAYMENT_COMPLETED"
	EventPaymentFailed      PaymentEventType = "PAYPAL_PAYMENT_FAILED"
	EventWebhookReceived    PaymentEventType = "PAYPAL_WEBHOOK_RECEIVED"
	EventRefundRequested    PaymentEventType = "REFUND_REQUESTED"
	EventRefundCompleted    PaymentEventType = "REFUND_EXECUTED"
	EventReleaseAttempted   PaymentEventType = "PAYMENT_ACTION_ATTEMPTED"
	EventReleaseCompleted   PaymentEventType = "PAYMENT_ACTION_EXECUTED"
	EventReleaseBlocked     PaymentEventType = "PAYMENT_ACTION_BLOCKED"
	EventPayoutCompleted    PaymentEventType = "PAYOUT_EXECUTED"
	EventFundingAllocated   PaymentEventType = "FUNDING_ALLOCATED"
)

// PaymentEvent is an append-only financial log entry. It complements audit_events
// by carrying provider references and amounts.
type PaymentEvent struct {
	ID          string           `json:"id"`
	DonationID  string           `json:"donation_id"`
	ProjectID   string           `json:"project_id"`
	MilestoneID string           `json:"milestone_id"`
	Type        PaymentEventType `json:"type"`
	Provider    string           `json:"provider"`
	ProviderRef string           `json:"provider_ref"`
	Amount      int64            `json:"amount"`
	Currency    string           `json:"currency"`
	Status      string           `json:"status"`
	Detail      string           `json:"detail"`
	Raw         string           `json:"-"`
	ActorID     string           `json:"actor_id"`
	Simulated   bool             `json:"simulated"`
	CreatedAt   time.Time        `json:"created_at"`
}

// MilestonePaymentKind distinguishes the conditional release prototype's
// operations.
type MilestonePaymentKind string

const (
	PaymentRelease MilestonePaymentKind = "RELEASE"
	PaymentRefund  MilestonePaymentKind = "REFUND"
	PaymentPayout  MilestonePaymentKind = "PAYOUT"
)

// MilestonePaymentStatus is the outcome of a milestone payment action.
type MilestonePaymentStatus string

const (
	MsPaymentPending  MilestonePaymentStatus = "PENDING"
	MsPaymentExecuted MilestonePaymentStatus = "EXECUTED"
	MsPaymentBlocked  MilestonePaymentStatus = "BLOCKED"
	MsPaymentFailed   MilestonePaymentStatus = "FAILED"
	MsPaymentSimulated MilestonePaymentStatus = "SIMULATED"
)

// PolicyCheck is one deterministic gate evaluated before money moves.
type PolicyCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// FXNote records the indicative currency conversion applied to a release.
// PayPal settles in the account's currency while a project budgets in its own, so
// the conversion is stored explicitly instead of being hidden inside one number.
type FXNote struct {
	FromCurrency string  `json:"from_currency"`
	ToCurrency   string  `json:"to_currency"`
	Rate         float64 `json:"rate"`
	LocalMinor   int64   `json:"local_minor"`
	AccountMinor int64   `json:"account_minor"`
	Source       string  `json:"source"`
}

// MilestonePayment records a conditional release attempt against a milestone.
type MilestonePayment struct {
	ID            string                 `json:"id"`
	MilestoneID   string                 `json:"milestone_id"`
	ProjectID     string                 `json:"project_id"`
	OrgID         string                 `json:"org_id"`
	DonationID    string                 `json:"donation_id,omitempty"`
	Kind          MilestonePaymentKind   `json:"kind"`
	Amount        int64                  `json:"amount"`
	Currency      string                 `json:"currency"`
	Status        MilestonePaymentStatus `json:"status"`
	Provider      string                 `json:"provider"`
	PayPalRef     string                 `json:"paypal_ref"`
	PayPalStatus  string                 `json:"paypal_status"`
	PolicyChecks  []PolicyCheck          `json:"policy_checks"`
	FX            FXNote                 `json:"fx"`
	BlockedReason string                 `json:"blocked_reason"`
	ExecutedBy    string                 `json:"executed_by"`
	Simulated     bool                   `json:"simulated"`
	CreatedAt     time.Time              `json:"created_at"`
	ExecutedAt    *time.Time             `json:"executed_at,omitempty"`
}

// Passed reports whether every policy gate succeeded.
func (p *MilestonePayment) Passed() bool {
	for _, c := range p.PolicyChecks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// WebhookEvent stores a verified inbound provider notification.
type WebhookEvent struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	EventID           string    `json:"event_id"`
	EventType         string    `json:"event_type"`
	ResourceType      string    `json:"resource_type"`
	ResourceID        string    `json:"resource_id"`
	SignatureVerified bool      `json:"signature_verified"`
	Processed         bool      `json:"processed"`
	Status            string    `json:"status"`
	Payload           string    `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
}
