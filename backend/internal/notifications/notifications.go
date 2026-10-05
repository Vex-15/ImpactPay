// Package notifications delivers in-app messages to donors and organizations.
package notifications

import (
	"context"
	"fmt"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
)

// Service writes and reads notifications.
type Service struct {
	db *db.DB
}

// New creates the notifications service.
func New(database *db.DB) *Service { return &Service{db: database} }

// Notify writes a notification inside an existing transaction, so a message is
// never delivered for a state change that then rolled back.
func (s *Service) Notify(ctx context.Context, ex db.Executor, userID, kind, title, body, link string) error {
	if userID == "" {
		return nil
	}
	_, err := ex.ExecContext(ctx, `
		INSERT INTO notifications (id, user_id, type, title, body, link, read, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
		core.NewID(), userID, kind, core.Truncate(title, 200), core.Truncate(body, 1000), link, core.NowStr())
	if err != nil {
		return fmt.Errorf("notifications: notify: %w", err)
	}
	return nil
}

// List returns a user's notifications, newest first.
func (s *Service) List(ctx context.Context, userID string, limit int) ([]models.Notification, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, type, title, body, link, read, created_at
		FROM notifications WHERE user_id = ?
		ORDER BY created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("notifications: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []models.Notification{}
	for rows.Next() {
		var (
			n         models.Notification
			read      int
			createdAt string
		)
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body, &n.Link, &read, &createdAt); err != nil {
			return nil, err
		}
		n.Read = read == 1
		n.CreatedAt = core.MustParseTime(createdAt)
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount returns how many unread notifications a user has.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read = 0`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("notifications: unread count: %w", err)
	}
	return n, nil
}

// MarkRead marks one notification as read.
func (s *Service) MarkRead(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET read = 1 WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// MarkAllRead marks every notification for a user as read.
func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET read = 1 WHERE user_id = ?`, userID)
	return err
}
