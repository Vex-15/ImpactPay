package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/auth"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/impact"
	"github.com/impactpay/backend/internal/models"
)

// handleHealth reports liveness and — importantly — which integrations are live
// versus simulated, so the UI can label things honestly.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	started := time.Now()
	dbOK := true
	dbErr := ""
	if err := s.DB.PingContext(ctx); err != nil {
		dbOK = false
		dbErr = err.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"service":  "impactpay-backend",
		"version":  Version,
		"env":      s.Config.Env,
		"demo":     s.Config.DemoMode(),
		"time":     core.NowStr(),
		"database": map[string]any{"driver": s.Config.DBDriver, "ok": dbOK, "error": dbErr, "latency_ms": float64(time.Since(started).Microseconds()) / 1000.0},
		"payments": map[string]any{
			"provider":          s.Payments.Provider().Name(),
			"live":              s.Payments.Provider().Live(),
			"simulated":         s.Payments.Provider().Simulated(),
			"account_currency":  s.Payments.AccountCurrency(),
			"paypal_configured": s.Config.PayPalEnabled(),
		},
		"ai": map[string]any{
			"provider": s.Config.AIProvider,
			"model":    s.Config.AIModel,
			"mode":     aiMode(s.Config.AIProvider),
		},
	})
}

func aiMode(provider string) string {
	if provider == "" || provider == "deterministic" {
		return "deterministic"
	}
	return "llm-assisted"
}

// handleMeta publishes the vocabulary the UI needs to render forms and filters
// without hardcoding lists in two places.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	categories, err := s.Projects.Categories(r.Context())
	if err != nil {
		s.handleServiceError(w, err, "meta categories")
		return
	}

	kinds := make([]string, 0, len(evidence.ValidKinds))
	for k := range evidence.ValidKinds {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	writeJSON(w, http.StatusOK, map[string]any{
		"version":            Version,
		"env":                s.Config.Env,
		"demo":               s.Config.DemoMode(),
		"categories":         categories,
		"evidence_kinds":     kinds,
		"requirement_kinds":  []string{"INVOICE", "RECEIPT", "PHOTOGRAPH", "REPORT", "DOCUMENT", "BENEFICIARY_RECORD", "LOCATION", "UNIT_REGISTRY"},
		"milestone_statuses": []string{"PENDING", "IN_PROGRESS", "EVIDENCE_SUBMITTED", "VERIFYING", "APPROVED", "HELD", "MORE_EVIDENCE_REQUESTED", "PAYMENT_RELEASED", "COMPLETED", "REFUNDED"},
		"decisions":          []string{"APPROVE", "HOLD", "REQUEST_MORE_EVIDENCE"},
		"currencies":         []string{"INR", "USD"},
		"default_currency":   "INR",
		"max_upload_bytes":   s.Evidence.MaxUploadBytes(),
		"account_currency":   s.Payments.AccountCurrency(),
		"fx": map[string]any{
			"note": "Conversions from a project's currency into the PayPal account currency use a configured indicative rate, never a live market rate.",
		},
		"policy_gates": []map[string]string{
			{"key": "project_funded", "label": "Project has captured funding"},
			{"key": "milestone_approved", "label": "Milestone is approved"},
			{"key": "verification_exists", "label": "A verification run exists"},
			{"key": "verification_approves", "label": "Verification recommends approval"},
			{"key": "quantity_verified", "label": "Required quantity fully verified"},
			{"key": "no_critical_findings", "label": "No unresolved critical inconsistencies"},
			{"key": "organization_verified", "label": "Organization is platform-verified"},
			{"key": "financial_reconciles", "label": "Documented spend reconciles with the budget"},
			{"key": "not_already_released", "label": "Funding has not already been released"},
			{"key": "funds_allocated", "label": "Milestone funding has been allocated"},
			{"key": "project_active", "label": "Project is active"},
		},
		"suggested_questions": impact.SuggestedQuestions,
	})
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	role := models.Role(strings.ToLower(strings.TrimSpace(req.Role)))
	if role == "" {
		role = models.RoleDonor
	}
	// The admin/verifier role cannot be self-assigned outside a demo instance.
	if role == models.RoleAdmin && !s.Config.DemoMode() {
		writeError(w, http.StatusForbidden, "forbidden",
			"Admin accounts are provisioned by the platform team, not self-registered.")
		return
	}
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_input", auth.ErrInvalidRole.Error())
		return
	}

	user, token, err := s.Auth.Register(r.Context(), auth.RegisterInput{
		Email:    req.Email,
		Password: req.Password,
		FullName: req.FullName,
		Role:     role,
	})
	if err != nil {
		s.handleServiceError(w, err, "register")
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "token": token})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, token, err := s.Auth.Login(r.Context(), req.Email, req.Password, r.UserAgent())
	if err != nil {
		s.handleServiceError(w, err, "login")
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "token": token})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := s.sessionToken(r); token != "" {
		if err := s.Auth.Logout(r.Context(), token); err != nil {
			s.handleServiceError(w, err, "logout")
			return
		}
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMe returns the session context the UI hydrates from on every load.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "authentication_required", "You must sign in to do that.")
		return
	}

	resp := map[string]any{
		"user":                 user,
		"organization":         nil,
		"unread_notifications": 0,
	}

	org, err := s.Organizations.ByUserID(r.Context(), user.ID)
	if err == nil {
		resp["organization"] = org
	}

	if n, err := s.Notifications.UnreadCount(r.Context(), user.ID); err == nil {
		resp["unread_notifications"] = n
	}

	writeJSON(w, http.StatusOK, resp)
}
