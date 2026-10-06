// Package seed builds ImpactPay's deterministic DEMO_MODE dataset.
//
// The seeded state is not a fixture of pre-written verdicts. Every artefact is
// uploaded through the real evidence service, analysed by the real deterministic
// extractor, verified by the real verification pipeline and released through the
// real conditional-release engine. The canonical narrative therefore falls out of
// the data:
//
//	milestone 1  site survey        complete evidence        -> APPROVE  -> released
//	milestone 2  equipment purchase invoice + receipt + registry -> APPROVE -> released
//	milestone 3  installation       submission 1: 87 of 100    -> HOLD
//	                                submission 2: 100 of 100   -> APPROVE  -> released
//	milestone 4  completion         evidence staged, unverified (the live demo)
//
// Because the pipeline is deterministic, re-running verification over the stored
// evidence reproduces exactly the same 87/100 HOLD and 100/100 APPROVE.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/impactpay/backend/internal/auth"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/donations"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/impact"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/payments"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/verification"
)

// Deps is everything the seeder needs. It mirrors the services the HTTP layer
// uses so the demo is built by the same rules the product runs on.
type Deps struct {
	DB            *db.DB
	Logger        *slog.Logger
	Auth          *auth.Service
	Organizations *organizations.Service
	Projects      *projects.Service
	Milestones    *milestones.Service
	Evidence      *evidence.Service
	Donations     *donations.Service
	Payments      *payments.Service
	Verification  *verification.Service
	Impact        *impact.Service
}

// Credential is a seeded sign-in the README publishes.
type Credential struct {
	Role     string `json:"role"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// Outcome records what the verification pipeline decided for a seeded milestone,
// so a test can assert the canonical story without re-deriving it.
type Outcome struct {
	Ordinal    int    `json:"ordinal"`
	Title      string `json:"title"`
	Decision   string `json:"decision"`
	Verified   int    `json:"verified"`
	Required   int    `json:"required"`
	Released   bool   `json:"released"`
	ReleasedMinor int64 `json:"released_minor"`
}

// Result summarises a seed run.
type Result struct {
	Seeded        bool
	Organization  string
	Project       string
	ProjectSlug   string
	Donors        int
	CapturedMinor int64
	Currency      string
	Milestones    []Outcome
	Warnings      []string
	Credentials   []Credential
}

const (
	demoOrgSlug     = "aquahope-foundation"
	demoProjectSlug = "clean-water-maharashtra"
)

type seeder struct {
	d     Deps
	ctx   context.Context
	users map[string]*models.User
	orgs  map[string]*models.Organization
	warn  []string
}

// Run seeds the demo dataset if it is not already present.
func Run(ctx context.Context, d Deps) (Result, error) {
	if d.DB == nil {
		return Result{}, errors.New("seed: database is required")
	}
	s := &seeder{d: d, ctx: ctx, users: map[string]*models.User{}, orgs: map[string]*models.Organization{}}
	return s.run()
}

func (s *seeder) run() (Result, error) {
	// Idempotency: a second boot must not duplicate the dataset.
	if existing, err := s.d.Projects.BySlug(s.ctx, demoProjectSlug); err == nil && existing != nil {
		return Result{
			Seeded:       false,
			Organization: "AquaHope Foundation",
			Project:      existing.Title,
			ProjectSlug:  existing.Slug,
		}, nil
	} else if err != nil && !errors.Is(err, projects.ErrNotFound) {
		return Result{}, fmt.Errorf("seed: look up demo project: %w", err)
	}

	if err := s.registerUsers(); err != nil {
		return Result{}, err
	}

	admin := s.users["verifier@impactpay.dev"]
	adminActor := actorOf(admin)
	adminCtx := s.ctxAs(admin)

	aquaUser := s.users["ops@aquahope.org"]
	aquaCtx := s.ctxAs(aquaUser)

	aqua, err := s.createOrganization(aquaCtx, aquaUser, organizations.CreateInput{
		UserID:             aquaUser.ID,
		Name:               "AquaHope Foundation",
		Mission:            "Bring safely managed drinking water to rural households through instrumented, evidence-verified water infrastructure.",
		Description:        "AquaHope Foundation is a Maharashtra-based water access organisation. Every programme is delivered against milestone budgets and published with the evidence behind it: invoices, serialised unit registries, installation photographs and household records.",
		Website:            "https://aquahope.example.org",
		Country:            "India",
		RegistrationNumber: "MH/2011/0142876",
		ContactEmail:       "ops@aquahope.org",
	})
	if err != nil {
		return Result{}, err
	}
	s.orgs[demoOrgSlug] = aqua

	if _, err := s.d.Organizations.Update(aquaCtx, aqua.ID, organizations.UpdateInput{
		PayoutEmail: strPtr("payouts@aquahope.org"),
	}); err != nil {
		return Result{}, fmt.Errorf("seed: set payout email: %w", err)
	}

	verifiedOrg, err := s.d.Organizations.Review(adminCtx, aqua.ID, admin.ID, true,
		"Registration certificate, 12A and 80G documents verified against the registrar record. Two prior water programmes and their evidence trails reviewed. Payout account confirmed.")
	if err != nil {
		return Result{}, fmt.Errorf("seed: verify organization: %w", err)
	}
	s.orgs[demoOrgSlug] = verifiedOrg

	// A second verified organization and a third awaiting review keep the
	// marketplace and the admin review queue from looking empty.
	if err := s.seedSecondaryOrganizations(adminCtx, adminActor); err != nil {
		return Result{}, err
	}

	// --- the headline project ----------------------------------------------
	inputs := make([]milestones.Input, 0, len(aquaHopeMilestones))
	for _, m := range aquaHopeMilestones {
		inputs = append(inputs, m.input())
	}

	project, seededMilestones, err := s.d.Projects.Create(aquaCtx, projects.CreateInput{
		OrgID:                  aqua.ID,
		CreatedBy:              aquaUser.ID,
		Title:                  "Clean Water Maharashtra",
		Category:               "Water & Sanitation",
		Summary:                "Install 100 household filtration units across five villages serving 500 families, each unit registered, photographed and verified before its funding is released.",
		Problem:                "Five villages in Nandurbar district rely on seasonal streams and open wells. During the dry months households collect water from sources that repeatedly fail coliform testing, and the burden falls on women and girls who walk the furthest.",
		Approach:               "Survey every village first, procure filtration units against a serialised registry, install them household by household with photographic evidence, then complete a household survey. Funding is released milestone by milestone, only after the evidence for that milestone has been verified.",
		Location:               "Nandurbar district, Maharashtra, India",
		Currency:               "INR",
		GoalAmount:             10_000_00 * 100,
		BeneficiaryCount:       500,
		BeneficiaryDescription: "500 households across five villages",
		OutcomeDescription:     "100 filtration units installed and serving 500 families with safely managed drinking water.",
		Milestones:             inputs,
	})
	if err != nil {
		return Result{}, fmt.Errorf("seed: create demo project: %w", err)
	}

	published, err := s.d.Projects.Publish(aquaCtx, project.ID)
	if err != nil {
		return Result{}, fmt.Errorf("seed: publish demo project: %w", err)
	}

	captured, donors, err := s.fund(aquaCtx, published, orgUserID(aquaUser), aquaHopeDonations)
	if err != nil {
		return Result{}, err
	}

	outcomes, err := s.seedAquaHopeMilestones(aquaCtx, aquaUser, published, seededMilestones)
	if err != nil {
		return Result{}, err
	}

	if err := s.seedProjectUpdates(aquaCtx, aquaUser, published); err != nil {
		return Result{}, err
	}

	// --- secondary project --------------------------------------------------
	if err := s.seedSecondaryProject(adminCtx); err != nil {
		return Result{}, err
	}

	return Result{
		Seeded:        true,
		Organization:  aqua.Name,
		Project:       published.Title,
		ProjectSlug:   published.Slug,
		Donors:        donors,
		CapturedMinor: captured,
		Currency:      published.Currency,
		Milestones:    outcomes,
		Warnings:      s.warn,
		Credentials:   credentials(),
	}, nil
}

// ---------------------------------------------------------------------------
// Users and organizations
// ---------------------------------------------------------------------------

func (s *seeder) registerUsers() error {
	for _, du := range demoUsers {
		user, _, err := s.d.Auth.Register(s.ctx, auth.RegisterInput{
			Email:    du.Email,
			Password: du.Password,
			FullName: du.Name,
			Role:     du.Role,
		})
		if errors.Is(err, auth.ErrEmailTaken) {
			existing, lookupErr := s.d.Auth.ByEmail(s.ctx, du.Email)
			if lookupErr != nil {
				return fmt.Errorf("seed: load existing user %s: %w", du.Email, lookupErr)
			}
			s.users[du.Email] = existing
			continue
		}
		if err != nil {
			return fmt.Errorf("seed: register %s: %w", du.Email, err)
		}
		s.users[du.Email] = user
	}
	return nil
}

func (s *seeder) createOrganization(ctx context.Context, user *models.User, in organizations.CreateInput) (*models.Organization, error) {
	org, err := s.d.Organizations.Create(ctx, in)
	if errors.Is(err, organizations.ErrAlreadyExists) {
		existing, lookupErr := s.d.Organizations.ByUserID(ctx, user.ID)
		if lookupErr != nil {
			return nil, fmt.Errorf("seed: load existing organization for %s: %w", user.Email, lookupErr)
		}
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("seed: create organization %q: %w", in.Name, err)
	}
	return org, nil
}

func (s *seeder) seedSecondaryOrganizations(adminCtx context.Context, adminActor models.Actor) error {
	// Sahayata Learning Trust — verified, with a live project.
	sahUser := s.users["programs@sahayata.org"]
	sahCtx := s.ctxAs(sahUser)
	sahayata, err := s.createOrganization(sahCtx, sahUser, organizations.CreateInput{
		UserID:             sahUser.ID,
		Name:               "Sahayata Learning Trust",
		Mission:            "Raise foundational literacy and digital access in government schools across eastern India.",
		Description:        "Sahayata Learning Trust runs classroom technology and teacher training programmes, reporting every cohort against verified attendance and assessment records.",
		Website:            "https://sahayata.example.org",
		Country:            "India",
		RegistrationNumber: "JH/2015/0099121",
		ContactEmail:       "programs@sahayata.org",
	})
	if err != nil {
		return err
	}
	verified, err := s.d.Organizations.Review(adminCtx, sahayata.ID, adminActor.ID, true,
		"Registration and FCRA documents verified. Prior assessments published as open data.")
	if err != nil {
		return fmt.Errorf("seed: verify Sahayata: %w", err)
	}
	s.orgs["sahayata-learning-trust"] = verified
	if _, err := s.d.Organizations.Update(sahCtx, verified.ID, organizations.UpdateInput{
		PayoutEmail: strPtr("finance@sahayata.org"),
	}); err != nil {
		return fmt.Errorf("seed: set Sahayata payout email: %w", err)
	}

	// Vidya Kiran Foundation — deliberately left PENDING so the admin review
	// queue has real work in it.
	vikUser := s.users["contact@vidyakiran.org"]
	vikCtx := s.ctxAs(vikUser)
	vidya, err := s.createOrganization(vikCtx, vikUser, organizations.CreateInput{
		UserID:             vikUser.ID,
		Name:               "Vidya Kiran Foundation",
		Mission:            "Scholarships and mentoring for first-generation learners.",
		Description:        "Vidya Kiran Foundation is awaiting platform review. Its registration documents were submitted with the application.",
		Country:            "India",
		RegistrationNumber: "KA/2019/0044188",
		ContactEmail:       "contact@vidyakiran.org",
	})
	if err != nil {
		return err
	}
	s.orgs["vidya-kiran-foundation"] = vidya
	return nil
}

func (s *seeder) seedSecondaryProject(adminCtx context.Context) error {
	org := s.orgs["sahayata-learning-trust"]
	if org == nil {
		return nil
	}
	owner := s.users["programs@sahayata.org"]
	orgCtx := s.ctxAs(owner)

	inputs := make([]milestones.Input, 0, len(sahayataMilestones))
	for _, m := range sahayataMilestones {
		inputs = append(inputs, m.input())
	}

	project, _, err := s.d.Projects.Create(orgCtx, projects.CreateInput{
		OrgID:                  org.ID,
		CreatedBy:              owner.ID,
		Title:                  "Digital Classrooms — Jharkhand",
		Category:               "Education",
		Summary:                "Provision 50 tablets across five government schools, train 30 teachers and publish a baseline-to-endline learning assessment for 1,200 students.",
		Problem:                "Government schools in the region have connectivity but almost no working classroom devices, so digital literacy lessons are taught from printed diagrams.",
		Approach:               "Provision devices with a serialised registry, train teachers on the digital curriculum, then assess students at baseline and endline and publish the dataset.",
		Location:               "Ranchi district, Jharkhand, India",
		Currency:               "INR",
		GoalAmount:             6_00_000_00,
		BeneficiaryCount:       1200,
		BeneficiaryDescription: "30 teachers and 1,200 students",
		OutcomeDescription:     "50 classroom tablets in use, 30 teachers trained and 1,200 students assessed.",
		Milestones:             inputs,
	})
	if err != nil {
		return fmt.Errorf("seed: create Sahayata project: %w", err)
	}
	published, err := s.d.Projects.Publish(orgCtx, project.ID)
	if err != nil {
		return fmt.Errorf("seed: publish Sahayata project: %w", err)
	}
	if _, _, err := s.fund(orgCtx, published, orgUserID(owner), sahayataDonations); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Funding
// ---------------------------------------------------------------------------

// fund captures a set of contributions through the real order/capture path. Each
// capture allocates the funding across the project's milestones in ordinal order,
// which is what makes the Proof Graph possible later.
func (s *seeder) fund(ctx context.Context, p *models.Project, orgOwnerUserID string, list []demoDonation) (int64, int, error) {
	var total int64
	seen := map[string]bool{}
	for _, dd := range list {
		donor, ok := s.users[dd.DonorEmail]
		if !ok {
			return 0, 0, fmt.Errorf("seed: unknown donor %s", dd.DonorEmail)
		}
		donCtx := s.ctxAs(donor)

		donation, err := s.d.Donations.Create(donCtx, donations.CreateInput{
			ProjectID:      p.ID,
			DonorID:        donor.ID,
			Amount:         dd.Amount,
			Currency:       p.Currency,
			Note:           dd.Note,
			Simulated:      s.d.Payments.Provider().Simulated(),
			OrgOwnerUserID: orgOwnerUserID,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("seed: create donation for %s: %w", dd.DonorEmail, err)
		}
		if _, _, err := s.d.Payments.CreateDonationOrder(donCtx, donation.ID,
			"http://localhost:3000/donor", "http://localhost:3000/projects"); err != nil {
			return 0, 0, fmt.Errorf("seed: create order for %s: %w", dd.DonorEmail, err)
		}
		if _, err := s.d.Payments.CaptureDonation(donCtx, donation.ID); err != nil {
			return 0, 0, fmt.Errorf("seed: capture donation for %s: %w", dd.DonorEmail, err)
		}
		total += dd.Amount
		seen[dd.DonorEmail] = true
	}
	return total, len(seen), nil
}

// ---------------------------------------------------------------------------
// Milestone workflows
// ---------------------------------------------------------------------------

func (s *seeder) seedAquaHopeMilestones(ctx context.Context, owner *models.User, p *models.Project, ms []models.Milestone) ([]Outcome, error) {
	if len(ms) < 4 {
		return nil, fmt.Errorf("seed: expected 4 milestones, got %d", len(ms))
	}
	actor := actorOf(owner)
	outcomes := make([]Outcome, 0, 4)

	// --- milestone 1: site survey ------------------------------------------
	if err := s.startMilestone(ctx, ms[0], actor, "Fieldwork scheduled with each gram panchayat."); err != nil {
		return nil, err
	}
	sub, err := s.d.Evidence.CreateSubmission(ctx, ms[0].ID, p.ID, owner.ID,
		"Site survey completion pack: signed report and one photograph per surveyed village.")
	if err != nil {
		return nil, fmt.Errorf("seed: create survey submission: %w", err)
	}
	if err := s.uploadAll(ctx, owner, p, ms[0], sub.ID, surveyAssets()); err != nil {
		return nil, err
	}
	outcome1, err := s.verifyAndRelease(ctx, owner, p, ms[0], actor, models.DecisionApprove, 5, 5)
	if err != nil {
		return nil, err
	}
	outcomes = append(outcomes, outcome1)

	// --- milestone 2: equipment purchase -----------------------------------
	if err := s.startMilestone(ctx, ms[1], actor, "Purchase order raised with PureFlow Systems."); err != nil {
		return nil, err
	}
	sub2, err := s.d.Evidence.CreateSubmission(ctx, ms[1].ID, p.ID, owner.ID,
		"Equipment purchase pack: tax invoice, supplier receipt and the purchased unit registry.")
	if err != nil {
		return nil, fmt.Errorf("seed: create equipment submission: %w", err)
	}
	if err := s.uploadAll(ctx, owner, p, ms[1], sub2.ID, equipmentAssets(100)); err != nil {
		return nil, err
	}
	outcome2, err := s.verifyAndRelease(ctx, owner, p, ms[1], actor, models.DecisionApprove, 100, 100)
	if err != nil {
		return nil, err
	}
	outcomes = append(outcomes, outcome2)

	// --- milestone 3: installation, the canonical 87 -> 100 story ----------
	if err := s.startMilestone(ctx, ms[2], actor, "Installation crews mobilised across the five villages."); err != nil {
		return nil, err
	}
	sub3a, err := s.d.Evidence.CreateSubmission(ctx, ms[2].ID, p.ID, owner.ID,
		"Installation batch 1: field photographs for the units commissioned so far, plus the installed registry as at 12 April.")
	if err != nil {
		return nil, fmt.Errorf("seed: create installation submission 1: %w", err)
	}
	if err := s.uploadAll(ctx, owner, p, ms[2], sub3a.ID, installationAssets1()); err != nil {
		return nil, err
	}
	outcome3a, err := s.verify(ctx, owner, p, ms[2], actor)
	if err != nil {
		return nil, err
	}
	if outcome3a.Decision != string(models.DecisionHold) {
		s.warnf("installation submission 1 expected HOLD, got %s", outcome3a.Decision)
	}
	outcomes = append(outcomes, outcome3a)

	sub3b, err := s.d.Evidence.CreateSubmission(ctx, ms[2].ID, p.ID, owner.ID,
		"Installation batch 2: the remaining 13 units commissioned, with the registry updated to the full 100 serial numbers.")
	if err != nil {
		return nil, fmt.Errorf("seed: create installation submission 2: %w", err)
	}
	if err := s.uploadAll(ctx, owner, p, ms[2], sub3b.ID, installationAssets2()); err != nil {
		return nil, err
	}
	outcome3b, err := s.verify(ctx, owner, p, ms[2], actor)
	if err != nil {
		return nil, err
	}
	if outcome3b.Decision != string(models.DecisionApprove) {
		s.warnf("installation submission 2 expected APPROVE, got %s", outcome3b.Decision)
	}
	released, err := s.release(ctx, ms[2], actor)
	if err != nil {
		return nil, err
	}
	outcome3b.Released = released.blocked == nil && released.record != nil
	if released.err != nil {
		s.warnf("installation release did not complete: %v", released.err)
	}
	outcomes = append(outcomes, outcome3b)

	// --- milestone 4: completion, staged but deliberately unverified -------
	if err := s.stageCompletionMilestone(ctx, owner, p, ms[3], actor); err != nil {
		return nil, err
	}
	outcomes = append(outcomes, Outcome{
		Ordinal:  ms[3].Ordinal,
		Title:    ms[3].Title,
		Decision: "AWAITING_VERIFICATION",
		Verified: 0,
		Required: ms[3].RequiredQuantity,
	})

	return outcomes, nil
}

// stageCompletionMilestone uploads the completion evidence and leaves the
// milestone in progress, so a live demonstration can submit it for verification
// and watch the agent approve or hold it in real time.
func (s *seeder) stageCompletionMilestone(ctx context.Context, owner *models.User, p *models.Project, m models.Milestone, actor models.Actor) error {
	if err := s.startMilestone(ctx, m, actor, "Final household survey under way."); err != nil {
		return err
	}
	sub, err := s.d.Evidence.CreateSubmission(ctx, m.ID, p.ID, owner.ID,
		"Completion pack staged: programme completion report and household beneficiary records, awaiting submission for verification.")
	if err != nil {
		return fmt.Errorf("seed: create completion submission: %w", err)
	}
	if err := s.uploadAll(ctx, owner, p, m, sub.ID, completionAssets()); err != nil {
		return err
	}
	return nil
}

func (s *seeder) startMilestone(ctx context.Context, m models.Milestone, actor models.Actor, note string) error {
	if m.Status != models.MilestonePending {
		return nil
	}
	if err := s.d.Milestones.SetStatus(ctx, m.ID, models.MilestoneInProgress, actor, map[string]any{
		"trigger": "demo_seed",
		"note":    note,
	}); err != nil && !errors.Is(err, milestones.ErrInvalidTransition) {
		return fmt.Errorf("seed: start milestone %d: %w", m.Ordinal, err)
	}
	return nil
}

func (s *seeder) uploadAll(ctx context.Context, owner *models.User, p *models.Project, m models.Milestone, submissionID string, assets []asset) error {
	for _, a := range assets {
		contentType := a.ContentType
		if contentType == "" {
			contentType = "text/plain"
		}
		if _, err := s.d.Evidence.Add(ctx, evidence.UploadInput{
			SubmissionID:   submissionID,
			MilestoneID:    m.ID,
			ProjectID:      p.ID,
			UploadedBy:     owner.ID,
			Kind:           a.Kind,
			RequirementKey: a.RequirementKey,
			Filename:       a.Filename,
			ContentType:    contentType,
			Description:    a.Description,
			Hints:          a.Hints,
			Data:           a.Data,
		}); err != nil {
			return fmt.Errorf("seed: upload %s for milestone %d: %w", a.Filename, m.Ordinal, err)
		}
	}
	return nil
}

type releaseOutcome struct {
	record  *models.MilestonePayment
	blocked error
	err     error
}

func (s *seeder) release(ctx context.Context, m models.Milestone, actor models.Actor) (releaseOutcome, error) {
	record, err := s.d.Payments.ReleaseMilestone(ctx, m.ID, actor)
	if err == nil {
		return releaseOutcome{record: record}, nil
	}
	if errors.Is(err, payments.ErrReleaseBlocked) {
		return releaseOutcome{record: record, blocked: err}, nil
	}
	return releaseOutcome{record: record, err: err}, nil
}

// verify runs the pipeline and records the decision without releasing funding.
func (s *seeder) verify(ctx context.Context, owner *models.User, p *models.Project, m models.Milestone, actor models.Actor) (Outcome, error) {
	res, err := s.d.Verification.VerifyMilestone(ctx, m.ID, actor)
	if err != nil {
		return Outcome{}, fmt.Errorf("seed: verify milestone %d: %w", m.Ordinal, err)
	}
	if res.Verification.Status != res.Verification.Recommendation {
		s.warnf("milestone %d verification status %s differs from recommendation %s",
			m.Ordinal, res.Verification.Status, res.Verification.Recommendation)
	}
	return Outcome{
		Ordinal:  m.Ordinal,
		Title:    m.Title,
		Decision: string(res.Verification.Status),
		Verified: res.Verification.VerifiedQuantity,
		Required: res.Verification.RequiredQuantity,
	}, nil
}

// verifyAndRelease verifies a milestone, asserts the expected decision and then
// performs the conditional release.
func (s *seeder) verifyAndRelease(ctx context.Context, owner *models.User, p *models.Project, m models.Milestone, actor models.Actor, want models.VerificationDecision, wantVerified, wantRequired int) (Outcome, error) {
	outcome, err := s.verify(ctx, owner, p, m, actor)
	if err != nil {
		return Outcome{}, err
	}
	if outcome.Decision != string(want) {
		s.warnf("milestone %d expected %s, got %s", m.Ordinal, want, outcome.Decision)
	}
	if outcome.Verified != wantVerified || outcome.Required != wantRequired {
		s.warnf("milestone %d expected %d/%d verified, got %d/%d",
			m.Ordinal, wantVerified, wantRequired, outcome.Verified, outcome.Required)
	}

	released, err := s.release(ctx, m, actor)
	if err != nil {
		return Outcome{}, err
	}
	if released.err != nil {
		s.warnf("milestone %d release failed: %v", m.Ordinal, released.err)
	}
	if released.blocked != nil {
		s.warnf("milestone %d release was blocked by policy: %v", m.Ordinal, released.blocked)
	}
	if released.record != nil {
		outcome.Released = released.record.Status == models.MsPaymentExecuted || released.record.Status == models.MsPaymentSimulated
		outcome.ReleasedMinor = released.record.Amount
	}
	return outcome, nil
}

func (s *seeder) seedProjectUpdates(ctx context.Context, owner *models.User, p *models.Project) error {
	updates := []struct{ Title, Body string }{
		{
			Title: "Site survey complete in all five villages",
			Body:  "Survey teams completed the site survey across Kandhwad, Bhilwadi, Nandurbar, Shirala and Koregaon. Household allocations have been agreed with each gram panchayat, and the survey report together with one photograph per village has been submitted for verification.",
		},
		{
			Title: "113 installation photographs submitted — verification held",
			Body:  "The first installation batch was submitted with 113 field photographs. The verification agent confirmed 87 of the required 100 serial numbers and held the milestone funding pending evidence for the remaining units. The outstanding units have been commissioned and the updated registry resubmitted.",
		},
		{
			Title: "Installation milestone verified and funded",
			Body:  "All 100 units are now verified against serialised installation photographs and a complete unit registry. The installation milestone funding has been released to the organization through PayPal.",
		},
	}
	for _, u := range updates {
		if _, err := s.d.Projects.AddUpdate(ctx, p.ID, owner.ID, u.Title, u.Body); err != nil {
			return fmt.Errorf("seed: add project update: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *seeder) ctxAs(u *models.User) context.Context {
	return models.WithActor(s.ctx, actorOf(u))
}

func actorOf(u *models.User) models.Actor {
	return models.Actor{ID: u.ID, Role: u.Role, Label: u.FullName + " <" + u.Email + ">"}
}

func orgUserID(u *models.User) string { return u.ID }

func strPtr(v string) *string { return &v }

func (s *seeder) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.warn = append(s.warn, msg)
	if s.d.Logger != nil {
		s.d.Logger.Error("demo seed expectation not met", "detail", msg)
	}
}

func credentials() []Credential {
	out := make([]Credential, 0, len(demoUsers))
	for _, u := range demoUsers {
		out = append(out, Credential{
			Role:     string(u.Role),
			Email:    u.Email,
			Password: u.Password,
			Name:     u.Name,
		})
	}
	return out
}
