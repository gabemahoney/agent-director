package api_test

// find_missing_disagree_test.go: find-missing's ad.provenance.disagree records (SR-3.16, SR-11.3, SR-14): one per
// distinct reason per row per sweep, each carrying the fields of the observation that produced it, written after
// the row's write settled. Rows and writes come from the fake store, tmux from a tmuxfix.Recorder and process
// answers from procfix (find_missing_test.go).

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// dgOldServer is a server identity a row records that no longer answers on its socket; its process is gone
// unless a case sets it alive.
var dgOldServer = tmuxfix.Server{PID: 7002, Start: fmServer.Start - 600, ProcStart: fmStart}

// dgPanePID is a recorded pane pid (unreadable in most cases); dgTokenPID is the pid of a pane carrying a token.
const (
	dgPanePID  = 41
	dgTokenPID = 42
)

// dgWithServer records s as the row's tmux server identity.
func dgWithServer(s tmuxfix.Server) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) {
		r.Identity.ServerPID, r.Identity.ServerStart, r.Identity.ServerStarttime = s.PID, s.Start, s.ProcStart
	}
}

// dgUnknownPane records a pane whose start time the checker cannot read (evidence unknown).
func dgUnknownPane() fmRowOpt { return withPane(dgPanePID, fmStart) }

// dgSession is row r's own session: id, stored name and r's current label, with panes (none: one new pane).
func dgSession(r store.LiveSpawnIdentity, id, name string, panes ...tmuxfix.SeedPane) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{ID: id, Name: name, Label: tmuxfix.Valid(r.Identity.Token, r.ClaudeInstanceID, tmuxfix.StoreID),
		Panes: panes}
}

// dgServer is a Recorder whose fmServer on apitest.TestSocket holds sessions.
func dgServer(sessions ...tmuxfix.SeedSession) *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, fmServer).SeedSessions(apitest.TestSocket, sessions...)
}

// dgTokenPane is a pane carrying the default launch token, pid dgTokenPID.
var dgTokenPane = tmuxfix.SeedPane{PID: dgTokenPID, AdPane: tmuxfix.Token}

// dgChecker is a procfix checker with the recorded pane unreadable, the token pane alive, and procs on top.
func dgChecker(procs map[int]procfix.Process) *procfix.Checker {
	pc := procfix.New()
	pc.Set(dgPanePID, procfix.Unreadable())
	pc.Set(dgTokenPID, procfix.Alive(fmStart))
	for pid, p := range procs {
		pc.Set(pid, p)
	}
	return pc
}

// dgWant is one expected record; sessionID and current "" mean null.
type dgWant struct {
	reason, server, verdict, sessionID, current, action string
}

// dgCase is one row and the tmux, process and store answers that give it its reasons.
type dgCase struct {
	name  string
	opts  []fmRowOpt
	tmux  func(r store.LiveSpawnIdentity) *tmuxfix.Recorder
	procs map[int]procfix.Process
	// answer scripts the row's op ("adopt", "note") write.
	op     string
	answer fmAnswer
	want   []dgWant
}

// dgRenamed is the stored name of a row's session that was renamed by hand.
const dgRenamed = "renamed-by-hand"

// dgCases covers each lookup reason, the listing's own reason, the combined rows and every action.
func dgCases() []dgCase {
	// ours: the row's own session "$4" under name ("" = the recorded one) with panes.
	ours := func(name string, panes ...tmuxfix.SeedPane) func(store.LiveSpawnIdentity) *tmuxfix.Recorder {
		return func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
			stored := cmp.Or(name, r.TmuxSessionName)
			return dgServer(dgSession(r, "$4", stored, panes...))
		}
	}
	recorded := ours("")
	noServerListing := func(name string) func(store.LiveSpawnIdentity) *tmuxfix.Recorder {
		return func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
			return ours(name)(r).Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailNoServer}, tmux.CallListPanes)
		}
	}
	serverAlive := map[int]procfix.Process{fmServer.PID: procfix.Alive(fmServer.ProcStart)}
	unverified, unverifiedOn := "left_unverified", []fmRowOpt{dgUnknownPane(), withServer()}
	return []dgCase{
		{name: "server_restarted", opts: []fmRowOpt{dgUnknownPane(), dgWithServer(dgOldServer)},
			tmux: func(store.LiveSpawnIdentity) *tmuxfix.Recorder {
				return dgServer(tmuxfix.SeedSession{ID: "$2", Name: "other"})
			},
			want: []dgWant{{"server_restarted", "restarted", "gone", "", "", "marked_missing"}}},
		{name: "server_mismatch", opts: []fmRowOpt{dgUnknownPane(), dgWithServer(dgOldServer)}, tmux: recorded,
			procs: map[int]procfix.Process{dgOldServer.PID: procfix.Alive(dgOldServer.ProcStart)},
			want:  []dgWant{{"server_mismatch", "differs", "different_server", "", "", unverified}}},
		{name: "duplicate_label", opts: unverifiedOn,
			tmux: func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
				return dgServer(dgSession(r, "$4", r.TmuxSessionName), dgSession(r, "$5", "copy"))
			},
			want: []dgWant{{"duplicate_label", "match", "provenance_conflict", "", "", unverified}}},
		{name: "scope_value", opts: unverifiedOn,
			tmux: func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
				return recorded(r).SetScope(apitest.TestSocket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
			},
			want: []dgWant{{"scope_value", "match", "provenance_conflict", "", "", unverified}}},
		{name: "adopted", tmux: ours("", dgTokenPane),
			want: []dgWant{{"adopted", "unknown", "ours", "$4", "", "left_live"}}},
		{name: "name_changed", opts: unverifiedOn, tmux: ours(dgRenamed),
			want: []dgWant{{"name_changed", "match", "ours", "$4", dgRenamed, unverified}}},
		{name: "name_changed note refused", opts: unverifiedOn, tmux: ours(dgRenamed),
			op: "note", answer: fmAnswer{res: store.CondChanged},
			want: []dgWant{{"name_changed", "match", "ours", "$4", dgRenamed, "left_changed"}}},
		{name: "name_changed note store error", opts: unverifiedOn, tmux: ours(dgRenamed),
			op: "note", answer: fmAnswer{err: errors.New("disk I/O error")},
			want: []dgWant{{"name_changed", "match", "ours", "$4", dgRenamed, "store_error"}}},
		{name: "listing server_mismatch", opts: []fmRowOpt{withServer()}, procs: serverAlive, tmux: noServerListing(""),
			want: []dgWant{{"server_mismatch", "differs", "different_server", "", "", unverified}}},
		{name: "adopted and name_changed", tmux: ours(dgRenamed, dgTokenPane), want: []dgWant{
			{"adopted", "unknown", "ours", "$4", "", "left_live"},
			{"name_changed", "unknown", "ours", "$4", dgRenamed, "left_live"}}},
		{name: "listing server_mismatch and name_changed", opts: []fmRowOpt{withServer()}, procs: serverAlive,
			tmux: noServerListing(dgRenamed), want: []dgWant{
				{"server_mismatch", "differs", "different_server", "", "", unverified},
				{"name_changed", "match", "ours", "$4", dgRenamed, unverified}}},
		{name: "adoption refused", tmux: ours(dgRenamed, dgTokenPane), op: "adopt", answer: fmAnswer{res: store.CondChanged},
			want: []dgWant{{"name_changed", "unknown", "ours", "$4", dgRenamed, "left_changed"}}},
		{name: "adoption store error", tmux: ours(dgRenamed, dgTokenPane), op: "adopt",
			answer: fmAnswer{err: errors.New("disk I/O error")},
			want:   []dgWant{{"name_changed", "unknown", "ours", "$4", dgRenamed, "store_error"}}},
	}
}

// dgID is the instance id of case name under prefix.
func dgID(prefix, name string) string { return prefix + "-" + strings.ReplaceAll(name, " ", "-") }

// dgStore is a fake store over rows whose writes are stamped with the trail length.
func dgStore(t *testing.T, rows ...store.LiveSpawnIdentity) *fakeFindMissingStore {
	return &fakeFindMissingStore{rows: rows, trailAt: func() int { return trailLen(t) }}
}

// dgRun is one sweep of case c's row (id prefix-name): the row, the fake store, the result and the log.
type dgRun struct {
	row store.LiveSpawnIdentity
	st  *fakeFindMissingStore
	res api.FindMissingResult
	lg  *recordingLogger
}

// dgSweep sweeps case c's row once.
func dgSweep(t *testing.T, prefix string, c dgCase) dgRun {
	t.Helper()
	run := dgRun{row: liveRow(dgID(prefix, c.name), c.opts...), lg: &recordingLogger{}}
	run.st = dgStore(t, run.row)
	if c.op != "" {
		run.st.answers = map[string]map[string]fmAnswer{c.op: {run.row.ClaudeInstanceID: c.answer}}
	}
	run.res = mustSweep(t, run.st, dgChecker(c.procs), fmSweep{tmux: c.tmux(run.row), lg: run.lg})
	return run
}

// dgRecords returns id's ad.provenance.disagree records after trail line before, failing unless each follows every
// other trail line and store write of the row (written once its write settled).
func dgRecords(t *testing.T, before int, id string, st *fakeFindMissingStore) []map[string]any {
	t.Helper()
	settled := 0
	for _, c := range st.calls {
		if c.id == id {
			settled = max(settled, c.at)
		}
	}
	var out []map[string]any
	for i, l := range readAPITrailLines(t)[before:] {
		if l["claude_instance_id"] != id {
			continue
		}
		if l["event"] != "ad.provenance.disagree" {
			settled = max(settled, before+i+1)
			continue
		}
		if before+i < settled {
			t.Errorf("%s: disagree record at line %d precedes line %d, after the row's writes and other trail lines", id, before+i, settled)
		}
		out = append(out, l)
	}
	return out
}

// assertDisagree fails unless recs are exactly want, in order, with the pinned fields and no label content.
func assertDisagree(t *testing.T, recs []map[string]any, r store.LiveSpawnIdentity, want []dgWant) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("%s: ad.provenance.disagree records = %v; want %d (%+v)", r.ClaudeInstanceID, recs, len(want), want)
	}
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	for i, w := range want {
		fields := map[string]any{
			"claude_instance_id": r.ClaudeInstanceID, "verb": "find-missing", "source": "ad_find_missing",
			"reason": w.reason, "tmux_socket": apitest.TestSocket, "tmux_session_name": r.TmuxSessionName,
			"tmux_session_id": null(w.sessionID), "current_session_name": null(w.current), "server": w.server,
			"verdict": w.verdict, "action": w.action, "caller_pid": float64(os.Getpid()),
		}
		for k, v := range fields {
			if got, ok := recs[i][k]; !ok || got != v {
				t.Errorf("%s record %d [%q] = %v (present %t); want %v", r.ClaudeInstanceID, i, k, got, ok, v)
			}
		}
		for k, v := range recs[i] {
			if s, ok := v.(string); ok && (strings.Contains(s, r.Identity.Token) || strings.Contains(s, tmuxfix.StoreID)) {
				t.Errorf("%s record %d [%q] = %q carries label content", r.ClaudeInstanceID, i, k, s)
			}
		}
	}
}

// TestFindMissingDisagreeReasons: each reason the lookup, its adoption or its listing gives writes exactly one record
// per row, with the fields of the observation behind it and the row's settled action.
func TestFindMissingDisagreeReasons(t *testing.T) {
	for _, c := range dgCases() {
		t.Run(c.name, func(t *testing.T) {
			before := trailLen(t)
			run := dgSweep(t, "dg", c)
			assertDisagree(t, dgRecords(t, before, run.row.ClaudeInstanceID, run.st), run.row, c.want)
		})
	}
}

// TestFindMissingDisagreeSharedSocketPerSweep: rows sharing one socket's lookup each get their own record, and a
// second sweep writes one more per row.
func TestFindMissingDisagreeSharedSocketPerSweep(t *testing.T) {
	var (
		rows     []store.LiveSpawnIdentity
		sessions []tmuxfix.SeedSession
		wants    []dgWant
	)
	for i, sfx := range []string{"a", "b", "c"} {
		r := liveRow("dg-shared-"+sfx, dgUnknownPane(), withServer())
		id, name := fmt.Sprintf("$%d", 4+i), dgRenamed+"-"+sfx
		rows, sessions = append(rows, r), append(sessions, dgSession(r, id, name))
		wants = append(wants, dgWant{"name_changed", "match", "ours", id, name, "left_unverified"})
	}
	rec, st, clock := dgServer(sessions...), dgStore(t, rows...), tmuxfix.NewClock(fmNow)
	for sweep := 1; sweep <= 2; sweep++ {
		before := trailLen(t)
		mustSweep(t, st, dgChecker(nil), fmSweep{tmux: rec, clock: clock})
		assertLookups(t, rec, sweep)
		for i, r := range rows {
			assertDisagree(t, dgRecords(t, before, r.ClaudeInstanceID, st), r, wants[i:i+1])
		}
	}
}

// TestFindMissingDisagreeNoneWhenNormal: Ours under the recorded name on the recorded server writes no record, for
// unknown evidence and for none recorded.
func TestFindMissingDisagreeNoneWhenNormal(t *testing.T) {
	rows := []store.LiveSpawnIdentity{
		liveRow("dg-normal-unknown", dgUnknownPane(), withServer()),
		liveRow("dg-normal-none", withServer(), func(r *store.LiveSpawnIdentity) { r.Identity.PaneID = "%1" }),
	}
	rec := dgServer(dgSession(rows[0], "$4", rows[0].TmuxSessionName), dgSession(rows[1], "$5", rows[1].TmuxSessionName))
	st := dgStore(t, rows...)
	before := trailLen(t)
	res := mustSweep(t, st, dgChecker(nil), fmSweep{tmux: rec})
	assertLists(t, res, nil, []string{"dg-normal-none", "dg-normal-unknown"})
	assertLookups(t, rec, 1)
	for _, r := range rows {
		assertDisagree(t, dgRecords(t, before, r.ClaudeInstanceID, st), r, nil)
	}
}

// dgChildEnv gates TestFindMissingDisagreeFailOpenChild and carries the id prefix.
const dgChildEnv = "AD_FIND_MISSING_DISAGREE_TRAIL_FAIL_CHILD"

// dgLinePrefix marks the child's result lines in its output.
const dgLinePrefix = "DG|"

// dgFailOpenRuns sweeps every dgCases row under prefix and returns one line each: result lists, writes and log.
func dgFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	var lines []string
	for _, c := range dgCases() {
		run := dgSweep(t, prefix, c)
		var writes []string
		for _, w := range run.st.calls {
			writes = append(writes, fmt.Sprintf("%s/%s/%d/%+v", w.op, w.note, w.snap.RowVersion, w.identity))
		}
		lines = append(lines, fmt.Sprintf("%s ids=%v unverified=%v writes=%v logs=%d", run.row.ClaudeInstanceID,
			run.res.IDs, run.res.UnverifiedIDs, writes, len(run.lg.lines)))
	}
	return lines
}

// TestFindMissingDisagreeFailOpen: with the trail unwritable, every disagree case gives the same result lists,
// writes and log lines as with a working trail.
func TestFindMissingDisagreeFailOpen(t *testing.T) {
	prefix := "dg-failopen"
	before := trailLen(t)
	want := dgFailOpenRuns(t, prefix)
	wantRecs := 0
	for _, c := range dgCases() {
		wantRecs += len(c.want)
	}
	if n := len(trailSince(t, before, "ad.provenance.disagree")); n != wantRecs {
		t.Fatalf("working trail: ad.provenance.disagree records = %d; want %d", n, wantRecs)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFindMissingDisagreeFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), dgChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestFindMissingDisagreeFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, dgLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestFindMissingDisagreeFailOpenChild is TestFindMissingDisagreeFailOpen's child: it sweeps with an unwritable
// trail and prints the lines.
func TestFindMissingDisagreeFailOpenChild(t *testing.T) {
	prefix := os.Getenv(dgChildEnv)
	if prefix == "" {
		t.Skip("run only as TestFindMissingDisagreeFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.disagree_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}
	for _, l := range dgFailOpenRuns(t, prefix) {
		fmt.Println(dgLinePrefix + l)
	}
}
