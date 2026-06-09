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

// GetByEmail returns the user + its password hash, or (nil, "", nil) if absent.
func (s *Users) GetByEmail(ctx context.Context, email string) (*model.User, string, error) {
	var u model.User
	var hash string
	err := s.pool.QueryRow(ctx,
		"SELECT id::text, email, name, role, region, crisis_scope, password_hash FROM users WHERE email = $1", email).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.CrisisScope, &hash)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, "", nil
		}
		return nil, "", err
	}
	if u.CrisisScope == nil {
		u.CrisisScope = []string{}
	}
	return &u, hash, nil
}
