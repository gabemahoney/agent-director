package api_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

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

// lostReplyRec returns a Recorder whose server (fmServer on r's socket) holds r's own labelled session under its
// recorded name with panes (none: one pane with no @ad_pane).
func lostReplyRec(r store.LiveSpawnIdentity, panes ...tmuxfix.SeedPane) *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().StartServer(r.Identity.Socket, fmServer).SeedSessions(r.Identity.Socket, tmuxfix.SeedSession{
		Name: r.TmuxSessionName, Label: tmuxfix.Valid(r.Identity.Token, r.ClaudeInstanceID, tmuxfix.StoreID), Panes: panes})
}

// adoptChecker reads fmServer's process alive with its start time and fmAdoptPID as pane.
func adoptChecker(pane procfix.Process) *procfix.Checker {
	pc := procfix.New()
	pc.Set(fmServer.PID, procfix.Alive(fmServer.ProcStart))
	pc.Set(fmAdoptPID, pane)
	return pc
}

// paneAdopted is r's recorded identity with fmServer's identity and the token pane fmAdoptPane filled in, its
// start time read as start.
func paneAdopted(r store.LiveSpawnIdentity, start string) store.LaunchIdentity {
	li := r.Identity
	li.ServerPID, li.ServerStart, li.ServerStarttime = fmServer.PID, fmServer.Start, fmServer.ProcStart
	li.PaneID, li.PanePID, li.PaneStarttime = fmAdoptPane, fmAdoptPID, start
	return li
}

// assertOneDisagree fails unless id has exactly count reason records since before (one: with action).
func assertOneDisagree(t *testing.T, before int, id, reason, action string, count int) {
	t.Helper()
	var recs []map[string]any
	for _, r := range ptRecords(t, before, "ad.provenance.disagree", id) {
		if r["reason"] == reason {
			recs = append(recs, r)
		}
	}
	if len(recs) != count || (count == 1 && recs[0]["action"] != action) {
		t.Errorf("%s records on %s = %v; want %d with action %q", reason, id, recs, count, action)
	}
}

// TestFindMissingAdoptLostReplyJudgesAdoptedPane: a lost reply's Ours row adopts the server and its token pane
// (one listing), records adopted once, and is judged by that pane's process, guarded on the adoption's snapshot.
func TestFindMissingAdoptLostReplyJudgesAdoptedPane(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
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
			if got := st.ops("r"); !equalStrings(got, tc.ops) {
				t.Errorf("writes = %v; want %v", got, tc.ops)
			}
			for _, c := range st.calls {
				switch {
				case c.op == "adopt" && !reflect.DeepEqual(c.identity, paneAdopted(r, tc.start)):
					t.Errorf("adopted %+v; want %+v", c.identity, paneAdopted(r, tc.start))
				case c.op == "note" && c.note != tc.verdict:
					t.Errorf("note = %q; want %q", c.note, tc.verdict)
				case c.op == "mark":
					assertMarkReason(t, before, "r", tc.verdict)
				}
			}
			assertOneDisagree(t, before, "r", "adopted", tc.action, 1)
			if l, p := len(rec.SocketCallsOf(tmux.CallLookup)), len(rec.SocketCallsOf(tmux.CallListPanes)); l != 1 || p != 1 {
				t.Errorf("lookups %d, pane listings %d; want one each", l, p)
			}
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

// TestFindMissingVerdictAfterAdoptionRefused: an adoption, an applied adoption's mark, note or clear, and a plain
// Gone mark, each guarded and refused or failing: neither list, no tick, no close, no write after it; a failure
// alone is logged, naming the row; the disagree record's action says so (an adoption that did not apply records no
// adopted); the sweep still marks a later dead row.
func TestFindMissingVerdictAfterAdoptionRefused(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	restarted := tmuxfix.Server{PID: fmServer.PID + 1, Start: fmServer.Start + 1, ProcStart: fmStart}
	withTokenPane := func(r store.LiveSpawnIdentity) *tmuxfix.Recorder {
		return lostReplyRec(r, tokenPane(r, fmAdoptPane, fmAdoptPID, 0))
	}
	verdicts := []struct {
		name, op, reason string // reason: the disagree record ("": none)
		row              store.LiveSpawnIdentity
		rec              func(r store.LiveSpawnIdentity) *tmuxfix.Recorder
		pane             procfix.Process
	}{
		{"adoption", "adopt", "", liveRow("r"), withTokenPane, procfix.Gone()},
		{"adopted gone mark", "mark", "adopted", liveRow("r"),
			func(r store.LiveSpawnIdentity) *tmuxfix.Recorder { return lostReplyRec(r) }, procfix.Gone()},
		{"adopted pane note", "note", "adopted", liveRow("r"), withTokenPane, procfix.Unreadable()},
		{"adopted pane clear", "clear", "adopted", liveRow("r", withNote("probe_eacces")), withTokenPane, procfix.Alive(fmStart)},
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
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{v.row, liveRow("z", withSessionStart(20, fmStart))},
					answers: map[string]map[string]fmAnswer{v.op: {"r": out.answer}}}

				res, lg, before := sweepFrom(t, st, pc, fmSweep{tmux: v.rec(v.row)})
				assertLists(t, res, []string{"z"}, nil)
				if ops := st.ops("r"); len(ops) == 0 || ops[len(ops)-1] != v.op {
					t.Errorf("writes = %v; want the %s last, with no close", ops, v.op)
				}
				if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
					t.Errorf("ticks = %v; want none", ticks)
				}
				if v.reason == "" {
					assertOneDisagree(t, before, "r", "adopted", "", 0)
				} else {
					assertOneDisagree(t, before, "r", v.reason, out.action, 1)
				}
				if (len(lg.lines) != 0) != (out.answer.err != nil) || (len(lg.lines) != 0 && !strings.Contains(lg.lines[0], "(r)")) {
					t.Errorf("log = %v; want one line naming r only on a store error", lg.lines)
				}
			})
		}
	}
}
