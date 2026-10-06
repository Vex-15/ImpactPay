// Command server runs the ImpactPay API.
//
// Wiring is explicit and one-directional: config → database → services →
// providers → HTTP. The order matters because the payments service is the only
// component allowed to move money, and it is constructed last so it receives the
// fully-constructed verification and notification services it depends on.
//
// Two integrations degrade instead of failing:
//
//   - PayPal: when sandbox credentials are absent the payments service is wired
//     to a deterministic simulated provider. Every artefact it produces is
//     flagged simulated and labelled as such in the UI.
//   - The language model: when no API key is configured the verification narrator
//     and the impact answerer fall back to fully deterministic text. The numbers,
//     checks and recommendations are computed in Go either way; the model can only
//     rephrase what has already been decided.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/impactpay/backend/internal/ai"
	"github.com/impactpay/backend/internal/audit"
	"github.com/impactpay/backend/internal/auth"
	"github.com/impactpay/backend/internal/config"
	"github.com/impactpay/backend/internal/db"
	"github.com/impactpay/backend/internal/donations"
	"github.com/impactpay/backend/internal/evidence"
	"github.com/impactpay/backend/internal/httpapi"
	"github.com/impactpay/backend/internal/impact"
	"github.com/impactpay/backend/internal/milestones"
	"github.com/impactpay/backend/internal/notifications"
	"github.com/impactpay/backend/internal/organizations"
	"github.com/impactpay/backend/internal/payments"
	"github.com/impactpay/backend/internal/paypal"
	"github.com/impactpay/backend/internal/projects"
	"github.com/impactpay/backend/internal/seed"
	"github.com/impactpay/backend/internal/storage"
	"github.com/impactpay/backend/internal/verification"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "impactpay: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	logger.Info("starting impactpay",
		"env", cfg.Env,
		"port", cfg.Port,
		"database", cfg.DBDriver,
		"demo_mode", cfg.DemoMode(),
		"paypal_configured", cfg.PayPalEnabled(),
		"ai_provider", cfg.AIProvider,
	)

	// A boot context that is cancelled by SIGINT/SIGTERM lets long startup steps
	// (migrations, seeding) abort cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DBDriver, cfg.DatabaseURL, cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	if cfg.AutoMigrate {
		if err := database.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		logger.Info("migrations applied")
	}

	store, err := openStorage(cfg)
	if err != nil {
		return err
	}
	logger.Info("evidence storage ready", "driver", store.Driver())

	// --- services -----------------------------------------------------------
	auditSvc := audit.New(database)
	authSvc := auth.New(database, auth.Config{SessionTTL: cfg.SessionTTL})
	orgSvc := organizations.New(database, auditSvc)
	milestoneSvc := milestones.New(database, auditSvc)
	projectSvc := projects.New(database, auditSvc, milestoneSvc)
	evidenceSvc := evidence.New(database, auditSvc, store, cfg.MaxUploadBytes)
	notificationSvc := notifications.New(database)
	donationSvc := donations.New(database, auditSvc, milestoneSvc)
	impactSvc := impact.New(database)

	aiClient := ai.New(ai.Config{
		APIKey:  cfg.AIAPIKey,
		BaseURL: cfg.AIBaseURL,
		Model:   cfg.AIModel,
		Timeout: cfg.AITimeout,
	})
	verificationSvc := verification.New(database, auditSvc, evidenceSvc, milestoneSvc, projectSvc,
		ai.NewVerificationNarrator(aiClient))

	provider, providerLabel := paymentProvider(cfg)
	logger.Info("payment rail configured", "provider", providerLabel, "simulated", provider.Simulated())

	paymentsSvc := payments.New(payments.Deps{
		DB:              database,
		Audit:           auditSvc,
		Provider:        provider,
		Donations:       donationSvc,
		Projects:        projectSvc,
		Milestones:      milestoneSvc,
		Verifications:   verificationSvc,
		Organizations:   orgSvc,
		Notifications:   notificationSvc,
		AccountCurrency: cfg.PayPalCurrency,
		FXRateINRPerUSD: cfg.PayPalFXRateINRPerUSD,
	})

	// --- demo seed ----------------------------------------------------------
	if cfg.DemoMode() {
		result, err := seed.Run(ctx, seed.Deps{
			DB:            database,
			Logger:        logger,
			Auth:          authSvc,
			Organizations: orgSvc,
			Projects:      projectSvc,
			Milestones:    milestoneSvc,
			Evidence:      evidenceSvc,
			Donations:     donationSvc,
			Payments:      paymentsSvc,
			Verification:  verificationSvc,
			Impact:        impactSvc,
		})
		if err != nil {
			return fmt.Errorf("demo seed: %w", err)
		}
		if result.Seeded {
			logger.Info("demo data seeded",
				"organization", result.Organization,
				"project", result.Project,
				"donors", result.Donors,
				"credentials", result.Credentials,
			)
		} else {
			logger.Info("demo data already present", "project", result.Project)
		}
	}

	// --- HTTP ---------------------------------------------------------------
	api := httpapi.New(httpapi.Deps{
		Config:        cfg,
		DB:            database,
		Logger:        logger,
		Auth:          authSvc,
		Audit:         auditSvc,
		Organizations: orgSvc,
		Projects:      projectSvc,
		Milestones:    milestoneSvc,
		Evidence:      evidenceSvc,
		Donations:     donationSvc,
		Verification:  verificationSvc,
		Payments:      paymentsSvc,
		Impact:        impactSvc,
		Notifications: notificationSvc,
		Answerer:      ai.NewImpactAnswerer(aiClient),
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Expired sessions are purged periodically so the table cannot grow forever.
	go purgeSessions(ctx, logger, authSvc)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "public_url", cfg.PublicURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("stopped cleanly")
	return nil
}

// paymentProvider selects the PayPal sandbox rail when credentials are present
// and the deterministic simulated rail otherwise. ImpactPay never selects a live
// rail: config.Load refuses a non-sandbox base URL outright.
func paymentProvider(cfg *config.Config) (payments.Provider, string) {
	if !cfg.PayPalEnabled() {
		return payments.NewSimulatedProvider(), "simulated_sandbox"
	}
	client := paypal.New(paypal.Config{
		ClientID:     cfg.PayPalClientID,
		ClientSecret: cfg.PayPalClientSecret,
		BaseURL:      cfg.PayPalBaseURL,
		WebhookID:    cfg.PayPalWebhookID,
	})
	if !client.Enabled() {
		return payments.NewSimulatedProvider(), "simulated_sandbox"
	}
	return payments.NewPayPalProvider(client), "paypal_sandbox"
}

func openStorage(cfg *config.Config) (storage.Store, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.StorageDriver)) {
	case "s3":
		s3, err := storage.S3FromEnv()
		if err != nil {
			return nil, fmt.Errorf("storage: s3: %w", err)
		}
		return s3, nil
	case "", "local":
		local, err := storage.NewLocal(cfg.StorageLocalDir)
		if err != nil {
			return nil, fmt.Errorf("storage: local: %w", err)
		}
		return local, nil
	default:
		return nil, fmt.Errorf("storage: unknown driver %q (want local or s3)", cfg.StorageDriver)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	if lv := strings.TrimSpace(os.Getenv("LOG_LEVEL")); lv != "" {
		var parsed slog.Level
		if err := parsed.UnmarshalText([]byte(lv)); err == nil {
			level = parsed
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// purgeSessions deletes expired sessions every six hours until the context ends.
func purgeSessions(ctx context.Context, logger *slog.Logger, authSvc *auth.Service) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := authSvc.PurgeExpiredSessions(ctx); err != nil {
				logger.Warn("purge sessions failed", "error", err)
			} else if n > 0 {
				logger.Info("purged expired sessions", "count", n)
			}
		}
	}
}
