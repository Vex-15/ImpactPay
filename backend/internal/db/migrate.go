package db

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every embedded migration exactly once, recording applied
// versions in schema_migrations. Migrations are written in portable SQL so the
// same files run against PostgreSQL and SQLite.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("db: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		applied, err := d.migrationApplied(ctx, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		raw, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("db: read %s: %w", name, err)
		}

		if err := d.applyMigration(ctx, name, string(raw)); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) migrationApplied(ctx context.Context, version string) (bool, error) {
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
		return false, fmt.Errorf("db: check migration %s: %w", version, err)
	}
	return n > 0, nil
}

func (d *DB) applyMigration(ctx context.Context, version, raw string) error {
	return d.RunInTx(ctx, func(tx *Tx) error {
		for _, stmt := range splitStatements(raw) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("db: migration %s failed: %w\nstatement: %s", version, err, truncate(stmt, 400))
			}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, nowRFC3339())
		return err
	})
}

// splitStatements breaks a migration file into individual statements on ";"
// while respecting single-quoted string literals.
func splitStatements(raw string) []string {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
	)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		cur.WriteByte(c)
		if c == '\'' {
			// Handle escaped '' inside a literal.
			if inQuote && i+1 < len(raw) && raw[i+1] == '\'' {
				cur.WriteByte(raw[i+1])
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if c == ';' && !inQuote {
			stmt := strings.TrimSpace(cur.String())
			stmt = strings.TrimSuffix(stmt, ";")
			stmt = strings.TrimSpace(stmt)
			if stmt != "" && !isCommentOnly(stmt) {
				out = append(out, stmt)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" && !isCommentOnly(s) {
		out = append(out, s)
	}
	return out
}

func isCommentOnly(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
