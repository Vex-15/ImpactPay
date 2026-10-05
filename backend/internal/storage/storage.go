// Package storage abstracts evidence file persistence.
//
// Evidence artefacts (invoices, receipts, installation photographs, beneficiary
// records) are content addressed: the object key is derived from the SHA-256 of
// the bytes. That makes de-duplication a lookup rather than a heuristic, and it
// makes the same file uploaded twice collapse to one stored object while still
// producing two evidence rows the verification pipeline can flag.
//
// The local filesystem driver is the default for development and DEMO_MODE. An
// S3-compatible driver is used when STORAGE_DRIVER=s3.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned when an object key is absent.
var ErrNotFound = errors.New("storage: object not found")

// Object describes a stored artefact.
type Object struct {
	Key         string
	Size        int64
	ContentHash string // hex sha256 of the bytes
	ContentType string
}

// Store persists opaque byte objects under string keys.
type Store interface {
	// Put streams r into an object at key. It returns the object metadata
	// including the content hash computed while streaming.
	Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error)
	// Get opens an object for reading. Returns ErrNotFound when absent.
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	// Delete removes an object. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// URL returns a client-reachable reference. For the local driver this is a
	// backend API path, not a filesystem path.
	URL(key string) string
	// Driver returns the driver name for diagnostics.
	Driver() string
}

// MaxBytesReader guards uploads so a single request cannot exhaust disk.
func MaxBytesReader(r io.Reader, limit int64) io.Reader { return io.LimitReader(r, limit) }

// SanitizeFilename reduces a client-supplied name to a safe basename.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "evidence"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_' || r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 120 {
		ext := filepath.Ext(out)
		out = out[:120-len(ext)] + ext
	}
	if out == "" {
		out = "evidence"
	}
	return out
}

// EvidenceKey builds a content-addressed object key for an evidence artefact.
// The hash prefix shards objects so no directory grows without bound.
func EvidenceKey(projectID, milestoneID, contentHash, filename string) string {
	if contentHash == "" {
		sum := sha256.Sum256([]byte(projectID + "/" + milestoneID + "/" + filename + "/" + filename))
		contentHash = hex.EncodeToString(sum[:])
	}
	shard := contentHash[:2]
	return path.Join("evidence", shard, contentHash+"-"+SanitizeFilename(filename))
}

// ExtForContentType maps a small set of types to an extension, used when a
// client sends no filename.
func ExtForContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "pdf"):
		return ".pdf"
	case strings.Contains(ct, "csv"):
		return ".csv"
	case strings.Contains(ct, "json"):
		return ".json"
	case strings.Contains(ct, "text"):
		return ".txt"
	default:
		return ".bin"
	}
}

// AllowedContentTypes is the allowlist for evidence uploads. Anything else is
// rejected by the API layer before a byte reaches storage.
var AllowedContentTypes = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/webp":               true,
	"image/heic":               true,
	"application/pdf":          true,
	"text/plain":               true,
	"text/csv":                 true,
	"application/json":         true,
	"application/octet-stream": true,
}

// IsAllowedContentType reports whether the MIME type may be stored.
func IsAllowedContentType(ct string) bool {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if ct == "" {
		return false
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return AllowedContentTypes[ct]
}

// ---------------------------------------------------------------------------
// Local filesystem driver
// ---------------------------------------------------------------------------

// Local stores objects beneath a root directory. It is the default driver and
// the one exercised by the demo.
type Local struct {
	root string
}

// NewLocal creates a filesystem-backed store rooted at dir.
func NewLocal(dir string) (*Local, error) {
	if dir == "" {
		return nil, errors.New("storage: local root directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &Local{root: abs}, nil
}

// Driver implements Store.
func (l *Local) Driver() string { return "local" }

// resolve maps a key to an absolute path, refusing traversal outside the root.
func (l *Local) resolve(key string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(key, "\\", "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	full := filepath.Join(l.root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("storage: key escapes root: %q", key)
	}
	return full, nil
}

// Put implements Store.
func (l *Local) Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error) {
	full, err := l.resolve(key)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return Object{}, fmt.Errorf("storage: mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return Object{}, fmt.Errorf("storage: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return Object{}, fmt.Errorf("storage: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return Object{}, fmt.Errorf("storage: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Object{}, fmt.Errorf("storage: close: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return Object{}, fmt.Errorf("storage: commit: %w", err)
	}

	return Object{
		Key:         key,
		Size:        n,
		ContentHash: hex.EncodeToString(h.Sum(nil)),
		ContentType: contentType,
	}, nil
}

// Get implements Store.
func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	full, err := l.resolve(key)
	if err != nil {
		return nil, Object{}, err
	}
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Object{}, ErrNotFound
		}
		return nil, Object{}, fmt.Errorf("storage: open: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Object{}, fmt.Errorf("storage: stat: %w", err)
	}
	return f, Object{Key: key, Size: info.Size()}, nil
}

// Delete implements Store.
func (l *Local) Delete(ctx context.Context, key string) error {
	full, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage: delete: %w", err)
	}
	return nil
}

// URL implements Store. Local objects are served through the backend.
func (l *Local) URL(key string) string { return "/api/evidence/file/" + key }

// Root exposes the resolved root (diagnostics and seeding).
func (l *Local) Root() string { return l.root }
