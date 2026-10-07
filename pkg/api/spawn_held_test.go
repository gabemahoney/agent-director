package api_test

// spawn_held_test.go covers plain spawn after its create answers "duplicate
// session" (SR-9.4, SR-3.10, SR-5.8, SR-13.2, SR-14; AC-SPN-04, 06, 09, 10),
// per re-lookup outcome and end-write result. It holds the held-name fixture
// (heldEnv) and the trail-record checks the spawn files share; fail-open is
// spawn_reuse_trail_test.go's.

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// heldStoreLayout is the store's CURRENT_TIMESTAMP text layout for ended_at.
const heldStoreLayout = "2006-01-02 15:04:05"

// heldName is the requested name the tests here find held; heldCreated the
// holders' creation epoch.
const (
	heldName    = "held-name"
	heldCreated = int64(1790000100)
)

// ptKeys is an ad.launch.name_held record's full key set for plain spawn's
// held-name path: SR-14's fields plus the envelope's event and ts.
var ptKeys = []string{"event", "ts", "source", "claude_instance_id", "launch", "tmux_session_name",
	"tmux_socket", "tmux_session_id", "session_created", "store_id", "carries_this_id", "current_launch",
	"lookup_outcome", "outcome", "row_result", "store_error", "attach_command", "end_command",
	"caller_process", "caller_pid", "caller_hostname", "caller_user"}

// heldEnv is the held-name fixture: scanEnv (Recorder, clock, process
// checker, captured log, store id) with every tmux call charged its default
// timeout in virtual time from start.
type heldEnv struct {
	scanEnv
	start time.Time
}

// newHeldEnv builds a heldEnv; the Recorder is bound to the fixture's clock
// at the internal/config default timeouts.
func newHeldEnv(t *testing.T) heldEnv {
	t.Helper()
	e := newScanEnv(t)
	e.rec.WithVirtualTime(e.clock, tmux.Timeouts{})
	return heldEnv{scanEnv: e, start: e.clock.Now()}
}

// heldID returns a fresh caller-supplied instance id.
func heldID() string { return "held-" + uuid.NewString()[:8] }

// heldSession is a session holding name as sessionID with label (tmux.Label{}
// for none; labelSet marks a malformed value that is present but invalid).
func heldSession(name, sessionID string, label tmux.Label, labelSet bool) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{ID: sessionID, Name: name, Label: label, LabelSet: labelSet}
}

// heldHolder is a session holding heldName as sessionID, created at heldCreated.
func heldHolder(sessionID string, label tmux.Label, labelSet bool) tmuxfix.SeedSession {
	s := heldSession(heldName, sessionID, label, labelSet)
	s.Created = heldCreated
	return s
}

// heldRun is what spawnHeld observed: the Spawn error, the create's instance
// id, the clock when the create returned (the end write's time), the row's
// state when the re-lookup returned, and the socket's sessions when the create
// returned.
type heldRun struct {
	err              error
	id               string
	createdAt        time.Time
	stateAtRelookup  any
	sessionsAtCreate []tmuxfix.SeedSession
}

// spawnHeld runs a plain spawn of id requesting name: a caller-supplied id's
// scan makes the first lookup, a minted id ("") makes none. onScan, when not
// nil, runs as the scan's lookup returns (SR-20.9: a leftover appearing
// between scan and create).
func (e heldEnv) spawnHeld(t *testing.T, id, name string, onScan func()) heldRun {
	t.Helper()
	var run heldRun
	lookups := 0
	if id == "" {
		lookups = 1 // no scan: the first lookup is the re-lookup
	}
	e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
		lookups++
		switch {
		case lookups == 1 && onScan != nil:
			onScan()
		case lookups == 2:
			cols, err := apitest.ReadSpawnColumns(e.dbPath, run.id)
			if err != nil {
				t.Errorf("ReadSpawnColumns at the re-lookup: %v", err)
			}
			run.stateAtRelookup = cols.State
		}
	})
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		run.id = c.InstanceID
		run.createdAt = e.clock.Now()
		run.sessionsAtCreate = e.rec.Sessions(e.socket)
	})
	_, run.err = e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id,
		TmuxSessionName: name, TmuxSessionNameSupplied: true})
	return run
}

// readRow reads every column of id's row, failing the test on a read error.
func (e heldEnv) readRow(t *testing.T, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols
}

// assertRowIs checks id's row reads exactly as want.
func (e heldEnv) assertRowIs(t *testing.T, id, what string, want apitest.SpawnColumns) {
	t.Helper()
	if got := e.readRow(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("row %s:\ngot  %+v\nwant %+v", what, got, want)
	}
}

// assertEndedRow checks id's row was ended by the applied end write at
// endedAt: ended_at in the store's layout, no launch start, row_version 1,
// the launch token kept; it returns the token.
func (e heldEnv) assertEndedRow(t *testing.T, id string, endedAt time.Time) string {
	t.Helper()
	cols := e.readRow(t, id)
	if cols.State != store.StateEnded || cols.RowVersion != int64(1) || cols.LaunchStartedAt != nil {
		t.Errorf("row state, row_version, launch_started_at = %v, %v, %v; want ended, 1, NULL",
			cols.State, cols.RowVersion, cols.LaunchStartedAt)
	}
	if want := endedAt.UTC().Format(heldStoreLayout); cols.EndedAt != want {
		t.Errorf("ended_at = %v; want %q (the clock at the end write)", cols.EndedAt, want)
	}
	token, _ := cols.LaunchToken.(string)
	if token == "" {
		t.Errorf("launch_token = %v; want the insert's token kept", cols.LaunchToken)
	}
	return token
}

// ptRecords returns id's event records added after the first mark lines.
func ptRecords(t *testing.T, mark int, event, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range trailSince(t, mark, event) {
		if r["claude_instance_id"] == id {
			out = append(out, r)
		}
	}
	return out
}

// ptCaller is the caller fields every record carries: this test process.
func ptCaller() map[string]any {
	host, _ := os.Hostname()
	var username string
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return map[string]any{"caller_process": filepath.Base(os.Args[0]), "caller_pid": float64(os.Getpid()),
		"caller_hostname": host, "caller_user": username}
}

// ptErrName is err's catalogued err_name, as the record's outcome carries it.
func ptErrName(err error) string {
	name, _ := errnames.Classify(err)
	return name
}

// assertTrailRecord fails unless rec holds exactly keys, each of want's with
// its value (nil: present and null), and none of forbid in its text.
func assertTrailRecord(t *testing.T, rec map[string]any, keys []string, want map[string]any, forbid ...string) {
	t.Helper()
	for k, v := range want {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%v[%q] = %v (present %t); want %v", rec["event"], k, got, ok, v)
		}
	}
	got := make([]string, 0, len(rec))
	for k := range rec {
		got = append(got, k)
	}
	slices.Sort(got)
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Errorf("%v keys = %q; want %q", rec["event"], got, sorted)
	}
	text, _ := json.Marshal(rec)
	for _, v := range forbid {
		if v != "" && strings.Contains(string(text), v) {
			t.Errorf("record %s contains %q", text, v)
		}
	}
}

// ptWant is one name_held record's expected per-case fields. sessionID ""
// means no holder identified; carries and current are true, false or nil.
type ptWant struct {
	lookup, outcome, rowResult string
	sessionID                  string
	carries, current           any
	storeError                 bool // store_error holds the store's error text
}

// nameHeldFields is an ad.launch.name_held record's SR-14 fields (holder nil:
// none identified; nil values: null), this process the caller.
func nameHeldFields(launch, id, name, socket, storeID string, holder *tmuxfix.SeedSession, lookup, outcome, rowResult string,
	storeError, carries, current any) map[string]any {
	q := func(s string) string { return "'" + s + "'" }
	want := map[string]any{"source": "ad_spawn", "claude_instance_id": id, "launch": launch, "tmux_session_name": name,
		"tmux_socket": socket, "store_id": storeID, "lookup_outcome": lookup, "outcome": outcome, "row_result": rowResult,
		"store_error": storeError, "carries_this_id": carries, "current_launch": current,
		"tmux_session_id": nil, "session_created": nil, "attach_command": nil, "end_command": nil}
	if holder != nil {
		want["tmux_session_id"], want["session_created"] = holder.ID, float64(holder.Created)
		want["attach_command"] = "tmux -u -S " + q(socket) + " attach-session -r -t " + q(holder.ID)
		want["end_command"] = "tmux -u -S " + q(socket) + " kill-session -t " + q(holder.ID)
	}
	for k, v := range ptCaller() {
		want[k] = v
	}
	return want
}

// ptAssertRecord checks the one record of id's held-name spawn since mark
// matches w, with the exact key set and no forbidden value; a store error is
// the WARN line's text.
func (e heldEnv) ptAssertRecord(t *testing.T, mark int, id string, w ptWant, forbid ...string) {
	t.Helper()
	recs := ptRecords(t, mark, "ad.launch.name_held", id)
	if len(recs) != 1 {
		t.Fatalf("ad.launch.name_held records = %d; want 1: %v", len(recs), recs)
	}
	var holder *tmuxfix.SeedSession
	if w.sessionID != "" {
		holder = &tmuxfix.SeedSession{ID: w.sessionID, Created: heldCreated}
	}
	var storeErr any
	if w.storeError {
		msg, _ := recs[0]["store_error"].(string)
		if msg == "" || !strings.Contains(e.logs.String(), msg) {
			t.Errorf("store_error = %v; want the store's error text, as in the WARN line %q", recs[0]["store_error"], e.logs.String())
		}
		storeErr = msg
	}
	assertTrailRecord(t, recs[0], ptKeys, nameHeldFields("spawn", id, heldName, e.socket, e.storeID, holder, w.lookup, w.outcome,
		w.rowResult, storeErr, w.carries, w.current), forbid...)
}

// ptForbid lists what no record may carry: launch tokens, another store's id,
// the seeded label values and other ids.
func (e heldEnv) ptForbid(id, rowToken string, seeded []tmuxfix.SeedSession, extra ...string) []string {
	out := append([]string{rowToken, tmuxfix.Token, tmuxfix.OtherToken, apitest.OtherStoreID(e.storeID)}, extra...)
	for _, s := range seeded {
		if s.Label.Kind == tmux.LabelValid {
			out = append(out, tmuxfix.LabelValue(s.Label.Token, s.ID, id, s.Label.StoreID))
		}
	}
	return out
}

// TestSpawnHeldTrailPerOutcome (SR-9.4, SR-5.8, SR-13.2, SR-14; b.1qq): per
// re-lookup outcome the row is ended before the re-lookup, the one error says
// so, the scan (none for a minted id), create and re-lookup, each charged its
// default timeout, leave the holder untouched, and one
// ad.launch.name_held has every field. An end write a competing write or a
// delete-and-reinsert keeps from applying leaves the row as written
// (left_changed); a failed one leaves it pending with one WARN (still_pending);
// unanswered or vanished, such a row's description says not to retry until
// get shows it finished, then the opted-in retry once the name is free.
func TestSpawnHeldTrailPerOutcome(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	other := "other-" + uuid.NewString()[:8]
	noLabel := func(heldEnv, string) []tmuxfix.SeedSession {
		return []tmuxfix.SeedSession{heldHolder("$4", tmux.Label{}, false)}
	}
	script := func(s tmuxfix.Script) func(e heldEnv) {
		s.Times = 1
		return func(e heldEnv) { e.rec.Script(e.socket, s, tmux.CallLookup) }
	}
	after := func(d func(heldEnv) apitest.DescCase) func(heldEnv, string, apitest.HeldName) apitest.DescCase {
		return func(e heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return d(e).AfterHeldName(p) }
	}
	noValidID := func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldNoValidID(p) }
	conflict := ptErrName(api.ErrTmuxSessionConflict)
	cases := []struct {
		name     string
		holders  func(e heldEnv, id string) []tmuxfix.SeedSession // seeded before the spawn
		late     func(e heldEnv, id string) []tmuxfix.SeedSession // seeded as the scan's lookup returns
		relookup func(e heldEnv)                                  // run as the scan's lookup returns
		vanish   bool                                             // the holder is removed as the create returns
		desc     func(e heldEnv, id string, p apitest.HeldName) apitest.DescCase
		forbid   []string
		want     ptWant // its sessionID is the holder the description names too
		disagree bool   // one scope_value ad.provenance.disagree record
		unended  bool   // also run with the end write not applied and failed
		minted   bool   // the spawn names no id, so no scan
	}{
		{name: "old label", late: func(e heldEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover(heldName, "$4", id, heldCreated)}
		}, desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldLeftover(p) },
			want: ptWant{lookup: "leftover", outcome: conflict, sessionID: "$4", carries: true, current: false}},
		{name: "foreign label", holders: func(e heldEnv, _ string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldHolder("$4", tmuxfix.Valid(tmuxfix.OtherToken, other, e.storeID), true)}
		}, desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldDifferentID(p) },
			forbid: []string{other}, want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "another store's label naming this id", holders: func(e heldEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldHolder("$4", tmuxfix.Valid(tmuxfix.OtherToken, id, apitest.OtherStoreID(e.storeID)), true)}
		}, desc: func(e heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
			return apitest.DescHeldOtherStore(p, e.storeID)
		},
			want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "no label", holders: noLabel, desc: noValidID, unended: true,
			want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "no label, minted id", holders: noLabel, desc: noValidID, minted: true,
			want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "invalid label", holders: func(heldEnv, string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldHolder("$4", tmux.Label{}, true)}
		}, desc: noValidID, want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "vanished before the re-lookup", holders: noLabel, vanish: true, unended: true,
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: p.Name, Duplicate: true}).AfterHeldName(p)
			}, want: ptWant{lookup: "gone", outcome: ptErrName(api.ErrTmuxSessionCreate)}},
		// Plain spawn names cannot hold $ or \, so two entries with one stored name stand in.
		{name: "more than one listing entry matches", holders: func(heldEnv, string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldHolder("$4", tmux.Label{}, false), heldHolder("$5", tmux.Label{}, false)}
		}, desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldAmbiguous(p) },
			forbid: []string{"$4", "$5"}, unended: true, want: ptWant{lookup: "gone", outcome: ptErrName(api.ErrTmuxUnresponsive)}},
		{name: "conflicting labels", holders: noLabel, relookup: func(e heldEnv) {
			e.rec.SetScope(e.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}, desc: func(_ heldEnv, id string, p apitest.HeldName) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: id, Scope: true}).AfterHeldName(p)
		}, want: ptWant{lookup: "provenance_conflict", outcome: conflict, sessionID: "$4"}, disagree: true},
		{name: "unreadable", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailTimeout}), unended: true,
			desc: after(func(heldEnv) apitest.DescCase { return apitest.DescCallTimeout(tmux.CallLookup, boundQ) }),
			want: ptWant{lookup: "cant_tell", outcome: ptErrName(api.ErrTmuxUnresponsive)}},
		{name: "tmux unavailable", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			desc: after(func(e heldEnv) apitest.DescCase { return apitest.DescSocketPermission(e.socket) }),
			want: ptWant{lookup: "tmux_unavailable", outcome: ptErrName(api.ErrTmuxNotAvailable)}},
	}
	ends := []struct {
		name      string
		atCreate  func(t *testing.T, e heldEnv, id string) // as the create returns, before the end write; nil: it applies
		check     func(t *testing.T, e heldEnv, cols apitest.SpawnColumns, insertedAt time.Time)
		row       apitest.HeldRow
		rowResult string
		warns     int
	}{
		{name: "end write applied", row: apitest.HeldRowEnded, rowResult: "ended"},
		{name: "another versioned write", atCreate: func(t *testing.T, e heldEnv, id string) {
			apitest.SeedSessionID(t, e.dbPath, id, uuid.NewString())
		}, check: func(t *testing.T, _ heldEnv, cols apitest.SpawnColumns, _ time.Time) {
			if cols.State != store.StatePending || cols.RowVersion != int64(1) || cols.ClaudeSessionID == nil {
				t.Errorf("after the competing write: state %v, row_version %v, session %v; want pending, 1, set",
					cols.State, cols.RowVersion, cols.ClaudeSessionID)
			}
		}, row: apitest.HeldRowLeftAsIs, rowResult: "left_changed"},
		{name: "deleted and inserted again", atCreate: func(t *testing.T, e heldEnv, id string) {
			if res, _ := adminapi.Delete(e.c, []string{id}); res.Results[id] != "ok" {
				t.Fatalf("Delete(%s) = %v; want ok", id, res.Results)
			}
			if _, err := apitest.SeedSpawn(e.dbPath, id, store.StatePending, "", "", "", false,
				apitest.WithLaunchStartedAt(e.start.Add(-time.Hour).UnixMilli())); err != nil {
				t.Fatalf("SeedSpawn(%s): %v", id, err)
			}
		}, check: func(t *testing.T, e heldEnv, cols apitest.SpawnColumns, _ time.Time) {
			if cols.State != store.StatePending || cols.RowVersion != int64(0) || cols.LaunchStartedAt != e.start.Add(-time.Hour).UnixMilli() {
				t.Errorf("re-seeded row: state %v, row_version %v, launch_started_at %v; want pending, 0, an hour before the spawn",
					cols.State, cols.RowVersion, cols.LaunchStartedAt)
			}
		}, row: apitest.HeldRowLeftAsIs, rowResult: "left_changed"},
		{name: "end write failed", atCreate: func(t *testing.T, e heldEnv, id string) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		}, check: func(t *testing.T, _ heldEnv, cols apitest.SpawnColumns, insertedAt time.Time) {
			if cols.State != store.StatePending || cols.RowVersion != int64(0) || cols.EndedAt != nil ||
				cols.LaunchStartedAt != insertedAt.UnixMilli() {
				t.Errorf("row: state %v, row_version %v, ended_at %v, launch_started_at %v; want pending, 0, NULL, %d",
					cols.State, cols.RowVersion, cols.EndedAt, cols.LaunchStartedAt, insertedAt.UnixMilli())
			}
		}, row: apitest.HeldRowStoreError, rowResult: "still_pending", warns: 1},
	}
	for _, tc := range cases {
		for _, end := range ends {
			// A delete-and-reinsert is the same not-applied end write as another versioned write.
			if end.atCreate != nil && !tc.unended || end.name == "deleted and inserted again" && tc.name != "no label" {
				continue
			}
			t.Run(tc.name+"/"+end.name, func(t *testing.T) {
				e := newHeldEnv(t)
				id := heldID()
				wantCalls, wantCharge := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}, 2*boundQ+boundC // scan, create, re-lookup
				if tc.minted {
					id, wantCalls, wantCharge = "", wantCalls[1:], boundQ+boundC
				}
				var seeded []tmuxfix.SeedSession
				if tc.holders != nil {
					seeded = tc.holders(e, id)
					e.rec.SeedSessions(e.socket, seeded...)
				}
				if tc.vanish {
					e.rec.RemoveSessionAfter(tmux.CallCreate, e.socket, seeded[0].ID)
				}
				var written apitest.SpawnColumns
				if end.atCreate != nil {
					e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
						end.atCreate(t, e, id)
						written = e.readRow(t, id)
					})
				}
				var insertedAt time.Time
				mark := trailLen(t)

				run := e.spawnHeld(t, id, heldName, func() {
					insertedAt = e.clock.Now()
					if tc.late != nil {
						late := tc.late(e, id)
						seeded = append(seeded, late...)
						e.rec.SeedSessions(e.socket, late...)
					}
					if tc.relookup != nil {
						tc.relookup(e)
					}
				})
				id = run.id

				assertOneName(t, run.err, tc.want.outcome)
				var token string
				wantState := any(store.StateEnded)
				if end.atCreate == nil {
					token = e.assertEndedRow(t, id, run.createdAt)
				} else {
					end.check(t, e, written, insertedAt)
					e.assertRowIs(t, id, "after the spawn (the end write wrote nothing)", written)
					token, _ = written.LaunchToken.(string)
					wantState = store.StatePending
				}
				if run.stateAtRelookup != wantState {
					t.Errorf("row state when the re-lookup returned = %v; want %v (the end write first)", run.stateAtRelookup, wantState)
				}
				if run.err != nil {
					_, desc := errnames.Classify(run.err)
					p := apitest.HeldName{Name: heldName, SessionID: tc.want.sessionID, Row: end.row, InstanceID: id}
					apitest.AssertDescription(t, desc, tc.desc(e, id, p), append(e.forbid(id, seeded), append(tc.forbid, token)...)...)
				}
				lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
				if e.logs.Len() == 0 {
					lines = nil
				}
				if len(lines) != end.warns {
					t.Errorf("client log lines = %q; want %d", lines, end.warns)
				}
				if end.warns == 1 && len(lines) == 1 {
					apitest.AssertDescription(t, lines[0], apitest.DescCase{Name: "held-name end-write WARN line", Require: []string{"WARN", id}},
						append(e.forbid(id, seeded), token, heldName, "$4")...)
				}
				if got := callKinds(e.rec); !reflect.DeepEqual(got, wantCalls) || len(e.rec.Calls()) != 0 {
					t.Errorf("tmux calls = %q (+%d name-based); want %q", got, len(e.rec.Calls()), wantCalls)
				}
				if after := e.rec.Sessions(e.socket); !reflect.DeepEqual(after, run.sessionsAtCreate) {
					t.Errorf("sessions changed after the create:\nat create %+v\nafter     %+v", run.sessionsAtCreate, after)
				}
				if charged := e.clock.Now().Sub(e.start); charged != wantCharge {
					t.Errorf("virtual time charged = %v; want %v, each call its default timeout (SR-13.2)", charged, wantCharge)
				}
				w := tc.want
				w.rowResult, w.storeError = end.rowResult, end.warns == 1
				e.ptAssertRecord(t, mark, id, w, e.ptForbid(id, token, seeded, other)...)

				dis := ptRecords(t, mark, "ad.provenance.disagree", id)
				if !tc.disagree && len(dis) != 0 || tc.disagree && len(dis) != 1 {
					t.Fatalf("ad.provenance.disagree records = %v; want one only for conflicting labels", dis)
				}
				if tc.disagree {
					assertDisagreeRecord(t, dis[0], killRow{ID: id, Name: heldName, Socket: e.socket, Session: tmuxfix.SeedSession{ID: "$4"}},
						"spawn", "ad_spawn", disagreeWant{reason: string(tmux.ReasonScopeValue), server: string(tmux.ServerUnknown),
							verdict: "provenance_conflict", action: "ended", ours: true})
				}
			})
		}
	}
}
