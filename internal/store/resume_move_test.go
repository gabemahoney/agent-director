package store_test

// Store tests for resume's move to pending (SR-8.3, SR-5.3, SR-5.6, SR-5.8,
// SR-20.6): the applied write's cleared, set and kept columns and returned
// version, and the changed, absent and store-error outcomes that write
// nothing. Rows are seeded through apitest (SR-20.2); the restore's tests
// (resume_restore_test.go) reuse seedMoveRow and the move* fixtures below.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The launch the move records: its start, new token and socket, all distinct
// from the seeded row's values so "set" is never vacuous.
const (
	moveLaunchMillis int64 = 1790000000123
	moveToken              = "fedcba9876543210"
	moveSocket             = "/tmp/ad-move-test/new-sock"
)

// moveSessionID is the seeded row's current claude_session_id.
const moveSessionID = "sess-move-cur"

// moveIdentity is the seeded row's previous launch identity, every field set.
func moveIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{
		Token:           "0011223344556677",
		Socket:          "/tmp/ad-move-test/old-sock",
		ServerPID:       333,
		ServerStart:     1767225600,
		ServerStarttime: apitest.LinuxProcStarttime,
		PaneID:          "%9",
		PanePID:         444,
		PaneStarttime:   apitest.DarwinProcStarttime,
	}
}

// moveSpec overrides seedMoveRow's defaults; the zero value is the full
// ended row.
type moveSpec struct {
	state     string                // "" = ended
	noSession bool                  // NULL claude_session_id
	noPID     bool                  // NULL pid
	noJsonl   bool                  // NULL jsonl_path
	opts      []apitest.SpawnOption // after the defaults; a later option wins
}

// moveRow is a seeded row as resume examines it before the move.
type moveRow struct {
	id       string
	parent   string                 // the seeded parent_id
	examined store.Spawn            // GetSpawn after seeding
	before   apitest.SpawnColumns   // raw columns after seeding
	history  []apitest.HistoryEntry // session_history after seeding
}

// seedMoveRow seeds a row with a value in every column the move clears or keeps
// (non-default layouts, raw text), a parent and a history entry, read as resume would.
func seedMoveRow(t *testing.T, f *v5Store, spec moveSpec) moveRow {
	t.Helper()
	state, session := spec.state, moveSessionID
	if state == "" {
		state = store.StateEnded
	}
	if spec.noSession {
		session = ""
	}
	opts := []apitest.SpawnOption{
		apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"),
		apitest.WithEndedAt("2026-09-29T01:02:03.5Z"),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithLivenessUnverifiedSince("2026-09-28 10:00:00"),
		apitest.WithLivenessNote("move-test note"),
		apitest.WithLaunchIdentity(moveIdentity()),
		apitest.WithLaunchStartedAt(1767225600999),
		apitest.WithLifeNumber(3),
		apitest.WithNoPreTrust(),
		apitest.WithTmuxSessionName("move-ts"),
		apitest.WithRawLabels(`{"team":  "a", "b":"2"}`),
		apitest.WithRawClaudeArgs(`["--model",   "opus"]`),
		apitest.WithRawExtraEnv(`{"K":  "v"}`),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID: "sess-move-old", JSONLPath: "/tmp/ad-move-test/old.jsonl", Life: 2,
			RecordedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		}),
	}
	if !spec.noPID {
		opts = append(opts, apitest.WithPID(4242))
	}
	if !spec.noJsonl {
		opts = append(opts, apitest.WithJsonlPath("/tmp/ad-move-test/cur.jsonl"))
	}
	id := f.seed(state, session, append(opts, spec.opts...)...)
	parent := f.seed(store.StateWaiting, "")
	if err := apitest.SeedParentChild(f.path, parent, id); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	examined, err := f.s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return moveRow{id: id, parent: parent, examined: examined, before: f.rawColumns(id), history: f.historyAllLives(id)}
}

// move runs MoveToPending on r's examined snapshot with the test launch and
// parentID.
func (f *v5Store) move(r moveRow, parentID string) (store.CondResult, int64, error) {
	return f.s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, parentID)
}

// moveCleared are the columns the move sets to NULL.
func moveCleared(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"pid": c.PID, "proc_starttime": c.ProcStarttime, "ended_at": c.EndedAt,
		"liveness_unverified_since": c.LivenessUnverifiedSince, "liveness_note": c.LivenessNote,
		"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID,
		"pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime,
	}
}

// moveKept are the columns the move leaves exactly as stored.
func moveKept(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"claude_session_id": c.ClaudeSessionID, "jsonl_path": c.JSONLPath,
		"life_number": c.LifeNumber, "no_pre_trust": c.NoPreTrust,
		"started_at": c.StartedAt, "last_seen_at": c.LastSeenAt, "cwd": c.CWD,
		"tmux_session_name": c.TmuxSessionName, "claude_args": c.ClaudeArgs,
		"relay_mode": c.RelayMode, "labels": c.Labels, "extra_env": c.ExtraEnv,
	}
}

// requireSet fails when any of cols is NULL, so a cleared/kept check is not vacuous.
func requireSet(t *testing.T, cols map[string]any) {
	t.Helper()
	for name, v := range cols {
		if v == nil {
			t.Fatalf("seed left %s NULL; the check would be vacuous", name)
		}
	}
}

// assertMoveWroteNothing fails unless id's row and history read exactly as before.
func assertMoveWroteNothing(t *testing.T, f *v5Store, id string, before apitest.SpawnColumns, history []apitest.HistoryEntry) {
	t.Helper()
	if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed by a move that did not apply:\n before %+v\n after  %+v", before, after)
	}
	if got := f.historyAllLives(id); !reflect.DeepEqual(got, history) {
		t.Errorf("history %+v -> %+v; want unchanged", history, got)
	}
}

// TestMoveToPendingApplied checks the applied move on ended and missing rows:
// the returned version is stored, cleared columns are NULL, the launch is
// set and every kept column and the history are byte for byte unchanged.
func TestMoveToPendingApplied(t *testing.T) {
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, moveSpec{state: state})
			requireSet(t, moveCleared(r.before))
			requireSet(t, moveKept(r.before))
			if len(r.history) == 0 {
				t.Fatal("seed wrote no history; the history check would be vacuous")
			}
			newParent := f.seed(store.StateWaiting, "")

			res, v, err := f.move(r, newParent)
			if err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
			}
			after := f.rawColumns(r.id)
			if want := r.examined.Snapshot.RowVersion + 1; v != want || after.RowVersion != want {
				t.Errorf("movedVersion %d, stored row_version %#v; want both %d", v, after.RowVersion, want)
			}
			if after.State != store.StatePending {
				t.Errorf("state = %#v; want pending", after.State)
			}
			for col, got := range moveCleared(after) {
				if got != nil {
					t.Errorf("%s = %#v; want NULL", col, got)
				}
			}
			set := map[string][2]any{
				"launch_started_at": {after.LaunchStartedAt, moveLaunchMillis},
				"launch_token":      {after.LaunchToken, moveToken},
				"tmux_socket":       {after.TmuxSocket, moveSocket},
				"parent_id":         {after.ParentID, newParent},
			}
			for col, gw := range set {
				if gw[0] != gw[1] {
					t.Errorf("%s = %#v; want %#v", col, gw[0], gw[1])
				}
			}
			if got, want := moveKept(after), moveKept(r.before); !reflect.DeepEqual(got, want) {
				t.Errorf("kept columns changed:\n before %#v\n after  %#v", want, got)
			}
			if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want unchanged", r.history, got)
			}
		})
	}
}

// TestMoveToPendingParentAndLaunchInputs checks an empty parent id stores
// NULL and a row with no recorded token or socket gets the passed ones.
func TestMoveToPendingParentAndLaunchInputs(t *testing.T) {
	cases := []struct {
		name       string
		opts       []apitest.SpawnOption
		parent     bool // pass an existing row as parent; false passes ""
		wantParent bool // parent_id set after the move
	}{
		{"empty parent id stores NULL", nil, false, false},
		{"row with no token or socket", []apitest.SpawnOption{apitest.WithNoLaunchToken()}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, moveSpec{opts: tc.opts})
			parentID := ""
			if tc.parent {
				parentID = f.seed(store.StateWaiting, "")
			}
			if res, v, err := f.move(r, parentID); err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
			}
			after := f.rawColumns(r.id)
			if tc.wantParent && after.ParentID != parentID {
				t.Errorf("parent_id = %#v; want %q", after.ParentID, parentID)
			}
			if !tc.wantParent && after.ParentID != nil {
				t.Errorf("parent_id = %#v; want NULL", after.ParentID)
			}
			if after.LaunchToken != moveToken || after.TmuxSocket != moveSocket {
				t.Errorf("launch_token, tmux_socket = %#v, %#v; want %q, %q", after.LaunchToken, after.TmuxSocket, moveToken, moveSocket)
			}
		})
	}
}

// TestMoveToPendingChangedWritesNothing checks every changed-row case (each
// snapshot field, a non-finished state, an intervening write, the losing
// move of a race) returns CondChanged, version 0, and writes nothing.
func TestMoveToPendingChangedWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		spec   moveSpec
		mutate func(*store.RowSnapshot)                  // the examined snapshot's difference
		write  func(t *testing.T, f *v5Store, r moveRow) // a write after examination
	}{
		{name: "row_version ahead", mutate: func(s *store.RowSnapshot) { s.RowVersion++ }},
		{name: "row_version behind", mutate: func(s *store.RowSnapshot) { s.RowVersion-- }},
		{name: "started_at same instant, other text", mutate: func(s *store.RowSnapshot) { s.StartedAt = "2026-03-04T05:06:07.25+02:00" }},
		{name: "started_at re-formatted to the store layout", mutate: func(s *store.RowSnapshot) { s.StartedAt = "2026-03-04 03:06:07" }},
		{name: "claude_session_id differs", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "sess-other" }},
		{name: "claude_session_id empty, row has one", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "" }},
		{name: "claude_session_id NULL, examined set", spec: moveSpec{noSession: true},
			mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = moveSessionID }},
		{name: "pid differs", mutate: func(s *store.RowSnapshot) { s.PID++ }},
		{name: "pid zero, row has one", mutate: func(s *store.RowSnapshot) { s.PID = 0 }},
		{name: "pid NULL, examined set", spec: moveSpec{noPID: true}, mutate: func(s *store.RowSnapshot) { s.PID = 4242 }},
		{name: "proc_starttime differs", mutate: func(s *store.RowSnapshot) { s.ProcStarttime = apitest.DarwinProcStarttime }},
		{name: "proc_starttime empty, row has one", mutate: func(s *store.RowSnapshot) { s.ProcStarttime = "" }},
		{name: "tmux_session_name differs", mutate: func(s *store.RowSnapshot) { s.TmuxSessionName = "move-ts-2" }},
		{name: "pending row", spec: moveSpec{state: store.StatePending}},
		{name: "waiting row", spec: moveSpec{state: store.StateWaiting}},
		{name: "working row", spec: moveSpec{state: store.StateWorking}},
		{name: "HealJsonlPath after examination", spec: moveSpec{noJsonl: true},
			write: func(t *testing.T, f *v5Store, r moveRow) {
				ok, err := f.s.HealJsonlPath(r.id, moveSessionID, "/tmp/ad-move-test/healed.jsonl")
				wantBool(t, "HealJsonlPath", ok, err, true)
			}},
		{name: "SetParentID after examination",
			write: func(t *testing.T, f *v5Store, r moveRow) {
				if err := f.s.SetParentID(r.id, f.seed(store.StateWaiting, "")); err != nil {
					t.Fatalf("SetParentID: %v", err)
				}
			}},
		{name: "second move from the same snapshot loses",
			write: func(t *testing.T, f *v5Store, r moveRow) {
				if res, _, err := f.move(r, ""); err != nil || res != store.CondApplied {
					t.Fatalf("first MoveToPending = %v, %v; want CondApplied", res, err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, tc.spec)
			if tc.mutate != nil {
				tc.mutate(&r.examined.Snapshot)
			}
			before, history := r.before, r.history
			if tc.write != nil {
				tc.write(t, f, r)
				before, history = f.rawColumns(r.id), f.historyAllLives(r.id)
				if before.RowVersion == r.before.RowVersion {
					t.Fatal("the intervening write did not advance row_version")
				}
			}
			res, v, err := f.move(r, f.seed(store.StateWaiting, ""))
			if err != nil || res != store.CondChanged || v != 0 {
				t.Fatalf("MoveToPending = %v, %d, %v; want CondChanged, 0, nil", res, v, err)
			}
			assertMoveWroteNothing(t, f, r.id, before, history)
		})
	}
}

// TestMoveToPendingAbsent checks a row deleted after examination gives
// CondAbsent, version 0, and the move creates no row.
func TestMoveToPendingAbsent(t *testing.T) {
	f := newV5Store(t)
	r := seedMoveRow(t, f, moveSpec{})
	if err := f.s.DeleteSpawn(r.id); err != nil {
		t.Fatalf("DeleteSpawn: %v", err)
	}
	res, v, err := f.move(r, r.parent)
	if err != nil || res != store.CondAbsent || v != 0 {
		t.Fatalf("MoveToPending = %v, %d, %v; want CondAbsent, 0, nil", res, v, err)
	}
	if _, err := apitest.ReadSpawnColumns(f.path, r.id); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns after the move: %v; want ErrSpawnNotFound", err)
	}
}

// TestMoveToPendingStoreErrors checks a parent id naming no row and a closed
// store each return an error, no CondResult or version, and write nothing.
func TestMoveToPendingStoreErrors(t *testing.T) {
	cases := []struct {
		name   string
		parent string
		store  func(t *testing.T, f *v5Store) *store.Store // the store the move runs on
	}{
		{"parent id names no row", "no-such-parent", func(_ *testing.T, f *v5Store) *store.Store { return f.s }},
		{"closed store", "", func(t *testing.T, f *v5Store) *store.Store {
			s, err := store.Open(f.path)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, moveSpec{})
			s := tc.store(t, f)
			res, v, err := s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, tc.parent)
			if err == nil || res != 0 || v != 0 {
				t.Fatalf("MoveToPending = %v, %d, %v; want an error with no CondResult and version 0", res, v, err)
			}
			assertMoveWroteNothing(t, f, r.id, r.before, r.history)
		})
	}
}
