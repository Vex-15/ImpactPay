package models

import "time"

// VerificationDecision is the outcome of the AI verification pipeline. The
// backend treats it as an *input to policy*, never as permission: financial
// release always re-runs deterministic validation.
type VerificationDecision string

const (
	DecisionApprove       VerificationDecision = "APPROVE"
	DecisionHold          VerificationDecision = "HOLD"
	DecisionRequestMore   VerificationDecision = "REQUEST_MORE_EVIDENCE"
)

// Verification is a persisted run of the verification pipeline over one evidence
// submission.
type Verification struct {
	ID               string               `json:"id"`
	MilestoneID      string               `json:"milestone_id"`
	ProjectID        string               `json:"project_id"`
	SubmissionID     string               `json:"submission_id"`
	Status           VerificationDecision `json:"status"`
	Recommendation   VerificationDecision `json:"recommendation"`
	VerifiedQuantity int                  `json:"verified_quantity"`
	RequiredQuantity int                  `json:"required_quantity"`
	UnitLabel        string               `json:"unit_label"`
	Confidence       float64              `json:"confidence"`
	Claimed          []Claim               `json:"claimed"`
	EvidenceFound    []EvidenceFinding     `json:"evidence_found"`
	Inconsistencies  []Inconsistency       `json:"inconsistencies"`
	MissingEvidence  []MissingEvidence     `json:"missing_evidence"`
	Checks           []VerificationCheck   `json:"checks"`
	Result           VerificationResult    `json:"result"`
	Summary          string                `json:"summary"`
	Engine           string                `json:"engine"`
	Model            string                `json:"model"`
	IsOverride       bool                  `json:"is_override"`
	HumanDecision    string                `json:"human_decision,omitempty"`
	HumanNote        string                `json:"human_note,omitempty"`
	DecidedBy        string                `json:"decided_by,omitempty"`
	DecidedAt        *time.Time            `json:"decided_at,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
}

// Claim is what the organization says it did for this milestone.
type Claim struct {
	Label    string `json:"label"`
	Quantity int    `json:"quantity"`
	Unit     string `json:"unit"`
	Source   string `json:"source"`
}

// EvidenceFinding describes what the pipeline found in one artefact.
type EvidenceFinding struct {
	EvidenceID   string   `json:"evidence_id"`
	Filename     string   `json:"filename"`
	Kind         string   `json:"kind"`
	Requirement  string   `json:"requirement_key,omitempty"`
	Summary      string   `json:"summary"`
	UnitIDs      []string `json:"unit_ids,omitempty"`
	AmountMinor  int64    `json:"amount_minor,omitempty"`
	Usable       bool     `json:"usable"`
	Confidence   float64  `json:"confidence"`
	DuplicateOf  string   `json:"duplicate_of,omitempty"`
	Issue        string   `json:"issue,omitempty"`
}

// Inconsistency is a detected contradiction between claim and evidence.
type Inconsistency struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // info | warning | critical
	Detail   string `json:"detail"`
	Evidence string `json:"evidence_id,omitempty"`
}

// MissingEvidence is a required artefact that was not supplied, or was supplied
// in insufficient quantity or quality.
type MissingEvidence struct {
	RequirementKey string `json:"requirement_key"`
	Label          string `json:"label"`
	Kind           string `json:"kind"`
	Required       int    `json:"required_count"`
	Found          int    `json:"found_count"`
	Detail         string `json:"detail"`
}

// VerificationCheck is one named, deterministic assertion the pipeline evaluated.
// It is what makes the result auditable rather than a black-box opinion.
type VerificationCheck struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Status   string  `json:"status"` // pass | warn | fail
	Detail   string  `json:"detail"`
	Weight   float64 `json:"weight"`
	Observed float64 `json:"observed,omitempty"`
	Expected float64 `json:"expected,omitempty"`
	Unit     string  `json:"unit,omitempty"`
}

// FinancialCheck compares invoice/receipt amounts against the milestone budget.
type FinancialCheck struct {
	BudgetMinor       int64    `json:"budget_minor"`
	DocumentedMinor   int64    `json:"documented_minor"`
	VarianceMinor     int64    `json:"variance_minor"`
	VariancePercent   float64  `json:"variance_percent"`
	DocumentsAnalysed int      `json:"documents_analysed"`
	Currency          string   `json:"currency"`
	Status            string   `json:"status"` // ok | overspend | undocumented | mismatch
	Notes             []string `json:"notes"`
}

// VerificationStep is one executed tool call, surfaced in the Verification
// Workspace so an admin can inspect exactly how a decision was reached.
type VerificationStep struct {
	Index      int     `json:"index"`
	Tool       string  `json:"tool"`
	Title      string  `json:"title"`
	Input      string  `json:"input"`
	Output     string  `json:"output"`
	Status     string  `json:"status"` // ok | warn | fail
	DurationMS float64 `json:"duration_ms"`
}

// DuplicateGroup describes repeated artefacts detected in a submission.
type DuplicateGroup struct {
	ContentHash string   `json:"content_hash"`
	EvidenceIDs []string `json:"evidence_ids"`
	Reason      string   `json:"reason"`
}

// VerificationResult is the complete structured output of the pipeline. Every
// field is derived from stored data and is persisted verbatim.
type VerificationResult struct {
	MilestoneID      string               `json:"milestone_id"`
	ProjectID        string               `json:"project_id"`
	SubmissionID     string               `json:"submission_id"`
	Claimed          []Claim               `json:"claimed"`
	EvidenceFound    []EvidenceFinding     `json:"evidence_found"`
	VerifiedQuantity int                  `json:"verified_quantity"`
	RequiredQuantity int                  `json:"required_quantity"`
	UnitLabel        string               `json:"unit_label"`
	VerifiedUnitIDs  []string             `json:"verified_unit_ids"`
	Duplicates       []DuplicateGroup     `json:"duplicates"`
	MissingEvidence  []MissingEvidence     `json:"missing_evidence"`
	Inconsistencies  []Inconsistency       `json:"inconsistencies"`
	Financial        FinancialCheck        `json:"financial"`
	Checks           []VerificationCheck   `json:"checks"`
	Confidence       float64              `json:"confidence"`
	Recommendation   VerificationDecision `json:"recommendation"`
	Summary          string               `json:"summary"`
	NextAction       string               `json:"next_action"`
	Steps            []VerificationStep    `json:"steps"`
	Engine           string               `json:"engine"`
	Model            string               `json:"model"`
	Cumulative       bool                 `json:"cumulative"`
	PriorVerified    int                  `json:"prior_verified"`
	EvidenceCount    int                  `json:"evidence_count"`
	UsableCount      int                  `json:"usable_count"`
	DuplicateCount   int                  `json:"duplicate_count"`
	EvaluatedAt      string               `json:"evaluated_at"`
}
