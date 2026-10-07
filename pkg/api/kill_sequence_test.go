package api_test

// kill_sequence_test.go covers kill's sequence on a live row (SR-6.1, SR-6.2,
// SR-3.6, SR-3.7): the pane kill then the session kill by ids, shared
// windows, the Gone viewer pane, lost-reply adoption, sessions replaced or
// removed mid-call, failed kills and the socket; on a pending row (AC-KILL-17,
// AC-KILL-18) its own session's abort, a kill before the session exists, Gone
// beside an unlabelled session holding its name and the Leftover refusal (kill
// never reads a pending row's life, so one launch kind does); and with the
// finished-row opt-in on an ended row whose own session reported in and is
// past the stopping window and the starting-session bound (SR-6.5, SR-20.6),
// the same sequence by ids, the wait or one follow-up, a lost reply's pane
// used without a write, the row unchanged, and AC-KILL-11's kill and later
// resume launching on an ended row and again on a missing row. After the
// opt-in's check the sequence is kill's own, so its failed kills and the
// follow-up's other outcomes are TestKillSequenceCheckDecides' and
// TestKillCheckFollowUp's (an unanswered follow-up with the opt-in is
// TestKillTrailCalledPerReturnPath's); the opt-in never branches on ended against
// missing. Kill never changes a row's state. A
// session holding the row's name is TestKillIncludeFinishedTable's (its Gone
// rows) and the call table's, as is another store's session (AC-LKP-20).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The call sequences kill makes.
var (
	seqOurs      = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession}
	seqNoPane    = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillSession}
	seqFollowUp  = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillSession, tmux.CallLookup}
	seqGonePane  = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane}
	seqKillCalls = []tmux.Call{tmux.CallKillPane, tmux.CallKillSession}
)

// seqOutcome is what one kill must produce: its calls, kill_sent, the error
// (errName "" is success) with its description case, and the adoption write.
type seqOutcome struct {
	calls   []tmux.Call
	sent    bool
	errName string
	desc    func(e *killEnv, r killRow) apitest.DescCase
	forbid  []string
	adopted bool
}

// seqKill kills r and checks o: result or error, kill_sent (in the trail for
// an error), the calls, and the row unchanged but for the adoption write
// (state kept, row_version +1).
func seqKill(t *testing.T, e *killEnv, r killRow, o seqOutcome) {
	t.Helper()
	before := e.columns(t, r.ID)
	res, err := e.kill(r.ID)
	if o.errName == "" {
		if err != nil {
			t.Fatalf("kill: %v; want success", err)
		}
		if res.KillSent != o.sent {
			t.Errorf("kill_sent = %v; want %v", res.KillSent, o.sent)
		}
	} else {
		assertOneName(t, err, o.errName)
		apitest.AssertDescription(t, err.Error(), o.desc(e, r), o.forbid...)
	}
	if rec := killCalled(t, r.ID); len(rec) == 0 || rec[len(rec)-1]["kill_sent"] != o.sent {
		t.Errorf("ad.kill.called kill_sent: records %v; want last %v", rec, o.sent)
	}
	e.assertKillCalls(t, o.calls...)
	if !o.adopted {
		e.assertRowUnchanged(t, r.ID, before)
		return
	}
	if after := e.columns(t, r.ID); after.State != before.State || after.RowVersion.(int64) != before.RowVersion.(int64)+1 {
		t.Errorf("state, row_version = %v, %v; want %v kept, %d", after.State, after.RowVersion, before.State,
			before.RowVersion.(int64)+1)
	}
}

// seqWaitExpired is the ErrTmuxKillFailed wait-expired outcome with sent and
// the agent's pid named (survivors: r's teammates when teammates is set).
func seqWaitExpired(calls []tmux.Call, sent apitest.KillSent, agent, teammates bool) seqOutcome {
	return seqOutcome{calls: calls, sent: true, errName: "ErrTmuxKillFailed",
		desc: func(e *killEnv, r killRow) apitest.DescCase {
			p := apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name, Sent: sent, ExitWait: e.cfg.EffectiveKillExitWait()}
			if agent {
				p.AgentPID = r.AgentPID
			}
			if teammates {
				p.SurvivorPIDs = r.TeammatePIDs
			}
			return apitest.DescKillWaitExpired(p)
		}}
}

// seqAlivePID returns a new pid, alive in the fake until a call of kind exit.
func seqAlivePID(e *killEnv, exit tmux.Call) int {
	pid := e.newPID()
	e.pc.Set(pid, procfix.Alive(apitest.LinuxProcStarttime))
	e.setAfterCall(exit, procfix.Gone(), pid)
	return pid
}

// seqTarget is the target of the first call of kind call ("" when none).
func seqTarget(e *killEnv, call tmux.Call) string {
	if c := e.rec.SocketCallsOf(call); len(c) > 0 {
		return c[0].Target
	}
	return ""
}

// seqListing returns the ids of the sessions on socket that list paneID.
func seqListing(e *killEnv, socket, paneID string) []string {
	var ids []string
	for _, s := range e.rec.Sessions(socket) {
		for _, p := range s.Panes {
			if p.ID == paneID {
				ids = append(ids, s.ID)
			}
		}
	}
	return ids
}

// seqHas reports whether socket still holds the session id.
func seqHas(e *killEnv, socket, id string) bool {
	return slices.ContainsFunc(e.rec.Sessions(socket), func(s tmuxfix.SeedSession) bool { return s.ID == id })
}

// seqCharged is the virtual time calls take: Q per lookup or listing, A per kill.
func seqCharged(e *killEnv, calls []tmux.Call) time.Duration {
	var d time.Duration
	for _, c := range calls {
		if c == tmux.CallLookup || c == tmux.CallListPanes {
			d += e.cfg.EffectiveQueryTimeout()
		} else {
			d += e.cfg.EffectiveActionTimeout()
		}
	}
	return d
}

// TestKillSequenceEndsAgentPane: the agent's pane is killed by id before the
// session by id, wherever the window is shared; success once every process is
// gone (a teammate exiting during the wait is TestKillCheckWait's).
func TestKillSequenceEndsAgentPane(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		spec   killRowSpec
		setup  func(t *testing.T, e *killEnv, r *killRow)
		calls  []tmux.Call
		reason string // an ad.provenance.disagree reason the call writes
	}{
		// SR-20.6: formerly TestKillLiveRowInvokesTmux; the kills target the pane and session ids.
		{name: "reported-in agent, SessionStart pid is the pane pid (AC-KILL-01)", calls: seqOurs},
		{name: "a pending row's own session: the launch aborted, the row left pending (AC-KILL-17)",
			spec: killRowSpec{State: store.StatePending}, calls: seqOurs},
		{name: "renamed session, killed by id", spec: killRowSpec{NoSession: true}, calls: seqOurs,
			reason: tmux.ReasonNameChanged, setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionName("renamed-by-hand"))
			}},
		{name: "grouped session shares the window", calls: seqOurs,
			setup: func(t *testing.T, e *killEnv, r *killRow) { e.seedViewer(t, *r) }},
		{name: "linked window in another session", calls: seqOurs, setup: func(t *testing.T, e *killEnv, r *killRow) {
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "linker", Panes: []tmuxfix.SeedPane{
				{}, {Window: 3, ID: r.Spawn.Identity.PaneID, Shared: true}}})
		}},
		{name: "Gone, the pane lives on only in a viewer session", spec: killRowSpec{NoSession: true},
			calls: seqGonePane, setup: func(t *testing.T, e *killEnv, r *killRow) { e.seedViewer(t, *r) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, c.spec)
			if c.setup != nil {
				c.setup(t, e, &r)
			}
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			start := e.clock.Now()
			seqKill(t, e, r, seqOutcome{calls: c.calls, sent: true})
			if got, want := e.clock.Now().Sub(start), seqCharged(e, c.calls); got != want {
				t.Errorf("virtual time = %v; want %v (the calls, no wait)", got, want)
			}
			if got := seqTarget(e, tmux.CallKillPane); got != r.Spawn.Identity.PaneID {
				t.Errorf("pane kill targets %q; want the agent's pane %q", got, r.Spawn.Identity.PaneID)
			}
			if slices.Contains(c.calls, tmux.CallKillSession) {
				if got := seqTarget(e, tmux.CallKillSession); got != r.Session.ID {
					t.Errorf("session kill targets %q; want the labelled session %q", got, r.Session.ID)
				}
				if seqHas(e, r.Socket, r.Session.ID) {
					t.Errorf("labelled session %s still listed", r.Session.ID)
				}
			}
			if ids := seqListing(e, r.Socket, r.Spawn.Identity.PaneID); len(ids) > 0 {
				t.Errorf("sessions %v still show the agent's pane", ids)
			}
			if c.reason != "" && !slices.ContainsFunc(killDisagrees(t, r.ID), func(m map[string]any) bool {
				return m["reason"] == c.reason
			}) {
				t.Errorf("no ad.provenance.disagree %s record", c.reason)
			}
		})
	}
}

// TestKillSequenceCheckDecides: a survivor, a missing or respawned agent pane,
// failed kills and a session replaced or removed mid-call leave it to the check.
func TestKillSequenceCheckDecides(t *testing.T) {
	t.Parallel()
	both, session := apitest.KillSent{Pane: true, Session: true}, apitest.KillSent{Session: true}
	agentGoneAfter := func(call tmux.Call) func(*testing.T, *killEnv, *killRow) {
		return func(_ *testing.T, e *killEnv, r *killRow) { e.setAfterCall(call, procfix.Gone(), r.AgentPID) }
	}
	script := func(f tmux.Failure, call tmux.Call, then func(*testing.T, *killEnv, *killRow)) func(*testing.T, *killEnv, *killRow) {
		return func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: f, Times: 1}, call)
			if then != nil {
				then(t, e, r)
			}
		}
	}
	replace := func(then func(*testing.T, *killEnv, *killRow)) func(*testing.T, *killEnv, *killRow) {
		return func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.ReplaceSessionAfter(tmux.CallListPanes, r.Socket, r.Session.ID, tmux.Label{})
			if then != nil {
				then(t, e, r)
			}
		}
	}
	ok := seqOutcome{calls: seqOurs, sent: true}
	cases := []struct {
		name      string
		spec      killRowSpec
		setup     func(t *testing.T, e *killEnv, r *killRow)
		out       seqOutcome
		replaced  bool
		survivors bool
	}{
		{name: "a teammate ignoring the hangup survives (AC-KILL-19)", spec: killRowSpec{Teammates: 1},
			setup: agentGoneAfter(tmux.CallKillPane), out: seqWaitExpired(seqOurs, both, false, true), survivors: true},
		{name: "agent's pane respawned with another pid", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOurs(t, r, tmuxfix.SeedPane{ID: r.Spawn.Identity.PaneID,
					PID: seqAlivePID(e, tmux.CallKillSession), AdPane: r.Token})
			}, out: seqWaitExpired(seqNoPane, session, true, false)},
		{name: "agent's pane moved out of the session", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) { e.seedOurs(t, r, tmuxfix.SeedPane{}) },
			out:   seqWaitExpired(seqNoPane, session, true, false)},
		{name: "pane kill refused, the session kill ends the agent", out: ok,
			setup: script(tmux.FailSocketDenied, tmux.CallKillPane, agentGoneAfter(tmux.CallKillSession))},
		{name: "pane kill times out, the session kill ends the agent", out: ok,
			setup: script(tmux.FailTimeout, tmux.CallKillPane, agentGoneAfter(tmux.CallKillSession))},
		{name: "session kill times out after the pane kill", out: ok,
			setup: script(tmux.FailTimeout, tmux.CallKillSession, agentGoneAfter(tmux.CallKillPane))},
		{name: "both kills fail, the agent runs on", out: seqWaitExpired(seqOurs, both, true, false),
			setup: script(tmux.FailTimeout, tmux.CallKillPane, script(tmux.FailSocketDenied, tmux.CallKillSession, nil))},
		{name: "session replaced after the listing", out: ok, replaced: true,
			setup: replace(agentGoneAfter(tmux.CallListPanes))},
		{name: "session replaced after the listing, the agent runs on", replaced: true,
			setup: replace(nil), out: seqWaitExpired(seqOurs, both, true, false)},
		{name: "session removed after the listing", out: ok, setup: func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.RemoveSessionAfter(tmux.CallListPanes, r.Socket, r.Session.ID)
			agentGoneAfter(tmux.CallListPanes)(t, e, r)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, c.spec)
			c.setup(t, e, &r)
			seqKill(t, e, r, c.out)
			if c.replaced {
				seqAssertReplacementUntouched(t, e, r)
			}
			if c.survivors {
				rec := killCalled(t, r.ID)[0]
				want := []any{float64(r.TeammatePIDs[0])}
				if got, _ := rec["survivor_pids"].([]any); !slices.Equal(got, want) || rec["process_check"] != "gone" {
					t.Errorf("ad.kill.called survivor_pids %v, process_check %v; want %v, gone",
						rec["survivor_pids"], rec["process_check"], want)
				}
			}
		})
	}
}

// seqAssertReplacementUntouched fails unless a session under r's session's
// name with another id is still listed and no kill targeted it or its panes.
func seqAssertReplacementUntouched(t *testing.T, e *killEnv, r killRow) {
	t.Helper()
	var repl *tmuxfix.SeedSession
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Name == r.Session.Name && s.ID != r.Session.ID {
			repl = &s
		}
	}
	if repl == nil {
		t.Fatalf("replacement of %s (%q) is gone; want it untouched", r.Session.ID, r.Session.Name)
	}
	targets := []string{repl.ID}
	for _, p := range repl.Panes {
		targets = append(targets, p.ID)
	}
	for _, c := range e.rec.SocketCalls() {
		if slices.Contains(seqKillCalls, c.Call) && slices.Contains(targets, c.Target) {
			t.Errorf("%v targets the replacement's %s", c.Call, c.Target)
		}
	}
}

// TestKillSequenceAdoptsLostReply: after a lost create reply, the pane whose
// @ad_pane carries the row's token is adopted (never a teammate's, never by
// index), written once; a refused or failed write leaves the kill unchanged.
func TestKillSequenceAdoptsLostReply(t *testing.T) {
	t.Parallel()
	lost := killRowSpec{NoPane: true, NoServerIdentity: true}
	cases := []struct {
		name    string
		spec    killRowSpec
		panes   func(e *killEnv, r killRow) []tmuxfix.SeedPane // nil: the fixture's one-pane session
		store   func(s *killStore)
		calls   []tmux.Call
		adopted bool
	}{
		{name: "the session's one pane carries @ad_pane", spec: lost, calls: seqOurs, adopted: true},
		{name: "a teammate at 0.0, the agent's pane split after it (AC-LKP-21)", spec: killRowSpec{NoPane: true},
			calls: seqOurs, adopted: true, panes: func(e *killEnv, r killRow) []tmuxfix.SeedPane {
				return []tmuxfix.SeedPane{{PID: seqAlivePID(e, tmux.CallKillSession)}, {Index: 1, AdPane: r.Token}}
			}},
		{name: "the adoption write is not applied", spec: lost, calls: seqOurs,
			store: func(s *killStore) { s.refuseAdopt(api.CondChanged) }},
		{name: "the adoption write fails in the store", spec: lost, calls: seqOurs,
			store: func(s *killStore) { s.failAdopt(nil) }},
		{name: "no pane carries @ad_pane, the server identity adopted", spec: lost, calls: seqFollowUp, adopted: true,
			panes: func(*killEnv, killRow) []tmuxfix.SeedPane { return []tmuxfix.SeedPane{{}} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			spec := c.spec
			spec.NoSession = c.panes != nil
			r := e.seedRow(t, spec)
			if c.panes != nil {
				e.seedOurs(t, &r, c.panes(e, r)...)
			}
			if c.store != nil {
				c.store(e.store)
			}
			e.seedBystander(t, r.Socket)
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			seqKill(t, e, r, seqOutcome{calls: c.calls, sent: true, adopted: c.adopted})
			agentPane := ""
			for _, p := range r.Session.Panes {
				if p.AdPane == r.Token {
					agentPane = p.ID
				}
			}
			if got := seqTarget(e, tmux.CallKillPane); got != agentPane {
				t.Errorf("pane kill targets %q; want the @ad_pane pane %q", got, agentPane)
			}
			if got := seqTarget(e, tmux.CallKillSession); got != r.Session.ID {
				t.Errorf("session kill targets %q; want %q", got, r.Session.ID)
			}
			if n := len(killDisagrees(t, r.ID)); c.adopted != (n == 1) {
				t.Errorf("%d ad.provenance.disagree records; want adopted written %v", n, c.adopted)
			}
			if cols := e.columns(t, r.ID); c.adopted && c.spec.NoPane && agentPane != "" && cols.PaneID != agentPane {
				t.Errorf("pane_id = %v; want the adopted %s", cols.PaneID, agentPane)
			}
		})
	}
}

// TestKillSequenceUsesRowSocket: every call names the row's recorded socket
// whatever the caller's TMUX; with none recorded, the caller-resolved one, creating nothing.
func TestKillSequenceUsesRowSocket(t *testing.T) {
	// Serial: it sets TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name     string
		recorded bool // the row records apitest.TestSocket
		tmuxEnv  bool // TMUX names another server's socket
	}{
		// No commas in these names: t.TempDir keeps them, and TMUX's socket field ends at one.
		{"recorded socket with TMUX and TMUX_TMPDIR elsewhere", true, true},
		{"no recorded socket with TMUX naming the caller's server", false, true},
		{"no recorded socket and no per-user directory", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.ownSocketDir(t) // the last case removes the per-user directory
			elsewhere := filepath.Join(t.TempDir(), "elsewhere")
			spec := killRowSpec{}
			if !c.recorded {
				spec = killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSocket("")}}
			}
			r := e.seedRow(t, spec)
			if c.tmuxEnv {
				t.Setenv("TMUX", elsewhere+",4242,0")
				t.Setenv("TMUX_TMPDIR", t.TempDir())
			}
			out := seqOutcome{calls: seqOurs, sent: true}
			switch {
			case !c.recorded && c.tmuxEnv:
				r.Socket = elsewhere
				id := r.Spawn.Identity
				e.seedOurs(t, &r, tmuxfix.SeedPane{ID: id.PaneID, PID: id.PanePID, AdPane: r.Token})
			case !c.recorded:
				if err := os.Remove(filepath.Dir(e.defaultSocket)); err != nil {
					t.Fatalf("remove per-user directory: %v", err)
				}
				e.pc.Set(r.AgentPID, procfix.Gone())
				out = seqOutcome{calls: []tmux.Call{tmux.CallLookup}}
			}
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			seqKill(t, e, r, out)
			for _, call := range e.rec.SocketCalls() {
				if call.Socket != r.Socket {
					t.Errorf("%v on socket %q; want %q", call.Call, call.Socket, r.Socket)
				}
			}
			if !c.recorded && !c.tmuxEnv {
				if _, err := os.Stat(filepath.Dir(e.defaultSocket)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("per-user directory: stat err %v; want it still missing", err)
				}
			}
		})
	}
}

// kosFinished are the finished states the opt-in acts on.
var kosFinished = []string{store.StateEnded, store.StateMissing}

// kosReportedIn is a row in state that ended the window ago, records a pid
// and a session id, and whose own session (agent a) predates ended_at by the bound.
func kosReportedIn(state string, a agentState) startingRow {
	return startingRow{state: state, endedAgo: defWindow, agent: a, age: defWindow + defBound}
}

// kosKill runs kill with the opt-in on r and checks o (seqOutcome): the result
// or error, kill_sent in the result and the trail, the calls, each kill
// targeting r's agent pane and session by id, the sleeps (none after a
// follow-up) and the row unchanged in every column.
func kosKill(t *testing.T, e *killEnv, r killRow, o seqOutcome) {
	t.Helper()
	before := e.columns(t, r.ID)
	slept := 0
	prev := e.sleep
	e.sleep = func(d time.Duration) { slept++; prev(d) }
	res, err := e.killOptIn(r.ID)
	if o.errName == "" {
		if err != nil {
			t.Fatalf("kill: %v; want success", err)
		}
		if res.KillSent != o.sent {
			t.Errorf("kill_sent = %v; want %v", res.KillSent, o.sent)
		}
	} else {
		assertOneName(t, err, o.errName)
		apitest.AssertDescription(t, err.Error(), o.desc(e, r), r.Token, r.StoreID)
	}
	kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "lookup_outcome": "ours", "kill_sent": o.sent})
	e.assertKillCalls(t, o.calls...)
	if got := seqTarget(e, tmux.CallKillPane); got != "" && got != labelledPane(t, r.Session, r.Token) {
		t.Errorf("pane kill targets %q; want the agent's pane", got)
	}
	if got := seqTarget(e, tmux.CallKillSession); got != r.Session.ID {
		t.Errorf("session kill targets %q; want the labelled session %s", got, r.Session.ID)
	}
	if followUp := len(e.rec.SocketCallsOf(tmux.CallLookup)) > 1; followUp && slept != 0 {
		t.Errorf("%d sleeps with a follow-up lookup; want the wait or the follow-up, never both", slept)
	}
	e.assertRowUnchanged(t, r.ID, before)
}

// TestKillIncludeFinishedSequence: a reported-in session past both on an
// ended row is killed by ids, then the wait or one follow-up decides. The
// kill that ends the agent runs on an ended row and again on a missing row,
// each followed by a resume of the id that launches (AC-KILL-11; SRD
// Appendix C, TLA+ v2 F2-1: v2_Vr_NotHumanKillFinished, the human kill still
// firing with the fix).
func TestKillIncludeFinishedSequence(t *testing.T) {
	t.Parallel()
	both := apitest.KillSent{Pane: true, Session: true}
	cases := []struct {
		name   string
		agent  agentState
		setup  func(*testing.T, *killEnv, *killRow)
		out    seqOutcome
		resume bool // a later resume launches; run on each of kosFinished (AC-KILL-11)
	}{
		{name: "agent gone after the pane kill", resume: true, out: seqOutcome{calls: seqOurs, sent: true},
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			}},
		{name: "agent runs past the exit wait", out: seqWaitExpired(seqOurs, both, true, false)},
		{name: "agent unreadable, follow-up label gone", agent: agentUnreadable,
			out: seqOutcome{calls: withFollowUp(seqOurs), sent: true}},
		{name: "agent unreadable, follow-up label still there", agent: agentUnreadable,
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallKillPane, tmux.CallKillSession)
			}, out: seqOutcome{calls: withFollowUp(seqOurs), sent: true, errName: "ErrTmuxKillFailed",
				desc: func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescKillUncheckable(r.ID, r.Name, both) }}},
	}
	for _, tc := range cases {
		states := []string{store.StateEnded}
		if tc.resume {
			states = kosFinished
		}
		for _, state := range states {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedStarting(t, kosReportedIn(state, tc.agent))
				if tc.setup != nil {
					tc.setup(t, e, &r.killRow)
				}
				kosKill(t, e, r.killRow, tc.out)
				if tc.resume {
					kosAssertResumeLaunches(t, e, r.ID)
				}
			})
		}
	}
}

// kosAssertResumeLaunches fails unless a resume of id through e's Recorder
// creates one session and moves the row to pending.
func kosAssertResumeLaunches(t *testing.T, e *killEnv, id string) {
	t.Helper()
	if _, err := e.resume(id); err != nil {
		t.Fatalf("resume after the kill: %v", err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("session creations after resume = %d; want 1", n)
	}
	if got := e.columns(t, id).State; got != store.StatePending {
		t.Errorf("state after resume = %v; want pending", got)
	}
}

// TestKillIncludeFinishedSequenceUsesLostReplyPane: a reported-in row with no
// pane or server identity recorded has the pane carrying its token killed by
// id, with no adoption write (row_version and the identity unchanged).
func TestKillIncludeFinishedSequenceUsesLostReplyPane(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	pid := e.newPID()
	spec := e.resumableSpec(defWindow, agentAlive, apitest.WithPID(pid), apitest.WithProcStarttime(apitest.LinuxProcStarttime))
	spec.State, spec.NoPane, spec.NoServerIdentity = store.StateEnded, true, true
	r := e.seedResumableRow(t, spec).killRow
	r.Session = e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name, Label: r.current(),
		Created: e.ruleInstant().Add(-(defWindow + defBound)).Unix(), Panes: []tmuxfix.SeedPane{{PID: pid, AdPane: r.Token}}})
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), pid)

	kosKill(t, e, r, seqOutcome{calls: seqOurs, sent: true})
	if got := seqTarget(e, tmux.CallKillPane); got != r.Session.Panes[0].ID {
		t.Errorf("pane kill targets %q; want the token's pane %s", got, r.Session.Panes[0].ID)
	}
	if e.store.adoptTries != 0 {
		t.Errorf("adoption writes attempted = %d; want none on a finished row", e.store.adoptTries)
	}
}

// killPendSpec is a pending row as a spawn's insert leaves it before its create
// returns: token and socket, no server, pane or process identity, no session.
func killPendSpec(state string, opts ...apitest.SpawnOption) killRowSpec {
	return killRowSpec{State: state, NoSession: true, NoPane: true, NoServerIdentity: true,
		Agent: agentNotRecorded, Opts: opts}
}

// killPendCheck fails unless kill of r answered err (want; nil: success) with
// kill_sent false after the lookup alone, every session and the row
// untouched, and one ad.kill.called saying no kill was sent.
func killPendCheck(t *testing.T, e *killEnv, r killRow, before apitest.SpawnColumns, sessions []tmuxfix.SeedSession,
	res api.KillResult, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || (want == nil) != (err == nil) || res.KillSent {
		t.Fatalf("kill = %+v, %v; want %v with kill_sent false", res, err, want)
	}
	e.assertKillCalls(t, tmux.CallLookup)
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions after kill = %+v; want untouched %+v", got, sessions)
	}
	e.assertRowUnchanged(t, r.ID, before)
	if recs := killCalled(t, r.ID); len(recs) != 1 || recs[0]["kill_sent"] != false {
		t.Errorf("ad.kill.called records = %v; want one with kill_sent false", recs)
	}
}

// TestKillPendingGone: a pending row whose launch has no session finds Gone:
// success with kill_sent false, no kill, every session and the row untouched.
func TestKillPendingGone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow)
		// thenCreate runs the launch's create after the kill (AC-KILL-17, FR1 C7(l)).
		thenCreate bool
	}{
		{"before the launch created its session", killPendSpec(store.StatePending),
			func(t *testing.T, e *killEnv, r *killRow) {
				e.ensureServer(r)
				e.seedBystander(t, r.Socket)
				e.syncServers()
			}, true},
		{"no token, an unlabelled session holds its name",
			killPendSpec(store.StatePending, apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}),
				apitest.WithNoLaunchStartedAt()),
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(tmux.Label{}, false))
			}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			tc.setup(t, e, &r)
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			res, err := e.kill(r.ID)
			killPendCheck(t, e, r, before, sessions, res, err, nil)
			if !tc.thenCreate {
				return
			}
			if _, err := e.rec.NewSession(r.Socket, r.Name, "/tmp", nil, nil, r.Token, r.ID, r.StoreID); err != nil {
				t.Fatalf("create after kill: %v", err)
			}
			created := false
			for _, s := range e.rec.Sessions(r.Socket) {
				created = created || (s.Name == r.Name && s.Label == r.current())
			}
			if !created {
				t.Errorf("no session %q with the row's label after the create", r.Name)
			}
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestKillPendingBesideLeftover: a live row (pending, pending with no launch
// start, or revived to waiting) beside an earlier launch's session, under the
// recorded name or another, gets ErrTmuxSessionConflict naming it, and
// nothing is killed.
func TestKillPendingBesideLeftover(t *testing.T) {
	t.Parallel()
	revived := killPendSpec(store.StateWaiting)
	revived.Agent = agentAlive
	revived.Opts = []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 50),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime)}
	cases := []struct {
		name    string
		spec    killRowSpec
		renamed bool // the leftover runs under another name than the recorded one
	}{
		{"pending, recorded name", killPendSpec(store.StatePending), false},
		{"pending, another name", killPendSpec(store.StatePending), true},
		{"pending with no launch start and no token", killPendSpec(store.StatePending,
			apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}), apitest.WithNoLaunchStartedAt()), false},
		{"revived to waiting by the leftover's hooks", revived, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			opts := []tmuxfix.RowSessionOption{tmuxfix.WithRowSessionLabel(r.old(), true)}
			if tc.renamed {
				opts = append(opts, tmuxfix.WithRowSessionName("left-"+uuid.NewString()[:8]))
			}
			e.seedSession(t, &r, opts...)
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			res, err := e.kill(r.ID)
			killPendCheck(t, e, r, before, sessions, res, err, api.ErrTmuxSessionConflict)
			assertOneName(t, err, "ErrTmuxSessionConflict")
			forbid := []string{tmuxfix.OtherToken, r.StoreID}
			if r.Token != "" {
				forbid = append(forbid, r.Token)
			}
			apitest.AssertDescription(t, err.Error(),
				apitest.DescKillLeftover([]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}), forbid...)
		})
	}
}
