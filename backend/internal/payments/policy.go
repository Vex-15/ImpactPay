package payments

import (
	"fmt"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// ReleaseContext is everything the policy needs to judge a conditional release.
type ReleaseContext struct {
	Project      *models.Project
	Milestone    *models.Milestone
	Organization *models.Organization
	Verification *models.Verification
	AllocatedMinor int64
	AlreadyReleased bool
}

// ReleaseDecision is the outcome of the deterministic policy gate.
type ReleaseDecision struct {
	Allowed       bool
	AmountMinor   int64
	Checks        []models.PolicyCheck
	BlockedReason string
}

// EvaluateRelease is the backend's independent authorisation for moving money.
//
// The AI recommendation is an *input*: it is check P2, and it is only one of
// eleven. A model that says APPROVE cannot release funding unless every
// deterministic condition below also holds, and a human override still has to
// satisfy them. This is the "AI recommendation != automatic permission" rule made
// concrete and testable.
func EvaluateRelease(c ReleaseContext) ReleaseDecision {
	checks := []models.PolicyCheck{}
	add := func(key, label string, passed bool, detail string) {
		checks = append(checks, models.PolicyCheck{Key: key, Label: label, Passed: passed, Detail: detail})
	}

	p, m, org, v := c.Project, c.Milestone, c.Organization, c.Verification

	// P1 — funding must be captured on the project at all.
	add("project_funded", "Project has captured funding",
		p != nil && p.FundedAmount > 0,
		fmt.Sprintf("captured %s", moneyOrZero(p, p.FundedAmount)))

	// P2 — the milestone must be approved, whether by the agent or by a human.
	approved := m != nil && (m.Status == models.MilestoneApproved)
	add("milestone_approved", "Milestone is approved",
		approved,
		statusDetail(m))

	// P3 — a decisive verification must exist.
	add("verification_exists", "A verification decision exists",
		v != nil,
		verificationDetail(v))

	// P4 — the verification must permit release.
	recommends := v != nil && v.Recommendation == models.DecisionApprove
	add("verification_approves", "Latest verification recommends release",
		recommends,
		recommendationDetail(v))

	// P5 — the verified quantity must meet the requirement.
	quantityMet := m != nil && (m.RequiredQuantity <= 0 || m.VerifiedQuantity >= m.RequiredQuantity)
	add("quantity_verified", "Verified quantity meets the requirement",
		quantityMet,
		quantityDetail(m))

	// P6 — no unresolved critical inconsistency.
	critical := []string{}
	if v != nil {
		for _, inc := range v.Inconsistencies {
			if inc.Severity == "critical" {
				critical = append(critical, inc.Code)
			}
		}
	}
	add("no_critical_findings", "No unresolved critical findings",
		len(critical) == 0,
		criticalDetail(critical))

	// P7 — a confidential/verified organization may receive funds.
	add("organization_verified", "Organization is verified",
		org != nil && org.Verified,
		orgDetail(org))

	// P8 — existing financial evidence must reconcile.
	financialOK := true
	financialDetail := "no financial reconciliation required"
	if v != nil {
		switch v.Result.Financial.Status {
		case "overspend", "mismatch":
			financialOK = false
			financialDetail = fmt.Sprintf("documented spend variance of %d%% against the milestone budget",
				core.Percent(v.Result.Financial.VariancePercent))
		case "undocumented":
			financialOK = false
			financialDetail = "a required invoice or receipt was missing"
		case "ok":
			financialDetail = "documented spend reconciles with the milestone budget"
		}
	}
	add("financial_reconciles", "Financial evidence reconciles", financialOK, financialDetail)

	// P9 — the milestone must not already have been paid.
	add("not_already_released", "Milestone funding has not already been released",
		!c.AlreadyReleased, alreadyDetail(c.AlreadyReleased, m))

	// P10 — funds must actually be attributed to this milestone.
	allocated := c.AllocatedMinor
	budget := int64(0)
	if m != nil {
		budget = m.BudgetAmount
	}
	add("funds_allocated", "Contributions are attributed to this milestone",
		allocated > 0,
		fmt.Sprintf("%s attributed of a %s milestone budget",
			core.FormatMoney(core.Money(allocated), currencyOf(p)),
			core.FormatMoney(core.Money(budget), currencyOf(p))))

	// P11 — the project must not be suspended.
	add("project_active", "Project is active",
		p != nil && p.Status != models.ProjectSuspended && p.Status != models.ProjectArchived,
		projectStatusDetail(p))

	// The releasable amount is the smallest of what the milestone budget allows,
	// what donors actually attributed to it, and what the project still holds
	// unreserved. This is why the release figure is always defensible.
	available := int64(0)
	if p != nil {
		available = p.AvailableAmount()
		if available < 0 {
			available = 0
		}
	}
	amount := budget
	if allocated < amount {
		amount = allocated
	}
	if available < amount {
		amount = available
	}
	if amount < 0 {
		amount = 0
	}
	add("releasable_amount", "A positive releasable amount is available",
		amount > 0,
		fmt.Sprintf("%s releasable (budget %s, attributed %s, unreserved %s)",
			core.FormatMoney(core.Money(amount), currencyOf(p)),
			core.FormatMoney(core.Money(budget), currencyOf(p)),
			core.FormatMoney(core.Money(allocated), currencyOf(p)),
			core.FormatMoney(core.Money(available), currencyOf(p))))

	decision := ReleaseDecision{AmountMinor: amount, Checks: checks}
	blockers := []models.PolicyCheck{}
	for _, ck := range checks {
		if !ck.Passed {
			blockers = append(blockers, ck)
		}
	}
	decision.Allowed = len(blockers) == 0
	if !decision.Allowed {
		reason := blockers[0].Label
		if len(blockers) > 1 {
			reason = fmt.Sprintf("%s (and %d other unmet condition(s))", blockers[0].Label, len(blockers)-1)
		}
		decision.BlockedReason = reason + ": " + blockers[0].Detail
	}
	return decision
}

func currencyOf(p *models.Project) string {
	if p == nil || p.Currency == "" {
		return ""
	}
	return p.Currency
}

func moneyOrZero(p *models.Project, minor int64) string {
	return core.FormatMoney(core.Money(minor), currencyOf(p))
}

func statusDetail(m *models.Milestone) string {
	if m == nil {
		return "milestone not found"
	}
	return "milestone status is " + string(m.Status)
}

func verificationDetail(v *models.Verification) string {
	if v == nil {
		return "no verification has been recorded for this milestone"
	}
	return fmt.Sprintf("%s verification from %s with %d%% confidence",
		v.Recommendation, v.CreatedAt.Format("2006-01-02 15:04 UTC"), core.Percent(v.Confidence))
}

func recommendationDetail(v *models.Verification) string {
	if v == nil {
		return "no verification to evaluate"
	}
	if v.IsOverride {
		return fmt.Sprintf("human decision %s overrode the AI recommendation %s", v.HumanDecision, v.Recommendation)
	}
	return "agent recommendation is " + string(v.Recommendation)
}

func quantityDetail(m *models.Milestone) string {
	if m == nil {
		return "milestone not found"
	}
	if m.RequiredQuantity <= 0 {
		return "milestone declares no countable requirement"
	}
	return fmt.Sprintf("verified %d of %d %s", m.VerifiedQuantity, m.RequiredQuantity, m.UnitLabel)
}

func criticalDetail(codes []string) string {
	if len(codes) == 0 {
		return "no critical inconsistencies were reported"
	}
	return fmt.Sprintf("unresolved critical findings: %s", joinCodes(codes))
}

func orgDetail(org *models.Organization) string {
	if org == nil {
		return "organization not found"
	}
	return fmt.Sprintf("%s is %s", org.Name, org.VerificationStatus)
}

func alreadyDetail(released bool, m *models.Milestone) string {
	if released {
		return "a completed release payment already exists for this milestone"
	}
	if m != nil && (m.Status == models.MilestonePaymentReleased || m.Status == models.MilestoneCompleted) {
		return "milestone status is " + string(m.Status)
	}
	return "no prior release payment recorded"
}

func projectStatusDetail(p *models.Project) string {
	if p == nil {
		return "project not found"
	}
	return "project status is " + string(p.Status)
}

func joinCodes(codes []string) string {
	out := ""
	for i, c := range codes {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}
