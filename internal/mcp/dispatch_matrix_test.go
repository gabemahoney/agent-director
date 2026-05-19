package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api/manifest"
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/mcp"
	"github.com/gabemahoney/claude-director/internal/spawn"
	"github.com/gabemahoney/claude-director/internal/store"
	"github.com/gabemahoney/claude-director/internal/tmux"
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

		"list":         {args: `{"limit":10}`},
		"find-missing": {args: `{}`},
		"expire":       {args: `{"older_than":"7d"}`},

		// make-template needs an isolated HOME so we don't litter the
		// developer's ~/.claude-director/templates/ on test runs. The
		// per-subtest t.Setenv handles that.
		"make-template": {args: `{"name":"dispatch-matrix-test"}`},

		"delete": {args: `{"claude_instance_id":["` + matrixID + `"]}`, setup: seedEnded("off")},
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
			// touches ~/.claude-director/) writes into a temp dir that
			// the runtime cleans up.
			t.Setenv("HOME", t.TempDir())

			s := openMatrixStore(t)
			if tc.setup != nil {
				tc.setup(t, s)
			}
			d := mcp.NewLiveDispatcher(s, tmux.New(), config.Default())

			_, err := d.Call(context.Background(), mcp.ToolName(v.Name), json.RawMessage(tc.args))

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

// openMatrixStore opens a fresh on-disk SQLite store under t.TempDir().
// Used per-subtest so verb cases don't see each other's seeded rows.
func openMatrixStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
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
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			t.Fatalf("ApplyHookTransition(%s): %v", state, err)
		}
	}
}
