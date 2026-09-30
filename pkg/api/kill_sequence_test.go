package api_test

// kill_sequence_test.go covers kill's sequence on a live row (SR-6.1, SR-6.2,
// SR-3.6, SR-3.7): the pane kill then the session kill by ids, shared
// windows, the Gone viewer pane, teammates, lost-reply adoption, sessions
// replaced or removed mid-call, failed kills, held names and the socket.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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
// an error), the calls, the row's state kept and row_version moved only by
// the adoption write.
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
	after := e.columns(t, r.ID)
	if after.State != before.State {
		t.Errorf("state = %v; want %v kept", after.State, before.State)
	}
	want := before.RowVersion.(int64)
	if o.adopted {
		want++
	}
	if got := after.RowVersion.(int64); got != want {
		t.Errorf("row_version = %d; want %d", got, want)
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

// seqSeedOurs seeds r's current-labelled session with panes laid out by hand (e.seedOurs).
func seqSeedOurs(t *testing.T, e *killEnv, r *killRow, panes ...tmuxfix.SeedPane) {
	t.Helper()
	e.seedOurs(t, r, panes...)
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
// session by id, wherever the window is shared; success once every process is gone.
func TestKillSequenceEndsAgentPane(t *testing.T) {
	cases := []struct {
		name   string
		spec   killRowSpec
		setup  func(t *testing.T, e *killEnv, r *killRow)
		wait   time.Duration // the teammates exit after this much of the wait
		calls  []tmux.Call
		reason string // an ad.provenance.disagree reason the call writes
	}{
		{name: "reported-in agent, SessionStart pid is the pane pid (AC-KILL-01)", calls: seqOurs},
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
		{name: "teammate pane exits during the wait (AC-KILL-01, AC-KILL-19)", spec: killRowSpec{Teammates: 1},
			wait: 3 * api.KillPollInterval, calls: seqOurs},
		{name: "Gone, the pane lives on only in a viewer session", spec: killRowSpec{NoSession: true},
			calls: seqGonePane, setup: func(t *testing.T, e *killEnv, r *killRow) { e.seedViewer(t, *r) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, c.spec)
			if c.setup != nil {
				c.setup(t, e, &r)
			}
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			if len(r.TeammatePIDs) > 0 {
				e.setAfterWaiting(c.wait, procfix.Gone(), r.TeammatePIDs...)
			}
			start := e.clock.Now()
			seqKill(t, e, r, seqOutcome{calls: c.calls, sent: true})
			if got, want := e.clock.Now().Sub(start), seqCharged(e, c.calls)+c.wait; got != want {
				t.Errorf("virtual time = %v; want %v (the calls plus the teammates' exit)", got, want)
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
				seqSeedOurs(t, e, r, tmuxfix.SeedPane{ID: r.Spawn.Identity.PaneID,
					PID: seqAlivePID(e, tmux.CallKillSession), AdPane: r.Token})
			}, out: seqWaitExpired(seqNoPane, session, true, false)},
		{name: "agent's pane moved out of the session", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) { seqSeedOurs(t, e, r, tmuxfix.SeedPane{}) },
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
		{name: "base-index 1, the agent's pane is 1.1", spec: killRowSpec{NoPane: true}, calls: seqOurs, adopted: true,
			panes: func(e *killEnv, r killRow) []tmuxfix.SeedPane {
				return []tmuxfix.SeedPane{{Window: 1, Index: 1, AdPane: r.Token}}
			}},
		{name: "only the server identity was lost", spec: killRowSpec{NoServerIdentity: true}, calls: seqOurs, adopted: true},
		{name: "the adoption write is not applied", spec: lost, calls: seqOurs,
			store: func(s *killStore) { s.refuseAdopt(api.CondChanged) }},
		{name: "the adoption write fails in the store", spec: lost, calls: seqOurs,
			store: func(s *killStore) { s.failAdopt(nil) }},
		{name: "no pane carries @ad_pane, nothing new to adopt", spec: killRowSpec{NoPane: true}, calls: seqFollowUp,
			panes: func(*killEnv, killRow) []tmuxfix.SeedPane { return []tmuxfix.SeedPane{{}} }},
		{name: "no pane carries @ad_pane, the server identity adopted", spec: lost, calls: seqFollowUp, adopted: true,
			panes: func(*killEnv, killRow) []tmuxfix.SeedPane { return []tmuxfix.SeedPane{{}} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := c.spec
			spec.NoSession = c.panes != nil
			r := e.seedRow(t, spec)
			if c.panes != nil {
				seqSeedOurs(t, e, &r, c.panes(e, r)...)
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

// TestKillSequenceNameHeldElsewhere: a session holding the row's name with no
// valid label, or another row's, is never touched (AC-KILL-07).
func TestKillSequenceNameHeldElsewhere(t *testing.T) {
	holders := []struct {
		name string
		seed func(t *testing.T, e *killEnv, r *killRow) (sessionID, otherID string)
	}{
		{"a session with no label", func(t *testing.T, e *killEnv, r *killRow) (string, string) {
			e.ensureServer(r)
			pid := e.newPID()
			e.pc.Set(pid, procfix.Alive(apitest.LinuxProcStarttime))
			return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name, Panes: []tmuxfix.SeedPane{{PID: pid}}}).ID, ""
		}},
		{"another row's session", func(t *testing.T, e *killEnv, r *killRow) (string, string) {
			o := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(r.Name)}})
			return o.Session.ID, o.ID
		}},
	}
	for _, h := range holders {
		for _, alive := range []bool{false, true} {
			name := h.name + ", agent process gone"
			out := seqOutcome{calls: []tmux.Call{tmux.CallLookup}}
			if alive {
				name = h.name + ", agent process running"
				out = seqOutcome{calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}, errName: "ErrTmuxKillFailed",
					desc: func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescKillNoPane(r.ID, r.Name, r.AgentPID) }}
			}
			t.Run(name, func(t *testing.T) {
				e := newKillEnv(t)
				agent := agentGone
				if alive {
					agent = agentAlive
				}
				r := e.seedRow(t, killRowSpec{NoSession: true, Agent: agent})
				holder, other := h.seed(t, e, &r)
				if other != "" {
					out.forbid = []string{other}
				}
				seqKill(t, e, r, out)
				if !seqHas(e, r.Socket, holder) {
					t.Errorf("holder session %s is gone; want it untouched", holder)
				}
			})
		}
	}
}

// TestKillSequenceUsesRowSocket: every call names the row's recorded socket
// whatever the caller's TMUX; with none recorded, the caller-resolved one, creating nothing.
func TestKillSequenceUsesRowSocket(t *testing.T) {
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
				seqSeedOurs(t, e, &r, tmuxfix.SeedPane{ID: id.PaneID, PID: id.PanePID, AdPane: r.Token})
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
