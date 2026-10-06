package httpapi

import (
	"net/http"

	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/payments"
)

// handleMilestoneDetail returns a milestone with its evidence, submissions,
// verification history and funding state — the review surface for one unit of work.
func (s *Server) handleMilestoneDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathValue(r, "id")

	m, err := s.Milestones.ByID(ctx, id)
	if err != nil {
		s.handleServiceError(w, err, "milestone detail")
		return
	}
	visible, err := s.canSeeProject(ctx, m.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "milestone visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", "milestone not found")
		return
	}

	p, err := s.Projects.ByID(ctx, m.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "milestone project")
		return
	}
	org, err := s.Organizations.ByID(ctx, p.OrgID)
	if err != nil {
		s.handleServiceError(w, err, "milestone organization")
		return
	}

	view, err := s.milestonePublicView(ctx, *m, p, org)
	if err != nil {
		s.handleServiceError(w, err, "milestone view")
		return
	}

	items, err := s.Evidence.ListByMilestone(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "milestone evidence")
		return
	}
	submissions, err := s.Evidence.ListSubmissions(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "milestone submissions")
		return
	}
	verifications, err := s.Verification.ListForMilestone(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "milestone verifications")
		return
	}
	paymentsList, err := s.Payments.MilestonePayments(ctx, m.ID, 20)
	if err != nil {
		s.handleServiceError(w, err, "milestone payments")
		return
	}

	resp := map[string]any{
		"milestone":     view,
		"project":       p,
		"organization":  org,
		"evidence":      items,
		"submissions":   submissions,
		"verifications": sumVerifications(verifications),
		"payments":      paymentsList,
		"funding": map[string]any{
			"budget_minor":    m.BudgetAmount,
			"allocated_minor": view.AllocatedMinor,
			"currency":        p.Currency,
			"budget":          money(m.BudgetAmount, p.Currency),
			"allocated":       money(view.AllocatedMinor, p.Currency),
		},
		"cost_per_unit_minor": perUnit(m),
	}

	// The conditional-release preview (the eleven deterministic gates) is shown
	// to the owning organization and to admins, who are the only actors that can
	// ask for a release.
	if _, _, _, err := s.ownerMilestone(ctx, m.ID); err == nil {
		if preview, err := s.Payments.PreviewRelease(ctx, m.ID); err == nil {
			resp["release_preview"] = preview
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func sumVerifications(list []models.Verification) []verificationSummary {
	out := make([]verificationSummary, 0, len(list))
	for i := range list {
		if sv := summariseVerification(&list[i]); sv != nil {
			out = append(out, *sv)
		}
	}
	return out
}

func perUnit(m *models.Milestone) int64 {
	if m.RequiredQuantity <= 0 {
		return 0
	}
	return m.BudgetAmount / int64(m.RequiredQuantity)
}

func (s *Server) handleMilestoneVerifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, err := s.Milestones.ByID(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "milestone verifications")
		return
	}
	visible, err := s.canSeeProject(ctx, m.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "milestone verifications visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", "milestone not found")
		return
	}
	list, err := s.Verification.ListForMilestone(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "milestone verifications")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verifications": list, "summaries": sumVerifications(list)})
}

func (s *Server) handleMilestoneEvidence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, err := s.Milestones.ByID(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "milestone evidence")
		return
	}
	visible, err := s.canSeeProject(ctx, m.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "milestone evidence visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", "milestone not found")
		return
	}
	items, err := s.Evidence.ListByMilestone(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "milestone evidence")
		return
	}
	submissions, err := s.Evidence.ListSubmissions(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "milestone submissions")
		return
	}
	var coverage []evidence.RequirementsCoverage
	if len(m.EvidenceRequirements) > 0 {
		coverage = evidence.Coverage(m.EvidenceRequirements, items)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"evidence":    items,
		"submissions": submissions,
		"coverage":    coverage,
		"required":    m.EvidenceRequirements,
	})
}

func (s *Server) handleVerificationDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, err := s.Verification.ByID(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "verification detail")
		return
	}
	visible, err := s.canSeeProject(ctx, v.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "verification visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", "verification not found")
		return
	}
	m, _ := s.Milestones.ByID(ctx, v.MilestoneID)
	p, _ := s.Projects.ByID(ctx, v.ProjectID)

	resp := map[string]any{"verification": v, "result": v.Result}
	if m != nil {
		resp["milestone"] = m
	}
	if p != nil {
		resp["project"] = projectBriefOf(p)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVerificationTrace exposes the agent's tool-call trace: exactly which
// checks ran, in order, and what each one observed. This is what makes the
// decision auditable instead of a black box.
func (s *Server) handleVerificationTrace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, err := s.Verification.ByID(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "verification trace")
		return
	}
	visible, err := s.canSeeProject(ctx, v.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "verification trace visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", "verification not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verification_id":  v.ID,
		"status":           v.Status,
		"recommendation":   v.Recommendation,
		"engine":           v.Engine,
		"model":            v.Model,
		"summary":          v.Summary,
		"next_action":      v.Result.NextAction,
		"steps":            v.Result.Steps,
		"checks":           v.Checks,
		"claimed":          v.Claimed,
		"evidence_found":   v.EvidenceFound,
		"duplicates":       v.Result.Duplicates,
		"missing_evidence": v.MissingEvidence,
		"inconsistencies":  v.Inconsistencies,
		"financial":        v.Result.Financial,
		"confidence":       v.Confidence,
	})
}

// handleReleasePreview shows the deterministic policy result before money moves.
func (s *Server) handleReleasePreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, _, err := s.ownerMilestone(ctx, pathValue(r, "id")); err != nil {
		s.handleServiceError(w, err, "release preview")
		return
	}
	record, err := s.Payments.PreviewRelease(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "release preview")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release": record,
		"allowed": record.Passed(),
	})
}

// handleRelease performs the conditional milestone funding release. Every gate
// is re-evaluated here; a blocked attempt is recorded with its reasons rather
// than silently failing.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathValue(r, "id")
	if _, _, _, err := s.ownerMilestone(ctx, id); err != nil {
		s.handleServiceError(w, err, "release milestone")
		return
	}

	record, err := s.Payments.ReleaseMilestone(ctx, id, actorFrom(ctx))
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"release": record, "released": true})
		return
	}
	if record != nil && isReleaseBlocked(err) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"release":  record,
			"released": false,
			"error": map[string]any{
				"code":    "release_blocked",
				"message": "Milestone funding release was blocked by backend policy.",
				"detail":  releaseBlockedReason(record),
			},
		})
		return
	}
	s.handleServiceError(w, err, "release milestone")
}

func isReleaseBlocked(err error) bool {
	return err != nil && err.Error() == payments.ErrReleaseBlocked.Error()
}

func releaseBlockedReason(record *models.MilestonePayment) string {
	if record.BlockedReason != "" {
		return record.BlockedReason
	}
	return "One or more policy gates did not pass."
}
