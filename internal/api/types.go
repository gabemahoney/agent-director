// Package api is the stable contract surface between CLI/MCP front-ends and
// the underlying store/manifest layers, per SRD §4.5.
//
// Verb implementations live here as plain Go functions returning typed
// values and errors. The CLI in cmd/ marshals their return values to JSON;
// the MCP server in Epic 11 will reuse the same functions. The api package
// MUST NOT import internal/store or internal/config directly when a verb
// does not need them — Help, in particular, is pure manifest data.
package api

// VerbSummary is the per-verb shape returned by Help. It is the public
// projection of manifest.VerbDef restricted to the two fields the help
// output exposes. JSON tags are lowercase to match SRD §12.3 conventions.
type VerbSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
