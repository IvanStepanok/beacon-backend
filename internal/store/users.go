package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/model"
)

type Users struct{ pool *pgxpool.Pool }

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

func (s *Users) Count(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

// Create inserts a user (idempotent on email). Hash is precomputed by the caller.
func (s *Users) Create(ctx context.Context, email, hash, name, role string, region *string, scope []string) error {
	if scope == nil {
		scope = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (email, password_hash, name, role, region, crisis_scope)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (email) DO NOTHING`,
		email, hash, name, role, region, scope)
	return err
}

// GetByEmail returns the user, its password hash, and its MFA state (enabled flag +
// the encrypted TOTP secret), or (nil, "", false, "", nil) if absent.
func (s *Users) GetByEmail(ctx context.Context, email string) (*model.User, string, bool, string, error) {
	var u model.User
	var hash string
	var mfaEnabled bool
	var mfaSecret *string
	err := s.pool.QueryRow(ctx,
		"SELECT id::text, email, name, role, region, crisis_scope, password_hash, mfa_enabled, mfa_secret FROM users WHERE email = $1", email).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.CrisisScope, &hash, &mfaEnabled, &mfaSecret)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, "", false, "", nil
		}
		return nil, "", false, "", err
	}
	if u.CrisisScope == nil {
		u.CrisisScope = []string{}
	}
	secret := ""
	if mfaSecret != nil {
		secret = *mfaSecret
	}
	return &u, hash, mfaEnabled, secret, nil
}

// SetMFAPending stores a freshly-generated (already-encrypted) TOTP secret WITHOUT
// enabling MFA — the analyst must verify a code first (EnableMFA). Re-enrolling
// overwrites any prior pending/active secret and resets the enabled flag.
func (s *Users) SetMFAPending(ctx context.Context, id, encSecret string) error {
	_, err := s.pool.Exec(ctx, "UPDATE users SET mfa_secret=$2, mfa_enabled=false WHERE id=$1::uuid", id, encSecret)
	return err
}

// EnableMFA flips mfa_enabled true after a verified code; a no-op if no secret is set.
func (s *Users) EnableMFA(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "UPDATE users SET mfa_enabled=true WHERE id=$1::uuid AND mfa_secret IS NOT NULL", id)
	return err
}

// GetMFAByID returns (enabled, encryptedSecret) for the user id.
func (s *Users) GetMFAByID(ctx context.Context, id string) (bool, string, error) {
	var enabled bool
	var secret *string
	err := s.pool.QueryRow(ctx, "SELECT mfa_enabled, mfa_secret FROM users WHERE id=$1::uuid", id).Scan(&enabled, &secret)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, "", nil
		}
		return false, "", err
	}
	if secret == nil {
		return enabled, "", nil
	}
	return enabled, *secret, nil
}

// DisableMFA clears the secret + flag (after the caller has verified a current code).
func (s *Users) DisableMFA(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "UPDATE users SET mfa_secret=NULL, mfa_enabled=false WHERE id=$1::uuid", id)
	return err
}
