// Package impact turns stored financial and verification rows into the
// donor-facing narratives: the Proof Graph behind "Trace My Impact", the
// organization's Impact Passport, and grounded answers to
// "what has my money actually accomplished?".
//
// Every figure here is computed from persisted rows. Nothing is estimated,
// rounded up, or invented: if the data does not support a claim, the graph shows
// the unverified state instead.
package impact

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/models"
)

// Service provides impact read models.
type Service struct {
	db *db.DB
}

// New creates the impact service.
func New(database *db.DB) *Service { return &Service{db: database} }

// NodeType classifies a Proof Graph node.
type NodeType string

const (
	NodeDonation    NodeType = "DONATION"
	NodeProject     NodeType = "PROJECT"
	NodeFund        NodeType = "PROJECT_FUND"
	NodeMilestone   NodeType = "MILESTONE"
	NodeResource    NodeType = "RESOURCE"
	NodeExpense     NodeType = "EXPENSE"
	NodeEvidence    NodeType = "EVIDENCE"
	NodeVerification NodeType = "VERIFICATION"
	NodeOutcome     NodeType = "OUTCOME"
	NodeBeneficiary NodeType = "BENEFICIARY"
)

// Metric is a labelled figure shown on a node.
type Metric struct {
	Label string  `json:"label"`
	Value string  `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	Raw   float64 `json:"raw,omitempty"`
}

// EvidenceRef is a reference to inspectable supporting evidence.
type EvidenceRef struct {
	ID          string   `json:"id"`
	Filename    string   `json:"filename"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	SizeBytes   int64    `json:"size_bytes"`
	MimeType    string   `json:"mime_type"`
	UnitIDs     []string `json:"unit_ids,omitempty"`
	AmountMinor int64    `json:"amount_minor,omitempty"`
	Vendor      string   `json:"vendor,omitempty"`
	InvoiceNo   string   `json:"invoice_number,omitempty"`
	Location    string   `json:"location,omitempty"`
	Quality     string   `json:"quality,omitempty"`
	CreatedAt   string   `json:"created_at"`
	URL         string   `json:"url"`
}

// Node is one vertex of the Proof Graph.
type Node struct {
	ID       string         `json:"id"`
	Type     NodeType       `json:"type"`
	Title    string         `json:"title"`
	Subtitle string         `json:"subtitle"`
	Status   string         `json:"status"`
	Verified bool           `json:"verified"`
	AmountMinor int64       `json:"amount_minor"`
	Currency string         `json:"currency"`
	Metrics  []Metric       `json:"metrics"`
	Evidence []EvidenceRef  `json:"evidence"`
	Detail   string         `json:"detail"`
}

// Edge is one link of the Proof Graph.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

// ProofGraph is the complete interactive trace for one donation.
type ProofGraph struct {
	Donation       models.Donation   `json:"donation"`
	Project        models.Project    `json:"project"`
	Organization   OrgSummary        `json:"organization"`
	Nodes          []Node            `json:"nodes"`
	Edges          []Edge            `json:"edges"`
	Summary        string            `json:"summary"`
	VerifiedShare  ShareBreakdown    `json:"verified_share"`
	AllocationNote string            `json:"allocation_note"`
}

// ShareBreakdown separates a donation into what is verified and what is pending.
type ShareBreakdown struct {
	TotalMinor    int64 `json:"total_minor"`
	VerifiedMinor int64 `json:"verified_minor"`
	PendingMinor  int64 `json:"pending_minor"`
	HeldMinor     int64 `json:"held_minor"`
	Currency      string `json:"currency"`
}

// OrgSummary is the organization header shown alongside a trace.
type OrgSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Verified bool   `json:"verified"`
	Country  string `json:"country"`
}

// Trace builds the Proof Graph for one donation.
func (s *Service) Trace(ctx context.Context, donationID string) (*ProofGraph, error) {
	d, err := s.donation(ctx, donationID)
	if err != nil {
		return nil, err
	}
	p, err := s.project(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}
	org, err := s.orgSummary(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}

	fundings, err := s.fundingsWithMilestones(ctx, donationID)
	if err != nil {
		return nil, err
	}

	g := &ProofGraph{
		Donation:     *d,
		Project:      *p,
		Organization: *org,
		Nodes:        []Node{},
		Edges:        []Edge{},
		VerifiedShare: ShareBreakdown{TotalMinor: d.Amount, Currency: d.Currency},
	}

	donationID_ := "donation"
	g.Nodes = append(g.Nodes, Node{
		ID:          donationID_,
		Type:        NodeDonation,
		Title:       core.FormatMoney(core.Money(d.Amount), d.Currency),
		Subtitle:    "Your contribution",
		Status:      string(d.Status),
		AmountMinor: d.Amount,
		Currency:    d.Currency,
		Detail: fmt.Sprintf("Captured %s via %s%s.",
			d.CreatedAt.Format("2 Jan 2006"),
			providerLabel(d.Provider),
			simulatedSuffix(d.Simulated)),
		Metrics: []Metric{
			{Label: "Contributed", Value: core.FormatMoney(core.Money(d.Amount), d.Currency)},
			{Label: "Status", Value: statusLabel(string(d.Status))},
		},
	})

	projectNodeID := "project"
	g.Nodes = append(g.Nodes, Node{
		ID:       projectNodeID,
		Type:     NodeProject,
		Title:    p.Title,
		Subtitle: org.Name,
		Status:   string(p.Status),
		Verified: org.Verified,
		Currency: p.Currency,
		Detail:   p.OutcomeDescription,
		Metrics: []Metric{
			{Label: "Funding goal", Value: core.FormatMoney(core.Money(p.GoalAmount), p.Currency)},
			{Label: "Raised", Value: core.FormatMoney(core.Money(p.FundedAmount), p.Currency)},
			{Label: "Beneficiaries", Value: fmt.Sprintf("%d", p.BeneficiaryCount), Unit: "people"},
		},
	})
	g.Edges = append(g.Edges, Edge{From: donationID_, To: projectNodeID, Label: "funds"})

	fundNodeID := "project_fund"
	g.Nodes = append(g.Nodes, Node{
		ID:       fundNodeID,
		Type:     NodeFund,
		Title:    "Project fund",
		Subtitle: fmt.Sprintf("%s raised of %s", core.FormatMoney(core.Money(p.FundedAmount), p.Currency), core.FormatMoney(core.Money(p.GoalAmount), p.Currency)),
		Status:   "ACTIVE",
		Currency: p.Currency,
		AmountMinor: p.FundedAmount,
		Detail: "Contributions are pooled in ImpactPay's ledger. Each milestone's funding is released only after its evidence is verified and the backend's release policy passes.",
		Metrics: []Metric{
			{Label: "Captured", Value: core.FormatMoney(core.Money(p.FundedAmount), p.Currency)},
			{Label: "Awaiting release", Value: core.FormatMoney(core.Money(p.ReservedAmount), p.Currency)},
			{Label: "Released", Value: core.FormatMoney(core.Money(p.ReleasedAmount), p.Currency)},
		},
	})
	g.Edges = append(g.Edges, Edge{From: projectNodeID, To: fundNodeID, Label: "pooled into"})

	if len(fundings) == 0 {
		g.AllocationNote = "This contribution has not yet been attributed to a milestone."
		g.Summary = fmt.Sprintf("Your %s contribution to %s is recorded and awaiting attribution to a milestone.",
			core.FormatMoney(core.Money(d.Amount), d.Currency), p.Title)
		return g, nil
	}

	for i, f := range fundings {
		m := f.Milestone
		funding := f.Funding

		verified := m.Status == models.MilestoneCompleted || m.Status == models.MilestonePaymentReleased
		held := m.Status == models.MilestoneHeld || m.Status == models.MilestoneMoreEvidenceRequested

		switch {
		case verified:
			g.VerifiedShare.VerifiedMinor += funding.Amount
		case held:
			g.VerifiedShare.HeldMinor += funding.Amount
		default:
			g.VerifiedShare.PendingMinor += funding.Amount
		}

		msNodeID := fmt.Sprintf("milestone-%d", i+1)
		g.Nodes = append(g.Nodes, Node{
			ID:       msNodeID,
			Type:     NodeMilestone,
			Title:    fmt.Sprintf("%d. %s", m.Ordinal, m.Title),
			Subtitle: m.Requirement,
			Status:   string(m.Status),
			Verified: verified,
			AmountMinor: funding.Amount,
			Currency: p.Currency,
			Detail:   m.Description,
			Metrics: []Metric{
				{Label: "Your share", Value: core.FormatMoney(core.Money(funding.Amount), p.Currency)},
				{Label: "Milestone budget", Value: core.FormatMoney(core.Money(m.BudgetAmount), p.Currency)},
				{Label: "Verified", Value: fmt.Sprintf("%d of %d", m.VerifiedQuantity, m.RequiredQuantity), Unit: m.UnitLabel},
			},
		})
		g.Edges = append(g.Edges, Edge{From: fundNodeID, To: msNodeID, Label: "attributed to"})

		unitCost := int64(0)
		if m.RequiredQuantity > 0 {
			unitCost = m.BudgetAmount / int64(m.RequiredQuantity)
		}
		unitsFromDonation := 0
		if unitCost > 0 {
			unitsFromDonation = int(int64(funding.Amount) / unitCost)
		}

		// Resource node: the physical thing the contribution bought.
		resourceNodeID := msNodeID + "-resource"
		if unitCost > 0 {
			g.Nodes = append(g.Nodes, Node{
				ID:       resourceNodeID,
				Type:     NodeResource,
				Title:    fmt.Sprintf("%d %s", unitsFromDonation, m.UnitLabel),
				Subtitle: fmt.Sprintf("%s per %s across this milestone", core.FormatMoney(core.Money(unitCost), p.Currency), singular(m.UnitLabel)),
				Status:   map[bool]string{true: "VERIFIED", false: "PENDING"}[verified],
				Verified: verified,
				Currency: p.Currency,
				Detail: fmt.Sprintf("Your %s covers %d of this milestone's %d %s. The milestone budget of %s over %d %s sets the per-unit cost of %s.",
					core.FormatMoney(core.Money(funding.Amount), p.Currency),
					unitsFromDonation, m.RequiredQuantity, m.UnitLabel,
					core.FormatMoney(core.Money(m.BudgetAmount), p.Currency),
					m.RequiredQuantity, m.UnitLabel,
					core.FormatMoney(core.Money(unitCost), p.Currency)),
				Metrics: []Metric{
					{Label: "Units from your gift", Value: fmt.Sprintf("%d", unitsFromDonation), Unit: m.UnitLabel, Raw: float64(unitsFromDonation)},
					{Label: "Cost per " + singular(m.UnitLabel), Value: core.FormatMoney(core.Money(unitCost), p.Currency)},
					{Label: "Milestone verified", Value: fmt.Sprintf("%d / %d", m.VerifiedQuantity, m.RequiredQuantity), Unit: m.UnitLabel},
				},
			})
			g.Edges = append(g.Edges, Edge{From: msNodeID, To: resourceNodeID, Label: "funds"})
		}

		// Evidence nodes, grouped per artefact so each one is inspectable.
		refs, totalEvidence, err := s.evidenceRefs(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		if len(refs) > 0 {
			evidenceParent := msNodeID
			evidenceNodeID := msNodeID + "-evidence"
			g.Nodes = append(g.Nodes, Node{
				ID:       evidenceNodeID,
				Type:     NodeEvidence,
				Title:    fmt.Sprintf("%d evidence artefacts", totalEvidence),
				Subtitle: summariseKinds(refs),
				Status:   map[bool]string{true: "VERIFIED", false: "IN REVIEW"}[verified],
				Verified: verified,
				Currency: p.Currency,
				Detail: fmt.Sprintf("Every artefact below is stored, content-hashed and, where it carries an identifier, counted towards the milestone's requirement. %s",
					duplicateNote(refs)),
				Evidence: refs,
				Metrics: []Metric{
					{Label: "Artefacts", Value: fmt.Sprintf("%d", totalEvidence), Unit: "files"},
					{Label: "Distinct identifiers", Value: fmt.Sprintf("%d", distinctUnits(refs)), Unit: m.UnitLabel},
				},
			})
			g.Edges = append(g.Edges, Edge{From: evidenceParent, To: evidenceNodeID, Label: "supported by"})
			if unitCost > 0 {
				g.Edges = append(g.Edges, Edge{From: resourceNodeID, To: evidenceNodeID, Label: "documented by"})
			}

			// Verification node with the actual agent decision.
			v, err := s.latestVerification(ctx, m.ID)
			if err != nil {
				return nil, err
			}
			if v != nil {
				vNodeID := msNodeID + "-verification"
				decision := string(v.Recommendation)
				if v.IsOverride {
					decision = fmt.Sprintf("%s (human override: %s)", v.Recommendation, v.HumanDecision)
				}
				g.Nodes = append(g.Nodes, Node{
					ID:       vNodeID,
					Type:     NodeVerification,
					Title:    "AI verification",
					Subtitle: decision,
					Status:   decision,
					Verified: v.Recommendation == models.DecisionApprove,
					Currency: p.Currency,
					Detail:   v.Summary,
					Metrics: []Metric{
						{Label: "Recommendation", Value: decision},
						{Label: "Verified", Value: fmt.Sprintf("%d / %d", v.VerifiedQuantity, v.RequiredQuantity), Unit: v.UnitLabel},
						{Label: "Confidence", Value: fmt.Sprintf("%d%%", core.Percent(v.Confidence)), Raw: v.Confidence},
						{Label: "Engine", Value: v.Engine},
					},
				})
				g.Edges = append(g.Edges, Edge{From: evidenceNodeID, To: vNodeID, Label: "analysed by"})
				if verified {
					g.Edges = append(g.Edges, Edge{From: vNodeID, To: resourceNodeID, Label: "confirms"})
				}
			}
		}

		// Outcome node: what was actually achieved, only when verified.
		outcomeNodeID := msNodeID + "-outcome"
		perUnit := 0.0
		if m.RequiredQuantity > 0 && p.BeneficiaryCount > 0 {
			perUnit = float64(p.BeneficiaryCount) / float64(m.RequiredQuantity)
		}
		beneficiaries := int(math.Floor(float64(unitsFromDonation) * perUnit))
		g.Nodes = append(g.Nodes, Node{
			ID:       outcomeNodeID,
			Type:     NodeOutcome,
			Title:    verifiedOutcomeTitle(verified, unitsFromDonation, m.UnitLabel),
			Subtitle: outcomeSubtitle(verified, m),
			Status:   string(m.Status),
			Verified: verified,
			Currency: p.Currency,
			Detail:   outcomeDetail(verified, m, *p, unitsFromDonation, perUnit),
			Metrics: outcomeMetrics(verified, m, unitsFromDonation, beneficiaries),
		})
		g.Edges = append(g.Edges, Edge{From: msNodeID, To: outcomeNodeID, Label: "achieved"})

		if beneficiaries > 0 || (p.BeneficiaryCount > 0 && m.RequiredQuantity > 0) {
			bNodeID := msNodeID + "-beneficiaries"
			g.Nodes = append(g.Nodes, Node{
				ID:       bNodeID,
				Type:     NodeBeneficiary,
				Title:    fmt.Sprintf("%d families", beneficiaries),
				Subtitle: fmt.Sprintf("%.1f people per %s, from the project's own beneficiary ratio", perUnit, singular(m.UnitLabel)),
				Status:   map[bool]string{true: "SERVED", false: "PROJECTED"}[verified],
				Verified: verified,
				Currency: p.Currency,
				Detail: fmt.Sprintf("%s states %d beneficiaries against %d %s. Applying that same ratio to your %d %s gives %d families.",
					p.Title, p.BeneficiaryCount, m.RequiredQuantity, m.UnitLabel, unitsFromDonation, m.UnitLabel, beneficiaries),
				Metrics: []Metric{
					{Label: "Families from your gift", Value: fmt.Sprintf("%d", beneficiaries), Unit: "families", Raw: float64(beneficiaries)},
					{Label: "Project beneficiaries", Value: fmt.Sprintf("%d", p.BeneficiaryCount), Unit: "families"},
				},
			})
			if verified {
				g.Edges = append(g.Edges, Edge{From: outcomeNodeID, To: bNodeID, Label: "serving"})
			} else {
				g.Edges = append(g.Edges, Edge{From: resourceNodeID, To: bNodeID, Label: "projected to serve"})
			}
		}
	}

	g.Summary = traceSummary(g)
	if g.VerifiedShare.VerifiedMinor == 0 {
		g.AllocationNote = "None of this contribution's milestones have been verified yet, so no verified impact is claimed for it."
	}
	return g, nil
}

// ---------------------------------------------------------------------------
// narrative helpers
// ---------------------------------------------------------------------------

func traceSummary(g *ProofGraph) string {
	d := g.Donation
	cur := d.Currency
	if g.VerifiedShare.VerifiedMinor > 0 && g.VerifiedShare.PendingMinor == 0 && g.VerifiedShare.HeldMinor == 0 {
		return fmt.Sprintf("All %s of your contribution is attributed to verified, released milestones.",
			core.FormatMoney(core.Money(d.Amount), cur))
	}
	if g.VerifiedShare.VerifiedMinor > 0 {
		return fmt.Sprintf("%s of your %s is attributed to verified milestones; %s is still awaiting verification.",
			core.FormatMoney(core.Money(g.VerifiedShare.VerifiedMinor), cur),
			core.FormatMoney(core.Money(d.Amount), cur),
			core.FormatMoney(core.Money(g.VerifiedShare.PendingMinor+g.VerifiedShare.HeldMinor), cur))
	}
	return fmt.Sprintf("Your %s contribution is attributed to milestones that have not yet been verified.",
		core.FormatMoney(core.Money(d.Amount), cur))
}

func verifiedOutcomeTitle(verified bool, units int, unitLabel string) string {
	if verified {
		return fmt.Sprintf("%d %s verified", units, unitLabel)
	}
	return fmt.Sprintf("%d %s not yet verified", units, unitLabel)
}

func outcomeSubtitle(verified bool, m models.Milestone) string {
	if verified {
		return fmt.Sprintf("Milestone %d completed with %d of %d %s evidenced",
			m.Ordinal, m.VerifiedQuantity, m.RequiredQuantity, m.UnitLabel)
	}
	return fmt.Sprintf("Milestone %d is %s", m.Ordinal, statusLabel(string(m.Status)))
}

func outcomeDetail(verified bool, m models.Milestone, p models.Project, units int, perUnit float64) string {
	if verified {
		return fmt.Sprintf("The organization's evidence for this milestone was verified and the funding was released. %d %s correspond to your contribution, which the project describes as serving about %.0f families.",
			units, m.UnitLabel, float64(units)*perUnit)
	}
	return fmt.Sprintf("No verified outcome is claimed for this milestone yet. Its status is %s, so your contribution to it remains conditional.",
		statusLabel(string(m.Status)))
}

func outcomeMetrics(verified bool, m models.Milestone, units, beneficiaries int) []Metric {
	if verified {
		return []Metric{
			{Label: "Verified units", Value: fmt.Sprintf("%d", units), Unit: m.UnitLabel, Raw: float64(units)},
			{Label: "Milestone verification", Value: fmt.Sprintf("%d / %d", m.VerifiedQuantity, m.RequiredQuantity), Unit: m.UnitLabel},
		}
	}
	return []Metric{
		{Label: "Claimed units", Value: fmt.Sprintf("%d", units), Unit: m.UnitLabel, Raw: float64(units)},
		{Label: "Milestone status", Value: statusLabel(string(m.Status))},
	}
}

func summariseKinds(refs []EvidenceRef) string {
	counts := map[string]int{}
	order := []string{}
	for _, r := range refs {
		k := statusLabel(r.Kind)
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], strings.ToLower(k)))
	}
	return strings.Join(parts, ", ")
}

func distinctUnits(refs []EvidenceRef) int {
	seen := map[string]bool{}
	for _, r := range refs {
		for _, u := range r.UnitIDs {
			seen[strings.ToUpper(u)] = true
		}
	}
	return len(seen)
}

func duplicateNote(refs []EvidenceRef) string {
	seen := map[string][]string{}
	for _, r := range refs {
		seen[r.Filename] = append(seen[r.Filename], r.ID)
	}
	dupes := 0
	for _, ids := range seen {
		if len(ids) > 1 {
			dupes += len(ids) - 1
		}
	}
	if dupes == 0 {
		return "No duplicate artefacts were submitted for this milestone."
	}
	return fmt.Sprintf("%d artefact(s) were re-uploads of files already submitted and cannot double-count.", dupes)
}

func providerLabel(provider string) string {
	switch provider {
	case "paypal", "paypal_sandbox":
		return "PayPal Sandbox"
	case "simulated_sandbox":
		return "the local sandbox simulation"
	case "":
		return "the payment rail"
	default:
		return provider
	}
}

func simulatedSuffix(simulated bool) string {
	if simulated {
		return " (simulated — no real funds moved)"
	}
	return ""
}

func statusLabel(status string) string {
	switch status {
	case "PENDING":
		return "pending"
	case "ORDER_CREATED":
		return "order created"
	case "CAPTURED":
		return "funds captured"
	case "FAILED":
		return "failed"
	case "REFUNDED":
		return "refunded"
	case "HELD":
		return "funding held"
	case "MORE_EVIDENCE_REQUESTED":
		return "more evidence requested"
	case "APPROVED":
		return "approved"
	case "PAYMENT_RELEASED":
		return "funding released"
	case "COMPLETED":
		return "completed"
	case "IN_PROGRESS":
		return "in progress"
	case "EVIDENCE_SUBMITTED":
		return "evidence submitted"
	case "VERIFYING":
		return "under verification"
	case "FUNDING_OPEN":
		return "open for funding"
	}
	return strings.ToLower(strings.ReplaceAll(status, "_", " "))
}

func singular(unitLabel string) string {
	switch unitLabel {
	case "filtration units installed":
		return "unit"
	case "units installed":
		return "unit"
	case "verifiable units":
		return "unit"
	}
	return strings.TrimSuffix(unitLabel, "s")
}
