// Package organizations owns NGO profiles and the platform review that grants a
// verified badge.
package organizations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
)

// Sentinel errors.
var (
	ErrNotFound      = errors.New("organization not found")
	ErrNameRequired  = errors.New("organization name is required")
	ErrAlreadyExists = errors.New("this account already has an organization profile")
	ErrSlugTaken     = errors.New("an organization with that name already exists")
)

// Service provides organization operations.
type Service struct {
	db    *db.DB
	audit *audit.Service
}

// New creates the organizations service.
func New(database *db.DB, auditSvc *audit.Service) *Service {
	return &Service{db: database, audit: auditSvc}
}

// CreateInput is the organization onboarding payload.
type CreateInput struct {
	UserID             string
	Name               string
	Mission            string
	Description        string
	Website            string
	Country            string
	RegistrationNumber string
	ContactEmail       string
}

// Create registers an organization profile for a user.
func (s *Service) Create(ctx context.Context, in CreateInput) (*models.Organization, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}

	if existing, err := s.ByUserID(ctx, in.UserID); err == nil && existing != nil {
		return nil, ErrAlreadyExists
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	org := &models.Organization{
		ID:                 core.NewID(),
		UserID:             in.UserID,
		Name:               name,
		Slug:               core.Slugify(name),
		Mission:            strings.TrimSpace(in.Mission),
		Description:        strings.TrimSpace(in.Description),
		Website:            strings.TrimSpace(in.Website),
		Country:            strings.TrimSpace(in.Country),
		RegistrationNumber: strings.TrimSpace(in.RegistrationNumber),
		ContactEmail:       strings.ToLower(strings.TrimSpace(in.ContactEmail)),
		VerificationStatus: models.OrgPending,
		Verified:           false,
		CreatedAt:          core.Now(),
		UpdatedAt:          core.Now(),
	}
	if org.Slug == "" {
		org.Slug = "org-" + org.ID[:8]
	}

	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		slug, err := uniqueSlug(ctx, tx, org.Slug, "")
		if err != nil {
			return err
		}
		org.Slug = slug

		_, err = tx.ExecContext(ctx, `
			INSERT INTO organizations (
				id, user_id, name, slug, mission, description, website, country,
				registration_number, contact_email, verification_status, verified,
				review_note, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)`,
			org.ID, org.UserID, org.Name, org.Slug, org.Mission, org.Description,
			org.Website, org.Country, org.RegistrationNumber, org.ContactEmail,
			string(org.VerificationStatus), core.FormatTime(org.CreatedAt), core.FormatTime(org.UpdatedAt))
		if err != nil {
			return err
		}

		_, err = s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditOrganizationCreated,
			EntityType: "organization",
			EntityID:   org.ID,
			Summary:    fmt.Sprintf("%s registered for verification review", org.Name),
			Data:       map[string]any{"slug": org.Slug, "country": org.Country},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return org, nil
}

// UpdateInput carries editable organization fields.
type UpdateInput struct {
	Name         *string
	Mission      *string
	Description  *string
	Website      *string
	Country      *string
	ContactEmail *string
	PayoutEmail  *string
}

// Update applies a partial update to an organization.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*models.Organization, error) {
	org, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		org.Name = strings.TrimSpace(*in.Name)
	}
	if in.Mission != nil {
		org.Mission = strings.TrimSpace(*in.Mission)
	}
	if in.Description != nil {
		org.Description = strings.TrimSpace(*in.Description)
	}
	if in.Website != nil {
		org.Website = strings.TrimSpace(*in.Website)
	}
	if in.Country != nil {
		org.Country = strings.TrimSpace(*in.Country)
	}
	if in.ContactEmail != nil {
		org.ContactEmail = strings.ToLower(strings.TrimSpace(*in.ContactEmail))
	}
	if in.PayoutEmail != nil {
		org.PayoutEmail = strings.ToLower(strings.TrimSpace(*in.PayoutEmail))
	}
	org.UpdatedAt = core.Now()

	_, err = s.db.ExecContext(ctx, `
		UPDATE organizations SET name = ?, mission = ?, description = ?, website = ?,
			country = ?, contact_email = ?, payout_email = ?, updated_at = ? WHERE id = ?`,
		org.Name, org.Mission, org.Description, org.Website, org.Country,
		org.ContactEmail, org.PayoutEmail, core.FormatTime(org.UpdatedAt), org.ID)
	if err != nil {
		return nil, fmt.Errorf("organizations: update: %w", err)
	}
	return org, nil
}

// Review records an admin decision on an organization.
func (s *Service) Review(ctx context.Context, id, reviewerID string, approve bool, note string) (*models.Organization, error) {
	org, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}

	status := models.OrgRejected
	if approve {
		status = models.OrgVerified
	}
	org.VerificationStatus = status
	org.Verified = approve
	org.ReviewNote = strings.TrimSpace(note)
	org.ReviewedBy = reviewerID
	now := core.Now()
	org.ReviewedAt = &now
	org.UpdatedAt = now

	action := models.AuditOrganizationRejected
	summary := fmt.Sprintf("Organization %s rejected: %s", org.Name, org.ReviewNote)
	if approve {
		action = models.AuditOrganizationVerified
		summary = fmt.Sprintf("Organization %s verified", org.Name)
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE organizations SET verification_status = ?, verified = ?, review_note = ?,
				reviewed_by = ?, reviewed_at = ?, updated_at = ? WHERE id = ?`,
			string(status), boolToInt(approve), org.ReviewNote, reviewerID,
			core.FormatTime(now), core.FormatTime(now), org.ID)
		if err != nil {
			return err
		}
		_, err = s.audit.Record(ctx, tx, audit.Event{
			Action:     action,
			EntityType: "organization",
			EntityID:   org.ID,
			Severity:   severityFor(approve),
			Summary:    summary,
			Data:       map[string]any{"note": org.ReviewNote},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return org, nil
}

func severityFor(approve bool) string {
	if approve {
		return "info"
	}
	return "warning"
}

// ByID loads an organization.
func (s *Service) ByID(ctx context.Context, id string) (*models.Organization, error) {
	return s.scan(s.db.QueryRowContext(ctx, orgSelect+" WHERE id = ?", id))
}

// BySlug loads an organization by URL slug.
func (s *Service) BySlug(ctx context.Context, slug string) (*models.Organization, error) {
	return s.scan(s.db.QueryRowContext(ctx, orgSelect+" WHERE slug = ?", slug))
}

// ByUserID loads the organization owned by a user.
func (s *Service) ByUserID(ctx context.Context, userID string) (*models.Organization, error) {
	return s.scan(s.db.QueryRowContext(ctx, orgSelect+" WHERE user_id = ?", userID))
}

// Filter narrows organization listing.
type Filter struct {
	Query  string
	Status models.OrganizationVerificationStatus
	Limit  int
}

// List returns organizations, newest first.
func (s *Service) List(ctx context.Context, f Filter) ([]models.Organization, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		where []string
		args  []any
	)
	if f.Status != "" {
		where = append(where, "verification_status = ?")
		args = append(args, string(f.Status))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, "(LOWER(name) LIKE ? OR LOWER(mission) LIKE ?)")
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	q := orgSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("organizations: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]models.Organization, 0, limit)
	for rows.Next() {
		org, err := s.scanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *org)
	}
	return out, rows.Err()
}

const orgSelect = `SELECT id, user_id, name, slug, mission, description, website, country,
	registration_number, contact_email, payout_email, verification_status, verified,
	review_note, reviewed_by, reviewed_at, created_at, updated_at FROM organizations`

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Service) scan(row rowScanner) (*models.Organization, error) {
	org, err := s.scanRows(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return org, nil
}

func (s *Service) scanRows(row rowScanner) (*models.Organization, error) {
	var (
		org                          models.Organization
		verificationStatus           string
		reviewedBy, reviewedAt       sql.NullString
		createdAt, updatedAt         string
		verified                     int
	)
	if err := row.Scan(
		&org.ID, &org.UserID, &org.Name, &org.Slug, &org.Mission, &org.Description,
		&org.Website, &org.Country, &org.RegistrationNumber, &org.ContactEmail,
		&org.PayoutEmail, &verificationStatus, &verified, &org.ReviewNote,
		&reviewedBy, &reviewedAt, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	org.VerificationStatus = models.OrganizationVerificationStatus(verificationStatus)
	org.Verified = verified == 1
	org.ReviewedBy = reviewedBy.String
	if reviewedAt.Valid && reviewedAt.String != "" {
		t := core.MustParseTime(reviewedAt.String)
		org.ReviewedAt = &t
	}
	org.CreatedAt = core.MustParseTime(createdAt)
	org.UpdatedAt = core.MustParseTime(updatedAt)
	return &org, nil
}

func uniqueSlug(ctx context.Context, ex db.Executor, base, excludeID string) (string, error) {
	slug := base
	for i := 1; i <= 50; i++ {
		var n int
		q := `SELECT COUNT(*) FROM organizations WHERE slug = ?`
		args := []any{slug}
		if excludeID != "" {
			q += ` AND id <> ?`
			args = append(args, excludeID)
		}
		if err := ex.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i+1)
	}
	return base + "-" + core.RandomHex(3), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
