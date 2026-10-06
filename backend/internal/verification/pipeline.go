// Package verification implements ImpactPay's AI verification agent.
//
// The agent is a *tool-using pipeline*, not a prompt. Each stage is an explicit,
// named tool that reads stored data and returns a structured fact:
//
//	get_project              -> the project being funded
//	get_milestone            -> the milestone definition and its evidence requirements
//	get_evidence             -> the cumulative artefacts submitted for the milestone
//	analyze_image            -> per-photograph extraction (dimensions, quality, asset ids)
//	analyze_document         -> per-document extraction (text, identifiers, quantities)
//	extract_invoice_data     -> invoice/receipt parsing (vendor, totals, quantities, ids)
//	check_duplicate_evidence -> content-hash de-duplication
//	compare_requirements     -> claimed quantities vs evidence-backed quantities
//	check_financial_consistency -> documented spend vs milestone budget
//	calculate_verification   -> the verified/required quantity, checks and confidence
//
// Every tool call is recorded with its input, output, status and duration so the
// Verification Workspace can show an admin exactly how a decision was reached.
//
// The pipeline is deterministic: the same stored evidence always yields the same
// result. An LLM is used only to narrate the computed facts, never to decide them,
// which is why DEMO_MODE reproduces the 87/100 -> HOLD -> 100/100 -> APPROVE story
// on every run.
package verification

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// Input is the complete data set the pipeline evaluates.
type Input struct {
	Project   *models.Project
	Milestone *models.Milestone
	Evidence  []models.Evidence
}

// Evaluate runs the full verification pipeline over the supplied data.
func Evaluate(in Input) models.VerificationResult {
	r := &run{in: in}
	return r.execute()
}

// run is the pipeline state: it accumulates derived facts and the tool trace.
type run struct {
	in    Input
	steps []models.VerificationStep
	n     int

	// derived state, filled in stage by stage
	findings     []models.EvidenceFinding
	dupOf        map[string]string // evidence id -> id of the artefact it duplicates
	duplicates   []models.DuplicateGroup
	confirmed    map[string]string // unit id -> evidence id that established it
	confirmedIDs []string
	usable       []models.Evidence
	signals      []signal
	financial    models.FinancialCheck
	checks       []models.VerificationCheck
	inconsistent []models.Inconsistency
	missing      []models.MissingEvidence
	claimed      []models.Claim
	coverage     int
	coverageNeed int

	verifiedQuantity int
}

// signal records which counting mechanism produced the verified quantity so the
// result is self-explaining rather than a bare number.
type signal struct {
	Key      string
	Label    string
	Quantity int
	Source   string
}

// step records one executed tool call.
func (r *run) step(tool, title, input string, started time.Time, output string, status string) {
	r.n++
	r.steps = append(r.steps, models.VerificationStep{
		Index:      r.n,
		Tool:       tool,
		Title:      title,
		Input:      input,
		Output:     output,
		Status:     status,
		DurationMS: float64(time.Since(started).Microseconds()) / 1000.0,
	})
}

// tool runs fn, times it and records a step with its returned summary.
func (r *run) tool(name, title, input string, fn func() (string, string)) {
	started := time.Now()
	output, status := fn()
	if status == "" {
		status = "ok"
	}
	r.step(name, title, input, started, output, status)
}

func (r *run) execute() models.VerificationResult {
	m := r.in.Milestone
	p := r.in.Project

	r.tool("get_project", "Retrieve project definition",
		fmt.Sprintf("project_id=%s", p.ID), func() (string, string) {
			return fmt.Sprintf("%s (%s) — goal %s, funded %s",
				p.Title, p.Currency,
				core.FormatMoney(core.Money(p.GoalAmount), p.Currency),
				core.FormatMoney(core.Money(p.FundedAmount), p.Currency)), "ok"
		})

	r.tool("get_milestone", "Retrieve milestone requirements",
		fmt.Sprintf("milestone_id=%s", m.ID), func() (string, string) {
			reqs := len(m.EvidenceRequirements)
			return fmt.Sprintf("Milestone %d '%s' — requires %d %s, budget %s, %d evidence requirement(s)",
				m.Ordinal, m.Title, m.RequiredQuantity, m.UnitLabel,
				core.FormatMoney(core.Money(m.BudgetAmount), p.Currency), reqs), "ok"
		})

	r.tool("get_evidence", "Retrieve submitted evidence",
		fmt.Sprintf("milestone_id=%s", m.ID), func() (string, string) {
			if len(r.in.Evidence) == 0 {
				return "No evidence has been submitted for this milestone.", "fail"
			}
			kinds := map[string]int{}
			for _, e := range r.in.Evidence {
				kinds[string(e.Kind)]++
			}
			keys := make([]string, 0, len(kinds))
			for k := range kinds {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%d %s", kinds[k], strings.ToLower(k)))
			}
			return fmt.Sprintf("%d artefact(s): %s", len(r.in.Evidence), strings.Join(parts, ", ")), "ok"
		})

	// Duplicate detection runs before analysis so that a re-uploaded artefact can
	// never contribute a second, independent unit to the verified count.
	r.computeDuplicates()
	r.analyseArtefacts()
	r.reportDuplicates()
	r.compareRequirements()
	r.checkFinancialConsistency()
	r.calculate()

	return r.result()
}

// result assembles the final structured output.
func (r *run) result() models.VerificationResult {
	m := r.in.Milestone

	res := models.VerificationResult{
		MilestoneID:      m.ID,
		ProjectID:        m.ProjectID,
		SubmissionID:     r.submissionID(),
		Claimed:          r.claimed,
		EvidenceFound:    r.findings,
		VerifiedQuantity: len(r.confirmedIDs),
		RequiredQuantity: m.RequiredQuantity,
		UnitLabel:        m.UnitLabel,
		VerifiedUnitIDs:  r.confirmedIDs,
		Duplicates:       r.duplicates,
		MissingEvidence:  r.missing,
		Inconsistencies:  r.inconsistent,
		Financial:        r.financial,
		Checks:           r.checks,
		Steps:            r.steps,
		EvidenceCount:    len(r.in.Evidence),
		UsableCount:      len(r.usable),
		DuplicateCount:   duplicateRows(r.duplicates),
		Cumulative:       r.Cumulative(),
		PriorVerified:    m.VerifiedQuantity,
		EvaluatedAt:      core.NowStr(),
	}
	if res.VerifiedQuantity > res.RequiredQuantity && res.RequiredQuantity > 0 {
		res.VerifiedQuantity = res.RequiredQuantity
		if len(res.VerifiedUnitIDs) > res.RequiredQuantity {
			res.VerifiedUnitIDs = res.VerifiedUnitIDs[:res.RequiredQuantity]
		}
	}
	res.Confidence = r.confidence(res)
	res.Recommendation, res.NextAction, res.Summary = r.recommend(res)
	return res
}

func (r *run) submissionID() string {
	latest := ""
	for _, e := range r.in.Evidence {
		if e.SubmissionID > latest {
			latest = e.SubmissionID
		}
	}
	if latest == "" {
		return r.in.Milestone.ID
	}
	return latest
}

// Cumulative reports whether evidence spans more than one review round.
func (r *run) Cumulative() bool {
	seen := map[string]bool{}
	for _, e := range r.in.Evidence {
		seen[e.SubmissionID] = true
	}
	return len(seen) > 1
}

func duplicateRows(groups []models.DuplicateGroup) int {
	n := 0
	for _, g := range groups {
		if len(g.EvidenceIDs) > 1 {
			n += len(g.EvidenceIDs) - 1
		}
	}
	return n
}

// sortStrings returns a sorted copy.
func sortStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Stage: per-artefact analysis
// ---------------------------------------------------------------------------

// analyseArtefacts runs analyze_image / analyze_document / extract_invoice_data
// over every submitted artefact and turns the extracted facts into findings.
func (r *run) analyseArtefacts() {
	m := r.in.Milestone
	r.confirmed = map[string]string{}

	toolName := "analyze_document"
	toolTitle := "Analyse submitted documents"
	switch m.PrimaryEvidenceKind() {
	case models.EvidencePhotograph:
		toolName, toolTitle = "analyze_image", "Analyse submitted photographs"
	case models.EvidenceInvoice, models.EvidenceReceipt:
		toolName, toolTitle = "extract_invoice_data", "Extract data from financial documents"
	}

	unidentifiable := 0
	for i := range r.in.Evidence {
		e := r.in.Evidence[i]
		f := r.finding(e)

		// A re-uploaded artefact is not independent corroboration.
		if owner, dup := r.dupOf[e.ID]; dup {
			f.Usable = false
			f.DuplicateOf = owner
			f.Issue = "duplicate artefact"
			f.Confidence = 0.05
		}
		r.findings = append(r.findings, f)

		if !f.Usable {
			continue
		}
		r.usable = append(r.usable, e)

		// Every confidently identified asset identifier is evidence of one unit.
		for _, id := range e.Extracted.UnitIDs {
			id = strings.ToUpper(strings.TrimSpace(id))
			if id == "" {
				continue
			}
			if _, ok := r.confirmed[id]; ok {
				continue
			}
			r.confirmed[id] = e.ID
		}
		if len(e.Extracted.UnitIDs) == 0 && e.Extracted.StatedQuantity == 0 && e.Extracted.Beneficiaries == 0 {
			unidentifiable++
		}
	}

	r.confirmedIDs = make([]string, 0, len(r.confirmed))
	for id := range r.confirmed {
		r.confirmedIDs = append(r.confirmedIDs, id)
	}
	sort.Strings(r.confirmedIDs)

	r.tool(toolName, toolTitle,
		fmt.Sprintf("%d artefact(s)", len(r.in.Evidence)), func() (string, string) {
			status := "ok"
			if len(r.usable) == 0 {
				status = "fail"
			}
			detail := fmt.Sprintf("%d usable, %d identified unit(s), %d artefact(s) yielded no countable fact, %d stated %s in total",
				len(r.usable), len(r.confirmedIDs), unidentifiable, r.documentedQuantity(), m.UnitLabel)
			return detail, status
		})

	if unidentifiable > 0 {
		r.inconsistent = append(r.inconsistent, models.Inconsistency{
			Code:     "ARTEFACT_NOT_IDENTIFIABLE",
			Severity: "warning",
			Detail: fmt.Sprintf("%d submitted artefact(s) contain no identifiable %s identifier, so they cannot be counted towards the requirement.",
				unidentifiable, m.UnitLabel),
		})
	}
}

// finding describes what the pipeline could read out of one artefact.
func (r *run) finding(e models.Evidence) models.EvidenceFinding {
	f := models.EvidenceFinding{
		EvidenceID:  e.ID,
		Filename:    e.Filename,
		Kind:        string(e.Kind),
		Requirement: e.RequirementKey,
		UnitIDs:     e.Extracted.UnitIDs,
		AmountMinor: e.Extracted.AmountMinor,
	}

	conf := 0.9
	parts := []string{}

	switch {
	case len(e.Extracted.UnitIDs) > 0:
		parts = append(parts, fmt.Sprintf("%d %s identifier(s)", len(e.Extracted.UnitIDs), r.in.Milestone.UnitLabel))
	case e.Extracted.Beneficiaries > 0:
		parts = append(parts, fmt.Sprintf("%d beneficiary record(s)", e.Extracted.Beneficiaries))
	case e.Extracted.StatedQuantity > 0:
		parts = append(parts, fmt.Sprintf("states a quantity of %d", e.Extracted.StatedQuantity))
	}
	if e.Extracted.AmountMinor > 0 {
		parts = append(parts, fmt.Sprintf("amount %s", core.FormatMoney(core.Money(e.Extracted.AmountMinor), r.in.Project.Currency)))
	}
	if e.Extracted.Vendor != "" {
		parts = append(parts, "vendor "+core.Truncate(e.Extracted.Vendor, 60))
	}
	if e.Extracted.InvoiceNumber != "" {
		parts = append(parts, "ref "+e.Extracted.InvoiceNumber)
	}
	if e.Extracted.Location != "" {
		parts = append(parts, "location "+core.Truncate(e.Extracted.Location, 60))
	}

	switch e.Extracted.Quality {
	case "insufficient":
		conf = 0.1
		f.Usable = false
		f.Issue = "insufficient quality"
	case "low":
		conf = 0.55
		f.Usable = len(e.Extracted.UnitIDs) > 0
		if !f.Usable {
			f.Issue = "low quality and no identifier"
		}
	default:
		f.Usable = len(e.Extracted.UnitIDs) > 0 || e.Extracted.StatedQuantity > 0 ||
			e.Extracted.Beneficiaries > 0 || e.Extracted.AmountMinor > 0
		if !f.Usable {
			f.Issue = "readable but no countable fact"
			conf = 0.3
		}
	}

	if len(parts) == 0 {
		f.Summary = "No extractable facts"
	} else {
		f.Summary = strings.Join(parts, ", ")
	}
	f.Confidence = conf
	return f
}

// documentedQuantity sums the stated quantities of usable artefacts. It is the
// financial-document fallback when no serialised identifiers are present.
func (r *run) documentedQuantity() int {
	total := 0
	for _, e := range r.usable {
		if len(e.Extracted.UnitIDs) > 0 {
			continue
		}
		if e.Extracted.StatedQuantity > 0 {
			total += e.Extracted.StatedQuantity
		} else if e.Extracted.Beneficiaries > 0 {
			total += e.Extracted.Beneficiaries
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// Stage: duplicate detection
// ---------------------------------------------------------------------------

// computeDuplicates groups artefacts by content hash. Because evidence keys are
// content addressed, a re-upload of the same bytes is an exact hash match rather
// than a fuzzy guess. Within a group the earliest artefact is the original and
// every later copy is marked as duplicating it.
func (r *run) computeDuplicates() {
	r.dupOf = map[string]string{}
	order := make([]string, 0, len(r.in.Evidence))
	groups := map[string][]models.Evidence{}

	for _, e := range r.in.Evidence {
		h := e.ContentHash
		if h == "" {
			h = "nohash:" + e.ID
		}
		if _, seen := groups[h]; !seen {
			order = append(order, h)
		}
		groups[h] = append(groups[h], e)
	}
	// Deterministic output regardless of submission ordering.
	sort.Strings(order)

	for _, h := range order {
		g := groups[h]
		if len(g) < 2 || strings.HasPrefix(h, "nohash:") {
			continue
		}
		sort.SliceStable(g, func(i, j int) bool {
			if g[i].CreatedAt.Equal(g[j].CreatedAt) {
				return g[i].ID < g[j].ID
			}
			return g[i].CreatedAt.Before(g[j].CreatedAt)
		})
		ids := make([]string, 0, len(g))
		for _, e := range g {
			ids = append(ids, e.ID)
		}
		for _, e := range g[1:] {
			r.dupOf[e.ID] = g[0].ID
		}
		r.duplicates = append(r.duplicates, models.DuplicateGroup{
			ContentHash: h,
			EvidenceIDs: ids,
			Reason: fmt.Sprintf("%d files share identical content; only one can count towards the requirement",
				len(ids)),
		})
	}
}

// reportDuplicates records the check_duplicate_evidence tool step and raises an
// inconsistency when duplicate artefacts were submitted.
func (r *run) reportDuplicates() {
	r.tool("check_duplicate_evidence", "Check for duplicate evidence",
		fmt.Sprintf("hashing %d artefact(s)", len(r.in.Evidence)), func() (string, string) {
			if len(r.duplicates) == 0 {
				return fmt.Sprintf("No duplicate content found across %d artefact(s).", len(r.in.Evidence)), "ok"
			}
			return fmt.Sprintf("%d duplicate group(s) covering %d redundant artefact(s). %s",
				len(r.duplicates), duplicateRows(r.duplicates), r.duplicateExamples()), "warn"
		})

	if len(r.duplicates) == 0 {
		return
	}
	n := duplicateRows(r.duplicates)
	r.inconsistent = append(r.inconsistent, models.Inconsistency{
		Code:     "DUPLICATE_EVIDENCE",
		Severity: "warning",
		Detail: fmt.Sprintf("%d submitted artefact(s) are byte-identical duplicates of another artefact in the same milestone. %s",
			n, r.duplicateExamples()),
	})
}

// duplicateExamples names up to three of the duplicated files for the UI.
func (r *run) duplicateExamples() string {
	byID := map[string]models.Evidence{}
	for _, e := range r.in.Evidence {
		byID[e.ID] = e
	}
	examples := []string{}
	for _, g := range r.duplicates {
		for _, id := range g.EvidenceIDs {
			if e, ok := byID[id]; ok {
				examples = append(examples, e.Filename)
			}
			if len(examples) >= 3 {
				break
			}
		}
		if len(examples) >= 3 {
			break
		}
	}
	if len(examples) == 0 {
		return ""
	}
	return "Examples: " + strings.Join(examples, ", ") + "."
}

// ---------------------------------------------------------------------------
// Stage: requirement comparison
// ---------------------------------------------------------------------------

// compareRequirements counts the units the evidence actually supports and checks
// each declared evidence requirement for coverage.
func (r *run) compareRequirements() {
	m := r.in.Milestone

	claim := models.Claim{
		Label:    firstNonEmpty(m.Requirement, m.Title),
		Quantity: m.RequiredQuantity,
		Unit:     m.UnitLabel,
		Source:   "milestone requirement",
	}
	r.claimed = append(r.claimed, claim)

	// Counting precedence: serialised identifiers beat site lists beat a quantity
	// stated only in prose. The chosen signal is recorded so the number is
	// explainable rather than mystical.
	r.signals = []signal{
		{Key: "identified_units", Label: "Distinct identifiers confirmed across artefacts",
			Quantity: len(r.confirmedIDs), Source: "analyze_image"},
		{Key: "documented_quantity", Label: "Quantity stated by submitted documents",
			Quantity: r.documentedQuantity(), Source: "extract_invoice_data"},
	}

	verified := 0
	usedSignal := r.signals[0]
	for _, s := range r.signals {
		if s.Quantity > 0 {
			verified = s.Quantity
			usedSignal = s
			break
		}
	}

	r.tool("compare_requirements", "Compare claim against evidence",
		fmt.Sprintf("require %d %s", m.RequiredQuantity, m.UnitLabel), func() (string, string) {
			if m.RequiredQuantity <= 0 {
				return "This milestone declares no countable quantity; evidence coverage is assessed qualitatively.", "ok"
			}
			status := "pass"
			if verified < m.RequiredQuantity {
				status = "fail"
			}
			return fmt.Sprintf("Evidence supports %d of the required %d %s (source: %s).",
				verified, m.RequiredQuantity, m.UnitLabel, usedSignal.Source), status
		})

	if usedSignal.Key != "identified_units" && verified > 0 {
		r.inconsistent = append(r.inconsistent, models.Inconsistency{
			Code:     "NO_SERIALISED_IDENTIFIERS",
			Severity: "info",
			Detail: fmt.Sprintf("No asset identifiers were present in the submitted evidence, so the count relies on stated quantities (%s). Uploading unit IDs would make this milestone independently auditable.",
				usedSignal.Label),
		})
	}

	// Evidence-requirement coverage.
	cov := r.coverageReport()
	r.coverage = 0
	r.coverageNeed = 0
	for _, c := range cov {
		if !c.Required {
			continue
		}
		r.coverageNeed++
		if c.Satisfied {
			r.coverage++
			continue
		}
		r.missing = append(r.missing, models.MissingEvidence{
			RequirementKey: c.Key,
			Label:          c.Label,
			Kind:           c.Kind,
			Required:       c.MinCount,
			Found:          c.Found,
			Detail: fmt.Sprintf("Requires at least %d %s artefact(s); %d submitted.",
				c.MinCount, strings.ToLower(c.Kind), c.Found),
		})
	}

	if m.RequiredQuantity > 0 && verified < m.RequiredQuantity {
		short := m.RequiredQuantity - verified
		r.missing = append(r.missing, models.MissingEvidence{
			RequirementKey: "quantity",
			Label:          fmt.Sprintf("%d %s", short, m.UnitLabel),
			Kind:           string(m.PrimaryEvidenceKind()),
			Required:       m.RequiredQuantity,
			Found:          verified,
			Detail: fmt.Sprintf("Submitted evidence currently supports %d of the required %d %s.",
				verified, m.RequiredQuantity, m.UnitLabel),
		})
	}

	r.checks = append(r.checks, models.VerificationCheck{
		Key: "quantity_supported", Label: "Verified quantity", Status: boolStatus(verified >= m.RequiredQuantity || m.RequiredQuantity == 0),
		Detail:   fmt.Sprintf("%d / %d %s", verified, m.RequiredQuantity, m.UnitLabel),
		Weight:   0.45,
		Observed: float64(verified), Expected: float64(m.RequiredQuantity), Unit: m.UnitLabel,
	})
	r.checks = append(r.checks, models.VerificationCheck{
		Key: "evidence_coverage", Label: "Evidence requirements satisfied",
		Status:   boolStatus(r.coverageNeed == 0 || r.coverage == r.coverageNeed),
		Detail:   fmt.Sprintf("%d of %d required artefact(s) provided", r.coverage, r.coverageNeed),
		Weight:   0.20,
		Observed: float64(r.coverage), Expected: float64(r.coverageNeed), Unit: "requirements",
	})
	r.checks = append(r.checks, models.VerificationCheck{
		Key: "duplicate_integrity", Label: "Evidence integrity (no duplicates)",
		Status:   boolStatus(len(r.duplicates) == 0),
		Detail:   fmt.Sprintf("%d duplicate artefact(s) detected", duplicateRows(r.duplicates)),
		Weight:   0.15,
		Observed: float64(duplicateRows(r.duplicates)), Expected: 0, Unit: "artefacts",
	})
	r.checks = append(r.checks, models.VerificationCheck{
		Key: "artefact_quality", Label: "Artefact usability",
		Status:   boolStatus(len(r.usable) == len(r.in.Evidence) && len(r.in.Evidence) > 0),
		Detail:   fmt.Sprintf("%d of %d artefact(s) usable", len(r.usable), len(r.in.Evidence)),
		Weight:   0.10,
		Observed: float64(len(r.usable)), Expected: float64(len(r.in.Evidence)), Unit: "artefacts",
	})

	r.verifiedQuantity = verified
}

// ---------------------------------------------------------------------------
// Stage: calculation
// ---------------------------------------------------------------------------

// calculate records the arithmetic stage of the pipeline. The numbers themselves
// were derived in the previous stages; this stage makes the derivation legible.
func (r *run) calculate() {
	m := r.in.Milestone
	r.tool("calculate_verification", "Calculate verification result",
		fmt.Sprintf("%d evidence artefact(s)", len(r.in.Evidence)), func() (string, string) {
			switch {
			case m.RequiredQuantity <= 0:
				return fmt.Sprintf("%d artefact(s) assessed; no countable requirement declared.", len(r.in.Evidence)), "ok"
			case r.verifiedQuantity >= m.RequiredQuantity:
				return fmt.Sprintf("VERIFIED %d/%d %s — requirement fully supported.",
					r.verifiedQuantity, m.RequiredQuantity, m.UnitLabel), "ok"
			default:
				return fmt.Sprintf("VERIFIED %d/%d %s — %d still unsupported.",
					r.verifiedQuantity, m.RequiredQuantity, m.UnitLabel,
					m.RequiredQuantity-r.verifiedQuantity), "warn"
			}
		})
}

// confidence blends coverage, integrity, completeness, financial reconciliation
// and artefact quality into a single 0..1 figure. The weights are fixed and
// documented so the number never drifts between runs.
func (r *run) confidence(res models.VerificationResult) float64 {
	coverage := 1.0
	if res.RequiredQuantity > 0 {
		coverage = core.Clamp(core.Ratio(float64(res.VerifiedQuantity), float64(res.RequiredQuantity)), 0, 1)
	}

	integrity := 1.0
	if len(r.in.Evidence) > 0 {
		integrity = core.Clamp(1-core.Ratio(float64(res.DuplicateCount), float64(len(r.in.Evidence))), 0, 1)
	}

	completeness := 1.0
	if r.coverageNeed > 0 {
		completeness = core.Ratio(float64(r.coverage), float64(r.coverageNeed))
	}

	financial := 1.0
	switch r.financial.Status {
	case "overspend":
		financial = 0.3
	case "undocumented":
		financial = 0.2
	case "mismatch":
		financial = 0.5
	}

	quality := 1.0
	if len(r.in.Evidence) > 0 {
		quality = core.Ratio(float64(len(r.usable)), float64(len(r.in.Evidence)))
	}

	return core.Clamp(0.45*coverage+0.15*integrity+0.20*completeness+0.10*financial+0.10*quality, 0, 1)
}

// recommend applies the decision policy and builds the human-readable summary.
//
// Policy, in order:
//
//  1. A critical inconsistency (budget exceeded, required financial document
//     missing) always holds — money must not move on contradicted evidence.
//  2. Full quantity coverage with every required artefact present approves.
//  3. A shortfall where enough artefacts *were* submitted, but could not support
//     the claim, holds. This is the 87/100 case: the evidence contradicts the
//     claim rather than merely being absent.
//  4. Otherwise the organization is asked for more evidence.
func (r *run) recommend(res models.VerificationResult) (models.VerificationDecision, string, string) {
	m := r.in.Milestone

	critical := false
	for _, inc := range r.inconsistent {
		if inc.Severity == "critical" {
			critical = true
			break
		}
	}

	coverageComplete := res.RequiredQuantity <= 0 || res.VerifiedQuantity >= res.RequiredQuantity
	everythingPresent := r.coverageNeed == 0 || r.coverage == r.coverageNeed
	financialOK := r.financial.Status == "ok" || r.financial.Status == "not_applicable"

	switch {
	case critical:
		return models.DecisionHold,
			"Resolve the flagged inconsistencies, then resubmit the milestone for verification.",
			r.holdSummary(res)

	case coverageComplete && everythingPresent && financialOK:
		return models.DecisionApprove,
			"Funding for this milestone is cleared for release subject to backend policy validation.",
			r.approveSummary(res)

	case res.RequiredQuantity > 0 && res.VerifiedQuantity < res.RequiredQuantity && len(r.usable) >= m.RequiredQuantity:
		return models.DecisionHold,
			"Submit evidence that identifies the remaining units; duplicate or unidentifiable artefacts cannot be counted.",
			r.holdSummary(res)

	case !everythingPresent || res.VerifiedQuantity < res.RequiredQuantity:
		return models.DecisionRequestMore,
			"Upload the missing evidence listed above, then resubmit the submission for verification.",
			r.requestSummary(res)
	}

	return models.DecisionHold,
		"A human verifier should review this milestone before any funding moves.",
		r.holdSummary(res)
}

func (r *run) approveSummary(res models.VerificationResult) string {
	m := r.in.Milestone
	if m.RequiredQuantity <= 0 {
		return fmt.Sprintf("All required evidence for '%s' was received and reconciled. Nothing contradicts the organization's claim, so the milestone is verified.", m.Title)
	}
	return fmt.Sprintf("Submitted evidence supports all %d required %s. %d of %d required evidence requirement(s) were satisfied and no inconsistencies remain. The milestone is verified and its funding is cleared for release.",
		m.RequiredQuantity, m.UnitLabel, r.coverage, r.coverageNeed)
}

func (r *run) holdSummary(res models.VerificationResult) string {
	m := r.in.Milestone
	if m.RequiredQuantity > 0 && res.VerifiedQuantity < m.RequiredQuantity {
		short := m.RequiredQuantity - res.VerifiedQuantity
		msg := fmt.Sprintf("Submitted evidence currently supports only %d of the required %d %s.",
			res.VerifiedQuantity, m.RequiredQuantity, m.UnitLabel)
		if dup := duplicateRows(r.duplicates); dup > 0 {
			msg += fmt.Sprintf(" %d artefact(s) were duplicates or could not be identified and are excluded.", dup)
		}
		msg += fmt.Sprintf(" %d %s remain unsupported. Do not release the milestone payment.", short, m.UnitLabel)
		return msg
	}
	return "The submitted evidence contains unresolved inconsistencies. Do not release the milestone payment until they are resolved."
}

func (r *run) requestSummary(res models.VerificationResult) string {
	m := r.in.Milestone
	if len(r.missing) == 0 {
		return "The submission is incomplete. Request the outstanding evidence before assessing this milestone."
	}
	parts := make([]string, 0, len(r.missing))
	for i, mi := range r.missing {
		if i >= 3 {
			break
		}
		parts = append(parts, mi.Label)
	}
	return fmt.Sprintf("Evidence for '%s' is incomplete. Outstanding: %s. Funding for this milestone remains unreleased until the remaining evidence is verified.",
		m.Title, strings.Join(parts, "; "))
}

// coverageReport counts how many artefacts satisfy each declared requirement.
func (r *run) coverageReport() []coverageRow {
	m := r.in.Milestone
	out := make([]coverageRow, 0, len(m.EvidenceRequirements))
	for _, req := range m.EvidenceRequirements {
		row := coverageRow{Key: req.Key, Label: req.Label, Kind: req.Kind, Required: req.Required}
		row.MinCount = req.MinCount
		if row.MinCount <= 0 {
			row.MinCount = 1
		}
		for _, e := range r.in.Evidence {
			if r.dupOf[e.ID] != "" {
				continue
			}
			if e.Extracted.Quality == "insufficient" {
				continue
			}
			if strings.EqualFold(e.RequirementKey, req.Key) ||
				(e.RequirementKey == "" && strings.EqualFold(string(e.Kind), req.Kind)) {
				row.Found++
			}
		}
		row.Satisfied = row.Found >= row.MinCount
		out = append(out, row)
	}
	return out
}

type coverageRow struct {
	Key       string
	Label     string
	Kind      string
	Required  bool
	MinCount  int
	Found     int
	Satisfied bool
}

func boolStatus(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Stage: financial consistency
// ---------------------------------------------------------------------------

// checkFinancialConsistency reconciles the amounts documented in invoices and
// receipts against the milestone budget. It only applies to milestones whose
// evidence requirements include a financial artefact.
func (r *run) checkFinancialConsistency() {
	m := r.in.Milestone
	p := r.in.Project

	r.financial = models.FinancialCheck{
		BudgetMinor: m.BudgetAmount,
		Currency:    p.Currency,
		Notes:       []string{},
	}

	needsFinancial := false
	for _, req := range m.EvidenceRequirements {
		if strings.EqualFold(req.Kind, string(models.EvidenceInvoice)) ||
			strings.EqualFold(req.Kind, string(models.EvidenceReceipt)) {
			needsFinancial = true
		}
	}
	for _, e := range r.in.Evidence {
		if e.Kind == models.EvidenceInvoice || e.Kind == models.EvidenceReceipt {
			needsFinancial = true
		}
	}

	if !needsFinancial {
		r.financial.Status = "not_applicable"
		r.financial.Notes = append(r.financial.Notes,
			"This milestone is verified on non-financial evidence, so no budget reconciliation is required.")
		r.tool("check_financial_consistency", "Reconcile spend against milestone budget",
			core.FormatMoney(core.Money(m.BudgetAmount), p.Currency), func() (string, string) {
				return "Not applicable: no financial artefact is required for this milestone.", "ok"
			})
		r.checks = append(r.checks, models.VerificationCheck{
			Key: "financial_consistency", Label: "Financial consistency", Status: "pass",
			Detail: "No financial evidence required", Weight: 0.10,
		})
		return
	}

	// Only non-duplicate, usable financial documents count.
	for _, e := range r.in.Evidence {
		if r.dupOf[e.ID] != "" {
			continue
		}
		if e.Kind != models.EvidenceInvoice && e.Kind != models.EvidenceReceipt {
			continue
		}
		if e.Extracted.AmountMinor > 0 {
			r.financial.DocumentedMinor += e.Extracted.AmountMinor
			r.financial.DocumentsAnalysed++
			continue
		}
		r.financial.Notes = append(r.financial.Notes,
			fmt.Sprintf("'%s' contained no readable total amount and could not be reconciled.", e.Filename))
	}

	r.financial.VarianceMinor = r.financial.DocumentedMinor - m.BudgetAmount
	if m.BudgetAmount > 0 {
		r.financial.VariancePercent = core.Ratio(float64(r.financial.VarianceMinor), float64(m.BudgetAmount))
	}

	switch {
	case r.financial.DocumentsAnalysed == 0:
		r.financial.Status = "undocumented"
		r.financial.Notes = append(r.financial.Notes,
			"No invoice or receipt with a readable amount was supplied for a milestone that requires one.")
		r.inconsistent = append(r.inconsistent, models.Inconsistency{
			Code:     "FINANCIAL_EVIDENCE_MISSING",
			Severity: "critical",
			Detail:   "The milestone requires an invoice or receipt, but no readable financial document was supplied.",
		})
	case m.BudgetAmount > 0 && r.financial.VariancePercent > 0.10:
		r.financial.Status = "overspend"
		r.financial.Notes = append(r.financial.Notes,
			fmt.Sprintf("Documented spend exceeds the milestone budget by %d%%.", core.Percent(r.financial.VariancePercent)))
		r.inconsistent = append(r.inconsistent, models.Inconsistency{
			Code:     "BUDGET_EXCEEDED",
			Severity: "critical",
			Detail: fmt.Sprintf("Documented spend of %s exceeds the milestone budget of %s by %d%%.",
				core.FormatMoney(core.Money(r.financial.DocumentedMinor), p.Currency),
				core.FormatMoney(core.Money(m.BudgetAmount), p.Currency),
				core.Percent(r.financial.VariancePercent)),
		})
	default:
		r.financial.Status = "ok"
		r.financial.Notes = append(r.financial.Notes,
			fmt.Sprintf("Documented spend of %s is within the milestone budget of %s.",
				core.FormatMoney(core.Money(r.financial.DocumentedMinor), p.Currency),
				core.FormatMoney(core.Money(m.BudgetAmount), p.Currency)))
	}

	r.tool("check_financial_consistency", "Reconcile spend against milestone budget",
		core.FormatMoney(core.Money(m.BudgetAmount), p.Currency), func() (string, string) {
			status := "ok"
			if r.financial.Status != "ok" {
				status = "warn"
			}
			if r.financial.Status == "undocumented" || r.financial.Status == "overspend" {
				status = "fail"
			}
			return fmt.Sprintf("%d document(s) analysed, documented %s against a budget of %s (variance %s / %d%%).",
				r.financial.DocumentsAnalysed,
				core.FormatMoney(core.Money(r.financial.DocumentedMinor), p.Currency),
				core.FormatMoney(core.Money(m.BudgetAmount), p.Currency),
				core.FormatMoney(core.Money(r.financial.VarianceMinor), p.Currency),
				core.Percent(r.financial.VariancePercent)), status
		})

	r.checks = append(r.checks, models.VerificationCheck{
		Key: "financial_consistency", Label: "Financial consistency",
		Status: boolStatus(r.financial.Status == "ok"),
		Detail: fmt.Sprintf("%s documented against a %s budget",
			core.FormatMoney(core.Money(r.financial.DocumentedMinor), p.Currency),
			core.FormatMoney(core.Money(m.BudgetAmount), p.Currency)),
		Weight: 0.10,
	})
}
