//go:build ignore

// generate.go produces pkg/api/errnames/catalog.json from the canonical
// pkg/api/errnames.Catalog slice.
//
// Run via:
//
//	go generate ./pkg/api/errnames/...
//	make errnames-json
//
// The output schema is locked for downstream consumption (Epic 2 pkg/cabi,
// Epic 5 TS/Bun bindings):
//
//	[
//	  {"name": "ErrCwdMissing", "package": "spawn", "description": ""},
//	  ...
//	]
//
// - name        — the err_name string (matches Catalog[i].Name exactly).
// - package     — short package suffix of the sentinel's origin package
//                 (e.g. "spawn", "store", "tmux", "config", "probe", "api",
//                 "errnames").
// - description — canonical message text (empty for now; downstream Epics
//                 may fill from doc-strings later). Key is locked in schema.
//
// 2-space indent, trailing newline, deterministic across runs (Catalog
// slice order, not alphabetical).

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// entryOutput is the per-entry JSON shape.
type entryOutput struct {
	Name        string `json:"name"`
	Package     string `json:"package"`
	Description string `json:"description"`
}

// packageOf maps each err_name to the short package suffix of the package
// that declares its sentinel. A hand-maintained map is used because
// errors.New returns *errorString from the standard library — the type
// carries no package attribution that reflection can recover.
var packageOf = map[string]string{
	// internal/spawn
	"ErrCwdMissing":            "spawn",
	"ErrCwdNotAPath":           "spawn",
	"ErrCwdNotFound":           "spawn",
	"ErrCwdNotADirectory":      "spawn",
	"ErrRelayModeInvalid":      "spawn",
	"ErrSpawnDeniedFlag":       "spawn",
	"ErrReservedEnvKey":        "spawn",
	"ErrInstanceIdCollision":   "spawn",
	"ErrTmuxSessionNameEmpty":   "spawn",
	"ErrTmuxSessionNameInvalid": "spawn",
	"ErrTmuxSessionNameTooLong": "spawn",

	// internal/store
	// ErrSchemaMismatch is intentionally absent: it is a store-initialization
	// error (not a verb error) removed from Catalog in Task 7.
	"ErrSpawnNotFound":           "store",
	"ErrNoOpenPermissionRequest": "store",
	"ErrAlreadyDecided":          "store",

	// internal/tmux
	// ErrTmuxKillFailed and ErrTmuxListPanesFailed are intentionally absent:
	// they were removed from Catalog in Task 7 (never surfaced to API callers).
	"ErrTmuxNotAvailable":  "tmux",
	"ErrTmuxSessionCreate": "tmux",
	"ErrTmuxSendKeys":      "tmux",
	"ErrTmuxCaptureFailed": "tmux",

	// internal/config
	"ErrTemplateNameUnsafe": "config",
	"ErrTemplateNotFound":   "config",
	"ErrTemplateMalformed":  "config",
	"ErrTemplateExists":     "config",

	// internal/probe
	"ErrProbeUnsupported": "probe",

	// pkg/api
	"ErrSpawnNotInteractive": "api",
	"ErrSendKeysWhileRelayed": "api",
	"ErrSpawnNotPausable":     "api",
	"ErrPauseTimeout":         "api",
	"ErrListInvalidLabel":     "api",
	"ErrSpawnNotResumable":    "api",
	"ErrNoSessionId":          "api",
	"ErrJsonlMissing":         "api",
	"ErrRelayModeOff":         "api",
	"ErrInvalidDecision":      "api",

	// ErrUnknownTool is intentionally absent: it was moved from pkg/api/errnames
	// to internal/mcp in Task 7 (dispatch-level error, not a verb-surface error).
}

func main() {
	out := make([]entryOutput, 0, len(errnames.Catalog))
	for _, entry := range errnames.Catalog {
		pkg, ok := packageOf[entry.Name]
		if !ok {
			fmt.Fprintf(os.Stderr, "generate: unknown package for entry %q — add it to packageOf map\n", entry.Name)
			os.Exit(1)
		}
		out = append(out, entryOutput{
			Name:        entry.Name,
			Package:     pkg,
			Description: "",
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: marshal: %v\n", err)
		os.Exit(1)
	}
	// Append trailing newline.
	data = append(data, '\n')

	// Write to pkg/api/errnames/catalog.json, relative to the module root.
	// __FILE__ resolution: this generator lives in pkg/api/errnames/; go up
	// two levels to reach the module root, then back down to the target.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "generate: cannot determine source file path")
		os.Exit(1)
	}
	// thisFile = .../pkg/api/errnames/generate.go
	// dir      = .../pkg/api/errnames/
	dir := filepath.Dir(thisFile)
	outPath := filepath.Join(dir, "catalog.json")

	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "generate: write %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", outPath, len(data))
}
