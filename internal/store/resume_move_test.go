package store_test

// Resume's move to pending (SR-8.3, SR-5.3, SR-5.6): the applied write's
// cleared, set and kept columns and returned version. Its refusals are
// row_version_test.go's no-op cases, its snapshot guard is
// find_missing_writes_test.go's matrix and its store errors are in
// store_errors_test.go. The restore's tests reuse seedMoveRow.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The launch the move records, unlike every seeded value.
const (
	moveLaunchMillis int64 = 1790000000123
	moveToken              = "fedcba9876543210"
	moveSocket             = "/tmp/ad-move-test/new-sock"
	moveSessionID          = "sess-move-cur"
)

// moveIdentity is the seeded row's previous launch identity, every field set.
func moveIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: "0011223344556677", Socket: "/tmp/ad-move-test/old-sock",
		ServerPID: 333, ServerStart: 1767225600, ServerStarttime: apitest.LinuxProcStarttime,
		PaneID: "%9", PanePID: 444, PaneStarttime: apitest.DarwinProcStarttime}
}

// moveSpec overrides seedMoveRow's full ended row.
type moveSpec struct {
	state string                // "" = ended
	opts  []apitest.SpawnOption // after the defaults; a later option wins
}

// moveRow is a seeded row as resume examines it before the move.
type moveRow struct {
	id       string
	examined store.Spawn            // GetSpawn after seeding
	before   apitest.SpawnColumns   // raw columns after seeding
	history  []apitest.HistoryEntry // session_history after seeding
}

// seedMoveRow seeds a row with a value in every column the move clears or
// keeps (non-default layouts, raw text), a parent and a history entry.
func seedMoveRow(t *testing.T, f *v5Store, spec moveSpec) moveRow {
	t.Helper()
	state := spec.state
	if state == "" {
		state = store.StateEnded
	}
	opts := []apitest.SpawnOption{
		apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"), apitest.WithEndedAt("2026-09-29T01:02:03.5Z"),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime), apitest.WithLivenessUnverifiedSince("2026-09-28 10:00:00"),
		apitest.WithLivenessNote("move-test note"), apitest.WithLaunchIdentity(moveIdentity()),
		apitest.WithLaunchStartedAt(1767225600999), apitest.WithLifeNumber(3), apitest.WithNoPreTrust(),
		apitest.WithTmuxSessionName("move-ts"), apitest.WithRawLabels(`{"team":  "a", "b":"2"}`),
		apitest.WithRawClaudeArgs(`["--model",   "opus"]`), apitest.WithRawExtraEnv(`{"K":  "v"}`),
		apitest.WithJsonlPath("/tmp/ad-move-test/cur.jsonl"), apitest.WithPID(4242),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sess-move-old", JSONLPath: "/tmp/ad-move-test/old.jsonl",
			Life: 2, RecordedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}),
	}
	id := f.seed(state, moveSessionID, append(opts, spec.opts...)...)
	if err := apitest.SeedParentChild(f.path, f.seed(store.StateWaiting, ""), id); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	return moveRow{id: id, examined: rvExamine(t, f, id), before: f.rawColumns(id), history: f.historyAllLives(id)}
}

// move runs MoveToPending on r's examined snapshot with the test launch and parentID.
func (f *v5Store) move(r moveRow, parentID string) (store.CondResult, int64, error) {
	return f.s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, parentID, store.LaunchOwner{})
}

// moveKept are the columns the move leaves exactly as stored.
func moveKept(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"claude_session_id": c.ClaudeSessionID, "jsonl_path": c.JSONLPath, "life_number": c.LifeNumber,
		"no_pre_trust": c.NoPreTrust, "started_at": c.StartedAt, "last_seen_at": c.LastSeenAt, "cwd": c.CWD,
		"tmux_session_name": c.TmuxSessionName, "claude_args": c.ClaudeArgs, "relay_mode": c.RelayMode,
		"labels": c.Labels, "extra_env": c.ExtraEnv,
	}
}

// requireSet fails when any of cols is NULL, so a cleared or kept check is not vacuous.
func requireSet(t *testing.T, cols map[string]any) {
	t.Helper()
	for name, v := range cols {
		if v == nil {
			t.Fatalf("seed left %s NULL; the check would be vacuous", name)
		}
	}
}

// TestMoveToPendingApplied checks the applied move on an ended and a missing
// row: the returned version is stored; pid, process start, ended_at, liveness
// and the server and pane identity are NULL; the launch start, token, socket
// and parent (NULL for "") are the move's, also over a row with no token; every
// kept column and the history are byte for byte unchanged.
func TestMoveToPendingApplied(t *testing.T) {
	cases := []struct {
		name   string
		spec   moveSpec
		parent bool // pass a live row as parent; false passes ""
	}{
		{"ended row, new parent", moveSpec{}, true},
		{"missing row, new parent", moveSpec{state: store.StateMissing}, true},
		{"empty parent id stores NULL", moveSpec{}, false},
		{"row with no token or socket", moveSpec{opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, tc.spec)
			requireSet(t, moveKept(r.before))
			var parent any
			parentID := ""
			if tc.parent {
				parentID = f.seed(store.StateWaiting, "")
				parent = parentID
			}
			res, v, err := f.move(r, parentID)
			if err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
			}
			after := f.rawColumns(r.id)
			want := map[string]any{"state": store.StatePending, "row_version": r.examined.Snapshot.RowVersion + 1,
				"launch_started_at": moveLaunchMillis, "launch_token": moveToken, "tmux_socket": moveSocket, "parent_id": parent,
				"pid": nil, "proc_starttime": nil, "ended_at": nil, "liveness_unverified_since": nil, "liveness_note": nil}
			for col, w := range wantIdentityColumns(store.LaunchIdentity{}) {
				want[col] = w
			}
			got := identityColumns(after)
			for col, g := range map[string]any{"state": after.State, "row_version": after.RowVersion, "launch_started_at": after.LaunchStartedAt,
				"launch_token": after.LaunchToken, "tmux_socket": after.TmuxSocket, "parent_id": after.ParentID, "pid": after.PID,
				"proc_starttime": after.ProcStarttime, "ended_at": after.EndedAt, "liveness_unverified_since": after.LivenessUnverifiedSince,
				"liveness_note": after.LivenessNote} {
				got[col] = g
			}
			if !reflect.DeepEqual(got, want) || v != r.examined.Snapshot.RowVersion+1 {
				t.Errorf("moved version %d, columns:\n got  %v\n want %v", v, got, want)
			}
			if !reflect.DeepEqual(moveKept(after), moveKept(r.before)) || !reflect.DeepEqual(f.historyAllLives(r.id), r.history) {
				t.Errorf("kept columns or history changed:\n before %v\n after  %v", moveKept(r.before), moveKept(after))
			}
		})
	}
}
