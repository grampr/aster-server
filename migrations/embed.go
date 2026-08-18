package migrations

import "embed"

// FS contains forward-only migrations used by ASTER_AUTO_MIGRATE.
//
//go:embed *.up.sql
var FS embed.FS
