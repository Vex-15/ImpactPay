// Package httpapi exposes ImpactPay's services over HTTP.
//
// The HTTP layer is deliberately thin: it authenticates, authorises, validates
// input and shapes responses. All business rules — verification, policy, funding
// state — live in the service packages, so the same rules apply whether an action
// is triggered by the UI, a webhook, or the seeder.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/auth"
	"github.com/impactpay/backend/internal/config"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/donations"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/impact"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/notifications"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/payments"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/verification"
)

// Deps is everything the API needs.
type Deps struct {
	Config        *config.Config
	DB            *db.DB
	Logger        *slog.Logger
	Auth          *auth.Service
	Audit         *audit.Service
	Organizations *organizations.Service
	Projects      *projects.Service
	Milestones    *milestones.Service
	Evidence      *evidence.Service
	Donations     *donations.Service
	Verification  *verification.Service
	Payments      *payments.Service
	Impact        *impact.Service
	Notifications *notifications.Service

	// Answerer optionally rephrases grounded impact answers for readability. It
	// can never change a figure, a citation or the grounding flag, and may be nil.
	Answerer impact.Answerer
}

// Server handles HTTP requests.
type Server struct {
	Deps
	mux *chi.Mux
}

// New builds the router.
func New(d Deps) *Server {
	s := &Server{Deps: d}
	s.mux = s.routes()
	return s
}

// Handler returns the configured HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.recoverer)
	r.Use(s.requestLogger)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.Config.AllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Requested-With"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Route("/api", func(api chi.Router) {
		// Public routes still resolve a session when one is present, so a page can
		// adapt ("manage this project") without a second round trip.
		api.Use(s.optionalAuth)

		api.Get("/health", s.handleHealth)
		api.Get("/meta", s.handleMeta)

		api.Route("/auth", func(ar chi.Router) {
			ar.Post("/register", s.handleRegister)
			ar.Post("/login", s.handleLogin)
			ar.Post("/logout", s.handleLogout)
			ar.With(s.requireAuth).Get("/me", s.handleMe)
		})

		// Public discovery surface.
		api.Get("/projects", s.handleListProjects)
		api.Get("/projects/categories", s.handleProjectCategories)
		api.Get("/projects/{slug}", s.handleProjectDetail)
		api.Get("/projects/{slug}/impact", s.handleProjectImpactSummary)
		api.Get("/organizations", s.handleListOrganizations)
		api.Get("/organizations/{slug}", s.handleOrganizationPassport)
		api.Get("/evidence/{id}/file", s.handleEvidenceFile)

		api.Post("/paypal/webhook", s.handlePayPalWebhook)

		// Authenticated surface.
		api.Group(func(pr chi.Router) {
			pr.Use(s.requireAuth)

			pr.Get("/notifications", s.handleListNotifications)
			pr.Post("/notifications/read", s.handleMarkNotificationsRead)

			pr.Post("/donations", s.handleCreateDonation)
			pr.Get("/donations", s.handleListMyDonations)
			pr.Get("/donations/{id}", s.handleGetDonation)
			pr.Post("/donations/{id}/order", s.handleCreateOrder)
			pr.Post("/donations/{id}/capture", s.handleCaptureDonation)
			pr.Get("/donor/dashboard", s.handleDonorDashboard)

			pr.Get("/impact/trace/{donationId}", s.handleTrace)
			pr.Post("/impact/ask", s.handleAsk)
			pr.Get("/impact/questions", s.handleSuggestedQuestions)

			pr.Get("/milestones/{id}", s.handleMilestoneDetail)
			pr.Get("/milestones/{id}/verifications", s.handleMilestoneVerifications)
			pr.Get("/milestones/{id}/evidence", s.handleMilestoneEvidence)
			pr.Get("/milestones/{id}/release", s.handleReleasePreview)
			pr.Post("/milestones/{id}/release", s.handleRelease)

			pr.Get("/verifications/{id}", s.handleVerificationDetail)
			pr.Get("/verifications/{id}/trace", s.handleVerificationTrace)

			// Organization surface.
			pr.Post("/organizations", s.handleCreateOrganization)
			pr.Get("/org/me", s.handleMyOrganization)
			pr.Put("/org/me", s.handleUpdateOrganization)
			pr.Get("/org/dashboard", s.handleOrgDashboard)
			pr.Post("/org/projects", s.handleCreateProject)
			pr.Get("/org/projects", s.handleOrgProjects)
			pr.Get("/org/projects/{id}", s.handleOrgProjectDetail)
			pr.Post("/org/projects/{id}/publish", s.handlePublishProject)
			pr.Post("/org/projects/{id}/updates", s.handlePostProjectUpdate)
			pr.Get("/org/activity", s.handleOrgActivity)

			pr.Post("/org/milestones/{id}/start", s.handleStartMilestone)
			pr.Post("/org/milestones/{id}/submissions", s.handleCreateSubmission)
			pr.Post("/org/milestones/{id}/submit", s.handleSubmitForVerification)
			pr.Post("/org/evidence", s.handleUploadEvidence)

			// Admin surface.
			pr.Group(func(ar chi.Router) {
				ar.Use(s.requireRole(models.RoleAdmin))
				ar.Get("/admin/queue", s.handleAdminQueue)
				ar.Get("/admin/organizations", s.handleAdminOrganizations)
				ar.Post("/admin/organizations/{id}/review", s.handleAdminReviewOrganization)
				ar.Get("/admin/projects", s.handleAdminProjects)
				ar.Post("/admin/projects/{id}/status", s.handleAdminProjectStatus)
				ar.Get("/admin/audit", s.handleAdminAudit)
				ar.Get("/admin/payments", s.handleAdminPayments)
				ar.Get("/admin/milestone-payments", s.handleAdminMilestonePayments)
				ar.Post("/admin/verifications/{id}/override", s.handleOverrideVerification)
			})
		})
	})

	return r
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSessionToken
)

// requireAuth resolves the session cookie or bearer token and loads the user.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.sessionToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "You must sign in to do that.")
			return
		}
		user, _, err := s.Auth.SessionUser(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session_invalid", "Your session has expired. Please sign in again.")
			return
		}

		actor := models.Actor{ID: user.ID, Role: user.Role, Label: user.FullName + " <" + user.Email + ">"}
		ctx := models.WithActor(r.Context(), actor)
		ctx = context.WithValue(ctx, ctxUser, user)
		ctx = context.WithValue(ctx, ctxSessionToken, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// optionalAuth attaches the signed-in user when a valid session is present and
// otherwise continues anonymously.
func (s *Server) optionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.sessionToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, _, err := s.Auth.SessionUser(r.Context(), token)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		actor := models.Actor{ID: user.ID, Role: user.Role, Label: user.FullName + " <" + user.Email + ">"}
		ctx := models.WithActor(r.Context(), actor)
		ctx = context.WithValue(ctx, ctxUser, user)
		ctx = context.WithValue(ctx, ctxSessionToken, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireRole(roles ...models.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := userFrom(r.Context())
			if u == nil {
				writeError(w, http.StatusUnauthorized, "authentication_required", "You must sign in to do that.")
				return
			}
			for _, role := range roles {
				if u.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeError(w, http.StatusForbidden, "forbidden",
				"Your account does not have access to this resource.")
		})
	}
}

// sessionToken reads the session from a cookie or an Authorization header.
func (s *Server) sessionToken(r *http.Request) string {
	if c, err := r.Cookie(s.Config.SessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// requestLogger records method, path, status and duration.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		level := slog.LevelInfo
		if ww.Status() >= 500 {
			level = slog.LevelError
		} else if ww.Status() >= 400 {
			level = slog.LevelWarn
		}
		s.Logger.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", float64(time.Since(started).Microseconds())/1000.0,
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// recoverer converts a panic into a 500 without killing the process.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Logger.Error("panic recovered", "error", fmt.Sprint(rec), "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our side.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func userFrom(ctx context.Context) *models.User {
	if u, ok := ctx.Value(ctxUser).(*models.User); ok {
		return u
	}
	return nil
}

func actorFrom(ctx context.Context) models.Actor { return models.ActorFrom(ctx) }

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// The status line is already written; nothing further can be done.
		return
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeErrorDetail(w http.ResponseWriter, status int, code, message string, detail any) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message, Detail: detail}})
}

// decodeJSON reads a JSON request body with a size cap.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "The request body could not be read: "+core.Truncate(err.Error(), 200))
		return false
	}
	return true
}

// handleServiceError maps a service error to an HTTP status without leaking
// internal detail for unexpected failures.
func (s *Server) handleServiceError(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredential):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", auth.ErrInvalidCredential.Error())
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email_taken", auth.ErrEmailTaken.Error())
	case errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrWeakPassword),
		errors.Is(err, auth.ErrNameRequired), errors.Is(err, auth.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, auth.ErrAccountSuspended):
		writeError(w, http.StatusForbidden, "account_suspended", err.Error())
	case errors.Is(err, organizations.ErrNotFound),
		errors.Is(err, projects.ErrNotFound),
		errors.Is(err, milestones.ErrNotFound),
		errors.Is(err, evidence.ErrNotFound),
		errors.Is(err, evidence.ErrSubmissionNotFound),
		errors.Is(err, verification.ErrNotFound),
		errors.Is(err, payments.ErrDonationNotFound),
		errors.Is(err, impact.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, projects.ErrNotOwner):
		writeError(w, http.StatusForbidden, "forbidden", projects.ErrNotOwner.Error())
	case errors.Is(err, payments.ErrReleaseBlocked):
		writeError(w, http.StatusConflict, "release_blocked", err.Error())
	default:
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		s.Logger.Error("service error", "context", context, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our side.")
	}
}

func isValidationError(err error) bool {
	targets := []error{
		organizations.ErrNameRequired, organizations.ErrAlreadyExists,
		projects.ErrTitleRequired, projects.ErrGoalRequired, projects.ErrOutcomeRequired,
		projects.ErrNoMilestones, projects.ErrBudgetExceedsGoal,
		milestones.ErrTitleRequired, milestones.ErrBudgetRequired, milestones.ErrInvalidTransition,
		evidence.ErrKindRequired, evidence.ErrInvalidKind, evidence.ErrEmptyFile,
		evidence.ErrFileTooLarge, evidence.ErrUnsupportedType, evidence.ErrFilenameRequired,
		donations.ErrAmountInvalid, donations.ErrAmountTooSmall, donations.ErrAmountTooLarge,
		donations.ErrProjectClosed, donations.ErrSelfDonation,
		payments.ErrOrderNotCreated, payments.ErrNotCaptured, payments.ErrRefundNotAllowed,
		payments.ErrCaptureAmountMismatch,
		verification.ErrNoEvidence, verification.ErrUnknownDecision,
	}
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}
