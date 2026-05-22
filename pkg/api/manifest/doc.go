// Package manifest contains the generated surface.json for the
// agent-director verb surface.
//
// surface.json is the machine-readable representation of the full verb
// surface: per-verb callable/handle_free flags, parameter and result field
// definitions with Nullable/AllowEmpty/AllowedValues markers, and the
// error_names catalog. Downstream Epics (pkg/cabi, TS/Bun bindings,
// envelope-diff harness) consume this file without importing internal/*.
//
// The source of truth is internal/api/manifest.Verbs. Run
// `go generate ./pkg/api/manifest/...` (or `make surface-json`) to
// regenerate surface.json after any manifest change.
//
//go:generate go run ./generate.go
package manifest
