// Package store holds all SQL access via pgx. No business logic lives here.
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunInTx runs fn inside a transaction, committing on success and rolling back
// on error or panic. Used by the versioning upsert (FOR UPDATE on the building's
// report set) so version/supersedes are computed atomically.
func RunInTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
