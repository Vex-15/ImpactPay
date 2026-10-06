package seed

import (
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
)

// demoUser is a seeded account. The passwords are deliberately well-known: this
// file is only ever used when DEMO_MODE/SEED_DEMO is enabled, and the README
// publishes these credentials so a reviewer can sign in as any role.
type demoUser struct {
	Role     models.Role
	Email    string
	Password string
	Name     string
}

// siteVillage is one surveyed village. Its site code is the identifier the
// verification pipeline counts for the site-survey milestone.
type siteVillage struct {
	Name       string
	Code       string
	Households int
	Source     string
}

// Canonical identities and channel data.
var demoUsers = []demoUser{
	{Role: models.RoleAdmin, Email: "verifier@impactpay.dev", Password: "impactpay-demo", Name: "Asha Verma (Platform Verifier)"},
	{Role: models.RoleOrganization, Email: "ops@aquahope.org", Password: "impactpay-demo", Name: "Meera Nair"},
	{Role: models.RoleOrganization, Email: "programs@sahayata.org", Password: "impactpay-demo", Name: "Imran Sheikh"},
	{Role: models.RoleOrganization, Email: "contact@vidyakiran.org", Password: "impactpay-demo", Name: "Leela Menon"},
	{Role: models.RoleDonor, Email: "priya@example.com", Password: "impactpay-demo", Name: "Priya Sharma"},
	{Role: models.RoleDonor, Email: "rohan@example.com", Password: "impactpay-demo", Name: "Rohan Mehta"},
	{Role: models.RoleDonor, Email: "ananya@example.com", Password: "impactpay-demo", Name: "Ananya Iyer"},
	{Role: models.RoleDonor, Email: "vikram@example.com", Password: "impactpay-demo", Name: "Vikram Rao"},
	{Role: models.RoleDonor, Email: "csr@wecarefoundation.org", Password: "impactpay-demo", Name: "WeCare CSR Trust"},
}

// The five villages the site-survey milestone must account for.
var demoVillages = []siteVillage{
	{Name: "Kandhwad", Code: "SUR-KAN-001", Households: 118, Source: "Borewell + hand pump"},
	{Name: "Bhilwadi", Code: "SUR-BHI-002", Households: 96, Source: "Seasonal stream"},
	{Name: "Nandurbar", Code: "SUR-NAN-003", Households: 141, Source: "Open well"},
	{Name: "Shirala", Code: "SUR-SHI-004", Households: 87, Source: "Canal-fed tank"},
	{Name: "Koregaon", Code: "SUR-KOR-005", Households: 128, Source: "Borewell"},
}

// transmissionUnitID renders the identifier scheme used across the demo: units
// bought under the equipment milestone are the same units installed later, so the
// two milestones share one serial space.
func transmissionUnitID(n int) string { return unitID(n) }

func unitID(n int) string {
	s := "0000"
	d := n
	for i := 3; i >= 0; i-- {
		s = s[:i] + string(rune('0'+d%10)) + s[i+1:]
		d /= 10
	}
	return "CW-MH-" + s
}

// The AquaHope demo project, exactly as specified in the product brief.
var aquaHopeMilestones = []milestoneSpec{
	{
		Title:            "Site survey",
		Description:      "Field survey of the five target villages to size demand, confirm water sources and agree household allocations with each gram panchayat.",
		Requirement:      "5 villages surveyed",
		UnitLabel:        "village surveyed",
		RequiredQuantity: 5,
		BudgetAmount:     50_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "survey_report", Label: "Village site survey report", Kind: string(models.EvidenceReport), Required: true, MinCount: 1, Detail: "Signed survey report covering every target village."},
			{Key: "survey_photographs", Label: "Survey photographs", Kind: string(models.EvidencePhotograph), Required: true, MinCount: 5, Detail: "One geotagged photograph per surveyed village."},
		},
	},
	{
		Title:            "Equipment purchase",
		Description:      "Procure 100 AquaPure HF-100 household filtration units and register every serial number against a target village.",
		Requirement:      "100 filtration units purchased",
		UnitLabel:        "filtration unit",
		RequiredQuantity: 100,
		BudgetAmount:     2_00_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "supplier_invoice", Label: "Supplier invoice", Kind: string(models.EvidenceInvoice), Required: true, MinCount: 1, Detail: "Tax invoice from the equipment supplier for all 100 units."},
			{Key: "supplier_receipt", Label: "Payment receipt", Kind: string(models.EvidenceReceipt), Required: true, MinCount: 1, Detail: "Supplier receipt for the advance payment."},
			{Key: "unit_registry", Label: "Purchased unit registry", Kind: string(models.EvidenceUnitRegistry), Required: true, MinCount: 1, Detail: "Serial numbers of every purchased unit."},
		},
	},
	{
		Title:            "Installation",
		Description:      "Install and commission the filtration units at household level across the five villages.",
		Requirement:      "100 units installed",
		UnitLabel:        "filtration unit",
		RequiredQuantity: 100,
		BudgetAmount:     4_00_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "installation_photographs", Label: "Installation photographs", Kind: string(models.EvidencePhotograph), Required: true, MinCount: 5, Detail: "Photographs of installed units, each captioned with its serial number."},
			{Key: "installation_registry", Label: "Installed unit registry", Kind: string(models.EvidenceUnitRegistry), Required: true, MinCount: 1, Detail: "Registry of installed serial numbers by village."},
			{Key: "location_evidence", Label: "Location evidence", Kind: string(models.EvidenceLocation), Required: true, MinCount: 1, Detail: "Location proof tying the installation site to a surveyed village."},
		},
	},
	{
		Title:            "Completion",
		Description:      "Complete the household survey, confirm service coverage and publish the programme completion report.",
		Requirement:      "500 households served",
		UnitLabel:        "household served",
		RequiredQuantity: 500,
		BudgetAmount:     3_50_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "completion_report", Label: "Programme completion report", Kind: string(models.EvidenceReport), Required: true, MinCount: 1, Detail: "Final report confirming households served."},
			{Key: "beneficiary_records", Label: "Household beneficiary records", Kind: string(models.EvidenceBeneficiaryRecord), Required: true, MinCount: 1, Detail: "Household-level records for the served families."},
		},
	},
}

// milestoneSpec mirrors milestones.Input but is declared here so the demo data is
// readable as a specification rather than as a constructor call.
type milestoneSpec struct {
	Title            string
	Description      string
	Requirement      string
	UnitLabel        string
	RequiredQuantity int
	BudgetAmount     int64
	Requirements     []models.EvidenceRequirementSpec
}

func (m milestoneSpec) input() milestones.Input {
	return milestones.Input{
		Title:                m.Title,
		Description:          m.Description,
		Requirement:          m.Requirement,
		UnitLabel:            m.UnitLabel,
		RequiredQuantity:     m.RequiredQuantity,
		BudgetAmount:         m.BudgetAmount,
		EvidenceRequirements: m.Requirements,
	}
}

// demoDonation is one seeded contribution.
type demoDonation struct {
	DonorEmail string
	Amount     int64
	Note       string
}

// AquaHope funding: ₹7,20,000 captured, which is enough to attribute the full
// budget of the first three milestones and part of the fourth.
var aquaHopeDonations = []demoDonation{
	{DonorEmail: "priya@example.com", Amount: 10_000_00, Note: "For the Kandhwad households."},
	{DonorEmail: "rohan@example.com", Amount: 2_50_000_00, Note: "Milestone-based funding — release on verified evidence."},
	{DonorEmail: "ananya@example.com", Amount: 1_00_000_00, Note: "Happy to see the evidence trail."},
	{DonorEmail: "vikram@example.com", Amount: 3_00_000_00, Note: "Backing the installation milestone."},
	{DonorEmail: "csr@wecarefoundation.org", Amount: 60_000_00, Note: "WeCare CSR — water access programme."},
}

// The secondary project keeps the marketplace from looking like a single card.
var sahayataMilestones = []milestoneSpec{
	{
		Title:            "Classroom devices",
		Description:      "Procure 50 refurbished tablets and charging cabinets for five government schools.",
		Requirement:      "50 tablets provisioned",
		UnitLabel:        "tablet",
		RequiredQuantity: 50,
		BudgetAmount:     2_00_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "supplier_invoice", Label: "Supplier invoice", Kind: string(models.EvidenceInvoice), Required: true, MinCount: 1},
			{Key: "device_registry", Label: "Device registry", Kind: string(models.EvidenceUnitRegistry), Required: true, MinCount: 1},
		},
	},
	{
		Title:            "Teacher training",
		Description:      "Train 30 teachers on the digital curriculum and classroom device management.",
		Requirement:      "30 teachers trained",
		UnitLabel:        "teacher trained",
		RequiredQuantity: 30,
		BudgetAmount:     1_50_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "attendance_records", Label: "Attendance records", Kind: string(models.EvidenceBeneficiaryRecord), Required: true, MinCount: 1},
			{Key: "training_report", Label: "Training report", Kind: string(models.EvidenceReport), Required: true, MinCount: 1},
		},
	},
	{
		Title:            "Learning assessment",
		Description:      "Baseline and endline assessment across 1,200 students, published as an open dataset.",
		Requirement:      "1200 students assessed",
		UnitLabel:        "student assessed",
		RequiredQuantity: 1200,
		BudgetAmount:     2_50_000_00,
		Requirements: []models.EvidenceRequirementSpec{
			{Key: "assessment_report", Label: "Assessment report", Kind: string(models.EvidenceReport), Required: true, MinCount: 1},
		},
	},
}

var sahayataDonations = []demoDonation{
	{DonorEmail: "ananya@example.com", Amount: 80_000_00, Note: "For the Jharkhand classrooms."},
	{DonorEmail: "rohan@example.com", Amount: 1_00_000_00, Note: "Teacher training matters most."},
}
