package httpapi

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

// handleListProjects powers the marketplace. Only non-draft projects are ever
// returned unless an org id is supplied (the org dashboard lists its own drafts).
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := projects.Filter{
		Query:    queryStr(r, "q"),
		Category: queryStr(r, "category"),
		Status:   models.ProjectStatus(strings.ToUpper(queryStr(r, "status"))),
		OrgID:    queryStr(r, "org_id"),
		Sort:     queryStr(r, "sort"),
		Limit:    queryInt(r, "limit", 24, 100),
		Offset:   queryInt(r, "offset", 0, 10000),
	}

	// Any account may list its own org's drafts; only admins may list another
	// org's drafts.
	user := currentUser(r)
	if filter.OrgID != "" {
		org, err := s.currentOrg(ctx, actorID(r))
		if err != nil || (org.ID != filter.OrgID && !isAdmin(user)) {
			filter.OrgID = ""
		}
	}
	if isAdmin(user) && queryStr(r, "status") != "" {
		// Admin moderation can filter by any status.
	} else if filter.Status == models.ProjectDraft {
		filter.Status = ""
	}

	list, err := s.Projects.List(ctx, filter)
	if err != nil {
		s.handleServiceError(w, err, "list projects")
		return
	}
	total, err := s.Projects.Count(ctx, filter)
	if err != nil {
		s.handleServiceError(w, err, "count projects")
		return
	}

	cards, err := s.projectCards(ctx, list)
	if err != nil {
		s.handleServiceError(w, err, "project cards")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": cards,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
	})
}

func isAdmin(u *models.User) bool { return u != nil && u.Role == models.RoleAdmin }

func actorID(r *http.Request) string {
	if u := currentUser(r); u != nil {
		return u.ID
	}
	return ""
}

// projectCards decorates projects with their organization header and verified
// milestone progress.
func (s *Server) projectCards(ctx context.Context, list []models.Project) ([]projectCard, error) {
	cards := make([]projectCard, 0, len(list))
	cache := map[string]orgBrief{}
	for i := range list {
		p := list[i]
		brief, ok := cache[p.OrgID]
		if !ok {
			org, err := s.Organizations.ByID(ctx, p.OrgID)
			if err != nil {
				brief = orgBrief{ID: p.OrgID}
			} else {
				brief = briefOf(org)
			}
			cache[p.OrgID] = brief
		}
		ms, err := s.Milestones.ListByProject(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		progress := milestones.Summarize(ms)
		cards = append(cards, projectCard{
			Project:         p,
			Organization:    brief,
			Progress:        progress,
			FundingPercent:  core.Percent(p.FundingRatio()),
			VerifiedPercent: verifiedPercent(progress, len(ms)),
		})
	}
	return cards, nil
}

func verifiedPercent(p milestones.Progress, total int) int {
	if total == 0 {
		return 0
	}
	return core.Percent(float64(p.Verified) / float64(total))
}

func (s *Server) handleProjectCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.Projects.Categories(r.Context())
	if err != nil {
		s.handleServiceError(w, err, "project categories")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

// handleProjectDetail returns everything a donor needs to evaluate a project:
// the organization's record, every milestone, its verification history and the
// evidence coverage behind it.
func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := pathValue(r, "slug")

	p, err := s.Projects.BySlug(ctx, slug)
	if err != nil {
		s.handleServiceError(w, err, "project detail")
		return
	}
	visible, err := s.canSeeProject(ctx, p.ID)
	if err != nil {
		s.handleServiceError(w, err, "project visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", projects.ErrNotFound.Error())
		return
	}

	org, err := s.Organizations.ByID(ctx, p.OrgID)
	if err != nil {
		s.handleServiceError(w, err, "project organization")
		return
	}
	ms, err := s.Milestones.ListByProject(ctx, p.ID)
	if err != nil {
		s.handleServiceError(w, err, "project milestones")
		return
	}

	views := make([]milestoneView, 0, len(ms))
	for i := range ms {
		v, err := s.milestonePublicView(ctx, ms[i], p, org)
		if err != nil {
			s.handleServiceError(w, err, "milestone view")
			return
		}
		views = append(views, v)
	}

	updates, err := s.Projects.ListUpdates(ctx, p.ID, 10)
	if err != nil {
		s.handleServiceError(w, err, "project updates")
		return
	}
	funding, err := s.Donations.FundingHistory(ctx, p.ID, 12)
	if err != nil {
		s.handleServiceError(w, err, "project funding history")
		return
	}
	verifications, err := s.Verification.ListForProject(ctx, p.ID, 25)
	if err != nil {
		s.handleServiceError(w, err, "project verifications")
		return
	}
	summaries := make([]verificationSummary, 0, len(verifications))
	for i := range verifications {
		if sv := summariseVerification(&verifications[i]); sv != nil {
			summaries = append(summaries, *sv)
		}
	}

	progress := milestones.Summarize(ms)
	user := currentUser(r)
	isOwner := false
	if user != nil {
		if own, err := s.Organizations.ByUserID(ctx, user.ID); err == nil && own.ID == p.OrgID {
			isOwner = true
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project":          p,
		"organization":     org,
		"milestones":       views,
		"updates":          updates,
		"funding_history":  funding,
		"verifications":    summaries,
		"progress":         progress,
		"funding_percent":  core.Percent(p.FundingRatio()),
		"verified_percent": verifiedPercent(progress, len(ms)),
		"is_owner":         isOwner,
		"can_contribute":   p.Status == models.ProjectFundingOpen || p.Status == models.ProjectInProgress,
		"available_minor":  p.AvailableAmount(),
		"reserved_minor":   p.ReservedAmount,
		"released_minor":   p.ReleasedAmount,
	})
}

// milestonePublicView attaches verification and evidence coverage to a milestone.
func (s *Server) milestonePublicView(ctx context.Context, m models.Milestone, p *models.Project, org *models.Organization) (milestoneView, error) {
	view := milestoneView{
		Milestone:       m,
		Project:         projectBriefOf(p),
		VerifiedPercent: core.Percent(m.VerifiedRatio()),
	}
	if org != nil {
		b := briefOf(org)
		view.Organization = &b
	}

	if v, err := s.Verification.LatestForMilestone(ctx, m.ID); err == nil {
		view.LatestVerification = summariseVerification(v)
	}

	items, err := s.Evidence.ListByMilestone(ctx, m.ID)
	if err != nil {
		return view, err
	}
	view.EvidenceCount = len(items)
	seen := map[string]bool{}
	dupes := 0
	for _, e := range items {
		if e.ContentHash != "" {
			if seen[e.ContentHash] {
				dupes++
				continue
			}
			seen[e.ContentHash] = true
		}
	}
	view.DuplicateCount = dupes
	if len(m.EvidenceRequirements) > 0 {
		view.Coverage = evidence.Coverage(m.EvidenceRequirements, items)
	}

	subs, err := s.Evidence.ListSubmissions(ctx, m.ID)
	if err != nil {
		return view, err
	}
	view.SubmissionCount = len(subs)

	if allocated, err := s.Donations.AllocatedToMilestone(ctx, s.DB, m.ID); err == nil {
		view.AllocatedMinor = allocated
	}
	return view, nil
}

// handleProjectImpactSummary is the public, project-level version of the Proof
// Graph: what has actually been verified, and how much of the funding has moved.
func (s *Server) handleProjectImpactSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Projects.BySlug(ctx, pathValue(r, "slug"))
	if err != nil {
		s.handleServiceError(w, err, "project impact")
		return
	}
	visible, err := s.canSeeProject(ctx, p.ID)
	if err != nil {
		s.handleServiceError(w, err, "project impact visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", projects.ErrNotFound.Error())
		return
	}

	ms, err := s.Milestones.ListByProject(ctx, p.ID)
	if err != nil {
		s.handleServiceError(w, err, "project impact milestones")
		return
	}
	progress := milestones.Summarize(ms)

	type milestoneImpact struct {
		ID               string `json:"id"`
		Ordinal          int    `json:"ordinal"`
		Title            string `json:"title"`
		UnitLabel        string `json:"unit_label"`
		Status           string `json:"status"`
		RequiredQuantity int    `json:"required_quantity"`
		VerifiedQuantity int    `json:"verified_quantity"`
		VerifiedPercent  int    `json:"verified_percent"`
		BudgetMinor      int64  `json:"budget_minor"`
		AllocatedMinor   int64  `json:"allocated_minor"`
		EvidenceCount    int    `json:"evidence_count"`
		CostPerUnitMinor int64  `json:"cost_per_unit_minor"`
	}

	impacts := make([]milestoneImpact, 0, len(ms))
	var evidenceTotal, duplicates int
	for _, m := range ms {
		items, err := s.Evidence.ListByMilestone(ctx, m.ID)
		if err != nil {
			s.handleServiceError(w, err, "project impact evidence")
			return
		}
		allocated, err := s.Donations.AllocatedToMilestone(ctx, s.DB, m.ID)
		if err != nil {
			s.handleServiceError(w, err, "project impact allocation")
			return
		}
		seen := map[string]bool{}
		distinct := 0
		for _, e := range items {
			if e.ContentHash == "" {
				distinct++
				evidenceTotal++
				continue
			}
			if seen[e.ContentHash] {
				duplicates++
				continue
			}
			seen[e.ContentHash] = true
			distinct++
			evidenceTotal++
		}
		var perUnit int64
		if m.RequiredQuantity > 0 {
			perUnit = m.BudgetAmount / int64(m.RequiredQuantity)
		}
		impacts = append(impacts, milestoneImpact{
			ID: m.ID, Ordinal: m.Ordinal, Title: m.Title, UnitLabel: m.UnitLabel,
			Status: string(m.Status), RequiredQuantity: m.RequiredQuantity,
			VerifiedQuantity: m.VerifiedQuantity, VerifiedPercent: core.Percent(m.VerifiedRatio()),
			BudgetMinor: m.BudgetAmount, AllocatedMinor: allocated,
			EvidenceCount: distinct, CostPerUnitMinor: perUnit,
		})
	}

	verifications, err := s.Verification.ListForProject(ctx, p.ID, 100)
	if err != nil {
		s.handleServiceError(w, err, "project impact verifications")
		return
	}
	mix := map[string]int{}
	unresolved := 0
	for _, v := range verifications {
		mix[string(v.Status)]++
		for _, inc := range v.Inconsistencies {
			if inc.Severity == "critical" {
				unresolved++
			}
		}
	}

	// Beneficiaries are reported as a proportion of the planned reach, weighted
	// by how much of the goal has actually been released. The basis is returned
	// with the figure so it is never mistaken for a measured count.
	reached := 0
	if p.GoalAmount > 0 {
		reached = int(float64(p.BeneficiaryCount) * core.Clamp(float64(p.ReleasedAmount)/float64(p.GoalAmount), 0, 1))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project":    p,
		"progress":   progress,
		"milestones": impacts,
		"funding": map[string]any{
			"goal_minor":      p.GoalAmount,
			"funded_minor":    p.FundedAmount,
			"reserved_minor":  p.ReservedAmount,
			"released_minor":  p.ReleasedAmount,
			"currency":        p.Currency,
			"funding_percent": core.Percent(p.FundingRatio()),
			"release_percent": core.Percent(core.Ratio(float64(p.ReleasedAmount), float64(p.GoalAmount))),
		},
		"beneficiaries": map[string]any{
			"planned": p.BeneficiaryCount,
			"reached": reached,
			"basis":   "Planned reach weighted by the share of the goal actually released to the organization. It is an attribution estimate, not a headcount.",
		},
		"evidence": map[string]any{
			"artefacts":  evidenceTotal,
			"duplicates": duplicates,
			"distinct":   evidenceTotal - duplicates,
		},
		"verification_mix":    mix,
		"unresolved_findings": unresolved,
		"verification_runs":   len(verifications),
		"planned_outcome":     p.OutcomeDescription,
	})
}

// ---------------------------------------------------------------------------
// Organizations
// ---------------------------------------------------------------------------

func (s *Server) handleListOrganizations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 24, 100)
	list, err := s.Organizations.List(ctx, organizations.Filter{
		Query: queryStr(r, "q"),
		Limit: limit,
	})
	if err != nil {
		s.handleServiceError(w, err, "list organizations")
		return
	}

	type orgCard struct {
		models.Organization
		ProjectCount int `json:"project_count"`
	}
	cards := make([]orgCard, 0, len(list))
	for _, org := range list {
		count, err := s.Projects.Count(ctx, projects.Filter{OrgID: org.ID})
		if err != nil {
			s.handleServiceError(w, err, "organization project count")
			return
		}
		cards = append(cards, orgCard{Organization: org, ProjectCount: count})
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": cards})
}

// handleOrganizationPassport returns an organization's transparent track record.
// Counts and ratios only — ImpactPay deliberately publishes no "trust score".
func (s *Server) handleOrganizationPassport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, err := s.Organizations.BySlug(ctx, pathValue(r, "slug"))
	if err != nil {
		s.handleServiceError(w, err, "organization passport")
		return
	}
	passport, err := s.Impact.BuildPassport(ctx, org.ID)
	if err != nil {
		s.handleServiceError(w, err, "build passport")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"organization": org,
		"passport":     passport,
	})
}

// ---------------------------------------------------------------------------
// Evidence files
// ---------------------------------------------------------------------------

// handleEvidenceFile streams a stored artefact. Evidence is content addressed,
// so the response is immutable and cacheable; the type of the *stored* artefact
// decides whether the browser may render it inline.
func (s *Server) handleEvidenceFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	item, err := s.Evidence.ByID(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "evidence file")
		return
	}
	visible, err := s.canSeeProject(ctx, item.ProjectID)
	if err != nil {
		s.handleServiceError(w, err, "evidence visibility")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found", evidence.ErrNotFound.Error())
		return
	}

	rc, obj, err := s.Evidence.Store().Get(ctx, item.StorageKey)
	if err != nil {
		s.handleServiceError(w, err, "read evidence object")
		return
	}
	defer func() { _ = rc.Close() }()

	contentType := obj.ContentType
	if contentType == "" {
		contentType = item.MimeType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := "attachment"
	if inlineSafe(contentType) {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", disposition+`; filename="`+storage.SanitizeFilename(item.Filename)+`"`)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("ETag", `"`+item.ContentHash+`"`)
	if obj.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func inlineSafe(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png", "image/jpeg", "image/webp", "application/pdf":
		return true
	}
	return false
}

// sortedKeys returns a stable ordering for map-derived lists.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
