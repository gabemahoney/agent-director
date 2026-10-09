package api_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's per-socket tmux path (SR-3.3, SR-3.15, SR-13.3, SR-13.5; AC-FM-04, AC-FM-11, AC-CLS-02, AC-EXP-10,
// AC-LKP-19): one lookup per socket, the stop rule per socket, the row's recorded socket and the no-socket rule.

// fskOther is a second recorded socket; fskUnreadablePID is the pid whose start time fskChecker cannot read.
const (
	fskOther         = "/tmp/fsk-other/default"
	fskUnreadablePID = 9101
)

// fskQueryTimeout is the default lookup timeout the Recorder charges each call in virtual time.
var fskQueryTimeout = config.Tmux{}.EffectiveQueryTimeout()

// fskChecker returns a process checker that cannot read fskUnreadablePID and reads fmServer's process alive (every
// other pid reads gone).
func fskChecker() *procfix.Checker {
	pc := procfix.New()
	pc.Set(fskUnreadablePID, procfix.Unreadable())
	pc.Set(fmServer.PID, procfix.Alive(fmServer.ProcStart))
	return pc
}

// onSocket records socket as the row's tmux socket.
func onSocket(socket string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.Identity.Socket = socket }
}

// preRelease makes the row one from before the release: no recorded socket and no launch token.
func preRelease() fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.Identity.Socket, r.Identity.Token = "", "" }
}

// unknownEvidence records a SessionStart identity whose start time fskChecker cannot read.
func unknownEvidence() fmRowOpt { return withSessionStart(fskUnreadablePID, fmStart) }

// fskWant is a row's expected outcome: marked missing with reason mark, or noted with note.
type fskWant struct{ mark, note string }

func fskMarked(reason string) fskWant { return fskWant{mark: reason} }
func fskNoted(note string) fskWant    { return fskWant{note: note} }

// fskCall is one expected socket-taking call.
type fskCall struct {
	call   tmux.Call
	socket string
}

func lookupOn(socket string) fskCall { return fskCall{tmux.CallLookup, socket} }

// fskSweep runs one sweep of rows on rec, sharing clock (nil: a new one at fmNow), and returns the fake store, the
// result and the trail checkpoint taken before it.
func fskSweep(t *testing.T, rec *tmuxfix.Recorder, pc *procfix.Checker, clock *tmuxfix.Clock, rows ...store.LiveSpawnIdentity) (*fakeFindMissingStore, api.FindMissingResult, int) {
	t.Helper()
	if clock == nil {
		clock = tmuxfix.NewClock(fmNow)
	}
	st := &fakeFindMissingStore{rows: rows}
	before := trailLen(t)
	return st, mustSweep(t, st, pc, fmSweep{tmux: rec, clock: clock}), before
}

// fskAssertRows fails unless the sweep marked and noted exactly the rows in want, each as wanted.
func fskAssertRows(t *testing.T, st *fakeFindMissingStore, res api.FindMissingResult, before int, want map[string]fskWant) {
	t.Helper()
	var ids, unverified []string
	for id, w := range want {
		wantOps := []string{"note"}
		if w.mark != "" {
			ids, wantOps = append(ids, id), []string{"mark"}
			assertMarkReason(t, before, id, w.mark)
		} else {
			unverified = append(unverified, id)
		}
		ops := slices.DeleteFunc(st.ops(id), func(op string) bool { return op == "adopt" })
		if !equalStrings(ops, wantOps) {
			t.Errorf("writes on %s = %v; want %v", id, ops, wantOps)
		}
		for _, c := range st.calls {
			if c.id == id && c.op == "note" && c.note != w.note {
				t.Errorf("note on %s = %q; want %q", id, c.note, w.note)
			}
		}
	}
	slices.Sort(ids)
	slices.Sort(unverified)
	assertLists(t, res, ids, unverified)
}

// fskAssertCalls fails unless rec saw exactly the socket-taking calls want, in order.
func fskAssertCalls(t *testing.T, rec *tmuxfix.Recorder, want ...fskCall) {
	t.Helper()
	var got []fskCall
	for _, c := range rec.SocketCalls() {
		got = append(got, fskCall{c.Call, c.Socket})
	}
	if !slices.Equal(got, want) {
		t.Errorf("tmux calls = %+v; want %+v", got, want)
	}
}

// fskAssertDisagree fails unless id's ad.provenance.disagree records since before are exactly reasons, each with
// server and verdict, on socket.
func fskAssertDisagree(t *testing.T, before int, id, socket, server, verdict string, reasons ...string) {
	t.Helper()
	var got []string
	for _, rec := range trailSince(t, before, "ad.provenance.disagree") {
		if rec["claude_instance_id"] != id {
			continue
		}
		got = append(got, rec["reason"].(string))
		if rec["tmux_socket"] != socket || rec["server"] != server || rec["verdict"] != verdict {
			t.Errorf("disagree %v on %s: socket=%v server=%v verdict=%v; want %s %s %s",
				rec["reason"], id, rec["tmux_socket"], rec["server"], rec["verdict"], socket, server, verdict)
		}
	}
	if !equalStrings(got, reasons) {
		t.Errorf("disagree reasons on %s = %v; want %v", id, got, reasons)
	}
}

// TestFindMissingSocketsOneLookupPerSocket: rows needing a lookup take exactly one call per socket, each naming
// that socket; rows the process decides take none; a different server or a provenance_conflict leaves its row
// unverified while the socket's later rows are still judged from the one lookup.
func TestFindMissingSocketsOneLookupPerSocket(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	third := "/tmp/fsk-third/default"
	conflict := tmuxfix.Valid(tmuxfix.Token, "r1", tmuxfix.StoreID)
	cases := []struct {
		name  string
		rec   *tmuxfix.Recorder
		rows  []store.LiveSpawnIdentity
		calls []fskCall
		want  map[string]fskWant
	}{
		{"many rows on one socket", tmuxfix.NewRecorder(), []store.LiveSpawnIdentity{
			liveRow("r1", unknownEvidence()), liveRow("r2"), liveRow("r3", unknownEvidence()), liveRow("r4"),
		}, []fskCall{lookupOn(apitest.TestSocket)}, map[string]fskWant{
			"r1": fskMarked("tmux_absent"), "r2": fskMarked("tmux_absent"),
			"r3": fskMarked("tmux_absent"), "r4": fskMarked("tmux_absent"),
		}},
		{"rows spread over three sockets", tmuxfix.NewRecorder(), []store.LiveSpawnIdentity{
			liveRow("r1"), liveRow("r2", onSocket(fskOther)), liveRow("r3", onSocket(third), unknownEvidence()),
			liveRow("r4", unknownEvidence()), liveRow("r5", onSocket(fskOther)), liveRow("r6", onSocket(third)),
		}, []fskCall{lookupOn(apitest.TestSocket), lookupOn(fskOther), lookupOn(third)}, map[string]fskWant{
			"r1": fskMarked("tmux_absent"), "r2": fskMarked("tmux_absent"), "r3": fskMarked("tmux_absent"),
			"r4": fskMarked("tmux_absent"), "r5": fskMarked("tmux_absent"), "r6": fskMarked("tmux_absent"),
		}},
		{"no row needs a lookup", tmuxfix.NewRecorder(), []store.LiveSpawnIdentity{
			liveRow("r1", withSessionStart(9102, fmStart)), liveRow("r2", onSocket(fskOther), withPane(9103, fmStart)),
		}, nil, map[string]fskWant{"r1": fskMarked("proc_absent"), "r2": fskMarked("proc_absent")}},
		{"different server stops nothing", tmuxfix.NewRecorder().StartServer(apitest.TestSocket, tmuxfix.Server{}),
			[]store.LiveSpawnIdentity{liveRow("r1", withServer()), liveRow("r2"), liveRow("r3", unknownEvidence())},
			[]fskCall{lookupOn(apitest.TestSocket)}, map[string]fskWant{
				"r1": fskNoted("tmux_server_changed"), "r2": fskMarked("tmux_absent"), "r3": fskMarked("tmux_absent")}},
		{"provenance conflict stops nothing", tmuxfix.NewRecorder().StartServer(apitest.TestSocket, fmServer).SeedSessions(
			apitest.TestSocket, tmuxfix.SeedSession{Name: "cd-r1", Label: conflict}, tmuxfix.SeedSession{Name: "cd-r1-copy", Label: conflict}),
			[]store.LiveSpawnIdentity{liveRow("r1", withServer()), liveRow("r2"), liveRow("r3", unknownEvidence())},
			[]fskCall{lookupOn(apitest.TestSocket)}, map[string]fskWant{
				"r1": fskNoted("provenance_conflict"), "r2": fskMarked("tmux_absent"), "r3": fskMarked("tmux_absent")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, res, before := fskSweep(t, c.rec, fskChecker(), nil, c.rows...)
			fskAssertRows(t, st, res, before, c.want)
			fskAssertCalls(t, c.rec, c.calls...)
		})
	}
}

// TestFindMissingSocketsStopRulePerSocket: a hung, unavailable or permission-denied socket (lookup or adoption
// listing) takes one call and one query timeout; its later rows are not called while the other socket's are judged.
func TestFindMissingSocketsStopRulePerSocket(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	cases := []struct {
		name    string
		failure tmux.Failure
		call    tmux.Call
		r1      string // the first row's note on the failing socket
	}{
		{"lookup hung", tmux.FailTimeout, tmux.CallLookup, "process_not_seen_tmux_unchecked"},
		{"tmux unavailable", tmux.FailUnavailable, tmux.CallLookup, "process_not_seen_tmux_unchecked"},
		{"socket permission denied", tmux.FailSocketDenied, tmux.CallLookup, "process_not_seen_tmux_unchecked"},
		{"adoption listing hung", tmux.FailTimeout, tmux.CallListPanes, "process_not_seen_session_present"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// r1 is a lost reply's row: Ours, with its pane to adopt; the other rows of its socket would read Gone.
			r1 := liveRow("r1", withServer())
			rec := fmOurs(r1).Script(apitest.TestSocket, tmuxfix.Script{Failure: c.failure}, c.call)
			clock := tmuxfix.NewClock(fmNow)
			st, res, before := fskSweep(t, rec, fskChecker(), clock, r1,
				liveRow("r2", onSocket(fskOther)), liveRow("r3"),
				liveRow("r4", onSocket(fskOther), unknownEvidence()), liveRow("r5", unknownEvidence()))

			fskAssertRows(t, st, res, before, map[string]fskWant{
				"r1": fskNoted(c.r1), "r3": fskNoted("process_not_seen_tmux_unchecked"), "r5": fskNoted("probe_eacces"),
				"r2": fskMarked("tmux_absent"), "r4": fskMarked("tmux_absent"),
			})
			calls := []fskCall{lookupOn(apitest.TestSocket)}
			if c.call == tmux.CallListPanes {
				calls = append(calls, fskCall{tmux.CallListPanes, apitest.TestSocket})
			}
			calls = append(calls, lookupOn(fskOther))
			fskAssertCalls(t, rec, calls...)
			if spent := clock.Now().Sub(fmNow); spent != time.Duration(len(calls))*fskQueryTimeout {
				t.Errorf("virtual time spent = %v; want %d query timeouts (%v each)", spent, len(calls), fskQueryTimeout)
			}
		})
	}
}

// TestFindMissingSocketsRecordedServerCheck: a no-server reply, an empty listing or another server's listing at
// the recorded socket marks the row while the recorded server process is gone, else notes tmux_server_changed;
// only a listing, empty or not, logs server_restarted (b.47f).
func TestFindMissingSocketsRecordedServerCheck(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	replies := []struct {
		name     string
		rec      func() *tmuxfix.Recorder
		restarts []string // disagree reasons when the recorded server process is gone
	}{
		{"no server running", func() *tmuxfix.Recorder {
			return tmuxfix.NewRecorder().SetNoServerFailure(apitest.TestSocket, tmux.FailNoServer)
		}, nil},
		{"empty listing", func() *tmuxfix.Recorder {
			return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, tmuxfix.Server{})
		}, []string{tmux.ReasonServerRestarted}},
		{"another server's listing", func() *tmuxfix.Recorder {
			return tmuxfix.NewRecorder().SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: "other"})
		}, []string{tmux.ReasonServerRestarted}},
	}
	servers := []struct {
		name string
		proc procfix.Process // the recorded server process as the reader answers it
		gone bool
	}{
		{"recorded server gone", procfix.Gone(), true},
		{"recorded server running", procfix.Alive(fmServer.ProcStart), false},
		{"recorded server unreadable", procfix.Unreadable(), false},
	}
	for _, reply := range replies {
		for _, srv := range servers {
			t.Run(reply.name+"/"+srv.name, func(t *testing.T) {
				pc := procfix.New()
				pc.Set(fmServer.PID, srv.proc)
				want, server, verdict, reasons := fskMarked("tmux_absent"), tmux.ServerRestarted, "gone", reply.restarts
				if !srv.gone {
					want, server, verdict = fskNoted("tmux_server_changed"), tmux.ServerDiffers, "different_server"
					reasons = []string{tmux.ReasonServerMismatch}
				}
				rec := reply.rec()
				st, res, before := fskSweep(t, rec, pc, nil, liveRow("srv", withServer()))

				fskAssertRows(t, st, res, before, map[string]fskWant{"srv": want})
				fskAssertDisagree(t, before, "srv", apitest.TestSocket, server, verdict, reasons...)
				fskAssertCalls(t, rec, lookupOn(apitest.TestSocket))
			})
		}
	}
}

// TestFindMissingSocketsNoSocketRule: rows with a recorded socket are looked up there whatever the caller's
// environment; a pre-release row uses the caller's socket, resolved creating nothing, or makes no call when refused;
// an unusable-name pre-release row read first resolves none and makes no call, and the usable one after it is
// resolved (or refused) as the first.
func TestFindMissingSocketsNoSocketRule(t *testing.T) {
	// Serial: it sets TMUX, TMUX_TMPDIR with t.Setenv; it checks the shared trail by literal row ids other
	// find-missing tests reuse.
	f := fmuReps()[1]
	t.Run("caller's TMUX names another server", func(t *testing.T) {
		caller := filepath.Join(t.TempDir(), "caller")
		t.Setenv("TMUX", caller+",4242,0")
		t.Setenv("TMUX_TMPDIR", t.TempDir())
		agent := liveRow("agent", withServer(), withPane(fskUnreadablePID, fmStart))
		rec := fmOurs(agent).StartServer(caller, tmuxfix.Server{})

		st, res, before := fskSweep(t, rec, fskChecker(), nil, fskUnusable("a-u", f, preRelease()), agent, liveRow("legacy", preRelease()))
		fskAssertRows(t, st, res, before, map[string]fskWant{
			"a-u": fskNoted(f.note), "agent": fskNoted("probe_eacces"), "legacy": fskMarked("tmux_absent"),
		})
		fskAssertCalls(t, rec, lookupOn(apitest.TestSocket), lookupOn(caller))
	})

	t.Run("no per-user directory", func(t *testing.T) {
		tmpdir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		t.Setenv("TMUX", "")
		t.Setenv("TMUX_TMPDIR", tmpdir)
		rec := tmuxfix.NewRecorder()

		st, res, before := fskSweep(t, rec, fskChecker(), nil, liveRow("agent"),
			liveRow("legacy-1", preRelease()), liveRow("legacy-2", preRelease(), unknownEvidence()))
		fskAssertRows(t, st, res, before, map[string]fskWant{"agent": fskMarked("tmux_absent"),
			"legacy-1": fskMarked("tmux_absent"), "legacy-2": fskMarked("tmux_absent")})
		fskAssertCalls(t, rec, lookupOn(apitest.TestSocket), lookupOn(filepath.Join(userSocketDir(tmpdir), "default")))
		if _, err := os.Stat(userSocketDir(tmpdir)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("per-user directory: stat err %v; want it still missing", err)
		}
	})

	t.Run("unusable socket directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
		t.Setenv("TMUX", "")
		t.Setenv("TMUX_TMPDIR", file)
		rec := tmuxfix.NewRecorder()
		clock := tmuxfix.NewClock(fmNow)

		st, res, before := fskSweep(t, rec, fskChecker(), clock, fskUnusable("a-u", f, preRelease()), liveRow("agent"),
			liveRow("legacy-1", preRelease()), liveRow("legacy-2", preRelease(), unknownEvidence()))
		fskAssertRows(t, st, res, before, map[string]fskWant{"a-u": fskNoted(f.note), "agent": fskMarked("tmux_absent"),
			"legacy-1": fskNoted("process_not_seen_tmux_unchecked"), "legacy-2": fskNoted("probe_eacces")})
		fskAssertCalls(t, rec, lookupOn(apitest.TestSocket))
		if spent := clock.Now().Sub(fmNow); spent != fskQueryTimeout {
			t.Errorf("virtual time spent = %v; want one query timeout (%v): a refusal charges nothing", spent, fskQueryTimeout)
		}
	})
}

// fskUnusable is a row with unknown evidence on the default socket recording f's unusable name; opts apply last.
func fskUnusable(id string, f unusableNameFixture, opts ...fmRowOpt) store.LiveSpawnIdentity {
	return liveRow(id, append([]fmRowOpt{unknownEvidence(), fmuNamed(f.raw)}, opts...)...)
}

// TestFindMissingSocketsUnusableName: an unusable-name row (each kind) read before, between or after usable rows
// of its socket, alone on its socket, or with every row needing tmux unusable, is noted with no call and charges
// no budget: a budget for one call still looks up the usable rows; each socket's one lookup judges them. After its
// socket stopped, or with the budget spent, it still gets its own note, not the "not called" one.
func TestFindMissingSocketsUnusableName(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	third, oneCall := "/tmp/fsk-third/default", fskQueryTimeout*3/2
	type world struct {
		name   string
		rows   []store.LiveSpawnIdentity
		rec    *tmuxfix.Recorder
		budget time.Duration
		calls  []fskCall
		want   map[string]fskWant
	}
	for _, f := range fmuReps() {
		ours := liveRow("o", withServer(), withPane(fskUnreadablePID, fmStart))
		worlds := []world{
			{name: "alone on its socket", rows: []store.LiveSpawnIdentity{fskUnusable("u", f, onSocket(fskOther)), liveRow("g")},
				calls: []fskCall{lookupOn(apitest.TestSocket)}, want: map[string]fskWant{"u": fskNoted(f.note), "g": fskMarked("tmux_absent")}},
			{name: "every row needing tmux unusable", rows: []store.LiveSpawnIdentity{fskUnusable("u1", f),
				fskUnusable("u2", f, onSocket(fskOther)), liveRow("dead", withSessionStart(9102, fmStart))},
				want: map[string]fskWant{"u1": fskNoted(f.note), "u2": fskNoted(f.note), "dead": fskMarked("proc_absent")}},
			{name: "budget for one call", budget: oneCall, rows: []store.LiveSpawnIdentity{fskUnusable("a-u", f, onSocket(fskOther)),
				fskUnusable("b-u", f, onSocket(third)), liveRow("c", unknownEvidence())}, calls: []fskCall{lookupOn(apitest.TestSocket)},
				want: map[string]fskWant{"a-u": fskNoted(f.note), "b-u": fskNoted(f.note), "c": fskMarked("tmux_absent")}},
			// The name check comes before the socket's stop and the budget: the unusable row keeps its own note.
			{name: "after its socket stopped", rows: []store.LiveSpawnIdentity{liveRow("a", unknownEvidence()), fskUnusable("b-u", f)},
				rec:   tmuxfix.NewRecorder().Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup),
				calls: []fskCall{lookupOn(apitest.TestSocket)}, want: map[string]fskWant{"a": fskNoted("probe_eacces"), "b-u": fskNoted(f.note)}},
			{name: "budget spent", budget: fmBudgetSpent, rows: []store.LiveSpawnIdentity{fskUnusable("u", f), liveRow("g", unknownEvidence())},
				want: map[string]fskWant{"u": fskNoted(f.note), "g": fskNoted("probe_eacces")}},
		}
		for _, uid := range []string{"a-u", "h-u", "z-u"} {
			worlds = append(worlds, world{name: "beside usable rows " + uid, rec: fmOurs(ours),
				rows:  []store.LiveSpawnIdentity{ours, fskUnusable(uid, f), liveRow("g", unknownEvidence())},
				calls: []fskCall{lookupOn(apitest.TestSocket)},
				want:  map[string]fskWant{"o": fskNoted("probe_eacces"), "g": fskMarked("tmux_absent"), uid: fskNoted(f.note)}})
		}
		for _, w := range worlds {
			t.Run(f.label+"/"+w.name, func(t *testing.T) {
				rec, clock := w.rec, tmuxfix.NewClock(fmNow)
				if rec == nil {
					rec = tmuxfix.NewRecorder()
				}
				st := &fakeFindMissingStore{rows: w.rows}
				res, _, before := sweepFrom(t, st, fskChecker(), fmSweep{tmux: rec, clock: clock, budget: w.budget})

				fskAssertRows(t, st, res, before, w.want)
				fskAssertCalls(t, rec, w.calls...)
				if spent, want := clock.Now().Sub(fmNow), time.Duration(len(w.calls))*fskQueryTimeout; spent != want {
					t.Errorf("virtual time spent = %v; want %v", spent, want)
				}
			})
		}
	}
}
