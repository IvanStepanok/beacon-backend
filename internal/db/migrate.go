package db

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for goose only
	"github.com/pressly/goose/v3"

	"github.com/stepanok/beacon-server/internal/migrations"
)

// Migrate runs all pending goose migrations from the embedded FS. It opens a
// throwaway database/sql handle (goose's API) via the pgx stdlib driver and
// closes it immediately — the app itself uses pgxpool, not database/sql.
func Migrate(dsn string) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open sql for migrate: %w", err)
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
