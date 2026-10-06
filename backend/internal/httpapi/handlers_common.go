package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/projects"
)

// ---------------------------------------------------------------------------
// View types
//
// These are the shapes the UI consumes. They are assembled in the HTTP layer
// from service results so the domain models stay free of presentation concerns,
// and they never invent a figure: every number here is copied from a stored row.
// ---------------------------------------------------------------------------

// orgBrief is the organization header embedded in project and milestone views.
type orgBrief struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Slug               string `json:"slug"`
	Verified           bool   `json:"verified"`
	VerificationStatus string `json:"verification_status"`
	Country            string `json:"country"`
}

func briefOf(org *models.Organization) orgBrief {
	if org == nil {
		return orgBrief{}
	}
	return orgBrief{
		ID:                 org.ID,
		Name:               org.Name,
		Slug:               org.Slug,
		Verified:           org.Verified,
		VerificationStatus: string(org.VerificationStatus),
		Country:            org.Country,
	}
}

// projectCard is a marketplace/discovery row.
type projectCard struct {
	models.Project
	Organization    orgBrief            `json:"organization"`
	Progress        milestones.Progress `json:"progress"`
	FundingPercent  int                 `json:"funding_percent"`
	VerifiedPercent int                 `json:"verified_percent"`
}

// milestoneView is a milestone with the context a reviewer needs.
type milestoneView struct {
	models.Milestone
	Project            *projectBrief        `json:"project,omitempty"`
	Organization       *orgBrief            `json:"organization,omitempty"`
	LatestVerification *verificationSummary `json:"latest_verification,omitempty"`
	Coverage           any                  `json:"evidence_coverage,omitempty"`
	EvidenceCount      int                  `json:"evidence_count"`
	DuplicateCount     int                  `json:"duplicate_count"`
	SubmissionCount    int                  `json:"submission_count"`
	AllocatedMinor     int64                `json:"allocated_minor"`
	VerifiedPercent    int                  `json:"verified_percent"`
}

// projectBrief is a compact project reference.
type projectBrief struct {
	ID             string               `json:"id"`
	Title          string               `json:"title"`
	Slug           string               `json:"slug"`
	Category       string               `json:"category"`
	Status         models.ProjectStatus `json:"status"`
	Currency       string               `json:"currency"`
	GoalAmount     int64                `json:"goal_amount"`
	FundedAmount   int64                `json:"funded_amount"`
	ReleasedAmount int64                `json:"released_amount"`
}

func projectBriefOf(p *models.Project) *projectBrief {
	if p == nil {
		return nil
	}
	return &projectBrief{
		ID: p.ID, Title: p.Title, Slug: p.Slug, Category: p.Category, Status: p.Status,
		Currency: p.Currency, GoalAmount: p.GoalAmount, FundedAmount: p.FundedAmount,
		ReleasedAmount: p.ReleasedAmount,
	}
}

// verificationSummary is the reviewable digest of one verification run.
type verificationSummary struct {
	ID               string                      `json:"id"`
	Status           models.VerificationDecision `json:"status"`
	Recommendation   models.VerificationDecision `json:"recommendation"`
	Confidence       float64                     `json:"confidence"`
	VerifiedQuantity int                         `json:"verified_quantity"`
	RequiredQuantity int                         `json:"required_quantity"`
	UnitLabel        string                      `json:"unit_label"`
	Summary          string                      `json:"summary"`
	Engine           string                      `json:"engine"`
	Model            string                      `json:"model"`
	IsOverride       bool                        `json:"is_override"`
	HumanDecision    string                      `json:"human_decision,omitempty"`
	HumanNote        string                      `json:"human_note,omitempty"`
	CreatedAt        time.Time                   `json:"created_at"`
	DecidedAt        *time.Time                  `json:"decided_at,omitempty"`
	Claimed          []models.Claim              `json:"claimed"`
	EvidenceFound    []models.EvidenceFinding    `json:"evidence_found"`
	Inconsistencies  []models.Inconsistency      `json:"inconsistencies"`
	MissingEvidence  []models.MissingEvidence    `json:"missing_evidence"`
	Checks           []models.VerificationCheck  `json:"checks"`
	Financial        models.FinancialCheck       `json:"financial"`
	VerifiedUnitIDs  []string                    `json:"verified_unit_ids"`
	EvidenceCount    int                         `json:"evidence_count"`
	UsableCount      int                         `json:"usable_count"`
	DuplicateCount   int                         `json:"duplicate_count"`
	StepCount        int                         `json:"step_count"`
}

func summariseVerification(v *models.Verification) *verificationSummary {
	if v == nil {
		return nil
	}
	return &verificationSummary{
		ID: v.ID, Status: v.Status, Recommendation: v.Recommendation,
		Confidence: v.Confidence, VerifiedQuantity: v.VerifiedQuantity,
		RequiredQuantity: v.RequiredQuantity, UnitLabel: v.UnitLabel,
		Summary: v.Summary, Engine: v.Engine, Model: v.Model,
		IsOverride: v.IsOverride, HumanDecision: v.HumanDecision, HumanNote: v.HumanNote,
		CreatedAt: v.CreatedAt, DecidedAt: v.DecidedAt,
		Claimed: v.Claimed, EvidenceFound: v.EvidenceFound,
		Inconsistencies: v.Inconsistencies, MissingEvidence: v.MissingEvidence,
		Checks: v.Checks, Financial: v.Result.Financial,
		VerifiedUnitIDs: v.Result.VerifiedUnitIDs,
		EvidenceCount:   v.Result.EvidenceCount, UsableCount: v.Result.UsableCount,
		DuplicateCount: v.Result.DuplicateCount, StepCount: len(v.Result.Steps),
	}
}

// moneyView renders an amount in both minor units (for arithmetic) and a
// human-readable label, so no client has to re-implement currency formatting.
type moneyView struct {
	Minor    int64  `json:"minor"`
	Currency string `json:"currency"`
	Label    string `json:"label"`
	Short    string `json:"short"`
}

func money(minor int64, currency string) moneyView {
	return moneyView{
		Minor: minor, Currency: currency,
		Label: core.FormatMoney(core.Money(minor), currency),
		Short: core.FormatMoneyShort(core.Money(minor), currency),
	}
}

// ---------------------------------------------------------------------------
// Identity helpers
// ---------------------------------------------------------------------------

// currentUser returns the authenticated user, or nil when the route is public.
func currentUser(r *http.Request) *models.User { return userFrom(r.Context()) }

// currentOrg resolves the organization owned by the signed-in user.
func (s *Server) currentOrg(ctx context.Context, userID string) (*models.Organization, error) {
	org, err := s.Organizations.ByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return org, nil
}

// ownerProject loads a project and verifies the signed-in user's organization
// owns it. Admins bypass the ownership check; donors never do.
func (s *Server) ownerProject(ctx context.Context, projectID string) (*models.Project, *models.Organization, error) {
	u := models.ActorFrom(ctx)
	p, err := s.Projects.ByID(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	if u.Role == models.RoleAdmin {
		org, err := s.Organizations.ByID(ctx, p.OrgID)
		if err != nil {
			return nil, nil, err
		}
		return p, org, nil
	}
	org, err := s.Organizations.ByUserID(ctx, u.ID)
	if err != nil {
		return nil, nil, projects.ErrNotOwner
	}
	if org.ID != p.OrgID {
		return nil, nil, projects.ErrNotOwner
	}
	return p, org, nil
}

// ownerMilestone loads a milestone and verifies ownership through its project.
func (s *Server) ownerMilestone(ctx context.Context, milestoneID string) (*models.Milestone, *models.Project, *models.Organization, error) {
	m, err := s.Milestones.ByID(ctx, milestoneID)
	if err != nil {
		return nil, nil, nil, err
	}
	p, org, err := s.ownerProject(ctx, m.ProjectID)
	if err != nil {
		return nil, nil, nil, err
	}
	return m, p, org, nil
}

// canSeeVerification reports whether the signed-in user may read a verification
// of the given project. ImpactPay shows verification detail to everyone who can
// see the project, admins, and the owning organization, but never for a draft.
func (s *Server) canSeeProject(ctx context.Context, projectID string) (bool, error) {
	p, err := s.Projects.ByID(ctx, projectID)
	if err != nil {
		return false, err
	}
	if p.Status != models.ProjectDraft {
		return true, nil
	}
	_, _, err = s.ownerProject(ctx, projectID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, projects.ErrNotOwner) || errors.Is(err, organizations.ErrNotFound) {
		u := models.ActorFrom(ctx)
		return u.Role == models.RoleAdmin, nil
	}
	return false, err
}

// ---------------------------------------------------------------------------
// Session cookies
// ---------------------------------------------------------------------------

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	ttl := s.Config.SessionTTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.Config.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  core.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.Config.SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

func queryStr(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}

func queryInt(r *http.Request, key string, def, max int) int {
	raw := queryStr(r, key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < 0 {
		return 0
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

// pathValue reads a URL path parameter (e.g. /projects/{slug}).
func pathValue(r *http.Request, key string) string {
	return strings.TrimSpace(chi.URLParam(r, key))
}

// strPtr returns a pointer to a trimmed string, or nil when the field was absent
// from the request. It is how the API distinguishes "leave unchanged" from
// "clear this field".
func strPtr(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	return &s
}
