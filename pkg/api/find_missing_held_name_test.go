package api_test

// find_missing_held_name_test.go: find-missing's held-name path (SR-11.3, SR-14, SR-3.10; AC-FM-02, AC-FM-17,
// AC-FM-18). A row marked with tick reason tmux_name_held gets exactly one ad.launch.name_held from the sweep and
// the holder gets no call beyond the lookup. Rows are seeded through apitest.SeedSpawn in real stores; tmux
// answers come from a tmuxfix.Recorder. It holds the hnEnv fixture find_missing_held_name_launch_test.go uses.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// hnCreated is a holder's creation epoch; hnAgentPID is a running agent process's pid.
const (
	hnCreated  = int64(1790000200)
	hnAgentPID = 4711
)

// hnPast is the first whole second past the default pending grace period.
var hnPast = fmGrace + time.Second

// hnEnv is one real store with its id, a Recorder, the clock the sweep and the Recorder share, a process fake
// and the sweep's logger.
type hnEnv struct {
	s               *store.Store
	dbPath, storeID string
	rec             *tmuxfix.Recorder
	clock           *tmuxfix.Clock
	pc              *procfix.Checker
	lg              *recordingLogger
}

// newHNEnv builds an hnEnv over a fresh store, its clock at fmNow.
func newHNEnv(t *testing.T) *hnEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.InitStore(dbPath); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &hnEnv{s: s, dbPath: dbPath, storeID: storeID, rec: tmuxfix.NewRecorder(), clock: tmuxfix.NewClock(fmNow),
		pc: procfix.New(), lg: &recordingLogger{}}
}

// hnRow is a seeded row's id, recorded session name and launch token, as stored.
type hnRow struct{ id, name, token string }

// seedAs seeds row id in state with sessionID and opts and returns it as stored.
func (e *hnEnv) seedAs(t *testing.T, id, state, sessionID string, opts ...apitest.SpawnOption) hnRow {
	t.Helper()
	if _, err := apitest.SeedSpawn(e.dbPath, id, state, "", "", sessionID, false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	c := e.cols(t, id)
	name, _ := c.TmuxSessionName.(string)
	token, _ := c.LaunchToken.(string)
	return hnRow{id: id, name: name, token: token}
}

// pending seeds a pending row whose create made nothing (no pane), its launch started at the clock's now.
func (e *hnEnv) pending(t *testing.T, id, sessionID string, opts ...apitest.SpawnOption) hnRow {
	t.Helper()
	base := []apitest.SpawnOption{apitest.WithNoPane(), apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli())}
	return e.seedAs(t, id, store.StatePending, sessionID, append(base, opts...)...)
}

// cols reads id's row, failing the test on any error.
func (e *hnEnv) cols(t *testing.T, id string) apitest.SpawnColumns {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return c
}

// sweep advances the clock by d, runs one sweep and returns its result and the trail line count before it.
func (e *hnEnv) sweep(t *testing.T, d time.Duration) (api.FindMissingResult, int) {
	t.Helper()
	e.clock.Advance(d)
	mark := trailLen(t)
	return mustSweep(t, e.s, e.pc, fmSweep{tmux: e.rec, clock: e.clock, lg: e.lg}), mark
}

// assertHolderUntouched fails unless the Recorder saw exactly one lookup, no other call, and sessions unchanged.
func (e *hnEnv) assertHolderUntouched(t *testing.T, sessions []tmuxfix.SeedSession) {
	t.Helper()
	assertLookups(t, e.rec, 1)
	if n := len(e.rec.Calls()); n != 0 {
		t.Errorf("name-based tmux calls = %d; want 0", n)
	}
	if got := e.rec.Sessions(apitest.TestSocket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions changed:\nbefore %+v\nafter  %+v", sessions, got)
	}
}

// hnHolder is a session holding name as $4, created at created, with label (tmux.Label{} for none).
func hnHolder(name string, label tmux.Label, created int64) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{ID: "$4", Name: name, Created: created, Label: label}
}

// hnWant is a sweep record's per-case fields: holder nil when none was identified; carries and current true,
// false or nil (null); storeError asks for a non-empty store_error.
type hnWant struct {
	holder            *tmuxfix.SeedSession
	carries, current  any
	lookup, rowResult string
	storeError        bool
}

// assertSweepRecord fails unless recs is exactly one find-missing ad.launch.name_held for r with w's fields, the
// socket and this store's id, and exactly SR-14's keys.
func assertSweepRecord(t *testing.T, recs []map[string]any, r hnRow, socket, storeID string, w hnWant) {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("ad.launch.name_held records for %s = %d; want 1", r.id, len(recs))
	}
	rec := recs[0]
	q := func(s string) string { return "'" + s + "'" }
	want := map[string]any{"source": "ad_find_missing", "claude_instance_id": r.id, "launch": nil, "outcome": nil,
		"tmux_session_name": r.name, "tmux_socket": socket, "store_id": storeID, "lookup_outcome": w.lookup,
		"row_result": w.rowResult, "carries_this_id": w.carries, "current_launch": w.current, "store_error": nil,
		"tmux_session_id": nil, "session_created": nil, "attach_command": nil, "end_command": nil}
	if h := w.holder; h != nil {
		want["tmux_session_id"], want["session_created"] = h.ID, float64(h.Created)
		want["attach_command"] = "tmux -u -S " + q(socket) + " attach-session -r -t " + q(h.ID)
		want["end_command"] = "tmux -u -S " + q(socket) + " kill-session -t " + q(h.ID)
	}
	if w.storeError {
		if msg, _ := rec["store_error"].(string); msg == "" {
			t.Errorf("store_error = %v; want the store's error text", rec["store_error"])
		}
		delete(want, "store_error")
	}
	for k, v := range ptCaller() {
		want[k] = v
	}
	for k, v := range want {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("name_held[%q] = %v (present %t); want %v", k, got, ok, v)
		}
	}
	var keys []string
	for k := range rec {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wantKeys := slices.Clone(ptKeys)
	slices.Sort(wantKeys)
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("record keys = %q; want %q", keys, wantKeys)
	}
}

// assertMarkTick fails unless r's first tick since mark has reason and lookup_outcome lookup, and the recorded
// name exactly when reason is tmux_name_held.
func assertMarkTick(t *testing.T, mark int, r hnRow, reason, lookup string) {
	t.Helper()
	ticks := ticksSince(t, mark, r.id)
	if len(ticks) == 0 {
		t.Fatalf("no tick for %s; want a %s mark tick", r.id, reason)
	}
	assertAPITrailStr(t, ticks[0], "reconciliation_reason", reason)
	assertAPITrailStr(t, ticks[0], "lookup_outcome", lookup)
	name, ok := ticks[0]["tmux_session_name"]
	if held := reason == "tmux_name_held"; held != ok || (held && name != r.name) {
		t.Errorf("tick tmux_session_name = %v (present %t); want %q only on tmux_name_held", name, ok, r.name)
	}
}

// TestFindMissingHeldNameRecord: a row past grace whose lookup is Gone or Leftover is marked; exactly when a
// session holds its name the tick is tmux_name_held and one record names the holder (SR-14, AC-FM-02, AC-FM-18).
func TestFindMissingHeldNameRecord(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	const dollarName = "held$name" // its stored forms are held$name and held\$name
	launchSec := fmNow.Unix()
	leftover := func(created int64) func(*hnEnv, hnRow) []tmuxfix.SeedSession {
		return func(e *hnEnv, r hnRow) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{hnHolder(r.name, tmuxfix.Valid(tmuxfix.OtherToken, r.id, e.storeID), created)}
		}
	}
	cases := []struct {
		name     string
		working  bool   // a working row whose process is unreadable (AC-FM-02); else a no-pane pending row
		rowName  string // "" keeps SeedSpawn's name
		holders  func(e *hnEnv, r hnRow) []tmuxfix.SeedSession
		held     bool // a single holder is identified (holders[0])
		carries  any
		current  any
		lookup   string
		absentOK bool // tick tmux_absent, no record
	}{
		{name: "no valid label", holders: func(_ *hnEnv, r hnRow) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{hnHolder(r.name, tmux.Label{}, hnCreated)}
		}, held: true, carries: false, lookup: "gone"},
		{name: "another row's session", holders: func(e *hnEnv, r hnRow) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{hnHolder(r.name, tmuxfix.Valid(tmuxfix.OtherToken, "other-"+r.id, e.storeID), hnCreated)}
		}, held: true, carries: false, lookup: "gone"},
		{name: "another store's session naming this row", holders: func(e *hnEnv, r hnRow) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{hnHolder(r.name, tmuxfix.Valid(r.token, r.id, apitest.OtherStoreID(e.storeID)), hnCreated)}
		}, held: true, carries: false, lookup: "gone"},
		{name: "leftover of this agent", holders: leftover(hnCreated), held: true, carries: true, current: false, lookup: "leftover"},
		{name: "leftover created in the launch start's second", holders: leftover(launchSec),
			held: true, carries: true, current: false, lookup: "leftover"},
		{name: "leftover after a backward clock step", holders: leftover(launchSec - 3600),
			held: true, carries: true, current: false, lookup: "leftover"},
		{name: "leftover after a forward clock step", holders: leftover(launchSec + 3600),
			held: true, carries: true, current: false, lookup: "leftover"},
		{name: "two entries match a $ name", rowName: dollarName, holders: func(_ *hnEnv, _ hnRow) []tmuxfix.SeedSession {
			forms := tmux.StoredForms(dollarName)
			return []tmuxfix.SeedSession{{ID: "$4", Name: forms[0], Created: hnCreated}, {ID: "$5", Name: forms[1], Created: hnCreated}}
		}, lookup: "gone"},
		{name: "working row unreadable, no valid label", working: true, holders: func(_ *hnEnv, r hnRow) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{hnHolder(r.name, tmux.Label{}, hnCreated)}
		}, held: true, carries: false, lookup: "gone"},
		{name: "name free", holders: func(*hnEnv, hnRow) []tmuxfix.SeedSession { return nil }, lookup: "gone", absentOK: true},
		{name: "leftover under another name", holders: func(e *hnEnv, r hnRow) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{hnHolder("old-"+r.id, tmuxfix.Valid(tmuxfix.OtherToken, r.id, e.storeID), hnCreated)}
		}, lookup: "leftover", absentOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHNEnv(t)
			var opts []apitest.SpawnOption
			if tc.rowName != "" {
				opts = append(opts, apitest.WithTmuxSessionName(tc.rowName))
			}
			var r hnRow
			if tc.working {
				e.pc.Set(apitest.TestPanePID, procfix.Unreadable())
				r = e.seedAs(t, "h-1", store.StateWorking, uuid.NewString(), opts...)
			} else {
				r = e.pending(t, "h-1", "", opts...)
			}
			holders := tc.holders(e, r)
			if len(holders) > 0 {
				e.rec.SeedSessions(apitest.TestSocket, holders...)
			}
			sessions := e.rec.Sessions(apitest.TestSocket)

			res, mark := e.sweep(t, hnPast)
			assertLists(t, res, []string{r.id}, nil)
			if c := e.cols(t, r.id); c.State != store.StateMissing {
				t.Errorf("state = %v; want missing", c.State)
			}
			recs := ptRecords(t, mark, "ad.launch.name_held", r.id)
			if tc.absentOK {
				assertMarkTick(t, mark, r, "tmux_absent", tc.lookup)
				if len(recs) != 0 {
					t.Errorf("ad.launch.name_held records = %v; want none on tmux_absent", recs)
				}
			} else {
				assertMarkTick(t, mark, r, "tmux_name_held", tc.lookup)
				w := hnWant{carries: tc.carries, current: tc.current, lookup: tc.lookup, rowResult: "marked_missing"}
				if tc.held {
					w.holder = &holders[0]
				}
				assertSweepRecord(t, recs, r, apitest.TestSocket, e.storeID, w)
			}
			e.assertHolderUntouched(t, sessions)
		})
	}
}

// TestFindMissingHeldNamePendingGrace (AC-FM-17): a fresh or resumed pending row whose name an unlabelled session
// holds is left alone inside grace and marked by the first sweep past it, with one record; the holder is untouched.
func TestFindMissingHeldNamePendingGrace(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, kind := range []struct{ name, sessionID string }{{"fresh spawn", ""}, {"resumed", uuid.NewString()}} {
		for _, earlier := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/earlier process alive %t", kind.name, earlier), func(t *testing.T) {
				e := newHNEnv(t)
				r := e.pending(t, "p-1", kind.sessionID)
				perm, err := apitest.SeedPermissionRequest(e.dbPath, r.id, "Bash")
				if err != nil {
					t.Fatalf("SeedPermissionRequest: %v", err)
				}
				holder := hnHolder(r.name, tmux.Label{}, hnCreated)
				if earlier { // an earlier launch's process runs in the holder's pane
					holder.Panes = []tmuxfix.SeedPane{{PID: hnAgentPID}}
					e.pc.Set(hnAgentPID, procfix.Alive(fmStart))
				}
				e.rec.SeedSessions(apitest.TestSocket, holder)
				sessions := e.rec.Sessions(apitest.TestSocket)
				was := e.cols(t, r.id)

				res, mark := e.sweep(t, fmGrace-time.Second)
				assertLists(t, res, nil, nil)
				if now := e.cols(t, r.id); !reflect.DeepEqual(now, was) {
					t.Errorf("row inside grace:\ngot  %+v\nwant %+v", now, was)
				}
				if n := len(e.rec.SocketCalls()); n != 0 || len(readAPITrailLines(t)) != mark {
					t.Errorf("inside grace: %d tmux calls, trail %d -> %d lines; want none", n, mark, len(readAPITrailLines(t)))
				}

				res, mark = e.sweep(t, 2*time.Second)
				assertLists(t, res, []string{r.id}, nil)
				c := e.cols(t, r.id)
				if c.State != store.StateMissing || c.EndedAt == nil || c.LaunchStartedAt != nil {
					t.Errorf("state, ended_at, launch_started_at = %v, %v, %v; want missing, set, NULL", c.State, c.EndedAt, c.LaunchStartedAt)
				}
				pr, err := e.s.GetPermissionRequest(r.id, perm.RequestToken)
				if err != nil || pr.Decision != "deny" || pr.DecisionReason != store.DecisionReasonFindMissing {
					t.Errorf("permission request = %+v, %v; want denied by find-missing", pr, err)
				}
				assertMarkTick(t, mark, r, "tmux_name_held", "gone")
				assertSweepRecord(t, ptRecords(t, mark, "ad.launch.name_held", r.id), r, apitest.TestSocket, e.storeID,
					hnWant{holder: &sessions[0], carries: false, lookup: "gone", rowResult: "marked_missing"})
				e.assertHolderUntouched(t, sessions)
				if calls := e.pc.StartTimeCalls(); len(calls) != 0 {
					t.Errorf("start-time reads = %v; want none (the row records no process)", calls)
				}
			})
		}
	}
}

// TestFindMissingHeldNameGuard: a mark that finds the row changed or absent, or fails in the store, lists nothing,
// ticks nothing, and leaves one record saying so (left_changed, or still_pending with store_error).
func TestFindMissingHeldNameGuard(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	cases := []struct {
		name      string
		arrange   func(t *testing.T, e *hnEnv, r hnRow)
		rowResult string
		gone      bool // the row is deleted
	}{
		{"deleted after the lookup", func(t *testing.T, e *hnEnv, r hnRow) {
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				if err := e.s.DeleteSpawn(r.id); err != nil {
					t.Errorf("DeleteSpawn: %v", err)
				}
			})
		}, "left_changed", true},
		{"versioned write after the lookup", func(t *testing.T, e *hnEnv, r hnRow) {
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				apitest.SeedSessionID(t, e.dbPath, r.id, uuid.NewString())
			})
		}, "left_changed", false},
		{"store error on the mark", func(t *testing.T, e *hnEnv, r hnRow) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.id)
		}, "still_pending", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHNEnv(t)
			r := e.pending(t, "g-1", "")
			e.rec.SeedSessions(apitest.TestSocket, hnHolder(r.name, tmux.Label{}, hnCreated))
			sessions := e.rec.Sessions(apitest.TestSocket)
			tc.arrange(t, e, r)

			res, mark := e.sweep(t, hnPast)
			assertLists(t, res, nil, nil)
			if ticks := ticksSince(t, mark, r.id); len(ticks) != 0 {
				t.Errorf("ticks = %v; want none", ticks)
			}
			recs := ptRecords(t, mark, "ad.launch.name_held", r.id)
			storeErr := tc.rowResult == "still_pending"
			assertSweepRecord(t, recs, r, apitest.TestSocket, e.storeID,
				hnWant{holder: &sessions[0], carries: false, lookup: "gone", rowResult: tc.rowResult, storeError: storeErr})
			if msg, _ := recs[0]["store_error"].(string); storeErr && (len(e.lg.lines) != 1 || !strings.Contains(e.lg.lines[0], msg)) {
				t.Errorf("log = %q; want one line with the record's store_error %q", e.lg.lines, msg)
			}
			c, err := apitest.ReadSpawnColumns(e.dbPath, r.id)
			if tc.gone != errors.Is(err, store.ErrSpawnNotFound) || (!tc.gone && c.State != store.StatePending) {
				t.Errorf("row after the sweep = %+v, %v; want deleted %t, else pending", c, err, tc.gone)
			}
			e.assertHolderUntouched(t, sessions)
		})
	}
}

// hnChildEnv gates TestFindMissingHeldNameTrailFailOpenChild and carries the id prefix.
const hnChildEnv = "AD_FIND_MISSING_HELD_TRAIL_FAIL_CHILD"

// hnLinePrefix marks the child's result lines in its output.
const hnLinePrefix = "HN|"

// hnFailOpenRuns sweeps one held-name row per mark result, ids prefix-<name>, and returns one line each: the
// result lists, the row's columns and the log line count.
func hnFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	cases := []struct {
		name    string
		label   func(e *hnEnv, id string) tmux.Label
		failing bool // the mark fails in the store
	}{
		{name: "no-label", label: func(*hnEnv, string) tmux.Label { return tmux.Label{} }},
		{name: "leftover", label: func(e *hnEnv, id string) tmux.Label { return tmuxfix.Valid(tmuxfix.OtherToken, id, e.storeID) }},
		{name: "store-error", label: func(*hnEnv, string) tmux.Label { return tmux.Label{} }, failing: true},
	}
	var lines []string
	for _, tc := range cases {
		e := newHNEnv(t)
		r := e.pending(t, prefix+"-"+tc.name, "")
		e.rec.SeedSessions(apitest.TestSocket, hnHolder(r.name, tc.label(e, r.id), hnCreated))
		if tc.failing {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.id)
		}
		res, _ := e.sweep(t, hnPast)
		c := e.cols(t, r.id)
		lines = append(lines, fmt.Sprintf("%s ids=%v unverified=%v state=%v row_version=%v ended=%t launch_started_at=%v logs=%d",
			tc.name, res.IDs, res.UnverifiedIDs, c.State, c.RowVersion, c.EndedAt != nil, c.LaunchStartedAt, len(e.lg.lines)))
	}
	return lines
}

// TestFindMissingHeldNameTrailFailOpen: with the trail file unwritable, held-name sweeps give the same results and
// rows as with a working trail.
func TestFindMissingHeldNameTrailFailOpen(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	prefix := "hn-failopen-" + uuid.NewString()[:8]
	mark := trailLen(t)
	want := hnFailOpenRuns(t, prefix)
	for _, l := range want {
		id := prefix + "-" + strings.Fields(l)[0]
		if n := len(ptRecords(t, mark, "ad.launch.name_held", id)); n != 1 {
			t.Fatalf("working trail: ad.launch.name_held records for %s = %d; want 1", id, n)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestFindMissingHeldNameTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), hnChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestFindMissingHeldNameTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, hnLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestFindMissingHeldNameTrailFailOpenChild is TestFindMissingHeldNameTrailFailOpen's child: it makes the trail
// file read-only, runs the sweeps and prints their lines.
func TestFindMissingHeldNameTrailFailOpenChild(t *testing.T) {
	t.Parallel()
	prefix := os.Getenv(hnChildEnv)
	if prefix == "" {
		t.Skip("run only as TestFindMissingHeldNameTrailFailOpen's child")
	}
	if err := os.MkdirAll(filepath.Dir(apiTrailFilePath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(apiTrailFilePath(), nil, 0o400); err != nil {
		t.Fatalf("create read-only trail file: %v", err)
	}
	if err := trail.Emit(context.Background(), "ad.test.find_missing_held_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range hnFailOpenRuns(t, prefix) {
		fmt.Println(hnLinePrefix + l)
	}
}
