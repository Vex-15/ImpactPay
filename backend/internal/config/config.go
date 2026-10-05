// Package config loads and validates runtime configuration from environment
// variables. No secret gets a hardcoded default: when a credential is absent the
// corresponding integration degrades to an explicitly-labelled simulation rather
// than silently pretending to be live.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env       string
	Port      string
	PublicURL string

	// Database
	DatabaseURL   string
	SQLitePath    string
	DBDriver      string // "sqlite" | "postgres"
	AutoMigrate   bool
	SeedDemo      bool

	// Auth
	SessionTTL   time.Duration
	SessionCookie string
	CookieSecure bool
	AllowedOrigins []string

	// Storage
	StorageDriver string // "local" | "s3"
	StorageLocalDir string
	MaxUploadBytes int64

	// PayPal
	PayPalClientID     string
	PayPalClientSecret string
	PayPalBaseURL      string
	PayPalWebhookID    string
	PayPalCurrency     string

	// AI
	AIProvider  string // "deterministic" | "openai" | "anthropic"
	AIAPIKey    string
	AIModel     string
	AIBaseURL   string
	AITimeout   time.Duration
}

// Load reads configuration from the process environment.
func Load() (*Config, error) {
	c := &Config{
		Env:       env("APP_ENV", "development"),
		Port:      env("PORT", "8080"),
		PublicURL: env("PUBLIC_URL", "http://localhost:8080"),

		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		SQLitePath:  env("SQLITE_PATH", filepath.Join("data", "impactpay.db")),
		AutoMigrate: envBool("AUTO_MIGRATE", true),
		SeedDemo:    envBool("SEED_DEMO", envBool("DEMO_MODE", false)),

		SessionTTL:     envDuration("SESSION_TTL", 30*24*time.Hour),
		SessionCookie:  env("SESSION_COOKIE", "ip_session"),
		CookieSecure:   envBool("COOKIE_SECURE", false),
		AllowedOrigins: envList("ALLOWED_ORIGINS", []string{"http://localhost:3000"}),

		StorageDriver:   env("STORAGE_DRIVER", "local"),
		StorageLocalDir: env("STORAGE_LOCAL_DIR", filepath.Join("data", "storage")),
		MaxUploadBytes:  envInt64("MAX_UPLOAD_BYTES", 10<<20),

		PayPalClientID:     strings.TrimSpace(os.Getenv("PAYPAL_CLIENT_ID")),
		PayPalClientSecret: strings.TrimSpace(os.Getenv("PAYPAL_CLIENT_SECRET")),
		PayPalBaseURL:      env("PAYPAL_BASE_URL", "https://api-m.sandbox.paypal.com"),
		PayPalWebhookID:    strings.TrimSpace(os.Getenv("PAYPAL_WEBHOOK_ID")),
		PayPalCurrency:     env("PAYPAL_CURRENCY", "USD"),

		AIProvider: env("AI_PROVIDER", ""),
		AIAPIKey:   strings.TrimSpace(os.Getenv("AI_API_KEY")),
		AIModel:    env("AI_MODEL", "gpt-4o-mini"),
		AIBaseURL:  env("AI_BASE_URL", "https://api.openai.com/v1"),
		AITimeout:  envDuration("AI_TIMEOUT", 45*time.Second),
	}

	c.DBDriver = "sqlite"
	if c.DatabaseURL != "" {
		if !strings.HasPrefix(c.DatabaseURL, "postgres://") && !strings.HasPrefix(c.DatabaseURL, "postgresql://") {
			return nil, fmt.Errorf("config: DATABASE_URL must be a postgres:// or postgresql:// URL, got %q", redact(c.DatabaseURL))
		}
		c.DBDriver = "postgres"
	}

	// PayPal is only considered live when BOTH halves of the credential pair exist.
	if c.PayPalClientID == "" || c.PayPalClientSecret == "" {
		c.PayPalClientID, c.PayPalClientSecret = "", ""
	}
	if !strings.HasPrefix(c.PayPalBaseURL, "https://api-m.sandbox.paypal.com") &&
		!strings.HasPrefix(c.PayPalBaseURL, "https://api.sandbox.paypal.com") {
		return nil, fmt.Errorf("config: PAYPAL_BASE_URL must point at the PayPal *sandbox* (got %q); ImpactPay refuses to run live payments", c.PayPalBaseURL)
	}

	if c.AIProvider == "" {
		if c.AIAPIKey != "" {
			c.AIProvider = "openai"
		} else {
			c.AIProvider = "deterministic"
		}
	}

	if c.SeedDemo {
		c.Env = "demo"
	}
	return c, nil
}

// PayPalEnabled reports whether real PayPal sandbox credentials are configured.
func (c *Config) PayPalEnabled() bool {
	return c.PayPalClientID != "" && c.PayPalClientSecret != ""
}

// DemoMode mirrors SEED_DEMO.
func (c *Config) DemoMode() bool { return c.SeedDemo }

func (c *Config) IsProduction() bool { return c.Env == "production" }

func redact(v string) string {
	if i := strings.Index(v, "@"); i > 0 {
		return "postgres://<redacted>@" + v[i+1:]
	}
	return "<redacted>"
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt64(k string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envDuration(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envList(k string, def []string) []string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
