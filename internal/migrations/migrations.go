// Package migrations embeds the SQL migration files so goose can run them at
// startup from the single static binary (no files needed on disk in prod).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
