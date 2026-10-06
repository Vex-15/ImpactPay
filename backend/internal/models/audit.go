package models

import "time"

// AuditAction enumerates the immutable-style events ImpactPay records. Every
// state transition that matters to trust or money emits one.
type AuditAction string

const (
	AuditUserRegistered        AuditAction = "USER_REGISTERED"
	AuditUserLoggedIn          AuditAction = "USER_LOGGED_IN"
	AuditOrganizationCreated   AuditAction = "ORGANIZATION_CREATED"
	AuditOrganizationVerified  AuditAction = "ORGANIZATION_VERIFIED"
	AuditOrganizationRejected  AuditAction = "ORGANIZATION_REJECTED"
	AuditProjectCreated        AuditAction = "PROJECT_CREATED"
	AuditProjectPublished      AuditAction = "PROJECT_PUBLISHED"
	AuditProjectUpdated        AuditAction = "PROJECT_UPDATED"
	AuditProjectSuspended      AuditAction = "PROJECT_SUSPENDED"
	AuditMilestoneStarted      AuditAction = "MILESTONE_STARTED"
	AuditEvidenceSubmitted     AuditAction = "EVIDENCE_SUBMITTED"
	AuditEvidenceUploaded      AuditAction = "EVIDENCE_UPLOADED"
	AuditVerificationStarted   AuditAction = "AI_VERIFICATION_STARTED"
	AuditVerificationCompleted AuditAction = "AI_VERIFICATION_COMPLETED"
	AuditMilestoneApproved     AuditAction = "MILESTONE_APPROVED"
	AuditMilestoneHeld         AuditAction = "MILESTONE_HELD"
	AuditMoreEvidenceRequested AuditAction = "MORE_EVIDENCE_REQUESTED"
	AuditMilestoneCompleted    AuditAction = "MILESTONE_COMPLETED"
	AuditHumanOverride         AuditAction = "AI_DECISION_OVERRIDDEN"
	AuditDonationCreated       AuditAction = "DONATION_CREATED"
	AuditPayPalOrderCreated    AuditAction = "PAYPAL_ORDER_CREATED"
	AuditPaymentCompleted      AuditAction = "PAYPAL_PAYMENT_COMPLETED"
	AuditPaymentFailed         AuditAction = "PAYPAL_PAYMENT_FAILED"
	AuditWebhookReceived       AuditAction = "PAYPAL_WEBHOOK_RECEIVED"
	AuditRefundExecuted        AuditAction = "REFUND_EXECUTED"
	AuditPaymentActionExecuted AuditAction = "PAYMENT_ACTION_EXECUTED"
	AuditPaymentActionBlocked  AuditAction = "PAYMENT_ACTION_BLOCKED"
	AuditProjectUpdatePosted   AuditAction = "PROJECT_UPDATE_POSTED"
)

// AuditEvent is an append-only trust record. ImpactPay never updates or deletes
// audit rows; corrections are recorded as new events.
type AuditEvent struct {
	ID          string      `json:"id"`
	ActorID     string      `json:"actor_id"`
	ActorRole   string      `json:"actor_role"`
	ActorLabel  string      `json:"actor_label"`
	Action      AuditAction `json:"action"`
	EntityType  string      `json:"entity_type"`
	EntityID    string      `json:"entity_id"`
	ProjectID   string      `json:"project_id"`
	MilestoneID string      `json:"milestone_id"`
	DonationID  string      `json:"donation_id"`
	Severity    string      `json:"severity"`
	Summary     string      `json:"summary"`
	Data        string      `json:"-"`
	CreatedAt   time.Time   `json:"created_at"`
}

// Notification is an in-app message for one user.
type Notification struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Link      string    `json:"link"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}
