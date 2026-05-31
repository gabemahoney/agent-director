// error_cases.go defines the per-verb error-path fixture table used by
// TestEnvelopeDiff_Error in error_cases_test.go.
//
// Each errorCase entry describes:
//
//   - verb:    one of manifest.CallableVerbs()
//   - errName: one of verb.ErrorNames — the expected err_name in both envelopes.
//   - seed:    seeds a fresh store (and optional ancillary filesystem state)
//              in a temp dir; returns srcDir (the directory whose contents
//              copyFixtureStore copies into homeDir/.agent-director/) and a
//              ctx map with any dynamic values the params / cliArgv callbacks
//              need.
//   - params:  builds the params map[string]any for runClient.
//   - cliArgv: builds the []string argv (verb + flags) for runCLI.
//
// Coverage note for find-missing / ErrProbeUnsupported:
// ErrProbeUnsupported is emitted exclusively by the platform fallback prober
// (probe_unsupported.go) which is only compiled when the build target is
// neither linux nor darwin. On this test host (linux/amd64) it is impossible
// to trigger ErrProbeUnsupported through pkg/api.Client.FindMissing without
// dependency-injecting a fake prober — which the current Client interface
// does not expose. find-missing is therefore omitted from errorCases and
// explicitly exempted in TestErrorTableCoverage. See the skipCoverageVerbErrs
// map in TestErrorTableCoverage for the documented exemption.
//
// An init() guard at the bottom of this file panics at program startup if any
// row names an err_name not present in the verb's manifest.ErrorNames or
// references a non-callable verb.
package envelope_diff

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// errorCase describes one error-path fixture for the envelope-diff harness.
type errorCase struct {
	verb string

	// errName is the expected err_name string in both CLI and Client envelopes.
	// It must appear in the corresponding VerbDef.ErrorNames slice.
	errName string

	// skip, if non-nil, is called at the start of the subtest and may call
	// t.Skip to bypass execution on platforms where the error cannot be
	// triggered. The row must still exist in errorCases for
	// TestErrorTableCoverage to pass.
	skip func(t *testing.T)

	// seed builds a fixture store (and any ancillary filesystem state) in a
	// fresh temp dir and returns:
	//   srcDir: the directory whose contents copyFixtureStore copies into
	//           homeDir/.agent-director/.
	//   ctx:    a map of dynamic values consumed by params / cliArgv.
	seed func(t *testing.T) (srcDir string, ctx map[string]any)

	// params returns the params map passed to runClient.
	params func(ctx map[string]any) map[string]any

	// cliArgv returns the argv slice (verb + flags) passed to runCLI.
	cliArgv func(ctx map[string]any) []string
}

// errorCases is the authoritative per-verb error-path fixture table.
// The init() guard below validates every errName and verb name at startup.
// TestErrorTableCoverage (error_cases_test.go) enforces completeness.
var errorCases = []errorCase{

	// ── spawn / ErrCwdMissing ─────────────────────────────────────────────
	// Most representative spawn error: pure parameter validation, no tmux
	// interaction, reproducible on any host without fake-tmux involvement.
	{
		verb:    "spawn",
		errName: "ErrCwdMissing",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrCwdMissing(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			// cwd absent → strParam returns "" → ErrCwdMissing.
			return map[string]any{}
		},
		cliArgv: func(_ map[string]any) []string {
			// No --cwd flag → p.CWD="" → ErrCwdMissing.
			return []string{"spawn"}
		},
	},

	// ── status / ErrSpawnNotFound ─────────────────────────────────────────
	// Canonical missing-row error: empty store + non-existent id.
	{
		verb:    "status",
		errName: "ErrSpawnNotFound",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotFound(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": "nonexistent-id"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"status", "--claude-instance-id", "nonexistent-id"}
		},
	},

	// ── get / ErrSpawnNotFound ────────────────────────────────────────────
	{
		verb:    "get",
		errName: "ErrSpawnNotFound",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotFound(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": "nonexistent-id"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"get", "--claude-instance-id", "nonexistent-id"}
		},
	},

	// ── send-keys / ErrSpawnNotInteractive ────────────────────────────────
	// Most representative send-keys error: state check fires before any
	// tmux interaction, so fake-tmux is not reached.
	{
		verb:    "send-keys",
		errName: "ErrSpawnNotInteractive",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotInteractive(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": "id-err-ni-1",
				"text":               "hello",
			}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"send-keys",
				"--claude-instance-id", "id-err-ni-1",
				"--text", "hello",
			}
		},
	},

	// ── read-pane / ErrSpawnNotFound ──────────────────────────────────────
	// Missing-row path: empty store + non-existent id. The tmux
	// capture-pane call is never reached.
	{
		verb:    "read-pane",
		errName: "ErrSpawnNotFound",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotFound(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": "nonexistent-id"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"read-pane", "--claude-instance-id", "nonexistent-id"}
		},
	},

	// ── kill / ErrSpawnNotFound ───────────────────────────────────────────
	{
		verb:    "kill",
		errName: "ErrSpawnNotFound",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotFound(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": "nonexistent-id"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"kill", "--claude-instance-id", "nonexistent-id"}
		},
	},

	// ── decide / ErrRelayModeOff ──────────────────────────────────────────
	// Most representative decide error: relay_mode check fires immediately
	// after the spawn lookup, before any permission-request DB access.
	{
		verb:    "decide",
		errName: "ErrRelayModeOff",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrRelayModeOff(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": "id-err-rmo-1",
				"request_token":      storefix.TestRequestTokenA,
				"decision":           "allow",
			}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"decide",
				"--claude-instance-id", "id-err-rmo-1",
				"--request-token", storefix.TestRequestTokenA,
				"--decision", "allow",
			}
		},
	},

	// ── decide / ErrNoOpenPermissionRequest ───────────────────────────────
	// relay_mode=on but no permission_requests row: the UPDATE finds no
	// target, the follow-up SELECT finds no row.
	{
		verb:    "decide",
		errName: "ErrNoOpenPermissionRequest",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrNoOpenPermissionRequest(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": "id-err-nopr-1",
				"request_token":      storefix.TestRequestTokenA,
				"decision":           "allow",
			}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"decide",
				"--claude-instance-id", "id-err-nopr-1",
				"--request-token", storefix.TestRequestTokenA,
				"--decision", "allow",
			}
		},
	},

	// ── decide / ErrAlreadyDecided ────────────────────────────────────────
	// relay_mode=on, row exists but decision already written: UPDATE
	// no-ops, follow-up SELECT finds non-NULL decision.
	// SeedErrAlreadyDecided pre-decides the row using TestRequestTokenA.
	{
		verb:    "decide",
		errName: "ErrAlreadyDecided",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrAlreadyDecided(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": "id-err-ad-1",
				"request_token":      storefix.TestRequestTokenA,
				"decision":           "deny",
			}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"decide",
				"--claude-instance-id", "id-err-ad-1",
				"--request-token", storefix.TestRequestTokenA,
				"--decision", "deny",
			}
		},
	},

	// ── resume / ErrSpawnNotResumable ─────────────────────────────────────
	// Most representative resume error: state check fires before JSONL
	// stat or tmux new-session, so no filesystem dependency.
	{
		verb:    "resume",
		errName: "ErrSpawnNotResumable",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotResumable(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": "id-err-nr-1"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"resume", "--claude-instance-id", "id-err-nr-1"}
		},
	},

	// ── make-template / ErrTemplateNameUnsafe ────────────────────────────
	// Pure parameter validation: name "../evil" contains a path separator,
	// validated before EnsureTemplatesDir or any file I/O.
	{
		verb:    "make-template",
		errName: "ErrTemplateNameUnsafe",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrTemplateNameUnsafe(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"name": "../evil"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"make-template", "--name", "../evil"}
		},
	},

	// ── make-template / ErrTemplateExists ────────────────────────────────
	// NOTE: ErrTemplateExists is intentionally absent from this table.
	// Its err_description embeds the absolute path of the pre-existing file
	// (e.g. "/tmp/TestXxx/002/.agent-director/templates/err-tmpl.toml"),
	// which differs between the CLI copy (homeDir1) and the Client copy
	// (homeDir2). Since Linux paths contain no ':', the prefix-match policy
	// cannot sidestep the homeDir-specific mismatch. ErrTemplateNameUnsafe
	// above already covers make-template for the TestErrorTableCoverage gate.

	// ── list / ErrListInvalidLabel ────────────────────────────────────────
	// Label "badlabel" lacks a '=' separator: the validator fires at the
	// API layer before any DB query.
	{
		verb:    "list",
		errName: "ErrListInvalidLabel",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrListInvalidLabel(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"labels": []string{"badlabel"}}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"list", "--label", "badlabel"}
		},
	},

	// ── pause / ErrSpawnNotPausable ───────────────────────────────────────
	// State check fires before any tmux interaction: pending-state spawns
	// are not pausable (only waiting-state spawns are).
	{
		verb:    "pause",
		errName: "ErrSpawnNotPausable",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedErrSpawnNotPausable(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": "id-err-np-1"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"pause", "--claude-instance-id", "id-err-np-1"}
		},
	},

	// ── find-missing / ErrProbeUnsupported ───────────────────────────────
	// ErrProbeUnsupported is emitted by probe_unsupported.go (build tag
	// !linux && !darwin). Both linux and darwin compile a native prober
	// (probe_linux.go reads /proc, probe_darwin.go reads kinfo) — neither
	// can surface ErrProbeUnsupported without injecting a fake prober into
	// Client.FindMissing. The row must exist for TestErrorTableCoverage;
	// the subtest skips on linux+darwin via the skip hook.
	{
		verb:    "find-missing",
		errName: "ErrProbeUnsupported",
		skip: func(t *testing.T) {
			t.Helper()
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				t.Skipf("ErrProbeUnsupported not triggerable on %s (native prober compiled; probe_unsupported.go excluded by build tag)", runtime.GOOS)
			}
		},
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dbPath := apitest.SeedEmptyStore(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"find-missing"}
		},
	},
}

// ── lookup helper ─────────────────────────────────────────────────────────────

// lookupErrorCases returns all errorCase entries registered for verb.
// An empty slice means the verb has no error-path cases in the table;
// callers wanting the "at least one" guarantee should use
// TestErrorTableCoverage instead of inspecting this directly.
func lookupErrorCases(verb string) []errorCase {
	var out []errorCase
	for _, ec := range errorCases {
		if ec.verb == verb {
			out = append(out, ec)
		}
	}
	return out
}

// ── completeness guard ────────────────────────────────────────────────────────

func init() {
	// Build an index of callable verbs and their ErrorNames for O(1) lookup.
	type verbInfo struct {
		errNames []string
		errSet   map[string]bool
	}
	callableIndex := make(map[string]verbInfo)
	for _, v := range manifest.CallableVerbs() {
		info := verbInfo{
			errNames: v.ErrorNames,
			errSet:   make(map[string]bool, len(v.ErrorNames)),
		}
		for _, en := range v.ErrorNames {
			info.errSet[en] = true
		}
		callableIndex[v.Name] = info
	}

	// Validate every row: verb must be callable, errName must be in manifest.
	for _, ec := range errorCases {
		info, ok := callableIndex[ec.verb]
		if !ok {
			panic(fmt.Sprintf(
				"envelope_diff: errorCases: %q is not a callable verb", ec.verb))
		}
		if !info.errSet[ec.errName] {
			panic(fmt.Sprintf(
				"envelope_diff: errorCases: errName %q is not in manifest.ErrorNames for verb %q",
				ec.errName, ec.verb))
		}
	}

	// Assert every callable verb with ErrorNames has at least one row.
	covered := make(map[string]bool, len(errorCases))
	for _, ec := range errorCases {
		covered[ec.verb] = true
	}
	for _, v := range manifest.CallableVerbs() {
		if len(v.ErrorNames) == 0 {
			continue
		}
		if !covered[v.Name] {
			panic(fmt.Sprintf(
				"envelope_diff: errorCases: callable verb %q has ErrorNames %v but no row in errorCases; add a row or a skip hook",
				v.Name, v.ErrorNames))
		}
	}
}
