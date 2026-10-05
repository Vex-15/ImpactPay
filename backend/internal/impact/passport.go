package impact

import (
	"context"
	"fmt"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// Passport is an organization's public track record.
//
// Every figure is a count or a ratio over stored rows. There is deliberately no
// "trust score": a single opaque number would let a platform hide the parts of a
// record that matter, and ImpactPay would rather publish the underlying counts —
// including the unresolved discrepancies — than summarise them away.
type Passport struct {
	Organization        PassportOrg            `json:"organization"`
	ProjectsTotal       int                    `json:"projects_total"`
	ProjectsCompleted   int                    `json:"projects_completed"`
	ProjectsActive      int                    `json:"projects_active"`
	FundsRaisedMinor    int64                  `json:"funds_raised_minor"`
	Currency            string                 `json:"currency"`
	MilestonesTotal     int                    `json:"milestones_total"`
	MilestonesCompleted int                    `json:"milestones_completed"`
	MilestonesVerified  int                    `json:"milestones_verified"`
	MilestonesHeld      int                    `json:"milestones_held"`
	EvidenceCompleteness float64               `json:"evidence_completeness"`
	EvidenceRequired    int                    `json:"evidence_required"`
	EvidenceSupplied    int                    `json:"evidence_supplied"`
	DuplicateArtefacts  int                    `json:"duplicate_artefacts"`
	ContributionsCount  int                    `json:"contributions_count"`
	SupporterCount      int                    `json:"supporter_count"`
	UnresolvedFindings  int                    `json:"unresolved_findings"`
	VerificationMix     map[string]int         `json:"verification_mix"`
	Projects            []PassportProject      `json:"projects"`
	AverageConfidence   float64                `json:"average_confidence"`
}

// PassportOrg is the organization header.
type PassportOrg struct {
	ID                 string                             `json:"id"`
	Name               string                             `json:"name"`
	Slug               string                             `json:"slug"`
	Mission            string                             `json:"mission"`
	Description        string                             `json:"description"`
	Country            string                             `json:"country"`
	Website            string                             `json:"website"`
	VerificationStatus models.OrganizationVerificationStatus `json:"verification_status"`
	Verified           bool                               `json:"verified"`
	ReviewNote         string                             `json:"review_note"`
	MemberSince        string                             `json:"member_since"`
}

// PassportProject is one project row on the passport.
type PassportProject struct {
	ID                string        `json:"id"`
	Title             string        `json:"title"`
	Slug              string        `json:"slug"`
	Category          string        `json:"category"`
	Status            models.ProjectStatus `json:"status"`
	GoalAmount        int64         `json:"goal_amount"`
	FundedAmount      int64         `json:"funded_amount"`
	ReleasedAmount    int64         `json:"released_amount"`
	BeneficiaryCount  int           `json:"beneficiary_count"`
	MilestonesTotal   int           `json:"milestones_total"`
	MilestonesVerified int          `json:"milestones_verified"`
	EvidenceCompleteness float64    `json:"evidence_completeness"`
	UnresolvedFindings int          `json:"unresolved_findings"`
	Currency          string        `json:"currency"`
}

// BuildPassport assembles an organization's passport.
func (s *Service) BuildPassport(ctx context.Context, orgID string) (*Passport, error) {
	var (
		org        PassportOrg
		status     string
		verified   int
		createdAt  string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, mission, description, country, website,
		       verification_status, verified, review_note, created_at
		FROM organizations WHERE id = ?`, orgID).
		Scan(&org.ID, &org.Name, &org.Slug, &org.Mission, &org.Description, &org.Country,
			&org.Website, &status, &verified, &org.ReviewNote, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("impact: passport organization: %w", err)
	}
	org.VerificationStatus = models.OrganizationVerificationStatus(status)
	org.Verified = verified == 1
	if t := core.MustParseTime(createdAt); !t.IsZero() {
		org.MemberSince = t.Format("January 2006")
	}

	p := &Passport{
		Organization:    org,
		VerificationMix: map[string]int{},
		Projects:        []PassportProject{},
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, slug, category, status, goal_amount, funded_amount,
		       released_amount, beneficiary_count, currency
		FROM projects WHERE org_id = ?
		ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("impact: passport projects: %w", err)
	}
	type projRow struct {
		card PassportProject
	}
	projas := []projRow{}
	for rows.Next() {
		var card PassportProject
		var st string
		if err := rows.Scan(&card.ID, &card.Title, &card.Slug, &card.Category, &st,
			&card.GoalAmount, &card.FundedAmount, &card.ReleasedAmount,
			&card.BeneficiaryCount, &card.Currency); err != nil {
			_ = rows.Close()
			return nil, err
		}
		card.Status = models.ProjectStatus(st)
		projas = append(projas, projRow{card: card})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	totalCompleteness := 0.0
	for _, pr := range projas {
		card := pr.card

		msRows, err := s.db.QueryContext(ctx, `
			SELECT status, verified_quantity, required_quantity
			FROM milestones WHERE project_id = ?`, card.ID)
		if err != nil {
			return nil, err
		}
		milestoneIDs := []string{}
		idRows, err := s.db.QueryContext(ctx, `SELECT id FROM milestones WHERE project_id = ?`, card.ID)
		if err != nil {
			_ = msRows.Close()
			return nil, err
		}
		for idRows.Next() {
			var id string
			if err := idRows.Scan(&id); err != nil {
				_ = idRows.Close()
				_ = msRows.Close()
				return nil, err
			}
			milestoneIDs = append(milestoneIDs, id)
		}
		_ = idRows.Close()

		idx := 0
		for msRows.Next() {
			var (
				st         string
				verifiedQ  int
				requiredQ  int
			)
			if err := msRows.Scan(&st, &verifiedQ, &requiredQ); err != nil {
				_ = msRows.Close()
				return nil, err
			}
			card.MilestonesTotal++
			p.MilestonesTotal++

			fullyVerified := requiredQ <= 0 &&
				(st == string(models.MilestoneApproved) || st == string(models.MilestoneCompleted) || st == string(models.MilestonePaymentReleased)) ||
				(requiredQ > 0 && verifiedQ >= requiredQ)
			if fullyVerified {
				card.MilestonesVerified++
				p.MilestonesVerified++
			}
			switch models.MilestoneStatus(st) {
			case models.MilestoneCompleted, models.MilestonePaymentReleased:
				p.MilestonesCompleted++
			case models.MilestoneHeld, models.MilestoneMoreEvidenceRequested:
				p.MilestonesHeld++
			}
			idx++
		}
		_ = msRows.Close()

		stats, err := s.evidenceStatsForProject(ctx, card.ID)
		if err != nil {
			return nil, err
		}
		completeness := 1.0
		if len(stats) > 0 {
			sum := 0.0
			for _, id := range milestoneIDs {
				st, ok := stats[id]
				if !ok {
					continue
				}
				sum += st.Completeness()
				p.EvidenceRequired += st.RequiredArtefacts
				p.EvidenceSupplied += st.DistinctArtefacts
				p.DuplicateArtefacts += st.Duplicates
			}
			completeness = sum / float64(len(milestoneIDs))
		}
		card.EvidenceCompleteness = completeness
		totalCompleteness += completeness

		findings, err := s.countInconsistenciesForProject(ctx, card.ID)
		if err != nil {
			return nil, err
		}
		card.UnresolvedFindings = findings
		p.UnresolvedFindings += findings

		// Verification decision mix, for transparency about held milestones.
		vRows, err := s.db.QueryContext(ctx, `
			SELECT recommendation FROM verifications WHERE project_id = ?`, card.ID)
		if err != nil {
			return nil, err
		}
		for vRows.Next() {
			var rec string
			if err := vRows.Scan(&rec); err != nil {
				_ = vRows.Close()
				return nil, err
			}
			p.VerificationMix[rec]++
		}
		_ = vRows.Close()

		p.FundsRaisedMinor += card.FundedAmount
		if p.Currency == "" {
			p.Currency = card.Currency
		}
		switch card.Status {
		case models.ProjectCompleted:
			p.ProjectsCompleted++
		case models.ProjectArchived:
		default:
			p.ProjectsActive++
		}
		p.ProjectsTotal++
		p.Projects = append(p.Projects, card)
	}

	if p.ProjectsTotal > 0 {
		p.EvidenceCompleteness = totalCompleteness / float64(p.ProjectsTotal)
	} else {
		p.EvidenceCompleteness = 0
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT d.donor_id)
		FROM donations d JOIN projects pr ON pr.id = d.project_id
		WHERE pr.org_id = ? AND d.status = ?`,
		orgID, string(models.DonationCaptured)).Scan(&p.ContributionsCount, &p.SupporterCount); err != nil {
		return nil, fmt.Errorf("impact: passport contributions: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(AVG(v.confidence), 0)
		FROM verifications v JOIN projects pr ON pr.id = v.project_id
		WHERE pr.org_id = ?`, orgID).Scan(&p.AverageConfidence); err != nil {
		return nil, fmt.Errorf("impact: passport confidence: %w", err)
	}

	return p, nil
}
