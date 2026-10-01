package api_test

// spawn_reuse_test.go covers SpawnParams.ReuseFinished (SR-10.1, AC-REUSE-13)
// at the parameter level: with no explicit id it is an ordinary fresh spawn;
// a control-character id is still ErrInvalidFlags; without the opt-in a
// finished row still collides with ErrInstanceIdCollision.

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// finishedStates are the row states the reuse opt-in is about.
var finishedStates = []string{store.StateEnded, store.StateMissing}

// reuseRowState is everything about one row a reuse could change: its
// columns, its session history over every life and its permission requests.
type reuseRowState struct {
	cols    apitest.SpawnColumns
	history []apitest.HistoryEntry
	perms   []api.PermissionRow
}

// readReuseRowState reads id's reuseRowState from the store at dbPath.
func readReuseRowState(t *testing.T, dbPath, id string) reuseRowState {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	history, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	perms, err := st.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn(%s): %v", id, err)
	}
	return reuseRowState{cols: cols, history: history, perms: perms}
}

// seedFinishedRow seeds a finished row in state with a session id, one
// archived history entry and one permission request, and returns its id and
// its reuseRowState.
func seedFinishedRow(t *testing.T, dbPath, state string) (string, reuseRowState) {
	t.Helper()
	id := "reuse-" + uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", uuid.NewString(), false,
		apitest.WithLifeNumber(1),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: uuid.NewString(), JSONLPath: "/tmp/old.jsonl", Life: 1})); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	if _, err := apitest.SeedPermissionRequest(dbPath, id, "Bash"); err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return id, readReuseRowState(t, dbPath, id)
}

// assertRowStateUnchanged fails unless id's reuseRowState still equals before.
func assertRowStateUnchanged(t *testing.T, dbPath, id string, before reuseRowState) {
	t.Helper()
	if after := readReuseRowState(t, dbPath, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row %s changed:\nbefore %+v\nafter  %+v", id, before, after)
	}
}

// TestSpawnReuseFinishedWithoutIDIsFreshSpawn: with no explicit id the opt-in
// has no effect: a minted id, one pending row, one create, and a finished row
// with another id left as it was.
func TestSpawnReuseFinishedWithoutIDIsFreshSpawn(t *testing.T) {
	for _, state := range finishedStates {
		t.Run(state, func(t *testing.T) {
			env := newSpawnEnv(t)
			other, before := seedFinishedRow(t, env.dbPath, state)

			res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ReuseFinished: true})

			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			got := res.ClaudeInstanceID
			if _, perr := uuid.Parse(got); perr != nil {
				t.Errorf("minted id %q is not a UUID: %v", got, perr)
			}
			cols, err := apitest.ReadSpawnColumns(env.dbPath, got)
			if err != nil || cols.State != store.StatePending {
				t.Errorf("new row state = %v (err %v); want pending", cols.State, err)
			}
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("create calls = %d; want 1", n)
			}
			if ids := listIDs(t, env.c); len(ids) != 2 {
				t.Errorf("List ids = %q; want the finished row and one new row", ids)
			}
			assertRowStateUnchanged(t, env.dbPath, other, before)
		})
	}
}

// TestSpawnReuseFinishedControlCharacterID: the opt-in does not bypass the
// control-character check: ErrInvalidFlags, no row and no tmux call.
func TestSpawnReuseFinishedControlCharacterID(t *testing.T) {
	const marker = "reusemark"
	for _, tc := range []struct{ name, ctl string }{
		{"newline", "\n"},
		{"tab", "\t"},
		{"DEL", "\x7f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			id := marker + tc.ctl + "tail"

			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id, ReuseFinished: true})

			assertInvalidFlags(t, err, id, marker)
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want none", ids)
			}
		})
	}
}

// TestSpawnFinishedRowCollidesWithoutReuse: without the opt-in an ended or
// missing row is ErrInstanceIdCollision; the row, its history and permission
// requests are unchanged and no session is created.
func TestSpawnFinishedRowCollidesWithoutReuse(t *testing.T) {
	for _, state := range finishedStates {
		t.Run(state, func(t *testing.T) {
			env := newSpawnEnv(t)
			id, before := seedFinishedRow(t, env.dbPath, state)

			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id, ReuseFinished: false})

			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			assertRowStateUnchanged(t, env.dbPath, id, before)
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("create calls = %d; want 0", n)
			}
		})
	}
}
