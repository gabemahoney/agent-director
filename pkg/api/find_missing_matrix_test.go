package api_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// SR-20.6's pane identity x lookup matrix for pending rows (SR-11.3, the ordinary Gone rule; AC-FM-02, AC-FM-05,
// AC-FM-06, AC-FM-10, AC-FM-14, AC-FM-16..19, AC-LKP-20). The lookup columns of another store's sessions are the
// call table's (lookup_calltable_findmissing_test.go), which runs pending rows too; a pending row inside its grace
// period is find_missing_grace_sweep_test.go's and TestFindMissingHeldNamePendingGrace's.

// The pids the matrix's process checker answers for: the recorded pane process, the pane an adoption listing
// shows, and a live process carrying the row's id in its environment (a child or a leaked copy), in every cell.
const (
	mxPanePID  = 810
	mxAdoptPID = 820
	mxLeakPID  = 830
)

// mxOtherServer is a server other than the row's recorded fmServer.
var mxOtherServer = tmuxfix.Server{PID: 7002, Start: fmServer.Start + 60, ProcStart: fmStart}

// mxKind is a pending row kind past the default grace period: its started_at age before fmNow and row version.
type mxKind struct {
	name    string
	age     time.Duration
	version int64
}

// mxKinds: a fresh spawn's pending row and a resume's (started hours ago). The sweep reads a reuse's pending row
// as a fresh spawn's; a real reuse's is TestAdviceFollow_B1_LaunchInProgressFindMissing's ("reuse: create timed out").
var mxKinds = []mxKind{
	{name: "fresh spawn", age: fmGrace + time.Second, version: 1},
	{name: "resumed", age: 6 * time.Hour, version: 7},
}

// mxWant is a cell's expected outcome: the row's writes and the note written, its one tick's reason ("" = no
// tick) and lookup_outcome ("" = none), whether a session holds the recorded name (tmux_session_name on the tick
// and one ad.launch.name_held), the lookups and pane listings made, and, when not nil, the reader's pid calls.
type mxWant struct {
	ops               []string
	note              string
	reason, outcome   string
	held              bool
	lookups, listings int
	reads             []int
}

// mxMark is a mark decided by one lookup, with tick reason and lookup_outcome.
func mxMark(reason, outcome string, held bool) mxWant {
	return mxWant{ops: []string{"mark"}, reason: reason, outcome: outcome, held: held, lookups: 1}
}

// mxNote is an unverified row noted note after one lookup.
func mxNote(note string) mxWant {
	return mxWant{ops: []string{"note"}, note: note, reason: note, lookups: 1}
}

// mxPane is a recorded pane identity: its process's answer, or none recorded; fixed is the outcome whatever the
// lookup would say.
type mxPane struct {
	name  string
	proc  procfix.Process
	none  bool
	fixed *mxWant
}

// mxPanes: the recorded pane process alive (the row stays pending, noted unreported, b.kdf), dead or unreadable,
// or no pane recorded (a lost create reply).
var mxPanes = []mxPane{
	{name: "pane alive", proc: procfix.Alive(fmStart),
		fixed: &mxWant{ops: []string{"unreported"}, reason: "unreported", reads: []int{mxPanePID}}},
	{name: "pane dead", proc: procfix.Gone(),
		fixed: &mxWant{ops: []string{"mark"}, reason: "proc_absent", reads: []int{mxPanePID}}},
	{name: "pane unreadable", proc: procfix.Unreadable()},
	{name: "no pane", none: true},
}

// mxRow is a pending row past the default grace period, of kind k; with pane it records the pane process
// mxPanePID, with server fmServer's identity; opts apply last.
func mxRow(id string, k mxKind, pane, server bool, opts ...fmRowOpt) store.LiveSpawnIdentity {
	o := []fmRowOpt{withLaunch(store.StatePending, fmNow.Add(-fmGrace-time.Second).UnixMilli())}
	if server {
		o = append(o, withServer())
	}
	if pane {
		o = append(o, withPane(mxPanePID, fmStart))
	}
	r := liveRow(id, append(o, opts...)...)
	r.Snapshot.StartedAt, r.Snapshot.RowVersion = fmNow.Add(-k.age).Format(time.DateTime), k.version
	return r
}

// mxChecker is a process checker on which the tmux server fmServer runs and pid answers p.
func mxChecker(pid int, p procfix.Process) *procfix.Checker {
	pc := procfix.New()
	pc.Set(fmServer.PID, procfix.Alive(fmServer.ProcStart))
	pc.Set(pid, p)
	return pc
}

// mxRec is a Recorder whose server fmServer on apitest.TestSocket holds sessions.
func mxRec(sessions ...tmuxfix.SeedSession) *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, fmServer).SeedSessions(apitest.TestSocket, sessions...)
}

// mxSession is a session named name carrying label lb (zero: none), with panes (none: one unlabelled pane).
func mxSession(name string, lb tmux.Label, panes ...tmuxfix.SeedPane) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{Name: name, Label: lb, Panes: panes}
}

// mxLookup is a lookup column: its Recorder for row r (panes: the row's own session's panes), and the outcome
// for an unreadable pane process, which a row with no pane shares unless noneNote is set. For Ours, a row with no
// pane is decided by its adoption listing (mxListings); lostReply rows record no server identity either.
type mxLookup struct {
	name      string
	ours      bool
	lostReply bool
	rec       func(r store.LiveSpawnIdentity, panes []tmuxfix.SeedPane) *tmuxfix.Recorder
	unknown   mxWant
	noneNote  string
}

// mxHeldBy returns a column whose only session, labelled lb(r), is named name(r) on fmServer.
func mxHeldBy(name func(r store.LiveSpawnIdentity) string, lb func(r store.LiveSpawnIdentity) tmux.Label) func(store.LiveSpawnIdentity, []tmuxfix.SeedPane) *tmuxfix.Recorder {
	return func(r store.LiveSpawnIdentity, panes []tmuxfix.SeedPane) *tmuxfix.Recorder {
		return mxRec(mxSession(name(r), lb(r), panes...))
	}
}

// Session names and labels the lookup columns use.
var (
	mxRecorded = func(r store.LiveSpawnIdentity) string { return r.TmuxSessionName }
	mxRenamed  = func(r store.LiveSpawnIdentity) string { return "renamed-" + r.TmuxSessionName }
	mxOwn      = func(r store.LiveSpawnIdentity) tmux.Label {
		return tmuxfix.Valid(tmuxfix.Token, r.ClaudeInstanceID, tmuxfix.StoreID)
	}
	mxOld = func(r store.LiveSpawnIdentity) tmux.Label {
		return tmuxfix.Valid(tmuxfix.OtherToken, r.ClaudeInstanceID, tmuxfix.StoreID)
	}
	mxNoLabel  = func(store.LiveSpawnIdentity) tmux.Label { return tmux.Label{} }
	mxOtherRow = func(store.LiveSpawnIdentity) tmux.Label {
		return tmuxfix.Valid(tmuxfix.Token, "other-row", tmuxfix.StoreID)
	}
	mxNameHeld  = mxMark("tmux_name_held", "gone", true)
	mxNameFree  = mxMark("tmux_absent", "gone", false)
	mxOursPaned = mxNote("probe_eacces")
)

// mxLookups are the matrix's lookup columns.
var mxLookups = []mxLookup{
	{name: "ours under the recorded name", ours: true, rec: mxHeldBy(mxRecorded, mxOwn), unknown: mxOursPaned},
	{name: "ours renamed", ours: true, rec: mxHeldBy(mxRenamed, mxOwn), unknown: mxOursPaned},
	{name: "ours after a lost reply, adopted", ours: true, lostReply: true, rec: mxHeldBy(mxRecorded, mxOwn)},
	{name: "leftover holding the name", rec: mxHeldBy(mxRecorded, mxOld), unknown: mxMark("tmux_name_held", "leftover", true)},
	{name: "gone, name held by an unlabelled session", rec: mxHeldBy(mxRecorded, mxNoLabel), unknown: mxNameHeld},
	{name: "gone, name held by another row's session", rec: mxHeldBy(mxRecorded, mxOtherRow), unknown: mxNameHeld},
	{name: "gone, name free", rec: mxHeldBy(mxRenamed, mxNoLabel), unknown: mxNameFree},
	{name: "cant tell, different server", unknown: mxNote("tmux_server_changed"),
		rec: func(r store.LiveSpawnIdentity, _ []tmuxfix.SeedPane) *tmuxfix.Recorder {
			return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, mxOtherServer).
				SeedSessions(apitest.TestSocket, mxSession(r.TmuxSessionName, mxOwn(r)))
		}},
	{name: "cant tell, provenance_conflict", unknown: mxNote("provenance_conflict"),
		rec: func(r store.LiveSpawnIdentity, _ []tmuxfix.SeedPane) *tmuxfix.Recorder {
			return mxRec(mxSession(r.TmuxSessionName, mxOwn(r)), mxSession(mxRenamed(r), mxOwn(r)))
		}},
	{name: "cant tell, unreadable", unknown: mxNote("probe_eacces"), noneNote: "process_not_seen_tmux_unchecked",
		rec: func(store.LiveSpawnIdentity, []tmuxfix.SeedPane) *tmuxfix.Recorder { return fmCantTell() }},
}

// mxListing is what the adoption listing of an Ours row with no pane shows: its own session's panes (the adopted
// pane's pid answering proc), or a listing that fails unreadable; want is the outcome for a row recording its server.
type mxListing struct {
	name   string
	panes  []tmuxfix.SeedPane
	proc   procfix.Process
	failed bool
	want   mxWant
}

// mxTokenPane is a pane carrying the launch token.
var mxTokenPane = tmuxfix.SeedPane{PID: mxAdoptPID, AdPane: tmuxfix.Token}

// mxListings: build-lead decision 1 (Epic 14): one token pane is adopted and its process judged (alive: the row
// stays pending, noted unreported, b.kdf); no token pane counts as Gone (tmux_absent, lookup_outcome ours); two
// token panes or no listing leave it unverified.
var mxListings = []mxListing{
	{name: "token pane alive", panes: []tmuxfix.SeedPane{mxTokenPane}, proc: procfix.Alive(fmStart),
		want: mxWant{ops: []string{"adopt", "unreported"}, reason: "unreported"}},
	{name: "token pane dead", panes: []tmuxfix.SeedPane{mxTokenPane}, proc: procfix.Gone(),
		want: mxWant{ops: []string{"adopt", "mark"}, reason: "proc_absent"}},
	{name: "token pane unreadable", panes: []tmuxfix.SeedPane{mxTokenPane}, proc: procfix.Unreadable(),
		want: mxWant{ops: []string{"adopt", "note"}, note: "probe_eacces", reason: "probe_eacces"}},
	{name: "no token pane", panes: []tmuxfix.SeedPane{{PID: mxAdoptPID}}, proc: procfix.Alive(fmStart),
		want: mxWant{ops: []string{"mark"}, reason: "tmux_absent", outcome: "ours"}},
	{name: "two token panes", panes: []tmuxfix.SeedPane{mxTokenPane, {Index: 1, PID: mxAdoptPID + 1, AdPane: tmuxfix.Token}},
		proc: procfix.Alive(fmStart),
		want: mxWant{ops: []string{"note"}, note: "process_not_seen_session_present", reason: "process_not_seen_session_present"}},
	{name: "listing unreadable", panes: []tmuxfix.SeedPane{mxTokenPane}, proc: procfix.Alive(fmStart), failed: true,
		want: mxWant{ops: []string{"note"}, note: "process_not_seen_session_present", reason: "process_not_seen_session_present"}},
}

// mxCell is one sweep of a single row: the process checker, the Recorder and the expected outcome.
type mxCell struct {
	row  store.LiveSpawnIdentity
	pc   *procfix.Checker
	rec  *tmuxfix.Recorder
	want mxWant
}

// mxPlainCell is the cell of row kind k, pane identity p and lookup lk, for every cell but Ours with no pane.
func mxPlainCell(id string, k mxKind, p mxPane, lk mxLookup) mxCell {
	r := mxRow(id, k, !p.none, !lk.lostReply)
	c := mxCell{row: r, pc: mxChecker(mxPanePID, p.proc), rec: lk.rec(r, nil), want: lk.unknown}
	switch {
	case p.fixed != nil:
		c.want = *p.fixed
	case p.none && lk.noneNote != "":
		c.want = mxNote(lk.noneNote)
	}
	return c
}

// mxOursNoPaneCell is the cell of an Ours lookup lk for a row of kind k recording no pane, decided by listing v; a
// lost reply's row also adopts its server identity, so an adopt write comes first when listed.
func mxOursNoPaneCell(id string, k mxKind, lk mxLookup, v mxListing) mxCell {
	r := mxRow(id, k, false, !lk.lostReply)
	pc := mxChecker(mxAdoptPID, v.proc)
	pc.Set(mxAdoptPID+1, v.proc)
	rec := lk.rec(r, v.panes)
	if v.failed {
		rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized}, tmux.CallListPanes)
	}
	w := v.want
	w.lookups, w.listings = 1, 1
	if lk.lostReply && !v.failed && w.ops[0] != "adopt" {
		w.ops = append([]string{"adopt"}, w.ops...)
	}
	return mxCell{row: r, pc: pc, rec: rec, want: w}
}

// runMxCell sweeps c's row alone, a live process carrying its id beside it, and asserts its lists, writes, tick,
// held-name records, reader (never the process carrying the id, never an environment) and tmux calls.
func runMxCell(t *testing.T, c mxCell) {
	t.Helper()
	id, w := c.row.ClaudeInstanceID, c.want
	c.pc.Set(mxLeakPID, procfix.Alive(fmStart).WithEnv(map[string]string{probe.EnvKey: id}))
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{c.row}}
	before := len(readAPITrailLines(t))

	res := mustSweep(t, st, c.pc, fmSweep{tmux: c.rec})
	var ids, unver []string
	switch {
	case slices.Contains(w.ops, "mark"):
		ids = []string{id}
	case slices.Contains(w.ops, "note"):
		unver = []string{id}
	}
	assertLists(t, res, ids, unver)
	if got := st.ops(id); !equalStrings(got, w.ops) {
		t.Errorf("writes = %v; want %v", got, w.ops)
	}
	for _, call := range st.calls {
		if call.op == "note" && call.note != w.note {
			t.Errorf("note written = %q; want %q", call.note, w.note)
		}
	}
	assertMxTick(t, ticksSince(t, before, id), w, c.row.TmuxSessionName)
	assertMxHeld(t, before, id, w.held)
	if got := c.pc.StartTimeCalls(); w.reads != nil && !slices.Equal(got, w.reads) {
		t.Errorf("StartTime calls = %v; want %v", got, w.reads)
	}
	if slices.Contains(c.pc.StartTimeCalls(), mxLeakPID) || c.pc.EnvReads() != 0 {
		t.Errorf("reader read the leaked process %d or an environment (%d reads)", mxLeakPID, c.pc.EnvReads())
	}
	assertMxCalls(t, c.rec, w.lookups, w.listings)
}

// assertMxTick fails unless ticks is exactly w's one tick (none when w.reason is empty) with its fields.
func assertMxTick(t *testing.T, ticks []map[string]any, w mxWant, name string) {
	t.Helper()
	if w.reason == "" {
		if len(ticks) != 0 {
			t.Errorf("ticks = %v; want none", ticks)
		}
		return
	}
	if len(ticks) != 1 {
		t.Fatalf("ticks = %v; want one with reason %s", ticks, w.reason)
	}
	tk := ticks[0]
	if tk["reconciliation_reason"] != w.reason {
		t.Errorf("tick reason = %v; want %s", tk["reconciliation_reason"], w.reason)
	}
	if lo, ok := tk["lookup_outcome"]; ok != (w.outcome != "") || (ok && lo != w.outcome) {
		t.Errorf("tick lookup_outcome = %v (present %v); want %q", lo, ok, w.outcome)
	}
	if sn, ok := tk["tmux_session_name"]; ok != w.held || (ok && sn != name) {
		t.Errorf("tick tmux_session_name = %v (present %v); want %q only when held", sn, ok, name)
	}
}

// assertMxHeld fails unless id has exactly one find-missing ad.launch.name_held since before when held, else none.
func assertMxHeld(t *testing.T, before int, id string, held bool) {
	t.Helper()
	recs := ptRecords(t, before, "ad.launch.name_held", id)
	want := 0
	if held {
		want = 1
	}
	if len(recs) != want {
		t.Fatalf("ad.launch.name_held records = %v; want %d", recs, want)
	}
	for _, l := range recs {
		if l["source"] != "ad_find_missing" || l["launch"] != nil || l["outcome"] != nil || l["row_result"] != "marked_missing" {
			t.Errorf("name_held = %v; want source ad_find_missing, launch and outcome null, row_result marked_missing", l)
		}
	}
}

// assertMxCalls fails unless rec saw exactly lookups lookups and listings pane listings and nothing else: the
// holding session is never killed or sent to.
func assertMxCalls(t *testing.T, rec *tmuxfix.Recorder, lookups, listings int) {
	t.Helper()
	l, p := len(rec.SocketCallsOf(tmux.CallLookup)), len(rec.SocketCallsOf(tmux.CallListPanes))
	if l != lookups || p != listings || len(rec.SocketCalls()) != l+p || len(rec.Calls()) != 0 {
		t.Errorf("tmux calls = %+v %+v; want %d lookup(s), %d pane listing(s) and nothing else",
			rec.SocketCalls(), rec.Calls(), lookups, listings)
	}
}

// TestFindMissingPendingMatrix: each pending row kind x pane identity x lookup cell past the grace period gets
// SR-11.3's outcome (a live agent's row noted unreported, in neither list, b.kdf), and the holding session is never
// touched. Each cell sweeps its own fake store and reads only its own id's trail records, so the cells run in
// parallel.
func TestFindMissingPendingMatrix(t *testing.T) {
	t.Parallel()
	for ki, k := range mxKinds {
		for pi, p := range mxPanes {
			for li, lk := range mxLookups {
				if lk.lostReply && !p.none {
					continue // a lost create reply records no pane, so it has no pane identity to be alive, dead or unreadable
				}
				name, id := k.name+"/"+p.name+"/"+lk.name, fmt.Sprintf("mx-%d-%d-%d", ki, pi, li)
				if !lk.ours || !p.none {
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						runMxCell(t, mxPlainCell(id, k, p, lk))
					})
					continue
				}
				for vi, v := range mxListings {
					t.Run(name+"/"+v.name, func(t *testing.T) {
						t.Parallel()
						runMxCell(t, mxOursNoPaneCell(fmt.Sprintf("%s-%d", id, vi), k, lk, v))
					})
				}
			}
		}
	}
}

// TestFindMissingPendingMatrixExtraRows: a pending row with no launch start, one with no token (never Ours), and a
// working row whose name an unlabelled session holds (AC-FM-02), by the same rules (SR-22.8; AC-FM-14).
func TestFindMissingPendingMatrixExtraRows(t *testing.T) {
	t.Parallel()
	fresh, unreadable := mxKinds[0], procfix.Unreadable()
	cases := []struct {
		name string
		row  store.LiveSpawnIdentity
		rec  func(store.LiveSpawnIdentity, []tmuxfix.SeedPane) *tmuxfix.Recorder
		want mxWant
	}{
		{"pending no launch start/pane unreadable/ours", mxRow("mx-x1", fresh, true, true, withLaunch(store.StatePending, 0)),
			mxHeldBy(mxRecorded, mxOwn), mxNote("probe_eacces")},
		{"pending no token/pane unreadable/session with its id and the launch token",
			mxRow("mx-x3", fresh, true, true, func(r *store.LiveSpawnIdentity) { r.Identity.Token = "" }),
			mxHeldBy(mxRecorded, mxOwn), mxMark("tmux_name_held", "leftover", true)},
		{"working/pane unreadable/name held by an unlabelled session", mxRow("mx-x5", fresh, true, true, withLaunch(store.StateWorking, 0)),
			mxHeldBy(mxRecorded, mxNoLabel), mxNameHeld},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // one id per case
			runMxCell(t, mxCell{row: tc.row, pc: mxChecker(mxPanePID, unreadable), rec: tc.rec(tc.row, []tmuxfix.SeedPane{mxTokenPane}), want: tc.want})
		})
	}
}
