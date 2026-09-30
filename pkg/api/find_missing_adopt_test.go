package api_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's adoption on the tmux path (SR-3.6, SR-11.3, SR-11.6; LFR H2) and the same-life guard around it:
// the fake store (find_missing_test.go) checks every verdict write is guarded on the adoption's snapshot.

// fmAdoptPane and fmAdoptPID are the pane carrying a lost-reply row's launch token.
const (
	fmAdoptPane = "%7"
	fmAdoptPID  = 4700
)

// tokenPane is a pane (window w) carrying r's launch token in @ad_pane.
func tokenPane(r store.LiveSpawnIdentity, id string, pid, w int) tmuxfix.SeedPane {
	return tmuxfix.SeedPane{Window: w, ID: id, PID: pid, AdPane: r.Identity.Token}
}

// ownSession is r's own labelled session under its recorded name, with panes (none: one pane with no @ad_pane).
func ownSession(r store.LiveSpawnIdentity, panes ...tmuxfix.SeedPane) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{Name: r.TmuxSessionName, Label: tmuxfix.Valid(r.Identity.Token, r.ClaudeInstanceID, tmuxfix.StoreID),
		Panes: panes}
}

// lostReplyRec returns a Recorder whose server (fmServer on r's socket) holds r's own session with panes.
func lostReplyRec(r store.LiveSpawnIdentity, panes ...tmuxfix.SeedPane) *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().StartServer(r.Identity.Socket, fmServer).SeedSessions(r.Identity.Socket, ownSession(r, panes...))
}

// adoptChecker reads fmServer's process alive with its start time and fmAdoptPID as pane.
func adoptChecker(pane procfix.Process) *procfix.Checker {
	pc := procfix.New()
	pc.Set(fmServer.PID, procfix.Alive(fmServer.ProcStart))
	pc.Set(fmAdoptPID, pane)
	return pc
}

// serverAdopted is r's recorded identity with fmServer's identity filled in.
func serverAdopted(r store.LiveSpawnIdentity) store.LaunchIdentity {
	li := r.Identity
	li.ServerPID, li.ServerStart, li.ServerStarttime = fmServer.PID, fmServer.Start, fmServer.ProcStart
	return li
}

// paneAdopted is serverAdopted plus the token pane fmAdoptPane, its start time read as start.
func paneAdopted(r store.LiveSpawnIdentity, start string) store.LaunchIdentity {
	li := serverAdopted(r)
	li.PaneID, li.PanePID, li.PaneStarttime = fmAdoptPane, fmAdoptPID, start
	return li
}

// withToken records tok as the row's launch token.
func withToken(tok string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.Identity.Token = tok }
}

// assertAdopts fails unless id's adoption writes carried exactly want, in order (none: no adoption write).
func assertAdopts(t *testing.T, st *fakeFindMissingStore, id string, want ...store.LaunchIdentity) {
	t.Helper()
	var got []store.LaunchIdentity
	for _, c := range st.calls {
		if c.op == "adopt" && c.id == id {
			got = append(got, c.identity)
		}
	}
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Errorf("adoptions of %s = %+v; want %+v", id, got, want)
	}
}

// disagreeOf returns id's ad.provenance.disagree records with reason written after trail line before.
func disagreeOf(t *testing.T, before int, id, reason string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range trailSince(t, before, "ad.provenance.disagree") {
		if r["claude_instance_id"] == id && r["reason"] == reason {
			out = append(out, r)
		}
	}
	return out
}

// assertOneDisagree fails unless id has exactly one reason record since before, with action (count 0: none).
func assertOneDisagree(t *testing.T, before int, id, reason, action string, count int) {
	t.Helper()
	recs := disagreeOf(t, before, id, reason)
	if len(recs) != count || (count == 1 && recs[0]["action"] != action) {
		t.Errorf("%s records on %s = %v; want %d with action %q", reason, id, recs, count, action)
	}
}

// assertCallsOn fails unless rec made n calls of kind call on socket.
func assertCallsOn(t *testing.T, rec *tmuxfix.Recorder, call tmux.Call, socket string, n int) {
	t.Helper()
	got := 0
	for _, c := range rec.SocketCallsOf(call) {
		if c.Socket == socket {
			got++
		}
	}
	if got != n {
		t.Errorf("%v calls on %s = %d; want %d", call, socket, got, n)
	}
}

// assertWrote fails unless id's writes are ops and its note (when noted) or first tick reason (when marked) is v.
func assertWrote(t *testing.T, st *fakeFindMissingStore, before int, id string, ops []string, v string) {
	t.Helper()
	if got := st.ops(id); !equalStrings(got, ops) {
		t.Errorf("writes on %s = %v; want %v", id, got, ops)
	}
	for _, c := range st.calls {
		if c.id == id && c.op == "note" && c.note != v {
			t.Errorf("note on %s = %q; want %q", id, c.note, v)
		}
		if c.id == id && c.op == "mark" {
			assertMarkReason(t, before, id, v)
		}
	}
}

// TestFindMissingAdoptLostReplyJudgesAdoptedPane: a lost reply's Ours row adopts the server and its token pane
// (one listing), records adopted once, and is judged by that pane's process, guarded on the adoption's snapshot.
func TestFindMissingAdoptLostReplyJudgesAdoptedPane(t *testing.T) {
	cases := []struct {
		name, note string // note: the note the row carries
		proc       procfix.Process
		start      string // the adopted pane start time
		ops        []string
		verdict    string
		ids, unver []string
		action     string
	}{
		{"alive clears", "process_not_seen_session_present", procfix.Alive(fmStart), fmStart,
			[]string{"adopt", "clear"}, "", nil, nil, "left_live"},
		{"gone marks", "", procfix.Gone(), "", []string{"adopt", "mark", "close"}, "proc_absent", []string{"r"}, nil,
			"marked_missing"},
		{"unreadable notes", "", procfix.Unreadable(), "", []string{"adopt", "note"}, "probe_eacces", nil,
			[]string{"r"}, "left_unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := liveRow("r", withNote(tc.note))
			rec := lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
			before := trailLen(t)

			res := mustSweep(t, st, adoptChecker(tc.proc), fmSweep{tmux: rec})
			assertLists(t, res, tc.ids, tc.unver)
			assertWrote(t, st, before, "r", tc.ops, tc.verdict)
			assertAdopts(t, st, "r", paneAdopted(r, tc.start))
			assertOneDisagree(t, before, "r", "adopted", tc.action, 1)
			assertCallsOn(t, rec, tmux.CallLookup, apitest.TestSocket, 1)
			assertCallsOn(t, rec, tmux.CallListPanes, apitest.TestSocket, 1)
		})
	}
}

// TestFindMissingAdoptListingDecides: with no single token pane only the server identity is adopted (none when
// recorded); no token pane counts as Gone (tmux_absent, lookup_outcome ours), two leave the row unverified.
func TestFindMissingAdoptListingDecides(t *testing.T) {
	two := func(r store.LiveSpawnIdentity) []tmuxfix.SeedPane {
		return []tmuxfix.SeedPane{tokenPane(r, fmAdoptPane, fmAdoptPID, 0), tokenPane(r, "%8", fmAdoptPID+1, 1)}
	}
	cases := []struct {
		name    string
		opts    []fmRowOpt
		panes   func(r store.LiveSpawnIdentity) []tmuxfix.SeedPane
		adopt   func(r store.LiveSpawnIdentity) []store.LaunchIdentity
		ops     []string
		verdict string
		ids     []string
	}{
		{"no token pane", nil, func(store.LiveSpawnIdentity) []tmuxfix.SeedPane { return nil },
			func(r store.LiveSpawnIdentity) []store.LaunchIdentity {
				return []store.LaunchIdentity{serverAdopted(r)}
			},
			[]string{"adopt", "mark", "close"}, "tmux_absent", []string{"r"}},
		{"no token pane server recorded", []fmRowOpt{withServer()}, func(store.LiveSpawnIdentity) []tmuxfix.SeedPane { return nil },
			func(store.LiveSpawnIdentity) []store.LaunchIdentity { return nil },
			[]string{"mark", "close"}, "tmux_absent", []string{"r"}},
		{"two token panes", nil, two,
			func(r store.LiveSpawnIdentity) []store.LaunchIdentity {
				return []store.LaunchIdentity{serverAdopted(r)}
			},
			[]string{"adopt", "note"}, "process_not_seen_session_present", nil},
		{"two token panes server recorded", []fmRowOpt{withServer()}, two,
			func(store.LiveSpawnIdentity) []store.LaunchIdentity { return nil },
			[]string{"note"}, "process_not_seen_session_present", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := liveRow("r", tc.opts...)
			rec := lostReplyRec(r, tc.panes(r)...)
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
			before := trailLen(t)

			res := mustSweep(t, st, adoptChecker(procfix.Alive(fmStart)), fmSweep{tmux: rec})
			unver := []string{"r"}
			if tc.ids != nil {
				unver = nil
			}
			assertLists(t, res, tc.ids, unver)
			assertWrote(t, st, before, "r", tc.ops, tc.verdict)
			want := tc.adopt(r)
			assertAdopts(t, st, "r", want...)
			assertOneDisagree(t, before, "r", "adopted", findMissingActionOf(tc.ids), len(want))
			if ticks := ticksSince(t, before, "r"); tc.ids != nil && (len(ticks) == 0 || ticks[0]["lookup_outcome"] != "ours") {
				t.Errorf("ticks = %v; want the mark's with lookup_outcome ours", ticks)
			}
			if held := trailOf(trailSince(t, before, "ad.launch.name_held"), "r"); len(held) != 0 {
				t.Errorf("ad.launch.name_held = %v; want none (the row's own session holds its name)", held)
			}
			assertCallsOn(t, rec, tmux.CallListPanes, apitest.TestSocket, 1)
		})
	}
}

// findMissingActionOf is the disagree action of a row marked (ids set) or left unverified.
func findMissingActionOf(ids []string) string {
	if ids != nil {
		return "marked_missing"
	}
	return "left_unverified"
}

// TestFindMissingAdoptSharedTokenPaneIsOne: a token pane a grouped session also lists counts as one pane, adopted.
func TestFindMissingAdoptSharedTokenPaneIsOne(t *testing.T) {
	r := liveRow("r")
	rec := lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0)).
		SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: "viewer", Panes: []tmuxfix.SeedPane{{ID: fmAdoptPane, Shared: true}}})
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
	before := trailLen(t)

	res := mustSweep(t, st, adoptChecker(procfix.Unreadable()), fmSweep{tmux: rec})
	assertLists(t, res, nil, []string{"r"})
	assertWrote(t, st, before, "r", []string{"adopt", "note"}, "probe_eacces")
	assertAdopts(t, st, "r", paneAdopted(r, ""))
}

// TestFindMissingAdoptUnlistedStopsSocket: a failed or skipped pane listing adopts nothing and records no adopted;
// the row is noted process_not_seen_session_present on the read snapshot and later rows of the socket not called.
func TestFindMissingAdoptUnlistedStopsSocket(t *testing.T) {
	q := config.Tmux{}.EffectiveQueryTimeout()
	cases := []struct {
		name   string
		script bool          // the listing fails unreadable
		budget time.Duration // 0: the default
	}{
		{"listing fails", true, 0},
		{"listing spends the budget", false, q + q/2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := liveRow("r")
			rec := lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0))
			if tc.script {
				rec.Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailUnrecognized}, tmux.CallListPanes)
			}
			pc := adoptChecker(procfix.Alive(fmStart))
			pc.Set(900, procfix.Unreadable())
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r,
				liveRow("s", withServer(), withPane(900, fmStart)), liveRow("u", withToken(tmuxfix.OtherToken))}}
			before := trailLen(t)

			res := mustSweep(t, st, pc, fmSweep{tmux: rec, budget: tc.budget})
			assertLists(t, res, nil, []string{"r", "s", "u"})
			assertWrote(t, st, before, "r", []string{"note"}, "process_not_seen_session_present")
			assertWrote(t, st, before, "s", []string{"note"}, "probe_eacces")
			assertWrote(t, st, before, "u", []string{"note"}, "process_not_seen_tmux_unchecked")
			assertOneDisagree(t, before, "r", "adopted", "", 0)
			if len(rec.SocketCalls()) != 2 {
				t.Errorf("tmux calls = %+v; want the lookup and the listing only", rec.SocketCalls())
			}
		})
	}
}

// TestFindMissingAdoptOneListingPerSocket: however many lost-reply rows adopt on a socket, the run takes one
// lookup and one pane listing there, and each row records its own adoption and adopted once.
func TestFindMissingAdoptOneListingPerSocket(t *testing.T) {
	sockB := apitest.TestSocket + "-b"
	a1, a2 := liveRow("a1"), liveRow("a2", withToken(tmuxfix.OtherToken))
	b1 := liveRow("b1", func(r *store.LiveSpawnIdentity) { r.Identity.Socket = sockB })
	rec := lostReplyRec(a1, tokenPane(a1, fmAdoptPane, fmAdoptPID, 0)).
		SeedSessions(apitest.TestSocket, ownSession(a2, tokenPane(a2, "%8", fmAdoptPID+1, 0))).
		StartServer(sockB, tmuxfix.Server{PID: fmServer.PID + 1, Start: fmServer.Start, ProcStart: fmServer.ProcStart}).
		SeedSessions(sockB, ownSession(b1, tokenPane(b1, fmAdoptPane, fmAdoptPID+2, 0)))
	pc := procfix.New()
	for i := range 3 {
		pc.Set(fmAdoptPID+i, procfix.Alive(fmStart))
	}
	rows := []store.LiveSpawnIdentity{a1, a2, b1}
	st := &fakeFindMissingStore{rows: rows}
	before := trailLen(t)

	res := mustSweep(t, st, pc, fmSweep{tmux: rec})
	assertLists(t, res, nil, nil)
	for _, r := range rows {
		assertWrote(t, st, before, r.ClaudeInstanceID, []string{"adopt"}, "")
		assertOneDisagree(t, before, r.ClaudeInstanceID, "adopted", "left_live", 1)
	}
	for _, sock := range []string{apitest.TestSocket, sockB} {
		assertCallsOn(t, rec, tmux.CallLookup, sock, 1)
		assertCallsOn(t, rec, tmux.CallListPanes, sock, 1)
	}
}

// TestFindMissingAdoptOnlyWhenDue: a full identity reading unreadable adopts nothing; a recorded pane missing only
// its server adopts the server with no listing; a lost reply whose lookup is Gone adopts nothing.
func TestFindMissingAdoptOnlyWhenDue(t *testing.T) {
	cases := []struct {
		name    string
		opts    []fmRowOpt
		ours    bool
		adopted bool
		ops     []string
		verdict string
	}{
		{"full identity unreadable", []fmRowOpt{withServer(), withPane(900, fmStart)}, true, false,
			[]string{"note"}, "probe_eacces"},
		{"pane recorded no server", []fmRowOpt{withPane(900, fmStart)}, true, true,
			[]string{"adopt", "note"}, "probe_eacces"},
		{"lost reply gone", nil, false, false, []string{"mark", "close"}, "tmux_absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := liveRow("r", tc.opts...)
			rec := tmuxfix.NewRecorder()
			if tc.ours {
				rec = fmOurs(r)
			}
			pc := adoptChecker(procfix.Alive(fmStart))
			pc.Set(900, procfix.Unreadable())
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
			before := trailLen(t)

			mustSweep(t, st, pc, fmSweep{tmux: rec})
			assertWrote(t, st, before, "r", tc.ops, tc.verdict)
			if tc.adopted {
				assertAdopts(t, st, "r", serverAdopted(r))
			}
			assertCallsOn(t, rec, tmux.CallListPanes, apitest.TestSocket, 0)
		})
	}
}

// fmAdoptOutcomes are a guarded write's refusals and failure, scripted on the fake store.
var fmAdoptOutcomes = []struct {
	name   string
	answer fmAnswer
	action string
}{
	{"changed", fmAnswer{res: store.CondChanged}, "left_changed"},
	{"absent", fmAnswer{res: store.CondAbsent}, "left_changed"},
	{"store error", fmAnswer{err: errors.New("disk I/O error")}, "store_error"},
}

// TestFindMissingAdoptRefused: an adoption that finds the row changed or absent, or fails (logged), ends the row:
// no verdict write, tick or adopted record, neither list; the sweep still marks a later dead row.
func TestFindMissingAdoptRefused(t *testing.T) {
	for _, out := range fmAdoptOutcomes {
		t.Run(out.name, func(t *testing.T) {
			r := liveRow("r")
			rec := lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0))
			lg := &recordingLogger{}
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r, liveRow("z", withSessionStart(20, fmStart))},
				answers: map[string]map[string]fmAnswer{"adopt": {"r": out.answer}}}
			before := trailLen(t)

			res := mustSweep(t, st, adoptChecker(procfix.Gone()), fmSweep{tmux: rec, lg: lg})
			assertLists(t, res, []string{"z"}, nil)
			assertWrote(t, st, before, "r", []string{"adopt"}, "")
			assertAdopts(t, st, "r", paneAdopted(r, ""))
			if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
				t.Errorf("ticks on r = %v; want none", ticks)
			}
			if recs := trailOf(trailSince(t, before, "ad.provenance.disagree"), "r"); len(recs) != 0 {
				t.Errorf("disagree records on r = %v; want none", recs)
			}
			wantLog := 0
			if out.answer.err != nil {
				wantLog = 1
			}
			if len(lg.lines) != wantLog || (wantLog == 1 && !strings.Contains(lg.lines[0], "AdoptIdentityIfSameLife(r)")) {
				t.Errorf("log = %v; want %d line(s) naming r", lg.lines, wantLog)
			}
		})
	}
}

// TestFindMissingVerdictAfterAdoptionRefused: an applied adoption's mark, note or clear (and a plain Gone mark),
// guarded on its snapshot, refused or failing: neither list, no tick, no close; the disagree action says so.
func TestFindMissingVerdictAfterAdoptionRefused(t *testing.T) {
	restarted := tmuxfix.Server{PID: fmServer.PID + 1, Start: fmServer.Start + 1, ProcStart: fmStart}
	verdicts := []struct {
		name, op, reason string
		row              store.LiveSpawnIdentity
		rec              func(r store.LiveSpawnIdentity) *tmuxfix.Recorder
		pane             procfix.Process
	}{
		{"adopted gone mark", "mark", "adopted", liveRow("r"),
			func(r store.LiveSpawnIdentity) *tmuxfix.Recorder { return lostReplyRec(r) }, procfix.Gone()},
		{"adopted pane note", "note", "adopted", liveRow("r"), func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
			return lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0))
		}, procfix.Unreadable()},
		{"adopted pane clear", "clear", "adopted", liveRow("r", withNote("probe_eacces")),
			func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
				return lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0))
			}, procfix.Alive(fmStart)},
		{"lookup gone mark", "mark", "server_restarted", liveRow("r", withServer(), withPane(fmAdoptPID, fmStart)),
			func(store.LiveSpawnIdentity) *tmuxfix.Recorder {
				return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, restarted).
					SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: "other"})
			}, procfix.Unreadable()},
	}
	for _, v := range verdicts {
		for _, out := range fmAdoptOutcomes {
			t.Run(v.name+" "+out.name, func(t *testing.T) {
				pc := adoptChecker(v.pane)
				pc.Set(fmServer.PID, procfix.Gone()) // the recorded server is gone: restarted, not different
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{v.row},
					answers: map[string]map[string]fmAnswer{v.op: {"r": out.answer}}}
				before := trailLen(t)

				res := mustSweep(t, st, pc, fmSweep{tmux: v.rec(v.row)})
				assertLists(t, res, nil, nil)
				if ops := st.ops("r"); len(ops) == 0 || ops[len(ops)-1] != v.op {
					t.Errorf("writes = %v; want the %s last, with no close", ops, v.op)
				}
				if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
					t.Errorf("ticks = %v; want none", ticks)
				}
				assertOneDisagree(t, before, "r", v.reason, out.action, 1)
			})
		}
	}
}
