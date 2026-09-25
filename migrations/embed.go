package migrations

import "embed"

// FS holds the ordered SQL migrations applied at startup.
//
//go:embed *.sql
var FS embed.FS
