// Package evidence owns the intake of supporting artefacts for a milestone.
//
// A milestone can receive several submissions. The first may be held for
// insufficient evidence; a later submission supplies the remainder. The
// verification pipeline always evaluates the *cumulative* evidence for a
// milestone, so a second submission that completes the picture is what flips a
// HOLD into an APPROVE.
package evidence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/extract"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/storage"
)

// Sentinel errors, surfaced to the API layer as 400/413/415 responses.
var (
	ErrNotFound          = errors.New("evidence not found")
	ErrSubmissionNotFound = errors.New("evidence submission not found")
	ErrKindRequired      = errors.New("evidence type is required")
	ErrInvalidKind       = errors.New("unsupported evidence type")
	ErrEmptyFile         = errors.New("the uploaded file is empty")
	ErrFileTooLarge      = errors.New("the uploaded file exceeds the size limit")
	ErrUnsupportedType   = errors.New("that file type is not accepted as evidence")
	ErrFilenameRequired  = errors.New("a filename is required")
)

// ValidKinds lists the accepted evidence types.
var ValidKinds = map[models.EvidenceKind]bool{
	models.EvidenceInvoice:           true,
	models.EvidenceReceipt:           true,
	models.EvidencePhotograph:        true,
	models.EvidenceReport:            true,
	models.EvidenceDocument:          true,
	models.EvidenceBeneficiaryRecord: true,
	models.EvidenceLocation:          true,
	models.EvidenceUnitRegistry:      true,
}

// Service provides evidence operations.
type Service struct {
	db       *db.DB
	audit    *audit.Service
	store    storage.Store
	maxBytes int64
}

// New creates the evidence service.
func New(database *db.DB, auditSvc *audit.Service, store storage.Store, maxBytes int64) *Service {
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	return &Service{db: database, audit: auditSvc, store: store, maxBytes: maxBytes}
}

// MaxUploadBytes exposes the configured limit for handlers and the UI.
func (s *Service) MaxUploadBytes() int64 { return s.maxBytes }

// Store exposes the underlying object store for file downloads.
func (s *Service) Store() storage.Store { return s.store }

// CreateSubmission opens a new review round for a milestone.
func (s *Service) CreateSubmission(ctx context.Context, milestoneID, projectID, submittedBy, note string) (*models.EvidenceSubmission, error) {
	sub := &models.EvidenceSubmission{
		ID:          core.NewID(),
		MilestoneID: milestoneID,
		ProjectID:   projectID,
		SubmittedBy: submittedBy,
		Note:        strings.TrimSpace(note),
		Status:      models.SubmissionSubmitted,
		CreatedAt:   core.Now(),
	}

	err := s.db.RunInTx(ctx, func(tx *db.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM evidence_submissions WHERE milestone_id = ?`, milestoneID).Scan(&n); err != nil {
			return err
		}
		sub.Sequence = n + 1

		_, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_submissions (
				id, milestone_id, project_id, submitted_by, sequence, note, status, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sub.ID, sub.MilestoneID, sub.ProjectID, sub.SubmittedBy, sub.Sequence,
			sub.Note, string(sub.Status), core.FormatTime(sub.CreatedAt))
		return err
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// UploadInput is one evidence artefact to store.
type UploadInput struct {
	SubmissionID   string
	MilestoneID    string
	ProjectID      string
	UploadedBy     string
	Kind           models.EvidenceKind
	RequirementKey string
	Filename       string
	ContentType    string
	Description    string
	Hints          models.EvidenceHints
	Data           []byte
}

// Add stores one artefact and records the evidence row.
func (s *Service) Add(ctx context.Context, in UploadInput) (*models.Evidence, error) {
	if in.Kind == "" {
		return nil, ErrKindRequired
	}
	if !ValidKinds[in.Kind] {
		return nil, fmt.Errorf("%w: %s", ErrInvalidKind, in.Kind)
	}
	if strings.TrimSpace(in.Filename) == "" {
		return nil, ErrFilenameRequired
	}
	if len(in.Data) == 0 {
		return nil, ErrEmptyFile
	}
	if int64(len(in.Data)) > s.maxBytes {
		return nil, fmt.Errorf("%w (limit %s)", ErrFileTooLarge, humanBytes(s.maxBytes))
	}
	if !storage.IsAllowedContentType(in.ContentType) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, in.ContentType)
	}

	facts := extract.Artefact(in.Kind, in.Filename, in.ContentType, in.Data, in.Hints)

	key := storage.EvidenceKey(in.ProjectID, in.MilestoneID, core.SHA256Hex(in.Data), in.Filename)
	obj, err := s.store.Put(ctx, key, strings.NewReader(string(in.Data)), in.ContentType)
	if err != nil {
		return nil, fmt.Errorf("evidence: store artefact: %w", err)
	}

	ev := &models.Evidence{
		ID:             core.NewID(),
		SubmissionID:   in.SubmissionID,
		MilestoneID:    in.MilestoneID,
		ProjectID:      in.ProjectID,
		UploadedBy:     in.UploadedBy,
		Kind:           in.Kind,
		RequirementKey: in.RequirementKey,
		Filename:       storage.SanitizeFilename(in.Filename),
		MimeType:       in.ContentType,
		SizeBytes:      obj.Size,
		StorageKey:     obj.Key,
		ContentHash:    obj.ContentHash,
		Description:    core.Truncate(strings.TrimSpace(in.Description), 1000),
		Declared:       in.Hints,
		Extracted:      facts,
		CreatedAt:      core.Now(),
	}

	err = s.db.RunInTx(ctx, func(tx *db.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO evidence (
				id, submission_id, milestone_id, project_id, uploaded_by, kind,
				requirement_key, filename, mime_type, size_bytes, storage_key,
				content_hash, description, declared, extracted, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID, ev.SubmissionID, ev.MilestoneID, ev.ProjectID, ev.UploadedBy,
			string(ev.Kind), ev.RequirementKey, ev.Filename, ev.MimeType, ev.SizeBytes,
			ev.StorageKey, ev.ContentHash, ev.Description,
			core.JSONString(ev.Declared), core.JSONString(ev.Extracted),
			core.FormatTime(ev.CreatedAt))
		if err != nil {
			return err
		}

		_, err = s.audit.Record(ctx, tx, audit.Event{
			Action:      models.AuditEvidenceUploaded,
			EntityType:  "evidence",
			EntityID:    ev.ID,
			ProjectID:   ev.ProjectID,
			MilestoneID: ev.MilestoneID,
			Summary: fmt.Sprintf("Uploaded %s '%s' (%s)", strings.ToLower(string(ev.Kind)),
				ev.Filename, humanBytes(ev.SizeBytes)),
			Data: map[string]any{
				"kind":           string(ev.Kind),
				"requirement":    ev.RequirementKey,
				"size_bytes":     ev.SizeBytes,
				"content_hash":   ev.ContentHash,
				"extracted_ids":  len(ev.Extracted.UnitIDs),
				"quality":        ev.Extracted.Quality,
			},
		})
		return err
	})
	if err != nil {
		// The DB write failed, so the stored object is orphaned; remove it so a
		// retry does not silently accumulate unreferenced files.
		_ = s.store.Delete(ctx, obj.Key)
		return nil, err
	}

	return ev, nil
}

// SubmissionStatus updates the review state of a submission.
func (s *Service) SetSubmissionStatus(ctx context.Context, ex db.Executor, submissionID string, status models.SubmissionStatus, verificationID string) error {
	if verificationID != "" {
		_, err := ex.ExecContext(ctx,
			`UPDATE evidence_submissions SET status = ?, verification_id = ? WHERE id = ?`,
			string(status), verificationID, submissionID)
		return err
	}
	_, err := ex.ExecContext(ctx,
		`UPDATE evidence_submissions SET status = ? WHERE id = ?`, string(status), submissionID)
	return err
}

// ByID loads an evidence row.
func (s *Service) ByID(ctx context.Context, id string) (*models.Evidence, error) {
	return s.scan(s.db.QueryRowContext(ctx, evidenceSelect+" WHERE id = ?", id))
}

// SubmissionByID loads a submission.
func (s *Service) SubmissionByID(ctx context.Context, id string) (*models.EvidenceSubmission, error) {
	var (
		sub       models.EvidenceSubmission
		status    string
		verifID   sql.NullString
		createdAt string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, milestone_id, project_id, submitted_by, sequence, note, status, verification_id, created_at
		FROM evidence_submissions WHERE id = ?`, id).
		Scan(&sub.ID, &sub.MilestoneID, &sub.ProjectID, &sub.SubmittedBy, &sub.Sequence,
			&sub.Note, &status, &verifID, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSubmissionNotFound
		}
		return nil, fmt.Errorf("evidence: load submission: %w", err)
	}
	sub.Status = models.SubmissionStatus(status)
	sub.VerificationID = verifID.String
	sub.CreatedAt = core.MustParseTime(createdAt)
	return &sub, nil
}

// ListByMilestone returns every artefact ever submitted for a milestone,
// oldest first. This is the cumulative set the verifier evaluates.
func (s *Service) ListByMilestone(ctx context.Context, milestoneID string) ([]models.Evidence, error) {
	rows, err := s.db.QueryContext(ctx,
		evidenceSelect+" WHERE milestone_id = ? ORDER BY created_at ASC, id ASC", milestoneID)
	if err != nil {
		return nil, fmt.Errorf("evidence: list by milestone: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// ListBySubmission returns the artefacts of one review round.
func (s *Service) ListBySubmission(ctx context.Context, submissionID string) ([]models.Evidence, error) {
	rows, err := s.db.QueryContext(ctx,
		evidenceSelect+" WHERE submission_id = ? ORDER BY created_at ASC, id ASC", submissionID)
	if err != nil {
		return nil, fmt.Errorf("evidence: list by submission: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

// ListSubmissions returns a milestone's review rounds in order.
func (s *Service) ListSubmissions(ctx context.Context, milestoneID string) ([]models.EvidenceSubmission, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, milestone_id, project_id, submitted_by, sequence, note, status, verification_id, created_at
		FROM evidence_submissions WHERE milestone_id = ? ORDER BY sequence ASC`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("evidence: list submissions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []models.EvidenceSubmission{}
	for rows.Next() {
		var (
			sub       models.EvidenceSubmission
			status    string
			verifID   sql.NullString
			createdAt string
		)
		if err := rows.Scan(&sub.ID, &sub.MilestoneID, &sub.ProjectID, &sub.SubmittedBy,
			&sub.Sequence, &sub.Note, &status, &verifID, &createdAt); err != nil {
			return nil, err
		}
		sub.Status = models.SubmissionStatus(status)
		sub.VerificationID = verifID.String
		sub.CreatedAt = core.MustParseTime(createdAt)
		out = append(out, sub)
	}
	return out, rows.Err()
}

// RequirementsCoverage summarises which declared requirements have artefacts.
type RequirementsCoverage struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
	MinCount int    `json:"min_count"`
	Found    int    `json:"found"`
	Satisfied bool  `json:"satisfied"`
}

// Coverage counts artefacts per declared requirement.
func Coverage(reqs []models.EvidenceRequirementSpec, items []models.Evidence) []RequirementsCoverage {
	out := make([]RequirementsCoverage, 0, len(reqs))
	for _, r := range reqs {
		c := RequirementsCoverage{
			Key: r.Key, Label: r.Label, Kind: r.Kind,
			Required: r.Required, MinCount: max(1, r.MinCount),
		}
		for _, e := range items {
			if strings.EqualFold(e.RequirementKey, r.Key) ||
				(e.RequirementKey == "" && strings.EqualFold(string(e.Kind), r.Kind)) {
				c.Found++
			}
		}
		c.Satisfied = c.Found >= c.MinCount
		out = append(out, c)
	}
	return out
}

const evidenceSelect = `SELECT id, submission_id, milestone_id, project_id, uploaded_by, kind,
	requirement_key, filename, mime_type, size_bytes, storage_key, content_hash,
	description, declared, extracted, created_at FROM evidence`

type rowScanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row rowScanner) (*models.Evidence, error) {
	e, err := s.scanRows(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}

func (s *Service) scanRows(row rowScanner) (*models.Evidence, error) {
	var (
		e                    models.Evidence
		kind                 string
		declared, extracted  string
		createdAt            string
	)
	if err := row.Scan(&e.ID, &e.SubmissionID, &e.MilestoneID, &e.ProjectID, &e.UploadedBy,
		&kind, &e.RequirementKey, &e.Filename, &e.MimeType, &e.SizeBytes, &e.StorageKey,
		&e.ContentHash, &e.Description, &declared, &extracted, &createdAt); err != nil {
		return nil, err
	}
	e.Kind = models.EvidenceKind(kind)
	core.DecodeJSON(declared, &e.Declared)
	core.DecodeJSON(extracted, &e.Extracted)
	e.CreatedAt = core.MustParseTime(createdAt)
	return &e, nil
}

func (s *Service) scanAll(rows *sql.Rows) ([]models.Evidence, error) {
	out := []models.Evidence{}
	for rows.Next() {
		e, err := s.scanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
