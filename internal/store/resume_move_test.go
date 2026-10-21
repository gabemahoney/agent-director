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
	// moveEndReported is the seeded row's end report (end_reported_at, Unix
	// ms), which the move clears; moveAgent is its agent unless a case names
	// one, which the move keeps.
	moveEndReported int64 = 1767225611000
	moveAgent             = store.AgentCodex
)

// moveAgents are the agents the move is checked for: it stores subagent_tracking
// by the row's agent (SR-9).
var moveAgents = []string{store.AgentClaude, store.AgentCodex}

// launchTracking is the subagent_tracking a launch of agent stores: 1 for a
// claude agent, 0 for a codex one (a Codex launch registers no subagent events).
func launchTracking(agent string) int64 {
	if agent == store.AgentCodex {
		return 0
	}
	return 1
}

// moveIdentity is the seeded row's previous launch identity, every field set.
func moveIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: "0011223344556677", Socket: "/tmp/ad-move-test/old-sock",
		ServerPID: 333, ServerStart: 1767225600, ServerStarttime: apitest.LinuxProcStarttime,
		PaneID: "%9", PanePID: 444, PaneStarttime: apitest.DarwinProcStarttime}
}

// moveSpec overrides seedMoveRow's full ended row.
type moveSpec struct {
	state string                // "" = ended
	agent string                // "" = moveAgent
	opts  []apitest.SpawnOption // after the defaults; a later option wins
}

// moveRow is a seeded row as resume examines it before the move.
type moveRow struct {
	id       string
	examined store.Spawn            // GetSpawn after seeding
	before   apitest.SpawnColumns   // raw columns after seeding
	history  []apitest.HistoryEntry // session_history after seeding
}

// seedMoveRow seeds a row (a codex one unless spec names its agent) with a value
// in every column the move clears or keeps (non-default layouts, raw text, an
// end report), subagent_tracking the opposite of what the move stores for its
// agent (0 for claude, 1 for codex), two subagent entries and a claimable
// launch, a parent and a history entry.
func seedMoveRow(t *testing.T, f *v5Store, spec moveSpec) moveRow {
	t.Helper()
	state, agent := spec.state, spec.agent
	if state == "" {
		state = store.StateEnded
	}
	if agent == "" {
		agent = moveAgent
	}
	opts := []apitest.SpawnOption{
		apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"), apitest.WithEndedAt("2026-09-29T01:02:03.5Z"),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime), apitest.WithLivenessUnverifiedSince("2026-09-28 10:00:00"),
		apitest.WithLivenessNote("move-test note"), apitest.WithLaunchIdentity(moveIdentity()),
		apitest.WithLaunchStartedAt(1767225600999), apitest.WithLifeNumber(3), apitest.WithNoPreTrust(),
		apitest.WithTmuxSessionName("move-ts"), apitest.WithRawLabels(`{"team":  "a", "b":"2"}`),
		apitest.WithRawAgentArgs(`["--model",   "opus"]`), apitest.WithRawExtraEnv(`{"K":  "v"}`),
		apitest.WithJsonlPath("/tmp/ad-move-test/cur.jsonl"), apitest.WithPID(4242),
		apitest.WithAgent(agent), apitest.WithEndReportedAt(moveEndReported), apitest.WithNoScreenChangeTimeout(int64(90)),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sess-move-old", JSONLPath: "/tmp/ad-move-test/old.jsonl",
			Life: 2, RecordedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}),
	}
	if launchTracking(agent) == 1 {
		opts = append(opts, apitest.WithNoSubagentTracking()) // a seeded row is tracked unless told otherwise
	}
	id := f.seed(state, moveSessionID, append(opts, spec.opts...)...)
	if err := apitest.SeedParentChild(f.path, f.seed(store.StateWaiting, ""), id); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	f.seedSubagentsOf(id)
	return moveRow{id: id, examined: rvExamine(t, f, id), before: f.rawColumns(id), history: f.historyAllLives(id)}
}

// move runs MoveToPending on r's examined snapshot with the test launch and parentID.
func (f *v5Store) move(r moveRow, parentID string) (store.CondResult, int64, error) {
	return f.s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, parentID, store.LaunchOwner{})
}

// moveKept are the columns the move leaves exactly as stored.
func moveKept(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"session_id": c.SessionID, "jsonl_path": c.JSONLPath, "life_number": c.LifeNumber,
		"no_pre_trust": c.NoPreTrust, "started_at": c.StartedAt, "last_seen_at": c.LastSeenAt, "cwd": c.CWD,
		"tmux_session_name": c.TmuxSessionName, "agent_args": c.AgentArgs, "relay_mode": c.RelayMode,
		"labels": c.Labels, "extra_env": c.ExtraEnv, "agent": c.Agent,
		"no_screen_change_timeout_seconds": c.NoScreenChangeTimeoutSeconds,
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
// row of each agent: the returned version is stored; pid, process start,
// ended_at, the end report, liveness and the server and pane identity are NULL;
// subagent_tracking is the agent's (claude 1 over the seeded 0, codex 0 over
// the seeded 1); the launch start, token, socket
// and parent (NULL for "") are the move's, also over a row with no token; every
// kept column (the agent and the no_screen_change override included) and the
// history are byte for byte unchanged.
// The move closes the row's close mix at the launch start it records: every
// running entry lost (agent_gone), every claimable launch deleted, every closed
// entry and another row's records untouched (SR-8.1).
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
	for _, agent := range moveAgents {
		for _, tc := range cases {
			t.Run(agent+", "+tc.name, func(t *testing.T) {
				tc.spec.agent = agent
				testMoveToPendingApplied(t, tc.spec, tc.parent)
			})
		}
	}
}

// testMoveToPendingApplied runs one case of TestMoveToPendingApplied: the move
// of spec's row, with a live parent when withParent is set.
func testMoveToPendingApplied(t *testing.T, spec moveSpec, withParent bool) {
	t.Helper()
	f := newV5Store(t)
	r := seedMoveRow(t, f, spec)
	requireSet(t, moveKept(r.before))
	requireSet(t, map[string]any{"end_reported_at": r.before.EndReportedAt})
	wantTracking := launchTracking(r.before.Agent.(string))
	if r.before.SubagentTracking != 1-wantTracking {
		t.Fatalf("seeded subagent_tracking = %v; want %d, or the move's write is vacuous", r.before.SubagentTracking, 1-wantTracking)
	}
	mix := f.seedCloseMix(r.id)
	var parent any
	parentID := ""
	if withParent {
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
		"pid": nil, "proc_starttime": nil, "ended_at": nil, "liveness_unverified_since": nil, "liveness_note": nil,
		"end_reported_at": nil, "subagent_tracking": wantTracking}
	for col, w := range wantIdentityColumns(store.LaunchIdentity{}) {
		want[col] = w
	}
	got := identityColumns(after)
	for col, g := range map[string]any{"state": after.State, "row_version": after.RowVersion, "launch_started_at": after.LaunchStartedAt,
		"launch_token": after.LaunchToken, "tmux_socket": after.TmuxSocket, "parent_id": after.ParentID, "pid": after.PID,
		"proc_starttime": after.ProcStarttime, "ended_at": after.EndedAt, "liveness_unverified_since": after.LivenessUnverifiedSince,
		"liveness_note": after.LivenessNote, "end_reported_at": after.EndReportedAt, "subagent_tracking": after.SubagentTracking} {
		got[col] = g
	}
	if !reflect.DeepEqual(got, want) || v != r.examined.Snapshot.RowVersion+1 {
		t.Errorf("moved version %d, columns:\n got  %v\n want %v", v, got, want)
	}
	if !reflect.DeepEqual(moveKept(after), moveKept(r.before)) || !reflect.DeepEqual(f.historyAllLives(r.id), r.history) {
		t.Errorf("kept columns or history changed:\n before %v\n after  %v", moveKept(r.before), moveKept(after))
	}
	f.assertMixClosedAt(mix, moveLaunchMillis)
}

// TestMoveToPendingNotAppliedClosesNothing checks a move that applies nothing
// leaves the row's entries and claimable launches, and another row's, as they
// were (SR-8.1): the guard refuses a live or pending row and a snapshot that no
// longer matches the row, the row may be gone, and a parent that names no row fails the
// statement itself.
func TestMoveToPendingNotAppliedClosesNothing(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		run     func(f *v5Store, r moveRow) (store.CondResult, error)
		want    store.CondResult
		wantErr bool
	}{
		{name: "live row", state: store.StateWaiting, want: store.CondChanged},
		{name: "pending row", state: store.StatePending, want: store.CondChanged},
		{name: "snapshot a version behind the row", want: store.CondChanged,
			run: func(f *v5Store, r moveRow) (store.CondResult, error) {
				stale := r.examined.Snapshot
				stale.RowVersion--
				res, _, err := f.s.MoveToPending(r.id, stale, moveLaunchMillis, moveToken, moveSocket, "", store.LaunchOwner{})
				return res, err
			}},
		{name: "row that is gone", want: store.CondAbsent,
			run: func(f *v5Store, r moveRow) (store.CondResult, error) {
				res, _, err := f.s.MoveToPending("move-no-such-row", r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, "", store.LaunchOwner{})
				return res, err
			}},
		{name: "parent id that names no row", wantErr: true,
			run: func(f *v5Store, r moveRow) (store.CondResult, error) {
				res, _, err := f.s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, "move-no-such-parent", store.LaunchOwner{})
				return res, err
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, moveSpec{state: tc.state})
			mix := f.seedCloseMix(r.id)
			run := tc.run
			if run == nil {
				run = func(f *v5Store, r moveRow) (store.CondResult, error) { res, _, err := f.move(r, ""); return res, err }
			}
			res, err := run(f, r)
			if tc.wantErr != (err != nil) || res != tc.want {
				t.Fatalf("MoveToPending = %v, %v; want %v, error %v", res, err, tc.want, tc.wantErr)
			}
			f.assertMixUnchanged(mix)
		})
	}
}

// TestMoveToPendingWithoutLaunchInstantFails checks a move given no launch
// instant (0), which the close of the row's running entries and claimable
// launches refuses, fails whole: an error with a zero result, and the row, its
// history, entries and claimable launches (and another row's) as they were
// (SR-8.1).
func TestMoveToPendingWithoutLaunchInstantFails(t *testing.T) {
	f := newV5Store(t)
	r := seedMoveRow(t, f, moveSpec{})
	mix := f.seedCloseMix(r.id)
	before := f.rawColumns(r.id)

	res, v, err := f.s.MoveToPending(r.id, r.examined.Snapshot, 0, moveToken, moveSocket, "", store.LaunchOwner{})

	if err == nil || res != 0 || v != 0 {
		t.Fatalf("MoveToPending(launch instant 0) = %v, %d, %v; want an error with a zero result", res, v, err)
	}
	if after := f.rawColumns(r.id); !reflect.DeepEqual(after, before) || !reflect.DeepEqual(f.historyAllLives(r.id), r.history) {
		t.Errorf("row or history changed:\n before %+v\n after  %+v", before, after)
	}
	f.assertMixUnchanged(mix)
}

// TestMoveToPendingRowComesBack checks a moved row, once its relaunch records
// the pane again, keeps every closed entry closed until a SubagentStart names it
// (SR-8.3): for a claude agent (tracked, SR-9) the start reopens the entry in
// the new launch, and nothing else; for a codex agent (untracked) it reopens
// nothing, and no entry changes.
func TestMoveToPendingRowComesBack(t *testing.T) {
	for _, agent := range moveAgents {
		t.Run(agent, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, moveSpec{agent: agent})
			mix := f.seedCloseMix(r.id)
			res, v, err := f.move(r, "")
			if err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
			}
			f.assertMixClosedAt(mix, moveLaunchMillis)
			if got := f.rawColumns(r.id).LifeNumber; got != mix.rows.Entries[0].LifeNumber {
				t.Fatalf("resume changed life to %v", got)
			}
			if res, err := f.s.RecordLaunchIdentity(r.id, v, moveToken, moveIdentity()); err != nil || res != store.CondApplied {
				t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
			}
			f.assertMixClosedAt(mix, moveLaunchMillis)
			if agent == store.AgentCodex {
				f.assertStartReopensNothing(r.id, moveLaunchMillis+60_000)
				return
			}
			f.assertStartReopens(r.id, moveToken, moveLaunchMillis+60_000)
		})
	}
}
