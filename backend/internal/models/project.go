package models

import "time"

// ProjectStatus is the lifecycle state of an impact project.
type ProjectStatus string

const (
	ProjectDraft       ProjectStatus = "DRAFT"
	ProjectFundingOpen ProjectStatus = "FUNDING_OPEN"
	ProjectInProgress  ProjectStatus = "IN_PROGRESS"
	ProjectCompleted   ProjectStatus = "COMPLETED"
	ProjectArchived    ProjectStatus = "ARCHIVED"
	ProjectSuspended   ProjectStatus = "SUSPENDED"
)

// Project is a fundable, milestone-driven impact programme.
type Project struct {
	ID                     string        `json:"id"`
	OrgID                  string        `json:"org_id"`
	Title                  string        `json:"title"`
	Slug                   string        `json:"slug"`
	Category               string        `json:"category"`
	Summary                string        `json:"summary"`
	Problem                string        `json:"problem"`
	Approach               string        `json:"approach"`
	Location               string        `json:"location"`
	Currency               string        `json:"currency"`
	GoalAmount             int64         `json:"goal_amount"`
	FundedAmount           int64         `json:"funded_amount"`
	ReservedAmount         int64         `json:"reserved_amount"`
	ReleasedAmount         int64         `json:"released_amount"`
	BeneficiaryCount       int           `json:"beneficiary_count"`
	BeneficiaryDescription string        `json:"beneficiary_description"`
	OutcomeDescription     string        `json:"outcome_description"`
	Status                 ProjectStatus `json:"status"`
	CoverSeed              string        `json:"cover_seed"`
	CreatedBy              string        `json:"created_by"`
	CreatedAt              time.Time     `json:"created_at"`
	UpdatedAt              time.Time     `json:"updated_at"`
	PublishedAt            *time.Time    `json:"published_at,omitempty"`
	CompletedAt            *time.Time    `json:"completed_at,omitempty"`
}

// FundingRatio returns funded/goal in the range 0..1.
func (p *Project) FundingRatio() float64 {
	if p.GoalAmount <= 0 {
		return 0
	}
	r := float64(p.FundedAmount) / float64(p.GoalAmount)
	if r > 1 {
		return 1
	}
	return r
}

// AvailableAmount is funding captured but not yet reserved for a milestone
// release.
func (p *Project) AvailableAmount() int64 {
	return p.FundedAmount - p.ReservedAmount
}

// ProjectUpdate is an organization-authored project news item.
type ProjectUpdate struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	AuthorID  string    `json:"author_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// EvidenceRequirementSpec declares one artefact a milestone needs.
type EvidenceRequirementSpec struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
	MinCount int    `json:"min_count"`
	Detail   string `json:"detail,omitempty"`
}
