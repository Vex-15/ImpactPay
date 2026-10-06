package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/payments"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Organization profile
// ---------------------------------------------------------------------------

type orgProfileRequest struct {
	Name               string `json:"name"`
	Mission            string `json:"mission"`
	Description        string `json:"description"`
	Website            string `json:"website"`
	Country            string `json:"country"`
	RegistrationNumber string `json:"registration_number"`
	ContactEmail       string `json:"contact_email"`
}

func (s *Server) handleCreateOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	var req orgProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	org, err := s.Organizations.Create(ctx, organizations.CreateInput{
		UserID:             user.ID,
		Name:               req.Name,
		Mission:            req.Mission,
		Description:        req.Description,
		Website:            req.Website,
		Country:            req.Country,
		RegistrationNumber: req.RegistrationNumber,
		ContactEmail:       req.ContactEmail,
	})
	if err != nil {
		s.handleServiceError(w, err, "create organization")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"organization": org})
}

func (s *Server) handleMyOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	org, err := s.Organizations.ByUserID(ctx, user.ID)
	if err != nil {
		if errors.Is(err, organizations.ErrNotFound) {
			// Not having an organization is a normal state, not an error: the UI
			// shows onboarding.
			writeJSON(w, http.StatusOK, map[string]any{"organization": nil})
			return
		}
		s.handleServiceError(w, err, "my organization")
		return
	}

	passport, err := s.Impact.BuildPassport(ctx, org.ID)
	if err != nil {
		s.handleServiceError(w, err, "organization passport")
		return
	}
	list, err := s.Projects.List(ctx, projects.Filter{OrgID: org.ID, Limit: 100})
	if err != nil {
		s.handleServiceError(w, err, "organization projects")
		return
	}
	cards, err := s.projectCards(ctx, list)
	if err != nil {
		s.handleServiceError(w, err, "organization project cards")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"organization": org,
		"passport":     passport,
		"projects":     cards,
	})
}

type orgUpdateRequest struct {
	Name         *string `json:"name"`
	Mission      *string `json:"mission"`
	Description  *string `json:"description"`
	Website      *string `json:"website"`
	Country      *string `json:"country"`
	ContactEmail *string `json:"contact_email"`
	PayoutEmail  *string `json:"payout_email"`
}

func (s *Server) handleUpdateOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	org, err := s.Organizations.ByUserID(ctx, user.ID)
	if err != nil {
		s.handleServiceError(w, err, "update organization")
		return
	}
	var req orgUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	updated, err := s.Organizations.Update(ctx, org.ID, organizations.UpdateInput{
		Name:         strPtr(req.Name),
		Mission:      strPtr(req.Mission),
		Description:  strPtr(req.Description),
		Website:      strPtr(req.Website),
		Country:      strPtr(req.Country),
		ContactEmail: strPtr(req.ContactEmail),
		PayoutEmail:  strPtr(req.PayoutEmail),
	})
	if err != nil {
		s.handleServiceError(w, err, "update organization")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": updated})
}

// handleOrgDashboard is the organization home: funded/released totals, milestone
// state, verification queue and recent activity.
func (s *Server) handleOrgDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	org, err := s.Organizations.ByUserID(ctx, user.ID)
	if err != nil {
		if errors.Is(err, organizations.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"organization": nil, "needs_profile": true})
			return
		}
		s.handleServiceError(w, err, "org dashboard")
		return
	}

	list, err := s.Projects.List(ctx, projects.Filter{OrgID: org.ID, Limit: 100})
	if err != nil {
		s.handleServiceError(w, err, "org dashboard projects")
		return
	}
	cards, err := s.projectCards(ctx, list)
	if err != nil {
		s.handleServiceError(w, err, "org dashboard cards")
		return
	}

	type projectSummary struct {
		Project         models.Project      `json:"project"`
		Progress        milestones.Progress `json:"progress"`
		FundedMinor     int64               `json:"funded_minor"`
		ReservedMinor   int64               `json:"reserved_minor"`
		ReleasedMinor   int64               `json:"released_minor"`
		AvailableMinor  int64               `json:"available_minor"`
		FundingPercent  int                 `json:"funding_percent"`
		VerifiedPercent int                 `json:"verified_percent"`
		Currency        string              `json:"currency"`
		Status          string              `json:"status"`
	}

	summaries := make([]projectSummary, 0, len(cards))
	var (
		totalFunded, totalReserved, totalReleased int64
		milestonesTotal, milestonesVerified       int
		pendingVerifications                      int
	)

	for _, c := range cards {
		ms, err := s.Milestones.ListByProject(ctx, c.Project.ID)
		if err != nil {
			s.handleServiceError(w, err, "org dashboard milestones")
			return
		}
		for _, m := range ms {
			milestonesTotal++
			if m.FullyVerified() {
				milestonesVerified++
			}
			if m.Status == models.MilestoneHeld || m.Status == models.MilestoneMoreEvidenceRequested {
				pendingVerifications++
			}
		}
		totalFunded += c.Project.FundedAmount
		totalReserved += c.Project.ReservedAmount
		totalReleased += c.Project.ReleasedAmount
		summaries = append(summaries, projectSummary{
			Project: c.Project, Progress: c.Progress,
			FundedMinor: c.Project.FundedAmount, ReservedMinor: c.Project.ReservedAmount,
			ReleasedMinor: c.Project.ReleasedAmount, AvailableMinor: c.Project.AvailableAmount(),
			FundingPercent: c.FundingPercent, VerifiedPercent: c.VerifiedPercent,
			Currency: c.Project.Currency, Status: string(c.Project.Status),
		})
	}

	activities, err := s.orgActivity(ctx, list, 20)
	if err != nil {
		s.handleServiceError(w, err, "org dashboard activity")
		return
	}
	unread, _ := s.Notifications.UnreadCount(ctx, user.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"organization": org,
		"projects":     summaries,
		"totals": map[string]any{
			"funded_minor":        totalFunded,
			"reserved_minor":      totalReserved,
			"released_minor":      totalReleased,
			"available_minor":     totalFunded - totalReserved,
			"projects":            len(list),
			"milestones":          milestonesTotal,
			"milestones_verified": milestonesVerified,
			"awaiting_review":     pendingVerifications,
		},
		"unread_notifications": unread,
		"recent_activity":      activities,
	})
}

// orgActivity merges the audit trail of every project the organization owns,
// newest first.
func (s *Server) orgActivity(ctx context.Context, list []models.Project, limit int) ([]models.AuditEvent, error) {
	merged := []models.AuditEvent{}
	for i, p := range list {
		if i >= 12 {
			break
		}
		events, err := s.Audit.List(ctx, audit.Filter{ProjectID: p.ID, Limit: limit})
		if err != nil {
			return nil, err
		}
		merged = append(merged, events...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].CreatedAt.After(merged[j].CreatedAt)
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// ---------------------------------------------------------------------------
// Projects (wizard)
// ---------------------------------------------------------------------------

type evidenceRequirementRequest struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required *bool  `json:"required"`
	MinCount int    `json:"min_count"`
	Detail   string `json:"detail"`
}

type milestoneRequest struct {
	Title                string                       `json:"title"`
	Description          string                       `json:"description"`
	Requirement          string                       `json:"requirement"`
	UnitLabel            string                       `json:"unit_label"`
	RequiredQuantity     int                          `json:"required_quantity"`
	BudgetAmount         int64                        `json:"budget_amount"`
	EvidenceRequirements []evidenceRequirementRequest `json:"evidence_requirements"`
}

type createProjectRequest struct {
	Title                  string             `json:"title"`
	Category               string             `json:"category"`
	Summary                string             `json:"summary"`
	Problem                string             `json:"problem"`
	Approach               string             `json:"approach"`
	Location               string             `json:"location"`
	Currency               string             `json:"currency"`
	GoalAmount             int64              `json:"goal_amount"`
	BeneficiaryCount       int                `json:"beneficiary_count"`
	BeneficiaryDescription string             `json:"beneficiary_description"`
	OutcomeDescription     string             `json:"outcome_description"`
	Milestones             []milestoneRequest `json:"milestones"`
	Publish                *bool              `json:"publish"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	org, err := s.Organizations.ByUserID(ctx, user.ID)
	if err != nil {
		s.handleServiceError(w, err, "create project organization")
		return
	}

	var req createProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Milestones) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_input", projects.ErrNoMilestones.Error())
		return
	}

	inputs := make([]milestones.Input, 0, len(req.Milestones))
	for _, m := range req.Milestones {
		reqs := make([]models.EvidenceRequirementSpec, 0, len(m.EvidenceRequirements))
		for _, er := range m.EvidenceRequirements {
			required := true
			if er.Required != nil {
				required = *er.Required
			}
			reqs = append(reqs, models.EvidenceRequirementSpec{
				Key:      er.Key,
				Label:    er.Label,
				Kind:     strings.ToUpper(strings.TrimSpace(er.Kind)),
				Required: required,
				MinCount: er.MinCount,
				Detail:   er.Detail,
			})
		}
		inputs = append(inputs, milestones.Input{
			Title:                m.Title,
			Description:          m.Description,
			Requirement:          m.Requirement,
			UnitLabel:            m.UnitLabel,
			RequiredQuantity:     m.RequiredQuantity,
			BudgetAmount:         m.BudgetAmount,
			EvidenceRequirements: reqs,
		})
	}

	publish := false
	if req.Publish != nil {
		publish = *req.Publish
	}

	project, created, err := s.Projects.Create(ctx, projects.CreateInput{
		OrgID:                  org.ID,
		CreatedBy:              user.ID,
		Title:                  req.Title,
		Category:               req.Category,
		Summary:                req.Summary,
		Problem:                req.Problem,
		Approach:               req.Approach,
		Location:               req.Location,
		Currency:               req.Currency,
		GoalAmount:             req.GoalAmount,
		BeneficiaryCount:       req.BeneficiaryCount,
		BeneficiaryDescription: req.BeneficiaryDescription,
		OutcomeDescription:     req.OutcomeDescription,
		Milestones:             inputs,
		Publish:                publish,
	})
	if err != nil {
		s.handleServiceError(w, err, "create project")
		return
	}

	resp := map[string]any{"project": project, "milestones": created}
	if publish {
		published, err := s.Projects.Publish(ctx, project.ID)
		if err != nil {
			s.handleServiceError(w, err, "publish project")
			return
		}
		resp["project"] = published
	}
	if !org.Verified {
		resp["warning"] = "Your organization is not yet platform-verified, so milestone funding cannot be released until the review completes."
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleOrgProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	org, err := s.Organizations.ByUserID(ctx, user.ID)
	if err != nil {
		s.handleServiceError(w, err, "org projects")
		return
	}
	list, err := s.Projects.List(ctx, projects.Filter{
		OrgID:  org.ID,
		Status: models.ProjectStatus(strings.ToUpper(queryStr(r, "status"))),
		Query:  queryStr(r, "q"),
		Sort:   queryStr(r, "sort"),
		Limit:  queryInt(r, "limit", 50, 100),
		Offset: queryInt(r, "offset", 0, 10000),
	})
	if err != nil {
		s.handleServiceError(w, err, "org projects")
		return
	}
	cards, err := s.projectCards(ctx, list)
	if err != nil {
		s.handleServiceError(w, err, "org project cards")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": cards})
}

func (s *Server) handleOrgProjectDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathValue(r, "id")

	p, org, err := s.ownerProject(ctx, id)
	if err != nil {
		s.handleServiceError(w, err, "org project detail")
		return
	}

	ms, err := s.Milestones.ListByProject(ctx, p.ID)
	if err != nil {
		s.handleServiceError(w, err, "org project milestones")
		return
	}
	views := make([]milestoneView, 0, len(ms))
	for i := range ms {
		v, err := s.milestonePublicView(ctx, ms[i], p, org)
		if err != nil {
			s.handleServiceError(w, err, "org project milestone view")
			return
		}
		views = append(views, v)
	}

	updates, err := s.Projects.ListUpdates(ctx, p.ID, 20)
	if err != nil {
		s.handleServiceError(w, err, "org project updates")
		return
	}
	funding, err := s.Donations.FundingHistory(ctx, p.ID, 30)
	if err != nil {
		s.handleServiceError(w, err, "org project funding")
		return
	}
	verifications, err := s.Verification.ListForProject(ctx, p.ID, 50)
	if err != nil {
		s.handleServiceError(w, err, "org project verifications")
		return
	}
	events, err := s.Payments.PaymentEvents(ctx, payments.EventFilter{ProjectID: p.ID, Limit: 100})
	if err != nil {
		s.handleServiceError(w, err, "org project payment events")
		return
	}
	activities, err := s.Audit.List(ctx, audit.Filter{ProjectID: p.ID, Limit: 100})
	if err != nil {
		s.handleServiceError(w, err, "org project activity")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project":         p,
		"organization":    org,
		"milestones":      views,
		"updates":         updates,
		"funding_history": funding,
		"verifications":   sumVerifications(verifications),
		"payment_events":  events,
		"activity":        activities,
		"funding": map[string]any{
			"goal_minor":      p.GoalAmount,
			"funded_minor":    p.FundedAmount,
			"reserved_minor":  p.ReservedAmount,
			"released_minor":  p.ReleasedAmount,
			"available_minor": p.AvailableAmount(),
			"currency":        p.Currency,
			"funding_percent": core.Percent(p.FundingRatio()),
		},
		"progress": milestones.Summarize(ms),
	})
}

func (s *Server) handlePublishProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, org, err := s.ownerProject(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "publish project")
		return
	}
	published, err := s.Projects.Publish(ctx, p.ID)
	if err != nil {
		s.handleServiceError(w, err, "publish project")
		return
	}
	resp := map[string]any{"project": published, "organization": org}
	if !org.Verified {
		resp["warning"] = "Your organization is not yet platform-verified, so milestone funding cannot be released until the review completes."
	}
	writeJSON(w, http.StatusOK, resp)
}

type projectUpdateRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (s *Server) handlePostProjectUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	p, _, err := s.ownerProject(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "post project update")
		return
	}
	var req projectUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "An update needs both a title and a body.")
		return
	}
	update, err := s.Projects.AddUpdate(ctx, p.ID, user.ID, req.Title, req.Body)
	if err != nil {
		s.handleServiceError(w, err, "post project update")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"update": update})
}

func (s *Server) handleOrgActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	org, err := s.Organizations.ByUserID(ctx, user.ID)
	if err != nil {
		s.handleServiceError(w, err, "org activity")
		return
	}
	list, err := s.Projects.List(ctx, projects.Filter{OrgID: org.ID, Limit: 100})
	if err != nil {
		s.handleServiceError(w, err, "org activity projects")
		return
	}
	limit := queryInt(r, "limit", 60, 300)
	activities, err := s.orgActivity(ctx, list, limit)
	if err != nil {
		s.handleServiceError(w, err, "org activity")
		return
	}

	// Also surface organization-level events (creation, verification) that carry
	// no project id.
	orgEvents, err := s.Audit.List(ctx, audit.Filter{Limit: limit})
	if err == nil {
		for _, e := range orgEvents {
			if e.ProjectID == "" && e.EntityID == org.ID {
				activities = append(activities, e)
			}
		}
		sort.SliceStable(activities, func(i, j int) bool {
			return activities[i].CreatedAt.After(activities[j].CreatedAt)
		})
		if len(activities) > limit {
			activities = activities[:limit]
		}
	}

	events, err := s.Payments.PaymentEvents(ctx, payments.EventFilter{Limit: 100})
	if err != nil {
		s.handleServiceError(w, err, "org payment events")
		return
	}
	orgProjects := map[string]bool{}
	for _, p := range list {
		orgProjects[p.ID] = true
	}
	filtered := make([]models.PaymentEvent, 0, len(events))
	for _, e := range events {
		if orgProjects[e.ProjectID] {
			filtered = append(filtered, e)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"activity":       activities,
		"payment_events": filtered,
	})
}

// ---------------------------------------------------------------------------
// Milestones and evidence
// ---------------------------------------------------------------------------

func (s *Server) handleStartMilestone(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, _, _, err := s.ownerMilestone(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "start milestone")
		return
	}
	if err := s.Milestones.SetStatus(ctx, m.ID, models.MilestoneInProgress, actorFrom(ctx), nil); err != nil {
		s.handleServiceError(w, err, "start milestone")
		return
	}
	updated, err := s.Milestones.ByID(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "start milestone")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"milestone": updated})
}

type submissionRequest struct {
	Note string `json:"note"`
}

func (s *Server) handleCreateSubmission(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	m, p, _, err := s.ownerMilestone(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "create submission")
		return
	}
	var req submissionRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	sub, err := s.Evidence.CreateSubmission(ctx, m.ID, p.ID, user.ID, req.Note)
	if err != nil {
		s.handleServiceError(w, err, "create submission")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"submission": sub})
}

type submitRequest struct {
	SubmissionID string `json:"submission_id"`
}

// handleSubmitForVerification runs the verification agent over the milestone's
// cumulative evidence and persists the structured result.
func (s *Server) handleSubmitForVerification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, p, _, err := s.ownerMilestone(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "submit for verification")
		return
	}

	var req submitRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.SubmissionID) != "" {
		sub, err := s.Evidence.SubmissionByID(ctx, strings.TrimSpace(req.SubmissionID))
		if err != nil {
			s.handleServiceError(w, err, "submit for verification")
			return
		}
		if sub.MilestoneID != m.ID {
			writeError(w, http.StatusBadRequest, "invalid_input", "That evidence submission belongs to a different milestone.")
			return
		}
	}

	result, err := s.Verification.VerifyMilestone(ctx, m.ID, actorFrom(ctx))
	if err != nil {
		s.handleServiceError(w, err, "verify milestone")
		return
	}
	updated, _ := s.Milestones.ByID(ctx, m.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"verification": result.Verification,
		"result":       result.Result,
		"milestone":    updated,
		"project":      projectBriefOf(p),
		"decision":     result.Verification.Status,
		"verified":     result.Verification.VerifiedQuantity,
		"required":     result.Verification.RequiredQuantity,
	})
}

// handleUploadEvidence accepts a multipart evidence artefact for a milestone.
func (s *Server) handleUploadEvidence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	maxBytes := s.Evidence.MaxUploadBytes()
	if err := r.ParseMultipartForm(maxBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload", "The upload could not be read: "+core.Truncate(err.Error(), 160))
		return
	}

	milestoneID := strings.TrimSpace(r.FormValue("milestone_id"))
	if milestoneID == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "milestone_id is required.")
		return
	}
	m, p, _, err := s.ownerMilestone(ctx, milestoneID)
	if err != nil {
		s.handleServiceError(w, err, "upload evidence")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload", "A file field is required.")
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload", "The uploaded file could not be read.")
		return
	}
	if int64(len(data)) > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", evidence.ErrFileTooLarge.Error())
		return
	}

	kind := models.EvidenceKind(strings.ToUpper(strings.TrimSpace(r.FormValue("kind"))))
	if kind == "" {
		// Fall back to the milestone's declared primary evidence kind.
		kind = m.PrimaryEvidenceKind()
	}

	submissionID := strings.TrimSpace(r.FormValue("submission_id"))
	if submissionID == "" {
		sub, err := s.Evidence.CreateSubmission(ctx, m.ID, p.ID, user.ID, r.FormValue("note"))
		if err != nil {
			s.handleServiceError(w, err, "create submission")
			return
		}
		submissionID = sub.ID
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if !storage.IsAllowedContentType(contentType) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_type",
			fmt.Sprintf("%s is not an accepted evidence type.", core.Truncate(contentType, 60)))
		return
	}

	item, err := s.Evidence.Add(ctx, evidence.UploadInput{
		SubmissionID:   submissionID,
		MilestoneID:    m.ID,
		ProjectID:      p.ID,
		UploadedBy:     user.ID,
		Kind:           kind,
		RequirementKey: strings.TrimSpace(r.FormValue("requirement_key")),
		Filename:       header.Filename,
		ContentType:    contentType,
		Description:    r.FormValue("description"),
		Hints:          parseHints(r),
		Data:           data,
	})
	if err != nil {
		s.handleServiceError(w, err, "upload evidence")
		return
	}

	items, err := s.Evidence.ListByMilestone(ctx, m.ID)
	if err != nil {
		s.handleServiceError(w, err, "upload evidence coverage")
		return
	}
	var coverage []evidence.RequirementsCoverage
	if len(m.EvidenceRequirements) > 0 {
		coverage = evidence.Coverage(m.EvidenceRequirements, items)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"evidence":      item,
		"coverage":      coverage,
		"submission_id": submissionID,
	})
}

// parseHints reads the uploader's structured assertions off the multipart form.
// These are treated as claims by the verification pipeline, never as facts.
func parseHints(r *http.Request) models.EvidenceHints {
	h := models.EvidenceHints{
		Location:      strings.TrimSpace(r.FormValue("location")),
		CapturedAt:    strings.TrimSpace(r.FormValue("captured_at")),
		UnitLabel:     strings.TrimSpace(r.FormValue("unit_label")),
		InvoiceNo:     strings.TrimSpace(r.FormValue("invoice_number")),
		Vendor:        strings.TrimSpace(r.FormValue("vendor")),
		UnitIDs:       splitList(r.FormValue("unit_ids")),
		Villages:      splitList(r.FormValue("villages")),
		AmountMinor:   parseAmountField(r.FormValue("amount_minor")),
		Quantity:      parseIntField(r.FormValue("quantity")),
		Beneficiaries: parseIntField(r.FormValue("beneficiaries")),
	}
	return h
}

func splitList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseAmountField(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if m, ok := core.ParseAmount(v); ok {
		return int64(m)
	}
	return 0
}

func parseIntField(v string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}
