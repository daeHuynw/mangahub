// Package data holds the catalog seed file, embedded into the catalog binary
// so `go run ./cmd/catalog` works from any directory without extra files.
package data

import _ "embed"

// MangaSeed is the content of manga_seed.json: a JSON array of manga records
// (metadata only). It is imported into catalog.db only when the table is empty.
//
//go:embed manga_seed.json
var MangaSeed []byte
