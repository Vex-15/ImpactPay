// Package db owns the database connection, the portable SQL dialect shim and the
// schema migrations.
//
// ImpactPay targets PostgreSQL in deployment. For local development and the
// deterministic DEMO_MODE it also runs on an embedded pure-Go SQLite file so the
// product can be exercised without external infrastructure. To make one schema
// and one set of queries serve both engines, the schema deliberately sticks to
// portable constructs:
//
//   - primary keys are application-generated TEXT UUIDs (no SERIAL/sequences)
//   - timestamps are TEXT RFC3339 values written by Go (no NOW()/CURRENT_TIMESTAMP)
//   - booleans are INTEGER 0/1
//   - money is INTEGER minor units (paise/cents) in int64
//   - JSON payloads are TEXT
//   - placeholders are written as "?" and rebound to $N for PostgreSQL
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver
	_ "modernc.org/sqlite"             // pure-Go sqlite driver (no cgo)
)

// Executor is satisfied by both *DB and *Tx. Services accept it so the same
// query code runs inside or outside a transaction without duplication.
//
// Callers must use the Rebind-aware Query*/Exec* methods below rather than the
// embedded sql.DB methods, since placeholders are dialect specific.
type Executor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Rebind(query string) string
}

type DB struct {
	*sql.DB
	driver string
}

// Open connects to PostgreSQL when a DATABASE_URL is supplied, otherwise to the
// embedded SQLite file at sqlitePath.
func Open(ctx context.Context, driver, databaseURL, sqlitePath string) (*DB, error) {
	var (
		sqlDB *sql.DB
		err   error
	)

	switch driver {
	case "postgres":
		sqlDB, err = sql.Open("pgx", databaseURL)
		if err != nil {
			return nil, fmt.Errorf("db: open postgres: %w", err)
		}
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(time.Hour)
	case "sqlite":
		if dir := filepath.Dir(sqlitePath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("db: create sqlite dir: %w", err)
			}
		}
		dsn := "file:" + filepath.ToSlash(sqlitePath) +
			"?_pragma=busy_timeout(5000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=foreign_keys(ON)" +
			"&_pragma=synchronous(NORMAL)"
		sqlDB, err = sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("db: open sqlite: %w", err)
		}
		// SQLite serialises writes; a single writer connection avoids
		// SQLITE_BUSY under concurrent requests.
		sqlDB.SetMaxOpenConns(1)
	default:
		return nil, fmt.Errorf("db: unsupported driver %q", driver)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return &DB{DB: sqlDB, driver: driver}, nil
}

// Driver returns the active dialect name.
func (d *DB) Driver() string { return d.driver }

// IsPostgres reports whether the PostgreSQL dialect is active.
func (d *DB) IsPostgres() bool { return d.driver == "postgres" }

// Rebind converts "?" placeholders into the numbered form PostgreSQL expects.
func (d *DB) Rebind(query string) string { return rebind(d.driver, query) }

func rebind(driver, query string) string {
	if driver != "postgres" {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inSingle := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '\'' {
			inSingle = !inSingle
			b.WriteByte(c)
			continue
		}
		if c == '?' && !inSingle {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// QueryContext runs a rebind-aware query.
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.Rebind(query), args...)
}

// QueryRowContext runs a rebind-aware single-row query.
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.Rebind(query), args...)
}

// ExecContext runs a rebind-aware statement.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, d.Rebind(query), args...)
}

// Tx is a transaction bound to the same dialect shim.
type Tx struct {
	*sql.Tx
	driver string
}

func (t *Tx) Rebind(query string) string { return rebind(t.driver, query) }

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.Rebind(query), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.Rebind(query), args...)
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.Rebind(query), args...)
}

// RunInTx executes fn inside a transaction, rolling back on error or panic.
// Financial state changes must go through this so that related rows, payment
// state and audit events commit atomically.
func (d *DB) RunInTx(ctx context.Context, fn func(tx *Tx) error) error {
	sqlTx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	tx := &Tx{Tx: sqlTx, driver: d.driver}

	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	committed = true
	return nil
}

// Exists reports whether a table is present in the current schema.
func (d *DB) Exists(ctx context.Context, table string) (bool, error) {
	q := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?"
	if d.IsPostgres() {
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_name=?"
	}
	var n int
	if err := d.QueryRowContext(ctx, q, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// SafeDatabaseLabel returns a redacted description of the connection for logs.
func (d *DB) SafeDatabaseLabel() string {
	if d.IsPostgres() {
		return "postgres"
	}
	return "sqlite"
}

// DSNFromParts builds a PostgreSQL URL from discrete pieces (helper for tests).
func DSNFromParts(host string, port int, user, pass, name string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, pass),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + name,
	}
	u.RawQuery = "sslmode=disable"
	return u.String()
}
