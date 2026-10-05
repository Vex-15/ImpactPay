package models

import "time"

// Role enumerates the actor types ImpactPay authorises.
type Role string

const (
	RoleDonor        Role = "donor"
	RoleOrganization Role = "organization"
	RoleAdmin        Role = "admin"
)

// Valid reports whether the role is one of the known roles.
func (r Role) Valid() bool {
	switch r {
	case RoleDonor, RoleOrganization, RoleAdmin:
		return true
	}
	return false
}

// User is an authenticated account.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	FullName     string    `json:"full_name"`
	Role         Role      `json:"role"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// IsAdmin reports whether the user is an admin/verifier.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// Session is a server-side opaque session record. Tokens are stored as primary
// keys and never re-issued, so revoking a session is a single row update.
type Session struct {
	Token     string    `json:"-"`
	UserID    string    `json:"user_id"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

// Expired reports whether the session is past its expiry.
func (s *Session) Expired(now time.Time) bool { return now.After(s.ExpiresAt) }
