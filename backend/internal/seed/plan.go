package seed

import (
	"fmt"
	"strings"

	"github.com/impactpay/backend/internal/models"
)

// surveyAssets is milestone 1's complete evidence set: the signed survey report
// plus one captioned photograph per surveyed village. The report's site codes are
// the identifiers the pipeline counts, and the photographs carry the same codes,
// so the two artefacts corroborate each other instead of inflating the total.
func surveyAssets() []asset {
	out := make([]asset, 0, 1+len(demoVillages))
	out = append(out, asset{
		Kind:           models.EvidenceReport,
		RequirementKey: "survey_report",
		Filename:       "survey-report-clean-water-maharashtra.txt",
		ContentType:    "text/plain",
		Description:    "Signed village site survey report covering all five target villages.",
		Data:           textAsset(siteSurveyReportText(demoVillages)),
		Hints: models.EvidenceHints{
			Villages:   villageNames(),
			CapturedAt: "2026-03-02",
		},
	})

	for i, v := range demoVillages {
		out = append(out, asset{
			Kind:           models.EvidencePhotograph,
			RequirementKey: "survey_photographs",
			Filename:       fmt.Sprintf("survey-photograph-%s.png", strings.ToLower(v.Name)),
			ContentType:    "image/png",
			Description:    fmt.Sprintf("Survey photograph for %s (%s).", v.Name, v.Code),
			Data: photoPNG(fmt.Sprintf(
				"ImpactPay survey photograph - %s, %s district. Site %s, %d households, water source: %s. Captured 2026-03-02.",
				v.Name, "Nandurbar", v.Code, v.Households, v.Source), 1000+i),
			Hints: models.EvidenceHints{
				Location:   v.Name,
				Villages:   []string{v.Name},
				CapturedAt: "2026-03-02",
			},
		})
	}
	return out
}

// equipmentAssets is milestone 2's evidence set: a tax invoice, a supplier
// receipt and the purchased unit registry. The two financial documents sum to the
// milestone budget, so the pipeline's reconciliation passes on its own merits.
func equipmentAssets(units int) []asset {
	ids := make([]string, 0, units)
	for i := 1; i <= units; i++ {
		ids = append(ids, unitID(i))
	}

	return []asset{
		{
			Kind:           models.EvidenceInvoice,
			RequirementKey: "supplier_invoice",
			Filename:       "supplier-invoice-AQINV20260142.txt",
			ContentType:    "text/plain",
			Description:    "Supplier tax invoice for 100 AquaPure HF-100 filtration units.",
			Data: textAsset(invoiceText(
				"AQINV20260142",
				"PureFlow Systems Pvt Ltd",
				"2026-03-14",
				14_500_000, // ₹1,45,000
				units,
				fmt.Sprintf("AquaPure HF-100 household filtration units for the Clean Water Maharashtra programme (%d units)", units),
			)),
			Hints: models.EvidenceHints{
				Vendor:      "PureFlow Systems Pvt Ltd",
				InvoiceNo:   "AQINV20260142",
				AmountMinor: 14_500_000,
				Quantity:    units,
				UnitLabel:   "filtration unit",
				CapturedAt:  "2026-03-14",
			},
		},
		{
			Kind:           models.EvidenceReceipt,
			RequirementKey: "supplier_receipt",
			Filename:       "supplier-receipt-AQRCPT20260143.txt",
			ContentType:    "text/plain",
			Description:    "Supplier receipt for the remaining balance on the equipment invoice.",
			Data: textAsset(receiptText(
				"AQRCPT20260143",
				"PureFlow Systems Pvt Ltd",
				"2026-03-20",
				"the equipment purchase order for this programme",
				5_500_000, // ₹55,000
			)),
			Hints: models.EvidenceHints{
				Vendor:      "PureFlow Systems Pvt Ltd",
				InvoiceNo:   "AQRCPT20260143",
				AmountMinor: 5_500_000,
				CapturedAt:  "2026-03-20",
			},
		},
		{
			Kind:           models.EvidenceUnitRegistry,
			RequirementKey: "unit_registry",
			Filename:       "purchased-unit-registry.csv",
			ContentType:    "text/csv",
			Description:    "Registry of the serial numbers of every purchased filtration unit.",
			Data:           textAsset(registryCSV(ids, "Kandhwad")),
			Hints: models.EvidenceHints{
				UnitIDs:   ids,
				UnitLabel: "filtration unit",
				Quantity:  units,
			},
		},
	}
}

// installationAssets1 is milestone 3's first submission: the registry as at
// 12 April 2026, which accounts for 87 of the 100 units, plus field photographs.
//
// The second batch of photographs deliberately repeats identifiers already in the
// registry. They are real, distinct files, so the pipeline cannot dismiss them as
// duplicate content — but they establish no new unit, which is exactly why the
// verified total is 87 rather than 100 and the milestone is held.
func installationAssets1() []asset {
	const (
		installed = 87
		photos    = 113
	)

	registryIDs := make([]string, 0, installed)
	for i := 1; i <= installed; i++ {
		registryIDs = append(registryIDs, unitID(i))
	}

	out := make([]asset, 0, photos+2)
	out = append(out, asset{
		Kind:           models.EvidenceUnitRegistry,
		RequirementKey: "installation_registry",
		Filename:       "installed-unit-registry-20260412.csv",
		ContentType:    "text/csv",
		Description:    fmt.Sprintf("Installed unit registry as at 12 April 2026 — %d serial numbers commissioned.", installed),
		Data:           textAsset(registryCSV(registryIDs, "Kandhwad")),
		Hints: models.EvidenceHints{
			UnitIDs:   registryIDs,
			UnitLabel: "filtration unit",
			Quantity:  installed,
			Location:  "Nandurbar district, Maharashtra",
		},
	})

	for i := 0; i < photos; i++ {
		// The first 87 photographs introduce one new unit each. The remaining 26
		// re-photograph units that are already established, so they corroborate
		// rather than extend the count.
		unitNo := i + 1
		if unitNo > installed {
			unitNo = i - installed + 1
		}
		out = append(out, installationPhoto(unitNo, i+1, "2026-04-12"))
	}

	out = append(out, locationAsset(900))
	return out
}

// installationAssets2 supplies the evidence the agent asked for: the registry
// updated to the full 100 serial numbers, plus the photographs for the units that
// were still outstanding.
func installationAssets2() []asset {
	registryIDs := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		registryIDs = append(registryIDs, unitID(i))
	}

	out := make([]asset, 0, 14)
	out = append(out, asset{
		Kind:           models.EvidenceUnitRegistry,
		RequirementKey: "installation_registry",
		Filename:       "installed-unit-registry-20260428.csv",
		ContentType:    "text/csv",
		Description:    "Installed unit registry updated to the full 100 commissioned serial numbers.",
		Data:           textAsset(registryCSV(registryIDs, "Bhilwadi")),
		Hints: models.EvidenceHints{
			UnitIDs:   registryIDs,
			UnitLabel: "filtration unit",
			Quantity:  100,
			Location:  "Nandurbar district, Maharashtra",
		},
	})

	for i := 88; i <= 100; i++ {
		out = append(out, installationPhoto(i, 200+i, "2026-04-28"))
	}
	return out
}

func installationPhoto(unitNo, variant int, captured string) asset {
	id := unitID(unitNo)
	return asset{
		Kind:           models.EvidencePhotograph,
		RequirementKey: "installation_photographs",
		Filename:       fmt.Sprintf("installation-%s.png", strings.ToLower(id)),
		ContentType:    "image/png",
		Description:    fmt.Sprintf("Installed filtration unit %s.", id),
		Data: photoPNG(fmt.Sprintf(
			"ImpactPay field photograph - installed filtration unit %s, household connection, Kandhwad village, Nandurbar district. Captured %s.",
			id, captured), variant),
		Hints: models.EvidenceHints{
			UnitIDs:    []string{id},
			UnitLabel:  "filtration unit",
			Location:   "Kandhwad village, Nandurbar district",
			CapturedAt: captured,
		},
	}
}

// locationAsset is a location-proof artefact. Its caption deliberately carries no
// asset identifier so it can never be mistaken for a countable unit.
func locationAsset(variant int) asset {
	return asset{
		Kind:           models.EvidenceLocation,
		RequirementKey: "location_evidence",
		Filename:       "location-evidence-kandhwad.png",
		ContentType:    "image/png",
		Description:    "Location evidence confirming the installation site is inside a surveyed village.",
		Data: photoPNG("ImpactPay location evidence. Installation site inside Kandhwad village, Nandurbar district, Maharashtra, India.", variant),
		Hints: models.EvidenceHints{
			Location:   "Kandhwad village, Nandurbar district",
			Villages:   []string{"Kandhwad"},
			CapturedAt: "2026-04-12",
		},
	}
}

// completionAssets is milestone 4's evidence set, staged ready for a live
// verification run: the programme completion report and 500 household-level
// beneficiary records.
func completionAssets() []asset {
	households := 500
	return []asset{
		{
			Kind:           models.EvidenceReport,
			RequirementKey: "completion_report",
			Filename:       "completion-report-clean-water-maharashtra.txt",
			ContentType:    "text/plain",
			Description:    "Programme completion report confirming 500 households served.",
			Data:           textAsset(completionReportText(households, villageNames())),
			Hints: models.EvidenceHints{
				Beneficiaries: households,
				Villages:      villageNames(),
				CapturedAt:    "2026-06-30",
			},
		},
		{
			Kind:           models.EvidenceBeneficiaryRecord,
			RequirementKey: "beneficiary_records",
			Filename:       "household-beneficiary-records.csv",
			ContentType:    "text/csv",
			Description:    "Household-level beneficiary records for every served family.",
			Data:           textAsset(beneficiaryRecordText(villageNames(), households)),
			Hints: models.EvidenceHints{
				Beneficiaries: households,
				Villages:      villageNames(),
				CapturedAt:    "2026-06-30",
			},
		},
	}
}

func villageNames() []string {
	out := make([]string, 0, len(demoVillages))
	for _, v := range demoVillages {
		out = append(out, v.Name)
	}
	return out
}
