package store_test

// Reuse's read and reset (SR-10.3, SR-5.3, SR-5.5, SR-5.6, SR-5.9). The
// refused and failed resets are in reuse_refused_test.go, the restore in
// reuse_restore_test.go, history by life in reuse_life_test.go; each seeds
// through seedReuseRow and resets with reuseFresh.

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

// reuseIdentity is the seeded row's launch identity, every field set.
func reuseIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: "0011223344556677", Socket: "/tmp/ad-reuse-test/old-sock",
		ServerPID: 333, ServerStart: 1767225600, ServerStarttime: apitest.LinuxProcStarttime,
		PaneID: "%9", PanePID: 444, PaneStarttime: apitest.DarwinProcStarttime}
}

// reuseSpec overrides seedReuseRow's ended row that opted out of pre-trust.
type reuseSpec struct {
	state     string                // "" = ended
	noSession bool                  // NULL claude_session_id
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
	childBefore       apitest.SpawnColumns
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
		apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"), apitest.WithPID(5151),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime), apitest.WithLivenessUnverifiedSince("2026-09-28 10:00:00"),
		apitest.WithLivenessNote("reuse-test note"), apitest.WithLaunchIdentity(reuseIdentity()),
		apitest.WithLaunchStartedAt(1767225600999), apitest.WithLifeNumber(reuseLife), apitest.WithTmuxSessionName("reuse-ts"),
		apitest.WithRawLabels(`{"team":  "a", "b":"2"}`), apitest.WithRawClaudeArgs(`["--model",   "opus"]`),
		apitest.WithRawExtraEnv(`{"K":  "v"}`), apitest.WithEndedAt("2026-09-29T01:02:03.5Z"), apitest.WithJsonlPath(reuseJsonl),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sess-reuse-old", JSONLPath: "/tmp/ad-reuse-test/old.jsonl",
			Life: 1, RecordedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}),
	}
	if !spec.preTrust {
		opts = append(opts, apitest.WithNoPreTrust())
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

// reuseFresh is the fresh launch a reset writes: a start off UTC and
// sub-second (stored as "2026-10-01 12:34:56"), new request fields, token and
// socket, parentID ("" = NULL) and the pre-trust choice. Its server and pane
// identity are ignored by the reset.
func reuseFresh(parentID string, noPreTrust bool) store.Spawn {
	return store.Spawn{
		ParentID: parentID, CWD: "/tmp/ad-reuse-test/new", TmuxSessionName: "reuse-ts-new",
		ClaudeArgs: []string{"--model", "sonnet"}, RelayMode: "on",
		Labels: map[string]string{"team": "z"}, ExtraEnv: map[string]string{"NEW": "1"},
		StartedAt:             time.Date(2026, 10, 1, 14, 34, 56, 700_000_000, time.FixedZone("UTC+2", 2*3600)),
		LaunchStartedAtMillis: 1790000000456, NoPreTrust: noPreTrust,
		Identity: store.LaunchIdentity{Token: "a1b2c3d4e5f60718", Socket: "/tmp/ad-reuse-test/new-sock",
			ServerPID: 777, ServerStart: 1767225700, ServerStarttime: apitest.LinuxProcStarttime,
			PaneID: "%7", PanePID: 888, PaneStarttime: apitest.LinuxProcStarttime},
	}
}

// reuseReset resets r from the snapshot its read examined with fresh.
func (f *v5Store) reuseReset(r reuseRow, fresh store.Spawn) (store.CondResult, string, int64, error) {
	return f.s.ResetForReuse(r.id, r.read.Snapshot, fresh)
}

// TestReuseReadForReuse checks the read's state, ended_at text (byte for byte,
// and parsed only when it parses), snapshot and identity against GetSpawn's,
// on a row GetSpawn cannot decode too; an unknown id is not found, no error.
func TestReuseReadForReuse(t *testing.T) {
	malformed := []apitest.SpawnOption{apitest.WithRawLabels(`{bad`), apitest.WithRawClaudeArgs(`[1,`),
		apitest.WithRawExtraEnv(`nope`), apitest.WithEndedAt("yesterday-ish")}
	cases := []struct {
		name, wantState, wantText string
		spec                      reuseSpec
		malformed                 bool // GetSpawn cannot decode the row
	}{
		{"ended, RFC 3339 text", store.StateEnded, "2026-09-29T01:02:03.5Z", reuseSpec{}, false},
		{"missing, store layout", store.StateMissing, "2026-09-29 01:02:03",
			reuseSpec{state: store.StateMissing, opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}}, false},
		{"missing, NULL", store.StateMissing, "", reuseSpec{state: store.StateMissing, opts: []apitest.SpawnOption{apitest.WithNoEndedAt()}}, false},
		{"malformed labels, args, env and ended_at", store.StateEnded, "yesterday-ish", reuseSpec{opts: malformed}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			got := r.read
			if got.State != tc.wantState || got.EndedAtText != tc.wantText {
				t.Errorf("State, EndedAtText = %q, %q; want %q, %q", got.State, got.EndedAtText, tc.wantState, tc.wantText)
			}
			sp, err := f.s.GetSpawn(r.id)
			if tc.malformed {
				if err == nil || got.EndedAt != nil {
					t.Errorf("GetSpawn err = %v, EndedAt = %v; want GetSpawn to fail and no parsed ended_at", err, got.EndedAt)
				}
				return
			}
			if err != nil || (got.EndedAt == nil) != (sp.EndedAt == nil) || (got.EndedAt != nil && !got.EndedAt.Equal(*sp.EndedAt)) ||
				got.Snapshot != sp.Snapshot || got.Identity != sp.Identity {
				t.Errorf("ReadForReuse %+v; want GetSpawn's ended_at, snapshot and identity %+v (%v)", got, sp, err)
			}
		})
	}
	f := newV5Store(t)
	if row, found, err := f.s.ReadForReuse("no-such-id"); err != nil || found || !reflect.DeepEqual(row, store.ReuseRow{}) {
		t.Errorf("ReadForReuse(unknown) = %+v, %v, %v; want zero, false, nil", row, found, err)
	}
}

// TestReuseResetApplied checks the applied reset on ended and missing rows:
// the archived session, cleared and fresh columns, life and version each +1,
// the pre-trust choice either way, an empty parent stored NULL; every request
// of the id deleted (another id's kept), the child and older history kept; a
// row with no session archives nothing.
func TestReuseResetApplied(t *testing.T) {
	cases := []struct {
		name       string
		spec       reuseSpec
		parent     bool  // pass a live row as the fresh parent; false passes ""
		noPreTrust bool  // the reset's own choice
		want       int64 // stored no_pre_trust
	}{
		{"ended, opted-out row reset allowing pre-trust", reuseSpec{}, true, false, 0},
		{"missing, opted-out row reset allowing pre-trust", reuseSpec{state: store.StateMissing}, true, false, 0},
		{"ended, allowing row reset opting out, empty parent", reuseSpec{preTrust: true}, false, true, 1},
		{"ended, no session id", reuseSpec{noSession: true}, true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			seedRequest(t, f, r.parent)
			otherRequests := f.reuseRequests(t, r.parent)
			var parent any
			parentID := ""
			if tc.parent {
				parentID = f.seed(store.StateWaiting, "")
				parent = parentID
			}
			wantArchived := reuseSessionID
			if tc.spec.noSession {
				wantArchived = ""
			}

			res, archived, v, err := f.reuseReset(r, reuseFresh(parentID, tc.noPreTrust))
			if err != nil || res != store.CondApplied || archived != wantArchived {
				t.Fatalf("ResetForReuse = %v, %q, %d, %v; want CondApplied, %q", res, archived, v, err, wantArchived)
			}
			a := f.rawColumns(r.id)
			got := map[string]any{"row_version": a.RowVersion, "state": a.State, "life_number": a.LifeNumber,
				"no_pre_trust": a.NoPreTrust, "started_at": a.StartedAt, "last_seen_at": a.LastSeenAt,
				"launch_started_at": a.LaunchStartedAt, "cwd": a.CWD, "tmux_session_name": a.TmuxSessionName,
				"claude_args": a.ClaudeArgs, "relay_mode": a.RelayMode, "labels": a.Labels, "extra_env": a.ExtraEnv,
				"parent_id": a.ParentID, "launch_token": a.LaunchToken, "tmux_socket": a.TmuxSocket,
				"claude_session_id": a.ClaudeSessionID, "jsonl_path": a.JSONLPath, "pid": a.PID, "proc_starttime": a.ProcStarttime,
				"ended_at": a.EndedAt, "liveness_unverified_since": a.LivenessUnverifiedSince, "liveness_note": a.LivenessNote}
			for col, g := range identityColumns(a) {
				got[col] = g
			}
			want := map[string]any{"row_version": r.read.Snapshot.RowVersion + 1, "state": store.StatePending,
				"life_number": reuseLife + 1, "no_pre_trust": tc.want, "started_at": "2026-10-01 12:34:56",
				"last_seen_at": "2026-10-01 12:34:56", "launch_started_at": int64(1790000000456), "cwd": "/tmp/ad-reuse-test/new",
				"tmux_session_name": "reuse-ts-new", "claude_args": `["--model","sonnet"]`, "relay_mode": "on",
				"labels": `{"team":"z"}`, "extra_env": `{"NEW":"1"}`, "parent_id": parent, "launch_token": "a1b2c3d4e5f60718",
				"tmux_socket": "/tmp/ad-reuse-test/new-sock", "claude_session_id": nil, "jsonl_path": nil, "pid": nil,
				"proc_starttime": nil, "ended_at": nil, "liveness_unverified_since": nil, "liveness_note": nil}
			for col, w := range wantIdentityColumns(store.LaunchIdentity{}) {
				want[col] = w
			}
			if !reflect.DeepEqual(got, want) || v != r.read.Snapshot.RowVersion+1 {
				t.Errorf("reset version %d, row:\n got  %v\n want %v", v, got, want)
			}
			if reqs := f.reuseRequests(t, r.id); len(r.requests) != 2 || len(reqs) != 0 {
				t.Errorf("the id's requests %d -> %d; want 2 -> 0", len(r.requests), len(reqs))
			}
			if got := f.reuseRequests(t, r.parent); !reflect.DeepEqual(got, otherRequests) {
				t.Errorf("another id's requests %+v -> %+v; want unchanged", otherRequests, got)
			}
			if got := f.rawColumns(r.child); !reflect.DeepEqual(got, r.childBefore) {
				t.Errorf("child %+v -> %+v; want unchanged", r.childBefore, got)
			}
			wantCur := 1
			if wantArchived == "" {
				wantCur = 0
			}
			cur, others := splitHistory(f.historyAllLives(r.id), reuseSessionID)
			if _, before := splitHistory(r.history, reuseSessionID); !reflect.DeepEqual(others, before) || len(cur) != wantCur {
				t.Errorf("history after the reset: %+v archived, others %+v; want %q archived once, the rest kept", cur, others, wantArchived)
			}
		})
	}
}

// splitHistory returns h's entries for sessionID and every other entry, in order.
func splitHistory(h []apitest.HistoryEntry, sessionID string) (cur, others []apitest.HistoryEntry) {
	others = []apitest.HistoryEntry{}
	for _, e := range h {
		if e.ClaudeSessionID == sessionID {
			cur = append(cur, e)
		} else {
			others = append(others, e)
		}
	}
	return cur, others
}
