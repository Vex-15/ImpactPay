package impact

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// Citation is a pointer to the stored row a statement is derived from, so a
// donor can check the source rather than trust the prose.
type Citation struct {
	Label      string `json:"label"`
	Detail     string `json:"detail"`
	EvidenceID string `json:"evidence_id,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	Kind       string `json:"kind"`
}

// Answer is a grounded response to a donor's question about their contribution.
type Answer struct {
	Question  string         `json:"question"`
	Answer    string         `json:"answer"`
	Citations []Citation     `json:"citations"`
	Grounded  bool           `json:"grounded"`
	Engine    string         `json:"engine"`
	Model     string         `json:"model"`
	Facts     map[string]any `json:"facts"`
	Suggested []string       `json:"suggested_questions"`
}

// Answerer optionally rewrites a grounded answer for readability. It receives the
// facts and the deterministic answer and may only rephrase them; the citations,
// the figures and the grounding flag stay under the platform's control.
type Answerer interface {
	Refine(ctx context.Context, question, deterministic string, facts map[string]any) (answer, engine, model string, ok bool)
}

// SuggestedQuestions are offered in the donor UI.
var SuggestedQuestions = []string{
	"What has my contribution actually accomplished?",
	"Which milestone did my money support?",
	"Is my contribution verified or still conditional?",
	"What evidence supports the verified milestone?",
	"How many people did my contribution reach?",
}

// Ask answers a donor's question using only stored data.
func (s *Service) Ask(ctx context.Context, donationID, question string, answerer Answerer) (*Answer, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		question = SuggestedQuestions[0]
	}

	graph, err := s.Trace(ctx, donationID)
	if err != nil {
		return nil, err
	}
	facts, citations := s.factsFor(graph)

	ans := &Answer{
		Question:  question,
		Grounded:  true,
		Engine:    "deterministic",
		Model:     "impactpay-grounded-v1",
		Facts:     facts,
		Citations: citations,
		Suggested: SuggestedQuestions,
	}
	ans.Answer = composeAnswer(question, graph, facts, citations)

	// Optional refinement. If the model is unavailable or returns something
	// unusable, the deterministic answer stands unchanged.
	if answerer != nil {
		if refined, engine, model, ok := answerer.Refine(ctx, question, ans.Answer, facts); ok && strings.TrimSpace(refined) != "" {
			ans.Answer = refined
			ans.Engine = engine
			ans.Model = model
		}
	}
	return ans, nil
}

// factsFor extracts the machine-readable facts the answer is built from, along
// with the citations that support each one.
func (s *Service) factsFor(g *ProofGraph) (map[string]any, []Citation) {
	d := g.Donation
	cur := d.Currency
	facts := map[string]any{
		"contribution_minor": d.Amount,
		"contribution":       core.FormatMoney(core.Money(d.Amount), cur),
		"currency":           cur,
		"donation_status":    string(d.Status),
		"project":            g.Project.Title,
		"organization":       g.Organization.Name,
		"organization_verified": g.Organization.Verified,
		"project_funded_minor":  g.Project.FundedAmount,
		"project_goal_minor":    g.Project.GoalAmount,
		"verified_minor":      g.VerifiedShare.VerifiedMinor,
		"pending_minor":       g.VerifiedShare.PendingMinor,
		"held_minor":          g.VerifiedShare.HeldMinor,
	}

	citations := []Citation{
		{Label: "Contribution", Kind: "donation", Detail: core.FormatMoney(core.Money(d.Amount), cur) + " captured on " + d.CreatedAt.Format("2 Jan 2006"), NodeID: "donation"},
		{Label: "Project", Kind: "project", Detail: g.Project.Title + " — " + g.Organization.Name, NodeID: "project"},
	}

	milestones := []map[string]any{}
	evidenceItems := []map[string]any{}
	outcomes := []map[string]any{}

	for _, n := range g.Nodes {
		switch n.Type {
		case NodeMilestone:
			entry := map[string]any{
				"title":    n.Title,
				"status":   n.Status,
				"verified": n.Verified,
				"share":    core.FormatMoney(core.Money(n.AmountMinor), cur),
			}
			for _, m := range n.Metrics {
				entry[strings.ToLower(strings.ReplaceAll(m.Label, " ", "_"))] = m.Value
			}
			milestones = append(milestones, entry)
			citations = append(citations, Citation{
				Label: n.Title, Kind: "milestone",
				Detail: n.MetricsSummary(), NodeID: n.ID,
			})
		case NodeResource, NodeOutcome:
			outcomes = append(outcomes, map[string]any{
				"node":     n.Title,
				"subtitle": n.Subtitle,
				"verified": n.Verified,
				"status":   n.Status,
			})
		case NodeEvidence:
			for _, e := range n.Evidence {
				evidenceItems = append(evidenceItems, map[string]any{
					"filename":    e.Filename,
					"kind":        e.Kind,
					"unit_ids":    e.UnitIDs,
					"amount":      core.FormatMoney(core.Money(e.AmountMinor), cur),
					"description": e.Description,
					"location":    e.Location,
					"recorded":    e.CreatedAt,
				})
				citations = append(citations, Citation{
					Label: e.Filename, Kind: "evidence", EvidenceID: e.ID,
					Detail: evidenceDetail(e), NodeID: n.ID,
				})
			}
		case NodeVerification:
			facts["verification_status"] = n.Status
			facts["verification_confidence"] = n.Subtitle
			citations = append(citations, Citation{
				Label: "AI verification", Kind: "verification",
				Detail: n.Title + ": " + n.Status, NodeID: n.ID,
			})
		case NodeBeneficiary:
			facts["beneficiaries_reached"] = n.Title
			citations = append(citations, Citation{
				Label: "Beneficiaries", Kind: "beneficiary",
				Detail: n.Title + " — " + n.Subtitle, NodeID: n.ID,
			})
		}
	}
	facts["milestones"] = milestones
	facts["evidence"] = evidenceItems
	facts["outcomes"] = outcomes
	facts["evidence_count"] = len(evidenceItems)
	return facts, citations
}

// MetricsSummary renders a node's metrics as a readable line.
func (n Node) MetricsSummary() string {
	parts := make([]string, 0, len(n.Metrics))
	for _, m := range n.Metrics {
		if m.Unit != "" {
			parts = append(parts, m.Label+": "+m.Value+" "+m.Unit)
		} else {
			parts = append(parts, m.Label+": "+m.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func evidenceDetail(e EvidenceRef) string {
	parts := []string{strings.ToLower(e.Kind) + " recorded " + e.CreatedAt}
	if len(e.UnitIDs) > 0 {
		parts = append(parts, formatIDs(e.UnitIDs))
	}
	if e.AmountMinor > 0 {
		parts = append(parts, core.FormatMoney(core.Money(e.AmountMinor), "INR"))
	}
	if e.Location != "" {
		parts = append(parts, "location "+e.Location)
	}
	return strings.Join(parts, " · ")
}

func formatIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) <= 4 {
		return "identifiers " + strings.Join(ids, ", ")
	}
	return "identifiers " + strings.Join(ids[:4], ", ") + " and " + itoa(len(ids)-4) + " more"
}

func itoa(n int) string { return strconv.Itoa(n) }

// composeAnswer builds the deterministic, grounded response.
func composeAnswer(question string, g *ProofGraph, facts map[string]any, citations []Citation) string {
	cur := g.Donation.Currency
	amount := core.FormatMoney(core.Money(g.Donation.Amount), cur)
	q := strings.ToLower(question)

	pending := g.VerifiedShare.PendingMinor
	held := g.VerifiedShare.HeldMinor

	var verifiedMilestones []Node
	var heldMilestones []Node
	var pendingMilestones []Node
	for _, n := range g.Nodes {
		if n.Type != NodeMilestone {
			continue
		}
		switch {
		case n.Verified:
			verifiedMilestones = append(verifiedMilestones, n)
		case n.Status == string(models.MilestoneHeld) || n.Status == string(models.MilestoneMoreEvidenceRequested):
			heldMilestones = append(heldMilestones, n)
		default:
			pendingMilestones = append(pendingMilestones, n)
		}
	}

	var b strings.Builder

	switch {
	case strings.Contains(q, "evidence") || strings.Contains(q, "proof") || strings.Contains(q, "support"):
		b.WriteString(evidenceAnswer(g, cur))
	case strings.Contains(q, "verified") || strings.Contains(q, "conditional") || strings.Contains(q, "hold") || strings.Contains(q, "release"):
		b.WriteString(verificationAnswer(g))
	case strings.Contains(q, "how many") || strings.Contains(q, "people") || strings.Contains(q, "families") || strings.Contains(q, "beneficiar"):
		b.WriteString(beneficiaryAnswer(g))
	case strings.Contains(q, "which milestone") || strings.Contains(q, "where"):
		b.WriteString(milestoneAnswer(g, cur))
	default:
		// The canonical question: what did my money accomplish?
		if len(verifiedMilestones) == 0 {
			b.WriteString(fmt.Sprintf("Your %s contribution to %s is recorded and attributed to %s.",
				amount, g.Project.Title, milestonePhrase(verifiedMilestones, heldMilestones, pendingMilestones)))
			b.WriteString(" No milestone funded by it has been verified yet, so ImpactPay is not claiming any accomplished outcome for it.")
			if held > 0 {
				b.WriteString(fmt.Sprintf(" %s of it is currently held because the organization's evidence did not yet support the claim.",
					core.FormatMoney(core.Money(held), cur)))
			} else {
				b.WriteString(" Its funding remains conditional until the evidence is verified.")
			}
			break
		}

		parts := []string{}
		for _, n := range verifiedMilestones {
			share := core.FormatMoney(core.Money(n.AmountMinor), cur)
			units := unitsFromMilestoneNode(g, n.ID)
			if units > 0 {
				parts = append(parts, fmt.Sprintf("%s of your contribution supported the %s, which is verified as complete and covers %d %s",
					share, lowerFirst(n.Title), units, unitLabelOf(n)))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s of your contribution supported the %s, which is verified as complete",
				share, lowerFirst(n.Title)))
		}

		b.WriteString(fmt.Sprintf("Your %s contribution was associated with %s. ", amount, g.Project.Title))
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString(".")

		if ev := evidenceSummaryLine(g); ev != "" {
			b.WriteString(" " + ev)
		}
		if pending > 0 || held > 0 {
			b.WriteString(fmt.Sprintf(" A further %s of your contribution is still conditional: %s.",
				core.FormatMoney(core.Money(pending+held), cur),
				milestonePhrase(nil, heldMilestones, pendingMilestones)))
		}
	}

	// A short provenance footer keeps the answer honest about its source.
	b.WriteString(fmt.Sprintf(" [Derived from %d stored record(s): %s]",
		len(citations), g.Project.Title))
	return b.String()
}

func evidenceAnswer(g *ProofGraph, cur string) string {
	byKind := map[string]int{}
	unitIDs := []string{}
	for _, n := range g.Nodes {
		if n.Type != NodeEvidence {
			continue
		}
		for _, e := range n.Evidence {
			byKind[statusLabel(e.Kind)]++
			unitIDs = append(unitIDs, e.UnitIDs...)
		}
	}
	if len(byKind) == 0 {
		return "No evidence has been submitted for the milestones this contribution supports, so there is nothing to show yet."
	}
	kinds := []string{}
	for _, k := range sortedKeys(byKind) {
		kinds = append(kinds, fmt.Sprintf("%d %s", byKind[k], strings.ToLower(k)))
	}
	out := fmt.Sprintf("The evidence stored for your milestones consists of %s.", strings.Join(kinds, ", "))
	if len(unitIDs) > 0 {
		seen := map[string]bool{}
		uniq := []string{}
		for _, u := range unitIDs {
			if !seen[strings.ToUpper(u)] {
				seen[strings.ToUpper(u)] = true
				uniq = append(uniq, u)
			}
		}
		out += fmt.Sprintf(" It contains %d distinct asset identifier(s), starting at %s.", len(uniq), uniq[0])
	}
	out += " Each file is content-hashed on upload, so a re-uploaded file is detected and cannot be counted twice."
	return out
}

func verificationAnswer(g *ProofGraph) string {
	cur := g.Donation.Currency
	v := g.VerifiedShare
	if v.VerifiedMinor == 0 && v.HeldMinor == 0 {
		return fmt.Sprintf("Your entire %s contribution is still conditional: the milestones it supports have not completed verification yet.",
			core.FormatMoney(core.Money(v.TotalMinor), cur))
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Of your %s, %s sits in verified, released milestones",
		core.FormatMoney(core.Money(v.TotalMinor), cur),
		core.FormatMoney(core.Money(v.VerifiedMinor), cur)))
	if v.HeldMinor > 0 {
		b.WriteString(fmt.Sprintf(", %s is held because the submitted evidence did not fully support the organization's claim",
			core.FormatMoney(core.Money(v.HeldMinor), cur)))
	}
	if v.PendingMinor > 0 {
		b.WriteString(fmt.Sprintf(", and %s is awaiting verification",
			core.FormatMoney(core.Money(v.PendingMinor), cur)))
	}
	b.WriteString(". Funding only moves after the verification pipeline and the backend's release policy both pass.")
	return b.String()
}

func beneficiaryAnswer(g *ProofGraph) string {
	for _, n := range g.Nodes {
		if n.Type == NodeBeneficiary {
			return fmt.Sprintf("%s. %s", n.Title+" are attributed to your contribution", n.Detail)
		}
	}
	return "The project has not published a beneficiary figure that can be attributed to your contribution yet."
}

func milestoneAnswer(g *ProofGraph, cur string) string {
	lines := []string{}
	for _, n := range g.Nodes {
		if n.Type != NodeMilestone {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s — %s of your contribution, status %s",
			n.Title, core.FormatMoney(core.Money(n.AmountMinor), cur), statusLabel(n.Status)))
	}
	if len(lines) == 0 {
		return "Your contribution has not been attributed to a milestone yet."
	}
	return "Your contribution was attributed to these milestones: " + strings.Join(lines, "; ") + "."
}

func evidenceSummaryLine(g *ProofGraph) string {
	for _, n := range g.Nodes {
		if n.Type == NodeEvidence && len(n.Evidence) > 0 {
			return fmt.Sprintf("The supporting evidence includes %s.", n.Subtitle)
		}
	}
	return ""
}

func milestonePhrase(verified, held, pending []Node) string {
	if len(verified) > 0 {
		return lowerFirst(verified[0].Title)
	}
	if len(held) > 0 {
		return lowerFirst(held[0].Title) + " (currently held)"
	}
	if len(pending) > 0 {
		return lowerFirst(pending[0].Title)
	}
	return "a milestone that has not been published"
}

// unitsFromMilestoneNode reads the resource node attached to a milestone to
// recover how many whole units the donor's share bought. The Proof Graph already
// computed it, so the answer reuses that figure rather than recomputing it.
func unitsFromMilestoneNode(g *ProofGraph, milestoneNodeID string) int {
	want := milestoneNodeID + "-resource"
	for _, n := range g.Nodes {
		if n.ID != want {
			continue
		}
		for _, m := range n.Metrics {
			if strings.HasPrefix(m.Label, "Units from") {
				return int(m.Raw)
			}
		}
	}
	return 0
}

// unitLabelOf returns the milestone's unit label, taken from the metric the
// milestone node already carries.
func unitLabelOf(n Node) string {
	for _, m := range n.Metrics {
		if m.Label == "Verified" && m.Unit != "" {
			return m.Unit
		}
	}
	for _, m := range n.Metrics {
		if m.Unit != "" {
			return m.Unit
		}
	}
	return "unit"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	// Preserve leading ordinals like "3. Installation".
	if len(r) > 2 && r[1] == '.' {
		return s
	}
	r[0] = []rune(strings.ToLower(string(r[0])))[0]
	return string(r)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
