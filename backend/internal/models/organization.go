package models

import "time"

// OrganizationVerificationStatus tracks the platform review of an organization.
type OrganizationVerificationStatus string

const (
	OrgPending   OrganizationVerificationStatus = "PENDING"
	OrgVerified  OrganizationVerificationStatus = "VERIFIED"
	OrgRejected  OrganizationVerificationStatus = "REJECTED"
	OrgSuspended OrganizationVerificationStatus = "SUSPENDED"
)

// Organization is an NGO or social enterprise that publishes projects.
type Organization struct {
	ID                 string                       `json:"id"`
	UserID             string                       `json:"user_id"`
	Name               string                       `json:"name"`
	Slug               string                       `json:"slug"`
	Mission            string                       `json:"mission"`
	Description        string                       `json:"description"`
	Website            string                       `json:"website"`
	Country            string                       `json:"country"`
	RegistrationNumber string                       `json:"registration_number"`
	ContactEmail       string                       `json:"contact_email"`
	PayoutEmail        string                       `json:"payout_email"`
	VerificationStatus OrganizationVerificationStatus `json:"verification_status"`
	Verified           bool                       `json:"verified"`
	ReviewNote         string                     `json:"review_note"`
	ReviewedBy         string                     `json:"reviewed_by,omitempty"`
	ReviewedAt         *time.Time                 `json:"reviewed_at,omitempty"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}
