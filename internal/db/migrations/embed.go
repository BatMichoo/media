// Package migrations holds the database schema changes (Goose), built
// into the binary.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
