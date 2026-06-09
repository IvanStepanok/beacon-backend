package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Settings is the global key/value app config (app_settings table). Used for the
// server-driven capture scale (damage_scale: tier3 | ems98) and future toggles.
type Settings struct{ pool *pgxpool.Pool }

func NewSettings(pool *pgxpool.Pool) *Settings { return &Settings{pool: pool} }

// Get returns the value for key, or def if unset.
func (s *Settings) Get(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = $1", key).Scan(&v)
	if err == pgx.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	return v, nil
}

// Set upserts a setting.
func (s *Settings) Set(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now()) "+
			"ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()",
		key, value)
	return err
}
