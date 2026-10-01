package store_test

// Store tests for reuse's read and reset (SR-10.3, SR-5.3, SR-5.5, SR-5.6,
// SR-5.8, SR-5.9): the read, the applied reset and a malformed row reused.
// The keyed archive, permission requests and children are in
// reuse_archive_test.go; the not-applied and failed resets in
// reuse_refused_test.go. Siblings (the restore, history by life) reuse
// seedReuseRow and reuseFresh below. Rows are seeded through apitest (SR-20.2).

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The seeded row's current session, transcript path and life.
const (
	reuseSessionID       = "sess-reuse-cur"
	reuseJsonl           = "/tmp/ad-reuse-test/cur.jsonl"
	reuseLife      int64 = 3
)

// reuseOldRecordedAt is the recorded_at every seeded history entry carries,
// so a refreshed entry is told apart without sleeps; reuseOldRecordedText is
// it as stored.
var reuseOldRecordedAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

const reuseOldRecordedText = "2026-09-01 00:00:00"

// reuseIdentity is the seeded row's launch identity, every field set; its pane
// lets the row's own agent's hooks pass the gate (SR-22.9).
func reuseIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{
		Token: "0011223344556677", Socket: "/tmp/ad-reuse-test/old-sock",
		ServerPID: 333, ServerStart: 1767225600, ServerStarttime: apitest.LinuxProcStarttime,
		PaneID: "%9", PanePID: 444, PaneStarttime: apitest.DarwinProcStarttime,
	}
}

// reuseSpec overrides seedReuseRow's defaults; the zero value is the full
// ended row that opted out of pre-trust.
type reuseSpec struct {
	state     string                // "" = ended
	noSession bool                  // NULL claude_session_id
	noJsonl   bool                  // NULL jsonl_path
	noEndedAt bool                  // no ended_at seeded (a missing row keeps NULL)
	preTrust  bool                  // no_pre_trust 0 instead of the opt-out
	opts      []apitest.SpawnOption // after the defaults; a later option wins
}

// reuseRow is a seeded row as reuse examines it, with what else it touches.
type reuseRow struct {
	id, parent, child string
	read              store.ReuseRow         // ReadForReuse after seeding
	before            apitest.SpawnColumns   // raw columns after seeding
	history           []apitest.HistoryEntry // every-life history after seeding
	requests          []store.PermissionRow  // the id's requests: one decided, one open
	childBefore       apitest.SpawnColumns   // the child's raw columns
}

// seedReuseRow seeds a finished row with a non-default value in every column
// the reset clears, sets or keeps, an older history entry, a parent, a child
// and two permission requests (one decided), read as reuse would.
func seedReuseRow(t *testing.T, f *v5Store, spec reuseSpec) reuseRow {
	t.Helper()
	state, session := spec.state, reuseSessionID
	if state == "" {
		state = store.StateEnded
	}
	if spec.noSession {
		session = ""
	}
	opts := []apitest.SpawnOption{
		apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"),
		apitest.WithPID(5151),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithLivenessUnverifiedSince("2026-09-28 10:00:00"),
		apitest.WithLivenessNote("reuse-test note"),
		apitest.WithLaunchIdentity(reuseIdentity()),
		apitest.WithLaunchStartedAt(1767225600999),
		apitest.WithLifeNumber(reuseLife),
		apitest.WithTmuxSessionName("reuse-ts"),
		apitest.WithRawLabels(`{"team":  "a", "b":"2"}`),
		apitest.WithRawClaudeArgs(`["--model",   "opus"]`),
		apitest.WithRawExtraEnv(`{"K":  "v"}`),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID: "sess-reuse-old", JSONLPath: "/tmp/ad-reuse-test/old.jsonl", Life: 1,
			RecordedAt: reuseOldRecordedAt,
		}),
	}
	if !spec.noEndedAt {
		opts = append(opts, apitest.WithEndedAt("2026-09-29T01:02:03.5Z"))
	}
	if !spec.preTrust {
		opts = append(opts, apitest.WithNoPreTrust())
	}
	if !spec.noJsonl {
		opts = append(opts, apitest.WithJsonlPath(reuseJsonl))
	}
	id := f.seed(state, session, append(opts, spec.opts...)...)
	r := reuseRow{id: id, parent: f.seed(store.StateWaiting, ""), child: f.seed(store.StateWaiting, "")}
	for _, pc := range [][2]string{{r.parent, id}, {id, r.child}} {
		if err := apitest.SeedParentChild(f.path, pc[0], pc[1]); err != nil {
			t.Fatalf("SeedParentChild(%s, %s): %v", pc[0], pc[1], err)
		}
	}
	decided := seedRequest(t, f, id)
	seedRequest(t, f, id)
	ok, err := f.s.DecidePermissionRequest(id, decided, "allow", "", "reuse_test")
	wantBool(t, "DecidePermissionRequest", ok, err, true)

	r.read = f.readForReuse(t, id)
	r.before, r.history, r.requests = f.rawColumns(id), f.historyAllLives(id), f.reuseRequests(t, id)
	r.childBefore = f.rawColumns(r.child)
	return r
}

// readForReuse runs ReadForReuse on id, failing unless the row is found.
func (f *v5Store) readForReuse(t *testing.T, id string) store.ReuseRow {
	t.Helper()
	row, found, err := f.s.ReadForReuse(id)
	if err != nil || !found {
		t.Fatalf("ReadForReuse(%s) = found %v, %v; want found", id, found, err)
	}
	return row
}

// reuseRequests returns every permission request of id, decided or not.
func (f *v5Store) reuseRequests(t *testing.T, id string) []store.PermissionRow {
	t.Helper()
	rows, err := f.s.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn(%s): %v", id, err)
	}
	return rows
}

// reuseFreshStart is the fresh launch's start: off UTC and sub-second, so the
// stored text shows the store layout (reuseFreshStartText).
var reuseFreshStart = time.Date(2026, 10, 1, 14, 34, 56, 700_000_000, time.FixedZone("UTC+2", 2*3600))

const reuseFreshStartText = "2026-10-01 12:34:56"

// reuseFresh is the fresh launch a reset writes: fixed times, new request
// fields, token and socket, parentID ("" = NULL) and the pre-trust choice.
// Its identity also carries server and pane values, which the reset ignores.
func reuseFresh(parentID string, noPreTrust bool) store.Spawn {
	return store.Spawn{
		ParentID: parentID, CWD: "/tmp/ad-reuse-test/new", TmuxSessionName: "reuse-ts-new",
		ClaudeArgs: []string{"--model", "sonnet"}, RelayMode: "on",
		Labels: map[string]string{"team": "z"}, ExtraEnv: map[string]string{"NEW": "1"},
		StartedAt: reuseFreshStart, LaunchStartedAtMillis: 1790000000456, NoPreTrust: noPreTrust,
		Identity: store.LaunchIdentity{
			Token: "a1b2c3d4e5f60718", Socket: "/tmp/ad-reuse-test/new-sock",
			ServerPID: 777, ServerStart: 1767225700, ServerStarttime: apitest.LinuxProcStarttime,
			PaneID: "%7", PanePID: 888, PaneStarttime: apitest.LinuxProcStarttime,
		},
	}
}

// reuseReset resets r from the snapshot its read examined with fresh.
func (f *v5Store) reuseReset(r reuseRow, fresh store.Spawn) (store.CondResult, string, int64, error) {
	return f.s.ResetForReuse(r.id, r.read.Snapshot, fresh)
}

// reuseCleared are the columns the reset sets to NULL.
func reuseCleared(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"claude_session_id": c.ClaudeSessionID, "jsonl_path": c.JSONLPath,
		"pid": c.PID, "proc_starttime": c.ProcStarttime, "ended_at": c.EndedAt,
		"liveness_unverified_since": c.LivenessUnverifiedSince, "liveness_note": c.LivenessNote,
		"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID,
		"pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime,
	}
}

// TestReuseReadForReuse checks the read's state, ended_at text byte for byte
// and parsed, snapshot and identity against GetSpawn's.
func TestReuseReadForReuse(t *testing.T) {
	cases := []struct {
		name     string
		spec     reuseSpec
		wantText string
	}{
		{"ended, RFC 3339 text", reuseSpec{}, "2026-09-29T01:02:03.5Z"},
		{"missing, store layout", reuseSpec{state: store.StateMissing,
			opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}}, "2026-09-29 01:02:03"},
		{"missing, NULL", reuseSpec{state: store.StateMissing, noEndedAt: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			sp, err := f.s.GetSpawn(r.id)
			if err != nil {
				t.Fatalf("GetSpawn: %v", err)
			}
			got, wantState := r.read, tc.spec.state
			if wantState == "" {
				wantState = store.StateEnded
			}
			if got.State != wantState || got.EndedAtText != tc.wantText {
				t.Errorf("State, EndedAtText = %q, %q; want %q, %q", got.State, got.EndedAtText, wantState, tc.wantText)
			}
			if (got.EndedAt != nil) != (tc.wantText != "") || (got.EndedAt != nil) != (sp.EndedAt != nil) ||
				(got.EndedAt != nil && !got.EndedAt.Equal(*sp.EndedAt)) {
				t.Errorf("EndedAt = %v; want GetSpawn's %v", got.EndedAt, sp.EndedAt)
			}
			if got.Snapshot != sp.Snapshot || got.Identity != sp.Identity {
				t.Errorf("Snapshot, Identity = %+v, %+v; want GetSpawn's %+v, %+v", got.Snapshot, got.Identity, sp.Snapshot, sp.Identity)
			}
		})
	}
}

// TestReuseReadForReuseNotFound checks an unknown id is not found, with no error.
func TestReuseReadForReuseNotFound(t *testing.T) {
	f := newV5Store(t)
	row, found, err := f.s.ReadForReuse("no-such-id")
	if err != nil || found || !reflect.DeepEqual(row, store.ReuseRow{}) {
		t.Fatalf("ReadForReuse = %+v, %v, %v; want zero, false, nil", row, found, err)
	}
}

// TestReuseResetApplied checks the applied reset on ended and missing rows:
// returned values, cleared and fresh columns, life and version each + 1, and
// the pre-trust choice in both directions.
func TestReuseResetApplied(t *testing.T) {
	cases := []struct {
		name       string
		spec       reuseSpec
		noPreTrust bool  // the reset's own choice
		want       int64 // stored no_pre_trust
	}{
		{"ended, opted-out row reset allowing pre-trust", reuseSpec{}, false, 0},
		{"missing, opted-out row reset allowing pre-trust", reuseSpec{state: store.StateMissing}, false, 0},
		{"ended, allowing row reset opting out", reuseSpec{preTrust: true}, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			requireSet(t, reuseCleared(r.before))
			if r.before.NoPreTrust == tc.want {
				t.Fatalf("seeded no_pre_trust %#v already %d; the check would be vacuous", r.before.NoPreTrust, tc.want)
			}
			newParent := f.seed(store.StateWaiting, "")

			res, archived, v, err := f.reuseReset(r, reuseFresh(newParent, tc.noPreTrust))
			if err != nil || res != store.CondApplied || archived != reuseSessionID {
				t.Fatalf("ResetForReuse = %v, %q, %d, %v; want CondApplied, %q", res, archived, v, err, reuseSessionID)
			}
			after := f.rawColumns(r.id)
			if want := r.read.Snapshot.RowVersion + 1; v != want || after.RowVersion != want {
				t.Errorf("resetVersion %d, stored row_version %#v; want both %d", v, after.RowVersion, want)
			}
			for col, got := range reuseCleared(after) {
				if got != nil {
					t.Errorf("%s = %#v; want NULL", col, got)
				}
			}
			set := map[string][2]any{
				"state":             {after.State, store.StatePending},
				"life_number":       {after.LifeNumber, reuseLife + 1},
				"no_pre_trust":      {after.NoPreTrust, tc.want},
				"started_at":        {after.StartedAt, reuseFreshStartText},
				"last_seen_at":      {after.LastSeenAt, reuseFreshStartText},
				"launch_started_at": {after.LaunchStartedAt, int64(1790000000456)},
				"cwd":               {after.CWD, "/tmp/ad-reuse-test/new"},
				"tmux_session_name": {after.TmuxSessionName, "reuse-ts-new"},
				"claude_args":       {after.ClaudeArgs, `["--model","sonnet"]`},
				"relay_mode":        {after.RelayMode, "on"},
				"labels":            {after.Labels, `{"team":"z"}`},
				"extra_env":         {after.ExtraEnv, `{"NEW":"1"}`},
				"parent_id":         {after.ParentID, newParent},
				"launch_token":      {after.LaunchToken, "a1b2c3d4e5f60718"},
				"tmux_socket":       {after.TmuxSocket, "/tmp/ad-reuse-test/new-sock"},
			}
			for col, gw := range set {
				if gw[0] != gw[1] {
					t.Errorf("%s = %#v; want %#v", col, gw[0], gw[1])
				}
			}
		})
	}
}

// TestReuseResetEmptyParent checks an empty fresh parent id stores NULL.
func TestReuseResetEmptyParent(t *testing.T) {
	f := newV5Store(t)
	r := seedReuseRow(t, f, reuseSpec{})
	if res, _, _, err := f.reuseReset(r, reuseFresh("", false)); err != nil || res != store.CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
	if got := f.rawColumns(r.id).ParentID; got != nil {
		t.Errorf("parent_id = %#v; want NULL", got)
	}
}

// TestReuseMalformedRow checks a row with malformed labels, args, env and
// ended_at, which GetSpawn cannot read, is read (ended_at text as stored) and
// reset without error.
func TestReuseMalformedRow(t *testing.T) {
	f := newV5Store(t)
	r := seedReuseRow(t, f, reuseSpec{opts: []apitest.SpawnOption{
		apitest.WithRawLabels(`{bad`), apitest.WithRawClaudeArgs(`[1,`),
		apitest.WithRawExtraEnv(`nope`), apitest.WithEndedAt("yesterday-ish"),
	}})
	if _, err := f.s.GetSpawn(r.id); err == nil {
		t.Fatal("GetSpawn read the malformed row; the case would be vacuous")
	}
	if r.read.State != store.StateEnded || r.read.EndedAtText != "yesterday-ish" || r.read.EndedAt != nil {
		t.Errorf("ReadForReuse = state %q, ended_at %q / %v; want ended, the stored text, nil",
			r.read.State, r.read.EndedAtText, r.read.EndedAt)
	}
	if res, _, _, err := f.reuseReset(r, reuseFresh("", false)); err != nil || res != store.CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
	if got := f.rawColumns(r.id).Labels; got != `{"team":"z"}` {
		t.Errorf("labels = %#v; want the fresh labels", got)
	}
}
