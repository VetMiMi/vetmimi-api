// Package migrations embeds the goose SQL migrations so the binary applies
// them at start-up without the files beside it. The goose CLI (make migrate)
// reads the same files from this directory.
package migrations

import "embed"

// FS holds every migration, in version order by file name.
//
//go:embed *.sql
var FS embed.FS
