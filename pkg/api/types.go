package api

import "github.com/gabemahoney/agent-director/internal/spawn"

// VerbSummary is the per-verb shape returned by Help. It is the public
// projection of manifest.VerbDef restricted to the two fields the help
// output exposes. JSON tags are lowercase to match SRD §12.3 conventions.
type VerbSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SpawnParams is the typed input for the spawn verb.
// Re-exported from internal/spawn so callers do not import internal/*.
type SpawnParams = spawn.SpawnParams

// Permissions holds the allow/deny/ask permission arrays for spawn.
// Re-exported from internal/spawn.
type Permissions = spawn.Permissions
