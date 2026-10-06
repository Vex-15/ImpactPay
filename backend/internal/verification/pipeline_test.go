package verification

import (
	"testing"
	"time"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// milestone builds the installation milestone from the product brief.
func installationMilestone(required int) *models.Milestone {
	return &models.Milestone{
		ID:               "msn_installation",
		ProjectID:        "prj_clean_water",
		Ordinal:          3,
		Title:            "Installation",
		Requirement:      "100 units installed",
		UnitLabel:        "filtration unit",
		RequiredQuantity: required,
		BudgetAmount:     40_000_000,
		EvidenceRequirements: []models.EvidenceRequirementSpec{
			{Key: "installation_photographs", Label: "Installation photographs", Kind: string(models.EvidencePhotograph), Required: true, MinCount: 5},
			{Key: "installation_registry", Label: "Installed unit registry", Kind: string(models.EvidenceUnitRegistry), Required: true, MinCount: 1},
			{Key: "location_evidence", Label: "Location evidence", Kind: string(models.EvidenceLocation), Required: true, MinCount: 1},
		},
	}
}

func demoProject() *models.Project {
	return &models.Project{
		ID:           "prj_clean_water",
		Title:        "Clean Water Maharashtra",
		Currency:     "INR",
		GoalAmount:   100_000_000,
		FundedAmount: 72_000_000,
		Status:       models.ProjectInProgress,
	}
}

func unitID(n int) string {
	s := "0000"
	d := n
	for i := 3; i >= 0; i-- {
		s = s[:i] + string(rune('0'+d%10)) + s[i+1:]
		d /= 10
	}
	return "CW-MH-" + s
}

// installationEvidence mirrors the seeded dataset: a registry that accounts for
// the units believed installed, one photograph per serial number, and a location
// artefact. The second batch of photographs deliberately repeats serial numbers
// already present in the registry.
func installationEvidence(registryUnits, photoCount int) []models.Evidence {
	items := []models.Evidence{}

	registryIDs := make([]string, 0, registryUnits)
	for i := 1; i <= registryUnits; i++ {
		registryIDs = append(registryIDs, unitID(i))
	}
	items = append(items, models.Evidence{
		ID:             "ev_registry",
		Kind:           models.EvidenceUnitRegistry,
		RequirementKey: "installation_registry",
		Filename:       "installed-unit-registry.csv",
		ContentHash:    "hash-registry",
		Extracted: models.ExtractedFacts{
			Kind: "UNIT_REGISTRY", Quality: "ok", ExtractionMode: "deterministic",
			UnitIDs: registryIDs,
		},
	})

	for i := 0; i < photoCount; i++ {
		unit := i + 1
		if unit > registryUnits {
			unit = i - registryUnits + 1
		}
		id := unitID(unit)
		items = append(items, models.Evidence{
			ID:             "ev_photo_" + id + "_" + string(rune('a'+(i%26))) + string(rune('a'+(i/26))),
			Kind:           models.EvidencePhotograph,
			RequirementKey: "installation_photographs",
			Filename:       "installation-" + id + ".png",
			ContentHash:    "hash-" + id + "-" + string(rune('a'+(i%26))) + string(rune('a'+(i/26))),
			Extracted: models.ExtractedFacts{
				Kind: "PHOTOGRAPH", Quality: "ok", ExtractionMode: "deterministic",
				UnitIDs: []string{id},
			},
		})
	}

	items = append(items, models.Evidence{
		ID:             "ev_location",
		Kind:           models.EvidenceLocation,
		RequirementKey: "location_evidence",
		Filename:       "location-evidence.png",
		ContentHash:    "hash-location",
		Extracted: models.ExtractedFacts{
			Kind: "LOCATION", Quality: "ok", ExtractionMode: "deterministic",
			Location: "Kandhwad village",
		},
	})

	return items
}

// TestPipelineHoldsAtEightySevenAndApprovesAtHundred is the contract the product
// brief depends on: incomplete evidence must hold, and the remainder must approve
// the same milestone. It is deterministic, so it must hold on every run.
func TestPipelineHoldsAtEightySevenAndApprovesAtHundred(t *testing.T) {
	project := demoProject()
	milestone := installationMilestone(100)

	// --- first submission: 113 photographs, 87 unique serial numbers ---------
	first := Evaluate(Input{
		Project:   project,
		Milestone: milestone,
		Evidence:  installationEvidence(87, 113),
	})

	if first.VerifiedQuantity != 87 {
		t.Fatalf("first submission: verified quantity = %d, want 87", first.VerifiedQuantity)
	}
	if first.RequiredQuantity != 100 {
		t.Fatalf("first submission: required quantity = %d, want 100", first.RequiredQuantity)
	}
	if first.Recommendation != models.DecisionHold {
		t.Fatalf("first submission: recommendation = %s, want HOLD (summary: %s)",
			first.Recommendation, first.Summary)
	}
	if !containsCode(first.MissingEvidence, "quantity") {
		t.Fatalf("first submission: expected a quantity shortfall, got %+v", first.MissingEvidence)
	}

	// Every photograph is byte-distinct and establishes a unit, so nothing may be
	// discarded as a duplicate; the shortfall is genuine.
	if first.DuplicateCount != 0 {
		t.Fatalf("first submission: duplicate count = %d, want 0", first.DuplicateCount)
	}

	// --- second submission: the registry updated to the full 100 ------------
	second := Evaluate(Input{
		Project:   project,
		Milestone: milestone,
		Evidence:  installationEvidence(100, 126),
	})

	if second.VerifiedQuantity != 100 {
		t.Fatalf("second submission: verified quantity = %d, want 100", second.VerifiedQuantity)
	}
	if second.Recommendation != models.DecisionApprove {
		t.Fatalf("second submission: recommendation = %s, want APPROVE (summary: %s)",
			second.Recommendation, second.Summary)
	}
	if len(second.MissingEvidence) != 0 {
		t.Fatalf("second submission: unexpected missing evidence: %+v", second.MissingEvidence)
	}
	if second.Confidence < 0.9 {
		t.Fatalf("second submission: confidence = %.2f, want >= 0.90", second.Confidence)
	}

	// Re-evaluating the first submission must reproduce the hold exactly.
	again := Evaluate(Input{
		Project:   project,
		Milestone: milestone,
		Evidence:  installationEvidence(87, 113),
	})
	if again.Recommendation != first.Recommendation || again.VerifiedQuantity != first.VerifiedQuantity {
		t.Fatalf("pipeline is not deterministic: %s/%d then %s/%d",
			first.Recommendation, first.VerifiedQuantity, again.Recommendation, again.VerifiedQuantity)
	}
}

// TestPipelineExcludesDuplicateContent proves that re-uploading the same bytes
// cannot inflate a verified count, even when the file claims a fresh unit.
func TestPipelineExcludesDuplicateContent(t *testing.T) {
	milestone := installationMilestone(2)
	items := []models.Evidence{
		{
			ID: "ev_1", Kind: models.EvidencePhotograph, RequirementKey: "installation_photographs",
			Filename: "a.png", ContentHash: "same", CreatedAt: mustTime("2026-04-01T00:00:00Z"),
			Extracted: models.ExtractedFacts{Quality: "ok", UnitIDs: []string{"CW-MH-0001"}},
		},
		{
			ID: "ev_2", Kind: models.EvidencePhotograph, RequirementKey: "installation_photographs",
			Filename: "b.png", ContentHash: "same", CreatedAt: mustTime("2026-04-02T00:00:00Z"),
			Extracted: models.ExtractedFacts{Quality: "ok", UnitIDs: []string{"CW-MH-0002"}},
		},
	}
	res := Evaluate(Input{Project: demoProject(), Milestone: milestone, Evidence: items})

	if res.VerifiedQuantity != 1 {
		t.Fatalf("verified quantity = %d, want 1 (the duplicate must not count)", res.VerifiedQuantity)
	}
	if res.DuplicateCount != 1 {
		t.Fatalf("duplicate count = %d, want 1", res.DuplicateCount)
	}
	if res.Recommendation == models.DecisionApprove {
		t.Fatalf("a shortfall must never approve")
	}
	if !hasCriticalOrWarning(res.Inconsistencies, "DUPLICATE_EVIDENCE") {
		t.Fatalf("expected a DUPLICATE_EVIDENCE inconsistency, got %+v", res.Inconsistencies)
	}
}

// TestPipelineFlagsBudgetOverspend proves the financial stage can hold a release
// on its own: a documented overspend is a critical finding.
func TestPipelineFlagsBudgetOverspend(t *testing.T) {
	milestone := &models.Milestone{
		ID: "msn_equipment", ProjectID: "prj_clean_water", Ordinal: 2, Title: "Equipment purchase",
		Requirement: "100 units purchased", UnitLabel: "filtration unit",
		RequiredQuantity: 100, BudgetAmount: 20_000_000,
		EvidenceRequirements: []models.EvidenceRequirementSpec{
			{Key: "supplier_invoice", Kind: string(models.EvidenceInvoice), Required: true, MinCount: 1},
		},
	}
	evidence := []models.Evidence{
		{
			ID: "ev_invoice", Kind: models.EvidenceInvoice, RequirementKey: "supplier_invoice",
			Filename: "invoice.txt", ContentHash: "hash-invoice",
			Extracted: models.ExtractedFacts{
				Quality: "ok", AmountMinor: 30_000_000, // ₹3,00,000 against a ₹2,00,000 budget
				UnitIDs: allUnitIDs(100),
			},
		},
	}
	res := Evaluate(Input{Project: demoProject(), Milestone: milestone, Evidence: evidence})

	if res.Financial.Status != "overspend" {
		t.Fatalf("financial status = %s, want overspend", res.Financial.Status)
	}
	if res.Recommendation != models.DecisionHold {
		t.Fatalf("recommendation = %s, want HOLD for a critical overspend", res.Recommendation)
	}
	if !hasCriticalOrWarning(res.Inconsistencies, "BUDGET_EXCEEDED") {
		t.Fatalf("expected a BUDGET_EXCEEDED inconsistency, got %+v", res.Inconsistencies)
	}
}

// ---------------------------------------------------------------------------

func containsCode(items []models.MissingEvidence, key string) bool {
	for _, m := range items {
		if m.RequirementKey == key {
			return true
		}
	}
	return false
}

func hasCriticalOrWarning(items []models.Inconsistency, code string) bool {
	for _, inc := range items {
		if inc.Code == code {
			return true
		}
	}
	return false
}

func allUnitIDs(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, unitID(i))
	}
	return out
}

func mustTime(v string) time.Time { return core.MustParseTime(v) }
