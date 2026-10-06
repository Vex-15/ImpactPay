package seed_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/auth"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/donations"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/impact"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/notifications"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/payments"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/seed"
	"github.com/impactpay/backend/internal/storage"
	"github.com/impactpay/backend/internal/verification"
)

type harness struct {
	db         *db.DB
	deps       seed.Deps
	projects   *projects.Service
	donations  *donations.Service
	verif      *verification.Service
	impact     *impact.Service
	milestones *milestones.Service
}

// newHarness wires the real services against a throwaway SQLite database, so the
// demo dataset is built by exactly the code the running server uses.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	database, err := db.Open(ctx, "sqlite", "", filepath.Join(dir, "impactpay.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := storage.NewLocal(filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("local storage: %v", err)
	}

	auditSvc := audit.New(database)
	authSvc := auth.New(database, auth.Config{SessionTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	orgSvc := organizations.New(database, auditSvc)
	msSvc := milestones.New(database, auditSvc)
	projSvc := projects.New(database, auditSvc, msSvc)
	evSvc := evidence.New(database, auditSvc, store, 10<<20)
	notifSvc := notifications.New(database)
	donSvc := donations.New(database, auditSvc, msSvc)
	verSvc := verification.New(database, auditSvc, evSvc, msSvc, projSvc, nil)
	paySvc := payments.New(payments.Deps{
		DB:            database,
		Audit:         auditSvc,
		Provider:      payments.NewSimulatedProvider(),
		Donations:     donSvc,
		Projects:      projSvc,
		Milestones:    msSvc,
		Verifications: verSvc,
		Organizations: orgSvc,
		Notifications: notifSvc,
	})
	impactSvc := impact.New(database)

	return &harness{
		db:       database,
		projects: projSvc,
		donations: donSvc,
		verif:    verSvc,
		impact:   impactSvc,
		milestones: msSvc,
		deps: seed.Deps{
			DB:            database,
			Auth:          authSvc,
			Organizations: orgSvc,
			Projects:      projSvc,
			Milestones:    msSvc,
			Evidence:      evSvc,
			Donations:     donSvc,
			Payments:      paySvc,
			Verification:  verSvc,
			Impact:        impactSvc,
		},
	}
}

// TestDemoSeedReproducesCanonicalStory is the acceptance test for DEMO_MODE.
//
// The seeded project must show, without human intervention:
//
//	installation submission 1 -> 87/100 HOLD
//	installation submission 2 -> 100/100 APPROVE -> funding released
//
// and the earlier milestones must be fully verified and released, with the
// completion milestone staged for a live run.
func TestDemoSeedReproducesCanonicalStory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	result, err := seed.Run(ctx, h.deps)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !result.Seeded {
		t.Fatalf("expected a fresh seed run, got %+v", result)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("seed reported unmet expectations: %v", result.Warnings)
	}

	outcomes := map[string]seed.Outcome{}
	for _, o := range result.Milestones {
		key := o.Title
		if o.Decision == "AWAITING_VERIFICATION" {
			outcomes[key] = o
			continue
		}
		// The installation milestone legitimately produces two decisions; keep
		// both under distinct keys.
		if _, exists := outcomes[key]; exists || (key == "Installation" && o.Decision == "APPROVE") {
			key = key + " (approved)"
		}
		outcomes[key] = o
	}

	expect := []struct {
		key      string
		decision string
		verified int
		required int
		released bool
	}{
		{"Site survey", "APPROVE", 5, 5, true},
		{"Equipment purchase", "APPROVE", 100, 100, true},
		{"Installation", "HOLD", 87, 100, false},
		{"Installation (approved)", "APPROVE", 100, 100, true},
	}
	for _, want := range expect {
		got, ok := outcomes[want.key]
		if !ok {
			t.Fatalf("no outcome recorded for %q (got %+v)", want.key, result.Milestones)
		}
		if got.Decision != want.decision {
			t.Errorf("%s: decision = %s, want %s", want.key, got.Decision, want.decision)
		}
		if got.Verified != want.verified || got.Required != want.required {
			t.Errorf("%s: verified %d/%d, want %d/%d",
				want.key, got.Verified, got.Required, want.verified, want.required)
		}
		if got.Released != want.released {
			t.Errorf("%s: released = %v, want %v", want.key, got.Released, want.released)
		}
	}

	if staged, ok := outcomes["Completion"]; !ok || staged.Decision != "AWAITING_VERIFICATION" {
		t.Errorf("completion milestone should be staged but unverified, got %+v", staged)
	}

	// Funding: ₹7,20,000 captured; the first three milestones released in full.
	project, err := h.projects.BySlug(ctx, result.ProjectSlug)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	if project.FundedAmount != 72_000_000 {
		t.Errorf("funded = %d, want 72000000", project.FundedAmount)
	}
	if project.ReleasedAmount != 65_000_000 {
		t.Errorf("released = %d, want 65000000 (₹50,000 + ₹2,00,000 + ₹4,00,000)", project.ReleasedAmount)
	}
	if project.ReservedAmount != 0 {
		t.Errorf("reserved = %d, want 0 after every release settled", project.ReservedAmount)
	}

	// Running the seed again must be a no-op.
	second, err := seed.Run(ctx, h.deps)
	if err != nil {
		t.Fatalf("second seed run: %v", err)
	}
	if second.Seeded {
		t.Fatalf("second run reseeded the dataset")
	}
}

// TestSeededInstallationMilestoneIsReproducible re-runs the verification pipeline
// over the stored installation evidence and asserts the same verdict. This is what
// makes the demo defensible: the 100/100 result is derived from persisted data,
// not recorded in advance.
func TestSeededInstallationMilestoneIsReproducible(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	result, err := seed.Run(ctx, h.deps)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	project, err := h.projects.BySlug(ctx, result.ProjectSlug)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	list, err := h.milestones.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("list milestones: %v", err)
	}

	var installation *models.Milestone
	for i := range list {
		if list[i].Title == "Installation" {
			installation = &list[i]
		}
	}
	if installation == nil {
		t.Fatal("installation milestone not found")
	}

	history, err := h.verif.ListForMilestone(ctx, installation.ID)
	if err != nil {
		t.Fatalf("verification history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least two verification runs for the installation milestone, got %d", len(history))
	}

	var sawHold, sawApprove bool
	for _, v := range history {
		switch v.Status {
		case models.DecisionHold:
			sawHold = true
			if v.VerifiedQuantity != 87 || v.RequiredQuantity != 100 {
				t.Errorf("HOLD run recorded %d/%d, want 87/100", v.VerifiedQuantity, v.RequiredQuantity)
			}
		case models.DecisionApprove:
			sawApprove = true
			if v.VerifiedQuantity != 100 {
				t.Errorf("APPROVE run recorded %d verified, want 100", v.VerifiedQuantity)
			}
		}
	}
	if !sawHold || !sawApprove {
		t.Fatalf("verification history must contain both HOLD and APPROVE (hold=%v approve=%v)", sawHold, sawApprove)
	}

	// Re-evaluating now must agree with the stored APPROVE and not add a hold.
	rerun, err := h.verif.VerifyMilestone(ctx, installation.ID, models.SystemActor())
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if rerun.Verification.VerifiedQuantity != 100 {
		t.Fatalf("re-verified quantity = %d, want 100", rerun.Verification.VerifiedQuantity)
	}
	if rerun.Verification.Status != models.DecisionApprove {
		t.Fatalf("re-verified status = %s, want APPROVE", rerun.Verification.Status)
	}
}

// TestDonorCanTraceTheirImpact exercises the Proof Graph over seeded data: the
// demo donor's contribution must resolve to a verified outcome with evidence.
func TestDonorCanTraceTheirImpact(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := seed.Run(ctx, h.deps); err != nil {
		t.Fatalf("seed: %v", err)
	}

	giver, err := h.deps.Auth.ByEmail(ctx, "priya@example.com")
	if err != nil {
		t.Fatalf("load donor: %v", err)
	}
	list, err := h.donations.ListByDonor(ctx, giver.ID, 10)
	if err != nil {
		t.Fatalf("list donor contributions: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("seeded donor has no contributions")
	}

	graph, err := h.impact.Trace(ctx, list[0].ID)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if graph.VerifiedShare.TotalMinor <= 0 {
		t.Fatalf("expected a positive contributed total, got %+v", graph.VerifiedShare)
	}
	if graph.VerifiedShare.VerifiedMinor <= 0 {
		t.Fatalf("expected some of the contribution to be verified, got %+v", graph.VerifiedShare)
	}

	var hasOutcome, hasEvidence bool
	for _, n := range graph.Nodes {
		switch n.Type {
		case impact.NodeOutcome:
			if n.Verified {
				hasOutcome = true
			}
		case impact.NodeEvidence:
			if len(n.Evidence) > 0 {
				hasEvidence = true
			}
		}
	}
	if !hasOutcome {
		t.Error("proof graph has no verified outcome node")
	}
	if !hasEvidence {
		t.Error("proof graph has no evidence-backed node")
	}
}
