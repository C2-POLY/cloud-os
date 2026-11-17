// Package migrations embeds the SQL migrations applied by the core at startup.
package migrations

import "embed"

// FS holds every migration file, named NNNN_<name>.sql.
//
//go:embed *.sql
var FS embed.FS
