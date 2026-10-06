package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/donations"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/payments"
)

// fundingView is one milestone's share of a contribution.
type fundingView struct {
	MilestoneID        string                 `json:"milestone_id"`
	Ordinal            int                    `json:"ordinal"`
	Title              string                 `json:"title"`
	UnitLabel          string                 `json:"unit_label"`
	Status             models.MilestoneStatus `json:"status"`
	AmountMinor        int64                  `json:"amount_minor"`
	Currency           string                 `json:"currency"`
	Amount             moneyView              `json:"amount"`
	RequiredQuantity   int                    `json:"required_quantity"`
	VerifiedQuantity   int                    `json:"verified_quantity"`
	VerifiedPercent    int                    `json:"verified_percent"`
	LatestVerification *verificationSummary   `json:"latest_verification,omitempty"`
}

// donationView is the donor-facing view of one contribution.
type donationView struct {
	models.Donation
	Amount        moneyView     `json:"amount_view"`
	Project       *projectBrief `json:"project,omitempty"`
	Organization  *orgBrief     `json:"organization,omitempty"`
	Fundings      []fundingView `json:"fundings"`
	VerifiedMinor int64         `json:"verified_minor"`
	PendingMinor  int64         `json:"pending_minor"`
}

// buildDonationView assembles the project, organization and per-milestone
// attribution for a contribution.
func (s *Server) buildDonationView(ctx context.Context, d models.Donation, withFundings bool) (donationView, error) {
	view := donationView{
		Donation: d,
		Amount:   money(d.Amount, d.Currency),
		Fundings: []fundingView{},
	}
	if p, err := s.Projects.ByID(ctx, d.ProjectID); err == nil {
		view.Project = projectBriefOf(p)
		if org, err := s.Organizations.ByID(ctx, p.OrgID); err == nil {
			b := briefOf(org)
			view.Organization = &b
		}
	}
	if !withFundings {
		return view, nil
	}

	fundings, err := s.Donations.FundingsForDonation(ctx, d.ID)
	if err != nil {
		return view, err
	}
	for _, f := range fundings {
		fv := fundingView{
			MilestoneID:      f.Milestone.ID,
			Ordinal:          f.Milestone.Ordinal,
			Title:            f.Milestone.Title,
			UnitLabel:        f.Milestone.UnitLabel,
			Status:           f.Milestone.Status,
			AmountMinor:      f.MilestoneFunding.Amount,
			Currency:         d.Currency,
			Amount:           money(f.MilestoneFunding.Amount, d.Currency),
			RequiredQuantity: f.Milestone.RequiredQuantity,
			VerifiedQuantity: f.Milestone.VerifiedQuantity,
			VerifiedPercent:  core.Percent(f.Milestone.VerifiedRatio()),
		}
		if v, err := s.Verification.LatestForMilestone(ctx, f.Milestone.ID); err == nil {
			fv.LatestVerification = summariseVerification(v)
		}
		if f.Milestone.Status == models.MilestoneCompleted || f.Milestone.Status == models.MilestonePaymentReleased {
			view.VerifiedMinor += f.MilestoneFunding.Amount
		} else {
			view.PendingMinor += f.MilestoneFunding.Amount
		}
		view.Fundings = append(view.Fundings, fv)
	}
	return view, nil
}

type createDonationRequest struct {
	ProjectID     string `json:"project_id"`
	ProjectSlug   string `json:"project_slug"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Note          string `json:"note"`
	Simulated     *bool  `json:"simulated"`
	ReturnURL     string `json:"return_url"`
	CancelURL     string `json:"cancel_url"`
	StartCheckout *bool  `json:"start_checkout"`
}

// handleCreateDonation opens a contribution and, by default, starts PayPal
// checkout in the same call so the donor only makes one round trip.
func (s *Server) handleCreateDonation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	var req createDonationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var p *models.Project
	var err error
	switch {
	case strings.TrimSpace(req.ProjectID) != "":
		p, err = s.Projects.ByID(ctx, strings.TrimSpace(req.ProjectID))
	case strings.TrimSpace(req.ProjectSlug) != "":
		p, err = s.Projects.BySlug(ctx, strings.TrimSpace(req.ProjectSlug))
	default:
		writeError(w, http.StatusBadRequest, "invalid_input", "A project must be selected.")
		return
	}
	if err != nil {
		s.handleServiceError(w, err, "create donation project")
		return
	}
	if p.Status != models.ProjectFundingOpen && p.Status != models.ProjectInProgress {
		writeError(w, http.StatusConflict, "project_closed", donations.ErrProjectClosed.Error())
		return
	}

	org, err := s.Organizations.ByID(ctx, p.OrgID)
	if err != nil {
		s.handleServiceError(w, err, "create donation organization")
		return
	}

	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = p.Currency
	}
	simulated := s.Payments.Provider().Simulated()
	if req.Simulated != nil {
		simulated = *req.Simulated
	}

	d, err := s.Donations.Create(ctx, donations.CreateInput{
		ProjectID:      p.ID,
		DonorID:        user.ID,
		Amount:         req.AmountMinor,
		Currency:       currency,
		Note:           req.Note,
		Simulated:      simulated,
		OrgOwnerUserID: org.UserID,
	})
	if err != nil {
		s.handleServiceError(w, err, "create donation")
		return
	}

	resp := map[string]any{
		"donation":     d,
		"donation_url": "/donor?donation=" + d.ID,
		"trace_url":    "/impact/" + d.ID,
	}

	startCheckout := true
	if req.StartCheckout != nil {
		startCheckout = *req.StartCheckout
	}
	if startCheckout {
		order, err := s.startCheckout(ctx, d.ID, req.ReturnURL, req.CancelURL, d.Currency)
		if err != nil {
			// The contribution exists; only the checkout failed. The client can
			// retry via POST /donations/{id}/order.
			s.handleServiceError(w, err, "create donation order")
			return
		}
		resp["order"] = order
	}

	writeJSON(w, http.StatusCreated, resp)
}

// startCheckout creates the provider order and attaches it to the donation.
func (s *Server) startCheckout(ctx context.Context, donationID, returnURL, cancelURL, currency string) (*payments.Order, error) {
	if strings.TrimSpace(returnURL) == "" {
		returnURL = strings.TrimRight(s.Config.PublicURL, "/") + "/donor"
	}
	if strings.TrimSpace(cancelURL) == "" {
		cancelURL = strings.TrimRight(s.Config.PublicURL, "/") + "/projects"
	}
	_, order, err := s.Payments.CreateDonationOrder(ctx, donationID, returnURL, cancelURL)
	if err != nil {
		return nil, err
	}
	return order, nil
}

// ownedDonation loads a contribution and enforces that the signed-in user owns
// it (admins may inspect any contribution).
func (s *Server) ownedDonation(ctx context.Context, id string) (*models.Donation, error) {
	d, err := s.Donations.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	u := models.ActorFrom(ctx)
	if u.Role == models.RoleAdmin || u.ID == d.DonorID {
		return d, nil
	}
	// An organization may inspect contributions to its own projects (for the
	// funding feed), but donors may not inspect each other's.
	if p, err := s.Projects.ByID(ctx, d.ProjectID); err == nil {
		if org, err := s.Organizations.ByUserID(ctx, u.ID); err == nil && org.ID == p.OrgID {
			return d, nil
		}
	}
	return nil, donations.ErrNotFound
}

func (s *Server) handleListMyDonations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	list, err := s.Donations.ListByDonor(ctx, user.ID, queryInt(r, "limit", 50, 200))
	if err != nil {
		s.handleServiceError(w, err, "list donations")
		return
	}
	views := make([]donationView, 0, len(list))
	for _, d := range list {
		v, err := s.buildDonationView(ctx, d, true)
		if err != nil {
			s.handleServiceError(w, err, "donation view")
			return
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"donations": views})
}

func (s *Server) handleGetDonation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.ownedDonation(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "get donation")
		return
	}
	view, err := s.buildDonationView(ctx, *d, true)
	if err != nil {
		s.handleServiceError(w, err, "donation view")
		return
	}
	events, err := s.Payments.PaymentEvents(ctx, payments.EventFilter{DonationID: d.ID, Limit: 50})
	if err != nil {
		s.handleServiceError(w, err, "donation events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"donation":       view,
		"payment_events": events,
	})
}

type orderRequest struct {
	ReturnURL string `json:"return_url"`
	CancelURL string `json:"cancel_url"`
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.ownedDonation(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "create order")
		return
	}
	var req orderRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	updated, order, err := s.Payments.CreateDonationOrder(ctx, d.ID, defaultReturn(req.ReturnURL, s.Config.PublicURL), defaultCancel(req.CancelURL, s.Config.PublicURL))
	if err != nil {
		s.handleServiceError(w, err, "create order")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"donation": updated, "order": order})
}

func (s *Server) handleCaptureDonation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.ownedDonation(ctx, pathValue(r, "id"))
	if err != nil {
		s.handleServiceError(w, err, "capture donation")
		return
	}
	result, err := s.Payments.CaptureDonation(ctx, d.ID)
	if err != nil {
		s.handleServiceError(w, err, "capture donation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"donation":    result.Donation,
		"capture":     result.Capture,
		"allocations": result.Allocations,
		"project":     result.Project,
		"trace_url":   "/impact/" + result.Donation.ID,
	})
}

// handleDonorDashboard is the donor home: totals, the contributions with their
// attribution, and what has actually been verified.
func (s *Server) handleDonorDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	stats, err := s.Donations.StatsForDonor(ctx, user.ID)
	if err != nil {
		s.handleServiceError(w, err, "donor stats")
		return
	}
	list, err := s.Donations.ListByDonor(ctx, user.ID, queryInt(r, "limit", 20, 100))
	if err != nil {
		s.handleServiceError(w, err, "donor donations")
		return
	}
	views := make([]donationView, 0, len(list))
	projects := map[string]projectBrief{}
	for _, d := range list {
		v, err := s.buildDonationView(ctx, d, true)
		if err != nil {
			s.handleServiceError(w, err, "donor donation view")
			return
		}
		if v.Project != nil {
			projects[v.Project.ID] = *v.Project
		}
		views = append(views, v)
	}

	unread, _ := s.Notifications.UnreadCount(ctx, user.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"stats":                stats,
		"donations":            views,
		"projects":             projects,
		"unread_notifications": unread,
		"currency":             currencyFor(stats, views),
	})
}

func currencyFor(stats donations.DonorStats, views []donationView) string {
	for _, v := range views {
		if v.Currency != "" {
			return v.Currency
		}
	}
	return "INR"
}

func defaultReturn(v, publicURL string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return strings.TrimRight(publicURL, "/") + "/donor"
}

func defaultCancel(v, publicURL string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return strings.TrimRight(publicURL, "/") + "/projects"
}
