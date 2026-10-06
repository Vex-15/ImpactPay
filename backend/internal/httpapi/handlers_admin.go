package httpapi

import (
	"net/http"
	"strings"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/payments"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/verification"
)

// handleAdminQueue is the verification workspace: every run that did not approve,
// newest first, with its milestone and project context.
func (s *Server) handleAdminQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	items, err := s.Verification.Queue(ctx, queryInt(r, "limit", 30, 100))
	if err != nil {
		s.handleServiceError(w, err, "admin queue")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": items})
}

func (s *Server) handleAdminOrganizations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := models.OrganizationVerificationStatus(strings.ToUpper(queryStr(r, "status")))
	list, err := s.Organizations.List(ctx, organizations.Filter{
		Query:  queryStr(r, "q"),
		Status: status,
		Limit:  queryInt(r, "limit", 50, 200),
	})
	if err != nil {
		s.handleServiceError(w, err, "admin organizations")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": list})
}

type reviewOrganizationRequest struct {
	Approve *bool  `json:"approve"`
	Note    string `json:"note"`
}

func (s *Server) handleAdminReviewOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	var req reviewOrganizationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Approve == nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "An explicit approve=true or approve=false decision is required.")
		return
	}
	org, err := s.Organizations.Review(ctx, pathValue(r, "id"), user.ID, *req.Approve, req.Note)
	if err != nil {
		s.handleServiceError(w, err, "review organization")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": org})
}

func (s *Server) handleAdminProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := projects.Filter{
		Query:    queryStr(r, "q"),
		Category: queryStr(r, "category"),
		Status:   models.ProjectStatus(strings.ToUpper(queryStr(r, "status"))),
		OrgID:    queryStr(r, "org_id"),
		Sort:     queryStr(r, "sort"),
		Limit:    queryInt(r, "limit", 50, 100),
		Offset:   queryInt(r, "offset", 0, 10000),
	}
	list, err := s.Projects.List(ctx, filter)
	if err != nil {
		s.handleServiceError(w, err, "admin projects")
		return
	}
	cards, err := s.projectCards(ctx, list)
	if err != nil {
		s.handleServiceError(w, err, "admin project cards")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": cards})
}

type projectStatusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (s *Server) handleAdminProjectStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req projectStatusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	status := models.ProjectStatus(strings.ToUpper(strings.TrimSpace(req.Status)))
	switch status {
	case models.ProjectFundingOpen, models.ProjectInProgress, models.ProjectCompleted,
		models.ProjectArchived, models.ProjectSuspended, models.ProjectDraft:
	default:
		writeError(w, http.StatusBadRequest, "invalid_input", "Unknown project status.")
		return
	}
	if err := s.Projects.SetStatus(ctx, pathValue(r, "id"), status, req.Reason); err != nil {
		s.handleServiceError(w, err, "admin project status")
		return
	}
	p, err := s.Projects.ByID(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "admin project status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p})
}

// handleAdminAudit exposes the immutable audit trail, filterable by entity.
func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var actions []models.AuditAction
	for _, a := range splitList(queryStr(r, "action")) {
		actions = append(actions, models.AuditAction(strings.ToUpper(a)))
	}
	events, err := s.Audit.List(ctx, audit.Filter{
		ProjectID:   queryStr(r, "project_id"),
		MilestoneID: queryStr(r, "milestone_id"),
		DonationID:  queryStr(r, "donation_id"),
		Actions:     actions,
		Limit:       queryInt(r, "limit", 100, 300),
	})
	if err != nil {
		s.handleServiceError(w, err, "admin audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleAdminPayments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	events, err := s.Payments.PaymentEvents(ctx, payments.EventFilter{
		DonationID:  queryStr(r, "donation_id"),
		ProjectID:   queryStr(r, "project_id"),
		MilestoneID: queryStr(r, "milestone_id"),
		Limit:       queryInt(r, "limit", 100, 300),
	})
	if err != nil {
		s.handleServiceError(w, err, "admin payments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":           events,
		"provider":         s.Payments.Provider().Name(),
		"simulated":        s.Payments.Provider().Simulated(),
		"account_currency": s.Payments.AccountCurrency(),
	})
}

func (s *Server) handleAdminMilestonePayments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	records, err := s.Payments.QueryMilestonePayments(ctx, payments.MilestonePaymentFilter{
		MilestoneID: queryStr(r, "milestone_id"),
		ProjectID:   queryStr(r, "project_id"),
		Kind:        models.MilestonePaymentKind(strings.ToUpper(queryStr(r, "kind"))),
		Status:      models.MilestonePaymentStatus(strings.ToUpper(queryStr(r, "status"))),
		Limit:       queryInt(r, "limit", 100, 300),
	})
	if err != nil {
		s.handleServiceError(w, err, "admin milestone payments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"milestone_payments": records})
}

type overrideRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// handleOverrideVerification records an explicit human decision over the agent's
// recommendation. The AI output is preserved alongside it; nothing is erased.
func (s *Server) handleOverrideVerification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req overrideRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	decision := models.VerificationDecision(strings.ToUpper(strings.TrimSpace(req.Decision)))
	if decision != models.DecisionApprove && decision != models.DecisionHold && decision != models.DecisionRequestMore {
		writeError(w, http.StatusBadRequest, "invalid_input", verification.ErrUnknownDecision.Error())
		return
	}
	if len(strings.TrimSpace(req.Note)) < 10 {
		writeError(w, http.StatusBadRequest, "invalid_input",
			"Overriding the AI requires a written justification of at least 10 characters.")
		return
	}
	v, err := s.Verification.Override(ctx, pathValue(r, "id"), decision, req.Note, actorFrom(ctx))
	if err != nil {
		s.handleServiceError(w, err, "override verification")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verification": v})
}
