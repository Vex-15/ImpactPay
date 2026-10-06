package httpapi

import (
	"net/http"
	"strings"
)

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	list, err := s.Notifications.List(ctx, user.ID, queryInt(r, "limit", 30, 100))
	if err != nil {
		s.handleServiceError(w, err, "list notifications")
		return
	}
	unread, err := s.Notifications.UnreadCount(ctx, user.ID)
	if err != nil {
		s.handleServiceError(w, err, "unread notifications")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": list, "unread": unread})
}

type markReadRequest struct {
	ID string `json:"id"`
}

// handleMarkNotificationsRead marks one notification, or every notification when
// no id is supplied.
func (s *Server) handleMarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	var req markReadRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if id := strings.TrimSpace(req.ID); id != "" {
		if err := s.Notifications.MarkRead(ctx, user.ID, id); err != nil {
			s.handleServiceError(w, err, "mark notification read")
			return
		}
	} else {
		if err := s.Notifications.MarkAllRead(ctx, user.ID); err != nil {
			s.handleServiceError(w, err, "mark notifications read")
			return
		}
	}
	unread, _ := s.Notifications.UnreadCount(ctx, user.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unread": unread})
}
