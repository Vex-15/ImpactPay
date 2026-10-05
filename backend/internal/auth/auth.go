// Package auth implements ImpactPay's account and session handling.
//
// Design: passwords are hashed with bcrypt; sessions are opaque 256-bit random
// tokens stored server side, delivered as an HttpOnly cookie, and revocable with
// a single row update. The client never receives a credential it can inspect, and
// role checks read from the database rather than from a client-supplied claim.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// Sentinel errors surfaced to the HTTP layer as 400/401 responses.
var (
	ErrEmailTaken        = errors.New("an account with that email already exists")
	ErrInvalidCredential = errors.New("email or password is incorrect")
	ErrInvalidEmail      = errors.New("enter a valid email address")
	ErrWeakPassword      = errors.New("password must be at least 8 characters")
	ErrNameRequired      = errors.New("full name is required")
	ErrInvalidRole       = errors.New("role must be donor, organization or admin")
	ErrSessionInvalid    = errors.New("session is invalid or expired")
	ErrAccountSuspended  = errors.New("account is suspended")
)

// Config tunes session lifetime.
type Config struct {
	SessionTTL time.Duration
	BcryptCost int
}

// Service owns user and session persistence.
type Service struct {
	db  *db.DB
	cfg Config
}

// New creates the auth service.
func New(database *db.DB, cfg Config) *Service {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	return &Service{db: database, cfg: cfg}
}

// RegisterInput is the signup payload.
type RegisterInput struct {
	Email    string
	Password string
	FullName string
	Role     models.Role
}

// Register creates a user and returns it with a fresh session token.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*models.User, string, error) {
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(in.FullName)
	if name == "" {
		return nil, "", ErrNameRequired
	}
	if len(in.Password) < 8 {
		return nil, "", ErrWeakPassword
	}
	if in.Role == "" {
		in.Role = models.RoleDonor
	}
	if !in.Role.Valid() {
		return nil, "", ErrInvalidRole
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.cfg.BcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("auth: hash password: %w", err)
	}

	user := &models.User{
		ID:           core.NewID(),
		Email:        email,
		PasswordHash: string(hash),
		FullName:     name,
		Role:         in.Role,
		Status:       "active",
		CreatedAt:    core.Now(),
		UpdatedAt:    core.Now(),
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		exists, err := emailExists(ctx, tx, email)
		if err != nil {
			return err
		}
		if exists {
			return ErrEmailTaken
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO users (id, email, password_hash, full_name, role, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			user.ID, user.Email, user.PasswordHash, user.FullName, string(user.Role),
			user.Status, core.FormatTime(user.CreatedAt), core.FormatTime(user.UpdatedAt))
		return err
	})
	if err != nil {
		return nil, "", err
	}

	token, err := s.createSession(ctx, s.db, user.ID, "")
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

// Login verifies credentials and issues a session.
func (s *Service) Login(ctx context.Context, email, password, userAgent string) (*models.User, string, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		// Do not leak which part failed.
		return nil, "", ErrInvalidCredential
	}

	user, err := s.ByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Compare against a dummy hash so timing does not reveal existence.
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(password))
			return nil, "", ErrInvalidCredential
		}
		return nil, "", err
	}
	if user.Status != "active" {
		return nil, "", ErrAccountSuspended
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, "", ErrInvalidCredential
	}

	token, err := s.createSession(ctx, s.db, user.ID, userAgent)
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

// SessionUser resolves a session token to its user, or ErrSessionInvalid.
func (s *Service) SessionUser(ctx context.Context, token string) (*models.User, *models.Session, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, ErrSessionInvalid
	}

	var (
		sess      models.Session
		createdAt string
		expiresAt string
		revoked   int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT token, user_id, user_agent, created_at, expires_at, revoked
		FROM sessions WHERE token = ?`, token).
		Scan(&sess.Token, &sess.UserID, &sess.UserAgent, &createdAt, &expiresAt, &revoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrSessionInvalid
		}
		return nil, nil, fmt.Errorf("auth: load session: %w", err)
	}
	sess.CreatedAt = core.MustParseTime(createdAt)
	sess.ExpiresAt = core.MustParseTime(expiresAt)
	sess.Revoked = revoked == 1

	if sess.Revoked || sess.Expired(core.Now()) {
		return nil, nil, ErrSessionInvalid
	}

	user, err := s.ByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrSessionInvalid
		}
		return nil, nil, err
	}
	if user.Status != "active" {
		return nil, nil, ErrAccountSuspended
	}
	return user, &sess, nil
}

// Logout revokes a session token.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked = 1 WHERE token = ?`, token)
	return err
}

// ByID loads a user by primary key.
func (s *Service) ByID(ctx context.Context, id string) (*models.User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, full_name, role, status, created_at, updated_at
		FROM users WHERE id = ?`, id))
}

// ByEmail loads a user by normalized email.
func (s *Service) ByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, full_name, role, status, created_at, updated_at
		FROM users WHERE email = ?`, email))
}

func (s *Service) scanUser(row *sql.Row) (*models.User, error) {
	var (
		u                    models.User
		role                 string
		createdAt, updatedAt string
	)
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &role, &u.Status, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	u.Role = models.Role(role)
	u.CreatedAt = core.MustParseTime(createdAt)
	u.UpdatedAt = core.MustParseTime(updatedAt)
	return &u, nil
}

func (s *Service) createSession(ctx context.Context, ex db.Executor, userID, userAgent string) (string, error) {
	token := core.Token(32)
	now := core.Now()
	_, err := ex.ExecContext(ctx, `
		INSERT INTO sessions (token, user_id, user_agent, created_at, expires_at, revoked)
		VALUES (?, ?, ?, ?, ?, 0)`,
		token, userID, core.Truncate(userAgent, 200),
		core.FormatTime(now), core.FormatTime(now.Add(s.cfg.SessionTTL)))
	if err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	return token, nil
}

// PurgeExpiredSessions removes stale session rows.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, core.NowStr())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func emailExists(ctx context.Context, ex db.Executor, email string) (bool, error) {
	var n int
	if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, email).Scan(&n); err != nil {
		return false, fmt.Errorf("auth: check email: %w", err)
	}
	return n > 0, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email, ".") {
		return "", ErrInvalidEmail
	}
	return email, nil
}
