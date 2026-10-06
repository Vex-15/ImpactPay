package models

import "time"

// MilestoneStatus is the lifecycle state of a milestone. The ordering mirrors the
// financial workflow: nothing can be released before verification approves it.
type MilestoneStatus string

const (
	MilestonePending               MilestoneStatus = "PENDING"
	MilestoneInProgress            MilestoneStatus = "IN_PROGRESS"
	MilestoneEvidenceSubmitted     MilestoneStatus = "EVIDENCE_SUBMITTED"
	MilestoneVerifying             MilestoneStatus = "VERIFYING"
	MilestoneApproved              MilestoneStatus = "APPROVED"
	MilestoneHeld                  MilestoneStatus = "HELD"
	MilestoneMoreEvidenceRequested MilestoneStatus = "MORE_EVIDENCE_REQUESTED"
	MilestonePaymentReleased       MilestoneStatus = "PAYMENT_RELEASED"
	MilestoneCompleted             MilestoneStatus = "COMPLETED"
	MilestoneRefunded              MilestoneStatus = "REFUNDED"
)

// Terminal reports whether the milestone has reached a final state.
func (s MilestoneStatus) Terminal() bool {
	return s == MilestoneCompleted || s == MilestoneRefunded
}

// Milestone is one funded, verifiable unit of work inside a project.
type Milestone struct {
	ID                   string                    `json:"id"`
	ProjectID            string                    `json:"project_id"`
	Ordinal              int                       `json:"ordinal"`
	Title                string                    `json:"title"`
	Description          string                    `json:"description"`
	Requirement          string                    `json:"requirement"`
	UnitLabel            string                    `json:"unit_label"`
	RequiredQuantity     int                       `json:"required_quantity"`
	BudgetAmount         int64                     `json:"budget_amount"`
	EvidenceRequirements []EvidenceRequirementSpec `json:"evidence_requirements"`
	Status               MilestoneStatus           `json:"status"`
	VerifiedQuantity     int                       `json:"verified_quantity"`
	CreatedAt            time.Time                 `json:"created_at"`
	UpdatedAt            time.Time                 `json:"updated_at"`
	StartedAt            *time.Time                `json:"started_at,omitempty"`
	SubmittedAt          *time.Time                `json:"submitted_at,omitempty"`
	DecidedAt            *time.Time                `json:"decided_at,omitempty"`
	CompletedAt          *time.Time                `json:"completed_at,omitempty"`
}

// VerifiedRatio returns verified/required quantity in the range 0..1.
func (m *Milestone) VerifiedRatio() float64 {
	if m.RequiredQuantity <= 0 {
		if m.Status == MilestoneCompleted || m.Status == MilestoneApproved {
			return 1
		}
		return 0
	}
	r := float64(m.VerifiedQuantity) / float64(m.RequiredQuantity)
	if r > 1 {
		return 1
	}
	return r
}

// PrimaryEvidenceKind returns the dominant evidence artefact this milestone
// expects. The verification pipeline uses it to choose which extraction tool to
// present in the audit trace (analyze_image, extract_invoice_data, ...).
func (m *Milestone) PrimaryEvidenceKind() EvidenceKind {
	for _, r := range m.EvidenceRequirements {
		if r.Required && r.Kind != "" {
			return EvidenceKind(r.Kind)
		}
	}
	for _, r := range m.EvidenceRequirements {
		if r.Kind != "" {
			return EvidenceKind(r.Kind)
		}
	}
	return EvidenceDocument
}

// FullyVerified reports whether the milestone reached 100% of its requirement.
func (m *Milestone) FullyVerified() bool {
	if m.RequiredQuantity <= 0 {
		return m.Status == MilestoneCompleted || m.Status == MilestoneApproved || m.Status == MilestonePaymentReleased
	}
	return m.VerifiedQuantity >= m.RequiredQuantity
}
