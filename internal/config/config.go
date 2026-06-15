// Package config loads service configuration from environment variables with
// sane defaults, so a single static binary is configured entirely via env.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stepanok/beacon-server/internal/crypto"
)

type Config struct {
	DatabaseURL       string
	HTTPAddr          string
	PgxMaxConns       int32
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	CORSOrigins       []string
	RateLimitRPS      int
	RunSeed           bool
	SeedDataset       string // dashboard | mobile | both
	JWTSecret         string // HS256 signing secret for analyst JWTs
	PhotoDir          string // directory where uploaded report photos are stored (mounted volume in prod)
	LogLevel          string
	Env               string // dev | prod (controls slog handler)
	BoundariesEnabled bool   // load admin boundaries (Natural Earth baseline + lazy geoBoundaries ADM1) for Area tagging
	TranslateURL      string // self-hosted LibreTranslate base URL ("" = disabled)
	TranslateTarget   string // target language for description translation

	// DataEncryptionKey is the 32-byte AES-256 key (DATA_ENCRYPTION_KEY, base64 or hex)
	// used to encrypt data at rest: report photos on the volume + stored TOTP secrets.
	// Required in prod (fail-closed in Validate); nil in dev = at-rest encryption off.
	DataEncryptionKey []byte

	// Emergent-crisis clustering thresholds (deployment-tunable). A new crowd cluster
	// has no crisis row yet, so FORMATION uses these global defaults; the effective
	// values are then stamped onto the crisis row for provenance / per-crisis tuning.
	// A cluster of EmergentMinReports DISTINCT submitters within EmergentRadiusKm over
	// the last EmergentWindowHrs proposes a crisis for analyst review (never auto-active).
	EmergentRadiusKm   float64
	EmergentWindowHrs  int
	EmergentMinReports int
}

func Load() (Config, error) {
	dek, err := crypto.ParseKey(env("DATA_ENCRYPTION_KEY", ""))
	if err != nil {
		return Config{}, err
	}
	c := Config{
		DatabaseURL:       env("DATABASE_URL", "postgres://beacon:beacon@localhost:5544/beacon?sslmode=disable"),
		HTTPAddr:          env("HTTP_ADDR", ":8080"),
		PgxMaxConns:       int32(envInt("PGX_MAX_CONNS", 20)),
		ReadTimeout:       time.Duration(envInt("READ_TIMEOUT_SEC", 15)) * time.Second,
		WriteTimeout:      time.Duration(envInt("WRITE_TIMEOUT_SEC", 30)) * time.Second,
		CORSOrigins:       envList("CORS_ORIGINS", []string{"http://localhost:3000"}),
		RateLimitRPS:      envInt("RATE_LIMIT_RPS", 20),
		RunSeed:           envBool("RUN_SEED", true),
		SeedDataset:       env("SEED_DATASET", "dashboard"),
		JWTSecret:         env("JWT_SECRET", "beacon-dev-secret-change-me"),
		PhotoDir:          env("PHOTO_DIR", "./data/photos"),
		LogLevel:          env("LOG_LEVEL", "info"),
		Env:               env("ENV", "dev"),
		BoundariesEnabled: envBool("BOUNDARIES_ENABLED", true),
		TranslateURL:      env("TRANSLATE_URL", ""),
		TranslateTarget:   env("TRANSLATE_TARGET", "en"),
		DataEncryptionKey: dek,

		EmergentRadiusKm:   envFloat("BEACON_EMERGENT_RADIUS_KM", 2.0),
		EmergentWindowHrs:  envInt("BEACON_EMERGENT_WINDOW_HRS", 24),
		EmergentMinReports: envInt("BEACON_EMERGENT_MIN_REPORTS", 3),
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
	// Emergent thresholds must be sane: a min of <1 distinct submitter would make a
	// single report propose a crisis (exactly the false-alarm behaviour we forbid).
	if c.EmergentRadiusKm <= 0 {
		return fmt.Errorf("BEACON_EMERGENT_RADIUS_KM must be > 0, got %v", c.EmergentRadiusKm)
	}
	if c.EmergentWindowHrs < 1 {
		return fmt.Errorf("BEACON_EMERGENT_WINDOW_HRS must be >= 1, got %d", c.EmergentWindowHrs)
	}
	if c.EmergentMinReports < 2 {
		return fmt.Errorf("BEACON_EMERGENT_MIN_REPORTS must be >= 2 (one report can never be a crisis), got %d", c.EmergentMinReports)
	}
	if c.Env == "prod" {
		if len(c.DataEncryptionKey) != crypto.KeyLen {
			return fmt.Errorf("DATA_ENCRYPTION_KEY (a 32-byte base64/hex value) is required when ENV=prod — it encrypts report photos + MFA secrets at rest")
		}
		if !sslEnforced(c.DatabaseURL) {
			return fmt.Errorf("DATABASE_URL must use sslmode=require|verify-ca|verify-full when ENV=prod — backend↔DB transit must be TLS-encrypted")
		}
	}
	return nil
}

// sslEnforced reports whether DATABASE_URL pins an sslmode that actually encrypts
// the connection. libpq's default (and 'prefer') silently fall back to plaintext, so
// only require/verify-ca/verify-full count as enforced transit security.
func sslEnforced(dbURL string) bool {
	const k = "sslmode="
	i := strings.Index(dbURL, k)
	if i < 0 {
		return false
	}
	v := dbURL[i+len(k):]
	if j := strings.IndexAny(v, "&"); j >= 0 {
		v = v[:j]
	}
	switch v {
	case "require", "verify-ca", "verify-full":
		return true
	}
	return false
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

func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
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
