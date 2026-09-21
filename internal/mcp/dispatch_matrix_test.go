package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// matrixID is the sentinel claude_instance_id used across every verb
// case. Tests seed a row at this id and pass the same id in the JSON
// args. If a future refactor drops a `json:"..."` tag on the
// dispatcher's params struct, the id field decodes to empty (Go's
// case-insensitive matcher does not bridge snake_case ↔ CamelCase) and
// the verb's store lookup returns ErrSpawnNotFound — the regression
// signal this matrix is designed to catch.
const matrixID = "DISPATCH-MATRIX-TEST-ID"

// matrixCase is one verb's recipe: the JSON args sent through the
// dispatcher and an optional store seed. setup may be nil for verbs
// that don't lookup by id (help, list, find-missing, expire,
// make-template).
type matrixCase struct {
	args  string
	setup func(t *testing.T, s *store.Store)
}

// matrixCases returns one entry per MCP-exposed verb. The test walks
// manifest.Verbs and looks up each verb's entry here. A new verb added
// to the manifest without a corresponding entry fails the walk — that
// is the auto-coverage drift gate from AC #2.
func matrixCases() map[string]matrixCase {
	seedEnded := func(relayMode string) func(t *testing.T, s *store.Store) {
		return func(t *testing.T, s *store.Store) {
			seedMatrixSpawn(t, s, matrixID, store.StateEnded, relayMode)
		}
	}
	return map[string]matrixCase{
		"help":    {args: `{}`},
		"version": {args: `{}`},

		// spawn: a non-existent cwd makes Validate return ErrCwdNotFound
		// before tmux is touched, so the test has no real-tmux side
		// effect. The round-trip signal here is the cwd field — a
		// dropped json tag would leave CWD empty and surface
		// ErrCwdMissing instead.
		"spawn": {args: `{"cwd":"/this/path/does/not/exist","claude_instance_id":"` + matrixID + `"}`},

		"status":    {args: `{"claude_instance_id":"` + matrixID + `"}`, setup: seedEnded("off")},
		"get":       {args: `{"claude_instance_id":"` + matrixID + `"}`, setup: seedEnded("off")},
		"send-keys": {args: `{"claude_instance_id":"` + matrixID + `","text":"hi"}`, setup: seedEnded("off")},
		"read-pane": {args: `{"claude_instance_id":"` + matrixID + `","n_lines":1,"ansi":false}`, setup: seedEnded("off")},
		"kill":      {args: `{"claude_instance_id":"` + matrixID + `"}`, setup: seedEnded("off")},
		"pause":     {args: `{"claude_instance_id":"` + matrixID + `"}`, setup: seedEnded("off")},
		"resume":    {args: `{"claude_instance_id":"` + matrixID + `"}`, setup: seedEnded("off")},

		// decide: relay_mode must be "on" so the row passes the
		// ErrRelayModeOff check after the id lookup succeeds.
		"decide": {args: `{"claude_instance_id":"` + matrixID + `","decision":"allow","reason":"ok"}`, setup: seedEnded("on")},

		// get-permission: token-only lookup. A random UUIDv4 against an
		// empty store returns ErrPermissionRequestNotFound — that's the
		// dispatch-decode regression signal (a dropped json:"request_token"
		// tag would surface as the same not-found error after a missing-
		// token rejection at the CLI, but at the dispatch layer the empty
		// string is passed through to the store, which still returns
		// not-found and proves the JSON shape decoded cleanly).
		"get-permission": {args: `{"request_token":"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"}`},

		"list":         {args: `{"limit":10}`},
		"find-missing": {args: `{}`},
		"expire":       {args: `{"older_than":"7d"}`},

		// make-template needs an isolated HOME so we don't litter the
		// developer's ~/.agent-director/templates/ on test runs. The
		// per-subtest t.Setenv handles that.
		"make-template": {args: `{"name":"dispatch-matrix-test"}`},

		"delete": {args: `{"claude_instance_id":["` + matrixID + `"]}`, setup: seedEnded("off")},

		// No entry for trail-emit (nor hook/serve): ExposedVerb excludes
		// those verbs from the MCP surface (server.go:318), so the matrix
		// walk skips them (see the !mcp.ExposedVerb continue below) and
		// they never need a dispatch case.
	}
}

// TestToolsCallDispatchMatrix walks every MCP-exposed verb and verifies
// the dispatcher's snake_case JSON → typed-struct decode is intact. The
// bug this guards against: a json tag missing from a dispatcher params
// struct silently leaves the field at the zero value, so a verb sees
// empty `claude_instance_id` and returns ErrSpawnNotFound for every
// call. Pre-fix, the existing tests caught only the spawn verb; this
// matrix extends the regression net to every exposed verb.
//
// Walking manifest.Verbs at runtime means adding a new verb auto-
// extends coverage — the lookup against matrixCases() forces the
// author to add a parallel test case.
func TestToolsCallDispatchMatrix(t *testing.T) {
	cases := matrixCases()

	for _, v := range manifest.Verbs {
		if !mcp.ExposedVerb(v.Name) {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			tc, ok := cases[v.Name]
			if !ok {
				t.Fatalf("missing dispatch matrix case for MCP-exposed verb %q — add an entry to matrixCases() so the regression net stays in sync with manifest.Verbs", v.Name)
			}

			// Isolate HOME so make-template (and any future verb that
			// touches ~/.agent-director/) writes into a temp dir that
			// the runtime cleans up.
			t.Setenv("HOME", t.TempDir())

			dir := t.TempDir()
			storePath := filepath.Join(dir, "state.db")
			cfgPath := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			// Seed the store via a short-lived handle before the client
			// opens it, matching the newTestClientWithRows pattern used in
			// pkg/api/methods_test.go.
			if tc.setup != nil {
				s, err := store.OpenOrInit(storePath)
				if err != nil {
					t.Fatalf("store.OpenOrInit: %v", err)
				}
				tc.setup(t, s)
				_ = s.Close()
			}

			client, err := api.New(api.Options{
				StorePath:       storePath,
				ConfigPath:      cfgPath,
				CreateIfMissing: true,
			})
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			d := mcp.NewLiveDispatcher(client)

			_, err = d.Call(context.Background(), mcp.ToolName(v.Name), json.RawMessage(tc.args))

			// Round-trip proof for verbs taking claude_instance_id: the
			// store was seeded at matrixID, so a healthy decode finds
			// the row. A dropped json tag silently leaves the id empty
			// and the lookup returns ErrSpawnNotFound — that's the
			// regression signal.
			if tc.setup != nil && errors.Is(err, store.ErrSpawnNotFound) {
				t.Fatalf("dispatcher returned ErrSpawnNotFound after a seeded row at id=%q — claude_instance_id did not round-trip from JSON to the dispatcher's typed params struct (check the verb's params struct still carries the json:\"claude_instance_id\" tag): %v", matrixID, err)
			}

			// Spawn round-trip is via cwd, not the seeded id: a healthy
			// decode hits ErrCwdNotFound (cwd is set, just nonexistent);
			// a dropped json tag would leave cwd empty and surface
			// ErrCwdMissing first.
			if v.Name == "spawn" && errors.Is(err, spawn.ErrCwdMissing) {
				t.Fatalf("dispatcher returned ErrCwdMissing — the cwd field did not round-trip from JSON to the dispatcher's spawn params struct (check the json:\"cwd\" tag on the dispatcher's local struct): %v", err)
			}

			// Catch direct decode errors too — if a future refactor
			// outright breaks json.Unmarshal (e.g. by typing a struct
			// field as a non-decodable type) we want a loud failure.
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "decode params") ||
					strings.Contains(msg, "decode spawn params") ||
					strings.Contains(msg, "decode status params") ||
					strings.Contains(msg, "decode get params") ||
					strings.Contains(msg, "decode list params") ||
					strings.Contains(msg, "decode make-template params") ||
					strings.Contains(msg, "decode expire params") ||
					strings.Contains(msg, "decode delete params") ||
					strings.Contains(msg, "json: cannot unmarshal") {
					t.Fatalf("dispatcher decode failure on verb %q: %v", v.Name, err)
				}
			}
		})
	}
}

// TestNoMigrationTriggerTool is the SR-1.6 public-surface guard for the MCP
// dispatch surface: no MCP-exposed tool may be an agent-reachable schema-
// migration trigger. Migration runs only under an out-of-band administrator
// sentinel (internal/store/migrate_auth.go); there is deliberately NO migrate
// tool and NO dispatch case for one.
//
// The assertion is computed live from manifest.Verbs filtered by
// mcp.ExposedVerb + mcp.ToolName — the same path buildToolList uses to emit the
// tools/list response — so it reflects the true MCP surface, not a golden. A
// migrate verb added to the manifest and exposed would surface a "migrate" (or
// "migrate_schema") tool name here and trip the check. It also drives a live
// dispatcher Call for any such tool name to prove no hidden handler answers it.
func TestNoMigrationTriggerTool(t *testing.T) {
	for _, v := range manifest.Verbs {
		if !mcp.ExposedVerb(v.Name) {
			continue
		}
		tool := mcp.ToolName(v.Name)
		if strings.Contains(strings.ToLower(tool), "migrate") {
			t.Errorf("MCP exposes tool %q (from verb %q) whose name implies a migration trigger; "+
				"SR-1.6 forbids any agent-reachable migration tool", tool, v.Name)
		}
	}

	// Belt-and-suspenders: a hidden dispatch handler keyed on a migrate tool
	// name (bypassing the manifest) must not answer. A well-behaved dispatcher
	// returns ErrUnknownTool for an unregistered name; anything else means a
	// migration entry point leaked into dispatch.go.
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.db")
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	client, err := api.New(api.Options{
		StorePath:       storePath,
		ConfigPath:      cfgPath,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	d := mcp.NewLiveDispatcher(client)
	for _, name := range []string{"migrate", "migrate_schema", "schema_migrate"} {
		_, err := d.Call(context.Background(), mcp.ToolName(name), json.RawMessage(`{}`))
		if !errors.Is(err, mcp.ErrUnknownTool) {
			t.Errorf("dispatcher answered migrate-shaped tool %q with err=%v; want ErrUnknownTool "+
				"(no migration handler may exist on the MCP surface)", name, err)
		}
	}
}

// seedMatrixSpawn inserts one Spawn row at the requested state and
// relay_mode. Mirrors the openStoreWithRow helper in
// internal/api/sendkeys_test.go but lives here to avoid an
// import cycle (mcp_test cannot import api_test).
func seedMatrixSpawn(t *testing.T, s *store.Store, id, state, relayMode string) {
	t.Helper()
	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-test-" + id,
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false, "test_seed"); err != nil {
			t.Fatalf("ApplyHookTransition(%s): %v", state, err)
		}
	}
}
