// Command server is the Beacon backend: a stateless Go HTTP service over
// PostgreSQL + PostGIS serving one JSON contract to the mobile app and the
// analyst dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stepanok/beacon-server/internal/api"
	"github.com/stepanok/beacon-server/internal/boundary"
	"github.com/stepanok/beacon-server/internal/config"
	"github.com/stepanok/beacon-server/internal/db"
	"github.com/stepanok/beacon-server/internal/feeds"
	"github.com/stepanok/beacon-server/internal/handler"
	"github.com/stepanok/beacon-server/internal/seed"
	"github.com/stepanok/beacon-server/internal/service"
	"github.com/stepanok/beacon-server/internal/store"
	"github.com/stepanok/beacon-server/internal/translate"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	// 1. migrations (own throwaway sql.DB)
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	logger.Info("migrations applied")

	// 2. pool
	ctx := context.Background()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.PgxMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("db pool ready", "maxConns", cfg.PgxMaxConns)

	// 3. stores + services
	reports := store.NewReports(pool)
	crises := store.NewCrises(pool)
	admin := store.NewAdmin(pool)
	users := store.NewUsers(pool)
	settings := store.NewSettings(pool)
	translator := translate.New(cfg.TranslateURL, cfg.TranslateTarget)

	// Admin-boundary loader: global ADM0 baseline (embedded) + lazy per-country
	// geoBoundaries ADM1, so every report gets an Area auto-tagged. nil when disabled.
	var boundaries *boundary.Loader
	if cfg.BoundariesEnabled {
		boundaries = boundary.New(pool, admin, logger)
	}
	reportSvc := service.NewReportService(pool, reports, admin, crises, translator, boundaries)
	statsSvc := service.NewStatsService(reports)

	// external disaster-feed ingester (USGS/GDACS) — nil if disabled.
	var ingester *feeds.Ingester
	if cfg.FeedsEnabled {
		ingester = feeds.Default(crises, logger)
	}

	// 4. seed (idempotent — reports gated by empty table, users by empty table;
	// PhotoDir receives the embedded demo evidence photos)
	if cfg.RunSeed {
		if err := seed.Run(ctx, pool, reports, crises, admin, users, cfg.SeedDataset, cfg.PhotoDir, logger); err != nil {
			return err
		}
	}

	// Global ADM0 country baseline (idempotent, fast) — gives every point a country
	// immediately and resolves point→ISO3 for the lazy ADM1 loader.
	if boundaries != nil {
		if err := boundaries.LoadCountriesBaseline(ctx); err != nil {
			logger.Warn("admin baseline load failed", "err", err)
		}
	}

	// 5. router
	h := handler.New(handler.Deps{
		Reports:   reports,
		Crises:    crises,
		Users:     users,
		ReportSvc: reportSvc,
		StatsSvc:  statsSvc,
		Settings:  settings,
		Ingester:  ingester,
		JWTSecret: cfg.JWTSecret,
		PhotoDir:  cfg.PhotoDir,
	})
	router := api.NewRouter(cfg, pool, h, logger)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  90 * time.Second,
	}

	// 6. run + graceful shutdown
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the disaster-feed poller bound to the server lifetime.
	if ingester != nil {
		ingester.Start(shutdownCtx, time.Duration(cfg.FeedsIntervalMin)*time.Minute)
		logger.Info("disaster-feed ingester started", "intervalMin", cfg.FeedsIntervalMin)
	}

	// Back-fill Areas for any existing reports whose country has no ADM1 loaded yet
	// (e.g. reports submitted before this feature). Background, best-effort.
	if boundaries != nil {
		go boundaries.SweepExisting(shutdownCtx)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-shutdownCtx.Done():
		logger.Info("shutdown signal received, draining")
		drainCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(drainCtx)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Env == "prod" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
