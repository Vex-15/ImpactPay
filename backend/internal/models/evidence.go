package models

import "time"

// EvidenceKind classifies an uploaded artefact.
type EvidenceKind string

const (
	EvidenceInvoice           EvidenceKind = "INVOICE"
	EvidenceReceipt           EvidenceKind = "RECEIPT"
	EvidencePhotograph        EvidenceKind = "PHOTOGRAPH"
	EvidenceReport            EvidenceKind = "REPORT"
	EvidenceDocument          EvidenceKind = "DOCUMENT"
	EvidenceBeneficiaryRecord EvidenceKind = "BENEFICIARY_RECORD"
	EvidenceLocation          EvidenceKind = "LOCATION"
	EvidenceUnitRegistry      EvidenceKind = "UNIT_REGISTRY"
)

// SubmissionStatus tracks the review state of one batch of evidence.
type SubmissionStatus string

const (
	SubmissionSubmitted             SubmissionStatus = "SUBMITTED"
	SubmissionVerifying             SubmissionStatus = "VERIFYING"
	SubmissionVerified              SubmissionStatus = "VERIFIED"
	SubmissionHeld                  SubmissionStatus = "HELD"
	SubmissionMoreEvidenceRequested SubmissionStatus = "MORE_EVIDENCE_REQUESTED"
)

// EvidenceSubmission groups the artefacts uploaded for one review round. A
// milestone can have several submissions: the first may be held, and a later one
// supplies the remainder.
type EvidenceSubmission struct {
	ID             string           `json:"id"`
	MilestoneID    string           `json:"milestone_id"`
	ProjectID      string           `json:"project_id"`
	SubmittedBy    string           `json:"submitted_by"`
	Sequence       int              `json:"sequence"`
	Note           string           `json:"note"`
	Status         SubmissionStatus `json:"status"`
	VerificationID string           `json:"verification_id,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
}

// Evidence is a stored artefact with its extracted structured facts.
type Evidence struct {
	ID             string         `json:"id"`
	SubmissionID   string         `json:"submission_id"`
	MilestoneID    string         `json:"milestone_id"`
	ProjectID      string         `json:"project_id"`
	UploadedBy     string         `json:"uploaded_by"`
	Kind           EvidenceKind   `json:"kind"`
	RequirementKey string         `json:"requirement_key"`
	Filename       string         `json:"filename"`
	MimeType       string         `json:"mime_type"`
	SizeBytes      int64          `json:"size_bytes"`
	StorageKey     string         `json:"-"`
	ContentHash    string         `json:"content_hash"`
	Description    string         `json:"description"`
	Declared       EvidenceHints  `json:"declared"`
	Extracted      ExtractedFacts `json:"extracted"`
	CreatedAt      time.Time      `json:"created_at"`
}

// EvidenceHints are the structured facts the uploader asserted alongside the
// file. They are treated as *claims* by the verification pipeline and are always
// cross-checked against the artefact itself, never trusted outright.
type EvidenceHints struct {
	UnitIDs       []string `json:"unit_ids,omitempty"`
	Location      string   `json:"location,omitempty"`
	CapturedAt    string   `json:"captured_at,omitempty"`
	Villages      []string `json:"villages,omitempty"`
	AmountMinor   int64    `json:"amount_minor,omitempty"`
	Quantity      int      `json:"quantity,omitempty"`
	UnitLabel     string   `json:"unit_label,omitempty"`
	InvoiceNo     string   `json:"invoice_number,omitempty"`
	Vendor        string   `json:"vendor,omitempty"`
	Beneficiaries int      `json:"beneficiaries,omitempty"`
}

// ExtractedFacts is the structured output of the extraction stage of the
// verification pipeline. It is what evidence "says", separated from what the
// milestone "requires", so the comparison stage stays purely deterministic.
type ExtractedFacts struct {
	Kind           string   `json:"kind"`
	DocumentType   string   `json:"document_type,omitempty"`
	Vendor         string   `json:"vendor,omitempty"`
	InvoiceNumber  string   `json:"invoice_number,omitempty"`
	DocumentDate   string   `json:"document_date,omitempty"`
	AmountMinor    int64    `json:"amount_minor,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	StatedQuantity int      `json:"stated_quantity,omitempty"`
	UnitLabel      string   `json:"unit_label,omitempty"`
	UnitIDs        []string `json:"unit_ids,omitempty"`
	Beneficiaries  int      `json:"beneficiaries,omitempty"`
	Villages       []string `json:"villages,omitempty"`
	Location       string   `json:"location,omitempty"`
	Width          int      `json:"width,omitempty"`
	Height         int      `json:"height,omitempty"`
	Quality        string   `json:"quality,omitempty"` // ok | low | insufficient
	ExtractionMode string   `json:"extraction_mode,omitempty"`
	Notes          []string `json:"notes,omitempty"`
	ParseErrors    []string `json:"parse_errors,omitempty"`
}
