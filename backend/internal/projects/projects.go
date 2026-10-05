// Package projects owns impact project definitions, the discovery listing and
// the funding counters attached to a project.
//
// The three funding counters are deliberately separate:
//
//	funded_amount   - every captured rupee, immediately after a successful PayPal capture
//	reserved_amount - funding earmarked for milestones that are verified but not yet released
//	released_amount - funding that has actually been paid out after verification
//
// Keeping them apart is what lets the UI say honestly "₹10L raised, ₹7L verified
// and released, ₹3L still conditional".
package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/models"
)

// Sentinel errors.
var (
	ErrNotFound       = errors.New("project not found")
	ErrTitleRequired  = errors.New("project title is required")
	ErrGoalRequired   = errors.New("funding goal must be greater than zero")
	ErrOutcomeRequired = errors.New("outcome description is required")
	ErrNoMilestones   = errors.New("a project needs at least one milestone")
	ErrBudgetExceedsGoal = errors.New("milestone budgets exceed the funding goal")
	ErrSlugTaken      = errors.New("a project with that name already exists")
	ErrNotOwner       = errors.New("you do not have access to this project")
)

// Service provides project operations.
type Service struct {
	db         *db.DB
	audit      *audit.Service
	milestones *milestones.Service
}

// New creates the projects service.
func New(database *db.DB, auditSvc *audit.Service, ms *milestones.Service) *Service {
	return &Service{db: database, audit: auditSvc, milestones: ms}
}

// CreateInput is the project wizard payload.
type CreateInput struct {
	OrgID                  string
	CreatedBy              string
	Title                  string
	Category               string
	Summary                string
	Problem                string
	Approach               string
	Location               string
	Currency               string
	GoalAmount             int64
	BeneficiaryCount       int
	BeneficiaryDescription string
	OutcomeDescription     string
	Milestones             []milestones.Input
	Publish                bool
}

// Validate checks the wizard payload before anything is written.
func (in *CreateInput) Validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return ErrTitleRequired
	}
	if in.GoalAmount <= 0 {
		return ErrGoalRequired
	}
	if strings.TrimSpace(in.OutcomeDescription) == "" {
		return ErrOutcomeRequired
	}
	if len(in.Milestones) == 0 {
		return ErrNoMilestones
	}
	var total int64
	for i := range in.Milestones {
		if err := in.Milestones[i].Validate(); err != nil {
			return fmt.Errorf("milestone %d: %w", i+1, err)
		}
		total += in.Milestones[i].BudgetAmount
	}
	if total > in.GoalAmount {
		return fmt.Errorf("%w: milestones total %s against a goal of %s",
			ErrBudgetExceedsGoal,
			core.FormatMoney(core.Money(total), in.Currency),
			core.FormatMoney(core.Money(in.GoalAmount), in.Currency))
	}
	if in.Currency == "" {
		in.Currency = "INR"
	}
	if in.Category == "" {
		in.Category = "General"
	}
	return nil
}

// Create writes a project and its milestone definition atomically.
func (s *Service) Create(ctx context.Context, in CreateInput) (*models.Project, []models.Milestone, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}

	p := &models.Project{
		ID:                     core.NewID(),
		OrgID:                  in.OrgID,
		Title:                  in.Title,
		Slug:                   core.Slugify(in.Title),
		Category:               in.Category,
		Summary:                strings.TrimSpace(in.Summary),
		Problem:                strings.TrimSpace(in.Problem),
		Approach:               strings.TrimSpace(in.Approach),
		Location:               strings.TrimSpace(in.Location),
		Currency:               in.Currency,
		GoalAmount:             in.GoalAmount,
		BeneficiaryCount:       in.BeneficiaryCount,
		BeneficiaryDescription: strings.TrimSpace(in.BeneficiaryDescription),
		OutcomeDescription:     strings.TrimSpace(in.OutcomeDescription),
		Status:                 models.ProjectDraft,
		CoverSeed:              core.RandomHex(4),
		CreatedBy:              in.CreatedBy,
		CreatedAt:              core.Now(),
		UpdatedAt:              core.Now(),
	}
	if p.Slug == "" {
		p.Slug = "project-" + p.ID[:8]
	}

	var created []models.Milestone
	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		slug, err := uniqueSlug(ctx, tx, p.Slug, "")
		if err != nil {
			return err
		}
		p.Slug = slug

		_, err = tx.ExecContext(ctx, `
			INSERT INTO projects (
				id, org_id, title, slug, category, summary, problem, approach, location,
				currency, goal_amount, funded_amount, reserved_amount, released_amount,
				beneficiary_count, beneficiary_description, outcome_description, status,
				cover_seed, created_by, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.OrgID, p.Title, p.Slug, p.Category, p.Summary, p.Problem, p.Approach,
			p.Location, p.Currency, p.GoalAmount, p.BeneficiaryCount,
			p.BeneficiaryDescription, p.OutcomeDescription, string(p.Status),
			p.CoverSeed, p.CreatedBy, core.FormatTime(p.CreatedAt), core.FormatTime(p.UpdatedAt))
		if err != nil {
			return err
		}

		created, err = s.milestones.CreateMany(ctx, tx, p.ID, in.Milestones)
		if err != nil {
			return err
		}

		if _, err := s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditProjectCreated,
			EntityType: "project",
			EntityID:   p.ID,
			ProjectID:  p.ID,
			Summary:    fmt.Sprintf("Project '%s' created with %d milestones", p.Title, len(created)),
			Data: map[string]any{
				"goal_minor":    p.GoalAmount,
				"currency":      p.Currency,
				"milestones":    len(created),
				"beneficiaries": p.BeneficiaryCount,
			},
		}); err != nil {
			return err
		}

		if in.Publish {
			return s.publishTx(ctx, tx, p)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	if in.Publish {
		p.Status = models.ProjectFundingOpen
	}
	return p, created, nil
}

// Publish moves a draft project to FUNDING_OPEN.
func (s *Service) Publish(ctx context.Context, projectID string) (*models.Project, error) {
	var p *models.Project
	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		loaded, err := s.ByIDTx(ctx, tx, projectID)
		if err != nil {
			return err
		}
		p = loaded
		if p.Status == models.ProjectFundingOpen || p.Status == models.ProjectInProgress {
			return nil
		}
		return s.publishTx(ctx, tx, p)
	})
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, projectID)
}

func (s *Service) publishTx(ctx context.Context, tx db.Executor, p *models.Project) error {
	now := core.Now()
	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = ?, published_at = ?, updated_at = ? WHERE id = ?`,
		string(models.ProjectFundingOpen), core.FormatTime(now), core.FormatTime(now), p.ID); err != nil {
		return fmt.Errorf("projects: publish: %w", err)
	}
	_, err := s.audit.Record(ctx, tx, audit.Event{
		Action:     models.AuditProjectPublished,
		EntityType: "project",
		EntityID:   p.ID,
		ProjectID:  p.ID,
		Summary:    fmt.Sprintf("Project '%s' published and open for funding", p.Title),
		Data:       map[string]any{"goal_minor": p.GoalAmount, "currency": p.Currency},
	})
	return err
}

// SetStatus forces a project status (admin moderation).
func (s *Service) SetStatus(ctx context.Context, projectID string, status models.ProjectStatus, reason string) error {
	action := models.AuditProjectUpdated
	severity := "info"
	if status == models.ProjectSuspended {
		action = models.AuditProjectSuspended
		severity = "critical"
	}
	return s.db.RunInTx(ctx, func(tx *db.Tx) error {
		p, err := s.ByIDTx(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = ?, updated_at = ? WHERE id = ?`,
			string(status), core.NowStr(), projectID); err != nil {
			return err
		}
		_, err = s.audit.Record(ctx, tx, audit.Event{
			Action:     action,
			EntityType: "project",
			EntityID:   projectID,
			ProjectID:  projectID,
			Severity:   severity,
			Summary:    fmt.Sprintf("Project '%s' status set to %s: %s", p.Title, status, reason),
			Data:       map[string]any{"from": string(p.Status), "to": string(status), "reason": reason},
		})
		return err
	})
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// ByID loads a project.
func (s *Service) ByID(ctx context.Context, id string) (*models.Project, error) {
	return s.scan(s.db.QueryRowContext(ctx, projectSelect+" WHERE id = ?", id))
}

// ByIDTx loads a project inside a transaction.
func (s *Service) ByIDTx(ctx context.Context, ex db.Executor, id string) (*models.Project, error) {
	return s.scan(ex.QueryRowContext(ctx, projectSelect+" WHERE id = ?", id))
}

// BySlug loads a project by URL slug.
func (s *Service) BySlug(ctx context.Context, slug string) (*models.Project, error) {
	return s.scan(s.db.QueryRowContext(ctx, projectSelect+" WHERE slug = ?", slug))
}

// Filter narrows project discovery.
type Filter struct {
	Query    string
	Category string
	Status   models.ProjectStatus
	OrgID    string
	Sort     string // recent | funded | progress | goal
	Limit    int
	Offset   int
}

// List returns projects for the marketplace. Draft projects are excluded unless
// an OrgID filter is supplied, so un-published work never leaks into discovery.
func (s *Service) List(ctx context.Context, f Filter) ([]models.Project, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := []string{}
	args := []any{}

	if f.OrgID != "" {
		where = append(where, "org_id = ?")
		args = append(args, f.OrgID)
	} else {
		where = append(where, "status <> ?")
		args = append(args, string(models.ProjectDraft))
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Category != "" && !strings.EqualFold(f.Category, "all") {
		where = append(where, "LOWER(category) = ?")
		args = append(args, strings.ToLower(f.Category))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		where = append(where, "(LOWER(title) LIKE ? OR LOWER(summary) LIKE ? OR LOWER(problem) LIKE ? OR LOWER(location) LIKE ? OR LOWER(category) LIKE ?)")
		args = append(args, like, like, like, like, like)
	}

	order := "published_at DESC, created_at DESC"
	switch f.Sort {
	case "funded":
		order = "funded_amount DESC"
	case "goal":
		order = "goal_amount DESC"
	case "progress":
		order = "funded_amount * 1.0 / (CASE WHEN goal_amount = 0 THEN 1 ELSE goal_amount END) DESC"
	case "recent":
		order = "created_at DESC"
	}

	q := projectSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY " + order + " LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("projects: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]models.Project, 0, limit)
	for rows.Next() {
		p, err := s.scanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Count returns the number of projects matching the same filters.
func (s *Service) Count(ctx context.Context, f Filter) (int, error) {
	where := []string{"status <> ?"}
	args := []any{string(models.ProjectDraft)}
	if f.OrgID != "" {
		where = []string{"org_id = ?"}
		args = []any{f.OrgID}
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Category != "" && !strings.EqualFold(f.Category, "all") {
		where = append(where, "LOWER(category) = ?")
		args = append(args, strings.ToLower(f.Category))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		where = append(where, "(LOWER(title) LIKE ? OR LOWER(summary) LIKE ? OR LOWER(problem) LIKE ? OR LOWER(location) LIKE ? OR LOWER(category) LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM projects WHERE "+strings.Join(where, " AND "), args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("projects: count: %w", err)
	}
	return n, nil
}

// Categories returns the distinct non-empty categories currently in use.
func (s *Service) Categories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT category FROM projects
		WHERE status <> ? AND category <> ''
		GROUP BY category ORDER BY COUNT(*) DESC, category ASC`,
		string(models.ProjectDraft))
	if err != nil {
		return nil, fmt.Errorf("projects: categories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Funding state mutations (called inside transactions owned by donations/payments)
// ---------------------------------------------------------------------------

// AddFunds increases the captured funding counter.
func (s *Service) AddFunds(ctx context.Context, ex db.Executor, projectID string, amount int64) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE projects SET funded_amount = funded_amount + ?, updated_at = ? WHERE id = ?`,
		amount, core.NowStr(), projectID)
	if err != nil {
		return fmt.Errorf("projects: add funds: %w", err)
	}
	return nil
}

// Reserve moves funding from available into reserved for a verified milestone.
func (s *Service) Reserve(ctx context.Context, ex db.Executor, projectID string, amount int64) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE projects SET reserved_amount = reserved_amount + ?, updated_at = ? WHERE id = ?`,
		amount, core.NowStr(), projectID)
	if err != nil {
		return fmt.Errorf("projects: reserve: %w", err)
	}
	return nil
}

// Release moves funding from reserved into released, after the payment action
// succeeded. Only the amount that was actually reserved can be released.
func (s *Service) Release(ctx context.Context, ex db.Executor, projectID string, amount int64) error {
	_, err := ex.ExecContext(ctx, `
		UPDATE projects
		SET reserved_amount = CASE WHEN reserved_amount >= ? THEN reserved_amount - ? ELSE 0 END,
		    released_amount = released_amount + ?,
		    updated_at = ?
		WHERE id = ?`,
		amount, amount, amount, core.NowStr(), projectID)
	if err != nil {
		return fmt.Errorf("projects: release: %w", err)
	}
	return nil
}

// Unreserve returns reserved funding to available (used when a release is
// blocked or a refund is issued).
func (s *Service) Unreserve(ctx context.Context, ex db.Executor, projectID string, amount int64) error {
	_, err := ex.ExecContext(ctx, `
		UPDATE projects
		SET reserved_amount = CASE WHEN reserved_amount >= ? THEN reserved_amount - ? ELSE 0 END,
		    updated_at = ?
		WHERE id = ?`,
		amount, amount, core.NowStr(), projectID)
	if err != nil {
		return fmt.Errorf("projects: unreserve: %w", err)
	}
	return nil
}

// MarkCompletedIfDone flips a project to COMPLETED once every milestone is done.
func (s *Service) MarkCompletedIfDone(ctx context.Context, ex db.Executor, projectID string) (bool, error) {
	ms, err := s.milestones.ListByProjectTx(ctx, ex, projectID)
	if err != nil {
		return false, err
	}
	if len(ms) == 0 {
		return false, nil
	}
	for _, m := range ms {
		if m.Status != models.MilestoneCompleted && m.Status != models.MilestonePaymentReleased {
			return false, nil
		}
	}
	now := core.Now()
	if _, err := ex.ExecContext(ctx,
		`UPDATE projects SET status = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status <> ?`,
		string(models.ProjectCompleted), core.FormatTime(now), core.FormatTime(now),
		projectID, string(models.ProjectCompleted)); err != nil {
		return false, err
	}
	return true, nil
}

// MarkInProgress flips a funded project to IN_PROGRESS once work begins.
func (s *Service) MarkInProgress(ctx context.Context, ex db.Executor, projectID string) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE projects SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(models.ProjectInProgress), core.NowStr(), projectID, string(models.ProjectFundingOpen))
	return err
}

// ---------------------------------------------------------------------------
// Project updates
// ---------------------------------------------------------------------------

// AddUpdate posts a project news item.
func (s *Service) AddUpdate(ctx context.Context, projectID, authorID, title, body string) (*models.ProjectUpdate, error) {
	u := &models.ProjectUpdate{
		ID:        core.NewID(),
		ProjectID: projectID,
		AuthorID:  authorID,
		Title:     strings.TrimSpace(title),
		Body:      strings.TrimSpace(body),
		CreatedAt: core.Now(),
	}
	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO project_updates (id, project_id, author_id, title, body, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			u.ID, u.ProjectID, u.AuthorID, u.Title, u.Body, core.FormatTime(u.CreatedAt)); err != nil {
			return err
		}
		_, err := s.audit.Record(ctx, tx, audit.Event{
			Action:     models.AuditProjectUpdatePosted,
			EntityType: "project",
			EntityID:   projectID,
			ProjectID:  projectID,
			Summary:    "Project update posted: " + u.Title,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ListUpdates returns a project's updates, newest first.
func (s *Service) ListUpdates(ctx context.Context, projectID string, limit int) ([]models.ProjectUpdate, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, author_id, title, body, created_at
		FROM project_updates WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("projects: list updates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []models.ProjectUpdate{}
	for rows.Next() {
		var (
			u         models.ProjectUpdate
			createdAt string
		)
		if err := rows.Scan(&u.ID, &u.ProjectID, &u.AuthorID, &u.Title, &u.Body, &createdAt); err != nil {
			return nil, err
		}
		u.CreatedAt = core.MustParseTime(createdAt)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

const projectSelect = `SELECT id, org_id, title, slug, category, summary, problem, approach,
	location, currency, goal_amount, funded_amount, reserved_amount, released_amount,
	beneficiary_count, beneficiary_description, outcome_description, status,
	cover_seed, created_by, created_at, updated_at, published_at, completed_at
	FROM projects`

type rowScanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row rowScanner) (*models.Project, error) {
	p, err := s.scanRows(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func (s *Service) scanRows(row rowScanner) (*models.Project, error) {
	var (
		p                              models.Project
		status                         string
		createdAt, updatedAt           string
		publishedAt, completedAt       sql.NullString
	)
	if err := row.Scan(
		&p.ID, &p.OrgID, &p.Title, &p.Slug, &p.Category, &p.Summary, &p.Problem,
		&p.Approach, &p.Location, &p.Currency, &p.GoalAmount, &p.FundedAmount,
		&p.ReservedAmount, &p.ReleasedAmount, &p.BeneficiaryCount,
		&p.BeneficiaryDescription, &p.OutcomeDescription, &status, &p.CoverSeed,
		&p.CreatedBy, &createdAt, &updatedAt, &publishedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	p.Status = models.ProjectStatus(status)
	p.CreatedAt = core.MustParseTime(createdAt)
	p.UpdatedAt = core.MustParseTime(updatedAt)
	if publishedAt.Valid && publishedAt.String != "" {
		t := core.MustParseTime(publishedAt.String)
		p.PublishedAt = &t
	}
	if completedAt.Valid && completedAt.String != "" {
		t := core.MustParseTime(completedAt.String)
		p.CompletedAt = &t
	}
	return &p, nil
}

func uniqueSlug(ctx context.Context, ex db.Executor, base, excludeID string) (string, error) {
	slug := base
	for i := 1; i <= 50; i++ {
		var n int
		q := `SELECT COUNT(*) FROM projects WHERE slug = ?`
		args := []any{slug}
		if excludeID != "" {
			q += ` AND id <> ?`
			args = append(args, excludeID)
		}
		if err := ex.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i+1)
	}
	return base + "-" + core.RandomHex(3), nil
}
