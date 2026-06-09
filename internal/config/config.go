// Package config loads service configuration from environment variables with
// sane defaults, so a single static binary is configured entirely via env.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL  string
	HTTPAddr     string
	PgxMaxConns  int32
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	CORSOrigins  []string
	RateLimitRPS int
	RunSeed      bool
	SeedDataset  string // dashboard | mobile | both
	JWTSecret    string // HS256 signing secret for analyst JWTs
	PhotoDir     string // directory where uploaded report photos are stored (mounted volume in prod)
	LogLevel     string
	Env          string // dev | prod (controls slog handler)
	FeedsEnabled bool   // poll external disaster feeds (USGS/GDACS) for crises
	FeedsIntervalMin int // minutes between feed ingest passes
	BoundariesEnabled bool // load admin boundaries (Natural Earth baseline + lazy geoBoundaries ADM1) for Area tagging
	TranslateURL    string // self-hosted LibreTranslate base URL ("" = disabled)
	TranslateTarget string // target language for description translation
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:  env("DATABASE_URL", "postgres://beacon:beacon@localhost:5544/beacon?sslmode=disable"),
		HTTPAddr:     env("HTTP_ADDR", ":8080"),
		PgxMaxConns:  int32(envInt("PGX_MAX_CONNS", 20)),
		ReadTimeout:  time.Duration(envInt("READ_TIMEOUT_SEC", 15)) * time.Second,
		WriteTimeout: time.Duration(envInt("WRITE_TIMEOUT_SEC", 30)) * time.Second,
		CORSOrigins:  envList("CORS_ORIGINS", []string{"http://localhost:3000"}),
		RateLimitRPS: envInt("RATE_LIMIT_RPS", 20),
		RunSeed:      envBool("RUN_SEED", true),
		SeedDataset:  env("SEED_DATASET", "dashboard"),
		JWTSecret:    env("JWT_SECRET", "beacon-dev-secret-change-me"),
		PhotoDir:     env("PHOTO_DIR", "./data/photos"),
		LogLevel:     env("LOG_LEVEL", "info"),
		Env:          env("ENV", "dev"),
		FeedsEnabled: envBool("FEEDS_ENABLED", true),
		FeedsIntervalMin: envInt("FEEDS_INTERVAL_MIN", 30),
		BoundariesEnabled: envBool("BOUNDARIES_ENABLED", true),
		TranslateURL:    env("TRANSLATE_URL", ""),
		TranslateTarget: env("TRANSLATE_TARGET", "en"),
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.PgxMaxConns < 1 {
		return fmt.Errorf("PGX_MAX_CONNS must be >= 1")
	}
	switch c.SeedDataset {
	case "dashboard", "mobile", "both":
	default:
		return fmt.Errorf("SEED_DATASET must be dashboard|mobile|both, got %q", c.SeedDataset)
	}
	switch c.Env {
	case "dev", "prod":
	default:
		return fmt.Errorf("ENV must be dev|prod, got %q (a typo would silently disable analyst auth)", c.Env)
	}
	// Fail-closed in prod: never boot with the default JWT signing secret.
	if c.Env == "prod" && (c.JWTSecret == "" || c.JWTSecret == "beacon-dev-secret-change-me") {
		return fmt.Errorf("JWT_SECRET must be set to a non-default value when ENV=prod")
	}
	return nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envList(key string, def []string) []string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}
