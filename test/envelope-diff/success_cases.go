// success_cases.go defines the per-verb fixture table used by
// TestEnvelopeDiff_Success in success_cases_test.go.
//
// Each successCase entry describes:
//
//   - verb:        one of manifest.CallableVerbs()
//   - seed:        seeds a fresh store in a temp dir; returns srcDir (the
//                  directory whose contents copyFixtureStore copies into
//                  homeDir/.agent-director/) and a ctx map with any dynamic
//                  values (ids, sessionIDs, cwds) the other callbacks need.
//   - params:      builds the params map[string]any for runClient.
//   - cliArgv:     builds the []string argv (verb + flags) for runCLI.
//   - extraSetup:  optional hook called after each copyFixtureStore with the
//                  resulting homeDir.  Used for verbs that need files outside
//                  .agent-director/ (e.g. resume needs a JSONL transcript at
//                  HOME/.claude/projects/<slug>/<session_id>.jsonl).
//
// The test driver (success_cases_test.go) always calls
// t.Setenv("HOME", homeDir) immediately before each extraSetup invocation so
// helpers that use os.UserHomeDir() (such as apitest.SeedJsonl) resolve to
// the correct homeDir.
//
// An init() guard at the bottom of this file panics at startup if any
// callable verb has no case, any case references a non-callable verb, or any
// verb appears more than once.
package envelope_diff

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// successCase describes one callable verb's happy-path fixture for the
// envelope-diff harness.
type successCase struct {
	verb string

	// seed builds a fixture store in a fresh temp dir and returns:
	//   srcDir: the directory whose contents copyFixtureStore copies into
	//           homeDir/.agent-director/.  A minimal srcDir contains only
	//           state.db; WAL shards are copied automatically when present.
	//   ctx:    a map of dynamic values (ids, sessionIDs, cwds, …) consumed
	//           by the params / cliArgv / extraSetup callbacks.
	seed func(t *testing.T) (srcDir string, ctx map[string]any)

	// params returns the params map passed to runClient.
	params func(ctx map[string]any) map[string]any

	// cliArgv returns the argv slice (verb + flags) passed to runCLI.
	cliArgv func(ctx map[string]any) []string

	// extraSetup is an optional post-copyFixtureStore hook called once per
	// homeDir copy (CLI copy and Client copy each receive their own call).
	// Use it for verbs that need files outside .agent-director/.
	//
	// The test driver sets HOME=homeDir via t.Setenv before invoking this
	// callback so any helper that calls os.UserHomeDir() (e.g.
	// apitest.SeedJsonl, config.EnsureTemplatesDir) resolves to homeDir.
	// nil for most verbs.
	extraSetup func(t *testing.T, homeDir string, ctx map[string]any)
}

// successCases is the authoritative per-verb fixture table.
// The init() guard below ensures every callable verb is represented exactly
// once, and that no non-callable verb sneaks in.
var successCases = []successCase{

	// ── spawn ─────────────────────────────────────────────────────────────
	// spawn mints a fresh claude_instance_id (excluded from diff via
	// nondeterministic.json ".claude_instance_id") and launches a tmux
	// session via fake-tmux. An empty store is sufficient — spawn creates
	// its own row.
	{
		verb: "spawn",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dir := t.TempDir()
			apitest.SeedStore(t, filepath.Join(dir, "state.db"))
			return dir, map[string]any{"cwd": "/tmp"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"cwd": ctx["cwd"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"spawn", "--cwd", ctx["cwd"].(string)}
		},
	},

	// ── status ────────────────────────────────────────────────────────────
	{
		verb: "status",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t,
				"id-status-1", "cd-status-1", store.StateWaiting, "off")
			return filepath.Dir(dbPath), map[string]any{"id": "id-status-1"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": ctx["id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"status", "--claude-instance-id", ctx["id"].(string)}
		},
	},

	// ── get ───────────────────────────────────────────────────────────────
	{
		verb: "get",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t,
				"id-get-1", "cd-get-1", store.StateWaiting, "off")
			return filepath.Dir(dbPath), map[string]any{"id": "id-get-1"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": ctx["id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"get", "--claude-instance-id", ctx["id"].(string)}
		},
	},

	// ── send-keys ─────────────────────────────────────────────────────────
	// send-keys result is empty ({}).  The row must be in an interactive
	// state (waiting); fake-tmux absorbs the send-keys syscall.
	{
		verb: "send-keys",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t,
				"id-sk-1", "cd-sk-1", store.StateWaiting, "off")
			return filepath.Dir(dbPath), map[string]any{
				"id":   "id-sk-1",
				"text": "hello",
			}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": ctx["id"],
				"text":               ctx["text"],
			}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"send-keys",
				"--claude-instance-id", ctx["id"].(string),
				"--text", ctx["text"].(string),
			}
		},
	},

	// ── read-pane ─────────────────────────────────────────────────────────
	// fake-tmux outputs "fake pane line one\nfake pane line two\n" for
	// capture-pane (no ANSI codes); default ANSI=false stripping leaves
	// the content unchanged.  Both CLI and Client call the same fake binary,
	// so the pane field is identical.
	{
		verb: "read-pane",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t,
				"id-rp-1", "cd-rp-1", store.StateWaiting, "off")
			return filepath.Dir(dbPath), map[string]any{"id": "id-rp-1"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": ctx["id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"read-pane",
				"--claude-instance-id", ctx["id"].(string),
			}
		},
	},

	// ── kill ──────────────────────────────────────────────────────────────
	// kill result is empty ({}).  fake-tmux absorbs the kill-session call.
	{
		verb: "kill",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t,
				"id-kill-1", "cd-kill-1", store.StateWaiting, "off")
			return filepath.Dir(dbPath), map[string]any{"id": "id-kill-1"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": ctx["id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"kill",
				"--claude-instance-id", ctx["id"].(string),
			}
		},
	},

	// ── decide ────────────────────────────────────────────────────────────
	// decide result is empty ({}).  The row must be in check_permission
	// with relay_mode=on and an open permission request (ErrRelayModeOff
	// guards the relay_mode=off path; ErrNoOpenPermissionRequest guards the
	// missing-request path).
	{
		verb: "decide",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			s, dbPath := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			return filepath.Dir(dbPath), map[string]any{"id": "id-d-1"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": ctx["id"],
				"request_token":      storefix.TestRequestTokenA,
				"decision":           "allow",
			}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"decide",
				"--claude-instance-id", ctx["id"].(string),
				"--request-token", storefix.TestRequestTokenA,
				"--decision", "allow",
			}
		},
	},

	// ── get-permission ────────────────────────────────────────────────────
	// Token-only lookup: seed an open permission_requests row and read it
	// back. The result carries 8 fields; nondeterminism.json must exclude
	// request_id (autoincrement) and requested_at (CURRENT_TIMESTAMP).
	{
		verb: "get-permission",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			s, dbPath := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"request_token": storefix.TestRequestTokenA}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"get-permission", "--request-token", storefix.TestRequestTokenA}
		},
	},

	// ── resume ────────────────────────────────────────────────────────────
	// resume needs:
	//   1. An ended row with ClaudeSessionID set (so the JSONL Stat can run).
	//   2. A JSONL transcript at HOME/.claude/projects/<slug(cwd)>/<sessID>.jsonl.
	//
	// extraSetup creates the JSONL inside homeDir after the test driver has
	// pointed HOME there via t.Setenv.  fake-tmux handles new-session
	// (has-session exits 1 = "absent"; new-session exits 0 = success).
	//
	// The result {claude_instance_id: "id-resume-1"} is fully deterministic;
	// nondeterministic.json lists no excluded fields for resume.
	{
		verb: "resume",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			const (
				id      = "id-resume-1"
				sessID  = "session-uuid-resume-1"
				cwd     = "/tmp"
			)
			s, dbPath := apitest.OpenStoreWithRow(t,
				id, "cd-resume-1", store.StateEnded, "off")
			if err := s.SetSessionID(id, sessID); err != nil {
				t.Fatalf("resume seed: SetSessionID: %v", err)
			}
			return filepath.Dir(dbPath), map[string]any{
				"id":     id,
				"sessID": sessID,
				"cwd":    cwd,
			}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": ctx["id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"resume",
				"--claude-instance-id", ctx["id"].(string),
			}
		},
		extraSetup: func(t *testing.T, _ string, ctx map[string]any) {
			// HOME has been pointed at homeDir by the test driver before
			// this call.  apitest.SeedJsonl calls spawn.JsonlPath which
			// calls os.UserHomeDir(), so the file lands at
			// HOME/.claude/projects/<slug(cwd)>/<sessID>.jsonl — the exact
			// path the resume verb Stat's during its pre-flight check.
			t.Helper()
			apitest.SeedJsonl(t,
				ctx["cwd"].(string),
				ctx["sessID"].(string))
		},
	},

	// ── find-missing ──────────────────────────────────────────────────────
	// An empty store has no live rows; find-missing returns {count:0,ids:null}
	// on both CLI and Client without touching tmux.
	{
		verb: "find-missing",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dir := t.TempDir()
			apitest.SeedStore(t, filepath.Join(dir, "state.db"))
			return dir, nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"find-missing"}
		},
	},

	// ── expire ────────────────────────────────────────────────────────────
	// SeedExpireFixture seeds 5 rows including two backdated terminal rows
	// (ended_at = now-2h) that the "--older-than 1h" window will catch.
	// The result {count:2, ids:["row-ended-old","row-missing-old"]} is
	// deterministic (sorted IDs, stable rows).
	{
		verb: "expire",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedExpireFixture(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"older_than": "1h"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"expire", "--older-than", "1h"}
		},
	},

	// ── delete ────────────────────────────────────────────────────────────
	// delete one ended row; result is {results:{"row-ended":"ok"}}.
	{
		verb: "delete",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedDeleteFixture(t)
			return filepath.Dir(dbPath), map[string]any{"id": "row-ended"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{
				"claude_instance_id": []string{ctx["id"].(string)},
			}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"delete",
				"--claude-instance-id", ctx["id"].(string),
			}
		},
	},

	// ── make-template ─────────────────────────────────────────────────────
	// make-template overwrite-existing: SeedErrTemplateExists pre-populates
	// HOME/.agent-director/templates/<name>.toml so the --overwrite path
	// exercises the atomic rename(2) write algorithm (SR-1.7) on a real
	// collision.  Without --overwrite this would yield ErrTemplateExists;
	// with it, the encoded body replaces the pre-existing file and the verb
	// returns its absolute path.  The ".path" field is excluded from diff
	// (nondeterministic.json) because the path embeds the ephemeral homeDir.
	{
		verb: "make-template",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			srcDir, name := apitest.SeedErrTemplateExists(t)
			return srcDir, map[string]any{"name": name}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{
				"name":      ctx["name"],
				"overwrite": true,
			}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"make-template",
				"--name", ctx["name"].(string),
				"--overwrite",
			}
		},
	},

	// ── list ──────────────────────────────────────────────────────────────
	// SeedListFixture inserts 6 rows with mixed states/labels/cwds.
	// Both CLI and Client read from identical SQLite copies, so row order
	// is the same on both sides (SQLite returns rows in rowid order for
	// queries without ORDER BY when the physical storage is identical).
	{
		verb: "list",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedListFixture(t)
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"list"}
		},
	},

	// ── pause ─────────────────────────────────────────────────────────────
	// pause on an ended row is a no-op success per SRD §9 ("Terminal states
	// are no-op success").  No tmux interaction occurs; result is empty ({}).
	{
		verb: "pause",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t,
				"id-pause-1", "cd-pause-1", store.StateEnded, "off")
			return filepath.Dir(dbPath), map[string]any{"id": "id-pause-1"}
		},
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"claude_instance_id": ctx["id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"pause",
				"--claude-instance-id", ctx["id"].(string),
			}
		},
	},

	// ── version ───────────────────────────────────────────────────────────
	// version is handle-free; no DB rows needed. ".version" and ".commit"
	// are excluded from diff (nondeterministic.json) because they embed
	// build-time stamps.  An empty store satisfies the Client open.
	{
		verb: "version",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dir := t.TempDir()
			apitest.SeedStore(t, filepath.Join(dir, "state.db"))
			return dir, nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"version"}
		},
	},
}

// ── completeness guard ────────────────────────────────────────────────────────

func init() {
	callable := manifest.CallableVerbs()

	// Index callable verbs for O(1) lookup.
	callableSet := make(map[string]bool, len(callable))
	for _, v := range callable {
		callableSet[v.Name] = true
	}

	// Each case must reference a callable verb with no duplicates.
	seen := make(map[string]bool, len(successCases))
	for _, sc := range successCases {
		if !callableSet[sc.verb] {
			panic(fmt.Sprintf(
				"envelope_diff: successCases: %q is not a callable verb", sc.verb))
		}
		if seen[sc.verb] {
			panic(fmt.Sprintf(
				"envelope_diff: successCases: duplicate entry for verb %q", sc.verb))
		}
		seen[sc.verb] = true
	}

	// Every callable verb must have exactly one case.
	for _, v := range callable {
		if !seen[v.Name] {
			panic(fmt.Sprintf(
				"envelope_diff: successCases: missing entry for callable verb %q", v.Name))
		}
	}
}

// lookupSuccessCase returns the successCase for verb, or (zero, false) when
// no entry exists.
func lookupSuccessCase(verb string) (successCase, bool) {
	for _, sc := range successCases {
		if sc.verb == verb {
			return sc, true
		}
	}
	return successCase{}, false
}
