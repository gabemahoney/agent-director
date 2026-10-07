package api_test

// spawn_reuse_launch_test.go covers reuse's launch after its reset (SR-3.5,
// SR-3.8, SR-10.4, SR-13.2; AC-LKP-17, AC-REUSE-08, AC-REUSE-16): the identity
// write and its guard, the label step, the create argv through test/fake-tmux
// (labels chained, by id for a $ name, a # doubled, equal to a plain spawn's),
// and the longest paths' virtual time.

import (
	"errors"
	"io"
	"log"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rlNewPanesAlive puts the panes of every session a create leaves under its
// name in e's fake, alive with apitest.DarwinProcStarttime.
func rlNewPanesAlive(e *killEnv) {
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		for _, s := range e.rec.Sessions(c.Socket) {
			if s.Name != storedFormOf(c.Target) {
				continue
			}
			for _, p := range s.Panes {
				e.pc.Set(p.PID, procfix.Alive(apitest.DarwinProcStarttime))
			}
		}
	})
}

// rlOneCreate returns the one recorded create, failing unless the calls were
// exactly want and every one was on socket.
func rlOneCreate(t *testing.T, e *killEnv, socket string, want ...tmux.Call) tmuxfix.SocketCall {
	t.Helper()
	if got := callKinds(e.rec); !reflect.DeepEqual(got, want) || len(e.rec.Calls()) != 0 {
		t.Fatalf("tmux calls = %v (+%d name-based); want %v", got, len(e.rec.Calls()), want)
	}
	for _, c := range e.rec.SocketCalls() {
		if c.Socket != socket {
			t.Errorf("%v on socket %q; want the row's %q", c.Call, c.Socket, socket)
		}
	}
	return e.rec.SocketCallsOf(tmux.CallCreate)[0]
}

// rlAssertNewLife fails unless cols is the reset's new life after a launch
// from launchStart with token tok: pending, life + 1, a new token, no session
// id, pid or ended_at.
func rlAssertNewLife(t *testing.T, r reuseRow, cols apitest.SpawnColumns, tok string, launchStart time.Time) {
	t.Helper()
	if !spawnTokenRE.MatchString(tok) || tok == r.Token {
		t.Errorf("create token %q; want a new 16-hex token (old %q)", tok, r.Token)
	}
	if cols.State != store.StatePending || cols.LifeNumber != reuseLife+1 || cols.LaunchToken != tok ||
		cols.LaunchStartedAt != launchStart.UnixMilli() || cols.ClaudeSessionID != nil || cols.PID != nil || cols.EndedAt != nil {
		t.Errorf("row {state %v, life %v, token %v, launch start %v, session %v, pid %v, ended_at %v}; want pending, %d, %s, %d, all NULL",
			cols.State, cols.LifeNumber, cols.LaunchToken, cols.LaunchStartedAt, cols.ClaudeSessionID, cols.PID, cols.EndedAt,
			reuseLife+1, tok, launchStart.UnixMilli())
	}
}

// TestSpawnReuseIdentityWrite: the labelled create (session and pane labels)
// is recorded by the identity write, the reset's version plus one, only while
// the row is still this launch's; another versioned write first leaves it
// writing nothing, and a hook first is ignored (SR-22.9).
func TestSpawnReuseIdentityWrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		setup    func(t *testing.T, e *killEnv, r reuseRow) (parent any)
		recorded bool
	}{
		{"labelled create", func(*testing.T, *killEnv, reuseRow) any { return nil }, true},
		{"another write between the create and the identity write", func(t *testing.T, e *killEnv, r reuseRow) any {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				if err := e.st.SetParentID(r.ID, r.ParentID); err != nil {
					t.Errorf("SetParentID: %v", err)
				}
			})
			return r.ParentID
		}, false},
		// The reset row records no pane until the identity write, so the
		// store's gated hook writes are ignored and the write applies.
		{"hooks between the create and the identity write ignored", func(t *testing.T, e *killEnv, r reuseRow) any {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				for _, ev := range []string{"Stop", "SessionStart"} {
					got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, "sess-new-"+uuid.NewString()[:8])
					if got != (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}) {
						t.Errorf("%s before the identity write = %+v; want ignored, %s", ev, got, store.HookReasonNoPaneRecorded)
					}
				}
			})
			return nil
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			rlNewPanesAlive(e)
			wantParent := tc.setup(t, e, r)
			before, start := e.columns(t, r.ID), e.clock.Now().Add(boundQ)

			if _, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{})); err != nil || strings.Contains(logs, "WARN") ||
				(tc.recorded && logs != "") {
				t.Fatalf("reuse = %v (log %q); want success, no WARN, no log at all once recorded", err, logs)
			}
			c := rlOneCreate(t, e, r.Socket, tmux.CallLookup, tmux.CallCreate)
			s := rplSessionNamed(t, e.rec, r.Socket, r.Name)
			if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.Panes[0].AdPane != c.Token {
				t.Errorf("session label %+v, pane label %q; want ad1 %s <$N> %s <store id>, pane %s", s.Label, s.Panes[0].AdPane,
					c.Token, r.ID, c.Token)
			}
			cols := e.columns(t, r.ID)
			rlAssertNewLife(t, r, cols, c.Token, start)
			want := noIdentity
			if tc.recorded {
				srv, _ := e.rec.Server(r.Socket)
				want = []any{int64(srv.PID), srv.Start, srv.ProcStart, s.Panes[0].ID, int64(s.Panes[0].PID), apitest.DarwinProcStarttime}
				if cols.RowVersion != before.RowVersion.(int64)+2 {
					t.Errorf("row_version %v; want the reset's and the identity write's, %d", cols.RowVersion, before.RowVersion.(int64)+2)
				}
			}
			if got := identityCols(cols); !reflect.DeepEqual(got, want) {
				t.Errorf("identity columns = %#v; want %#v", got, want)
			}
			if cols.ParentID != wantParent {
				t.Errorf("parent_id = %v; want %v", cols.ParentID, wantParent)
			}
		})
	}
}

// TestSpawnReuseLabelStep: a failed chained label is relabelled by id and the reuse succeeds; when the
// relabel fails too, the session is killed by id (a failed kill leaves it), ErrTmuxSessionCreate says so,
// and the restore runs (a successful kill's: TestSpawnReuseRestoreAfterEachLaunchFailure).
func TestSpawnReuseLabelStep(t *testing.T) {
	t.Parallel()
	lookup, create, label, kill := tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	cases := []struct {
		name                string
		labelFail, killFail bool
		want                []tmux.Call
	}{
		{"relabel by id succeeds", false, false, []tmux.Call{lookup, create, label}},
		{"relabel fails, kill by id fails", true, true, []tmux.Call{lookup, create, label, kill}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create)
			if tc.labelFail {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, label)
			}
			if tc.killFail {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, kill)
			}
			rlNewPanesAlive(e)
			var created tmuxfix.SeedSession
			e.rec.AfterCall(create, func(c tmuxfix.SocketCall, _ error) { created = rplSessionNamed(t, e.rec, c.Socket, r.Name) })
			before := e.columns(t, r.ID)

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
			c := rlOneCreate(t, e, r.Socket, tc.want...)
			l := e.rec.SocketCallsOf(label)[0]
			if l.Target != created.ID || l.PaneID != created.Panes[0].ID || l.Token != c.Token || l.InstanceID != r.ID || l.StoreID != e.storeID {
				t.Errorf("label by id = %+v; want session %s pane %s with token %s, id %s, store %s",
					l, created.ID, created.Panes[0].ID, c.Token, r.ID, e.storeID)
			}
			cols := e.columns(t, r.ID)
			restored := pendTrail(t, rutRestored, r.ID)
			if !tc.labelFail {
				if err != nil {
					t.Fatalf("reuse: %v", err)
				}
				s := rplSessionNamed(t, e.rec, r.Socket, r.Name)
				if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.Panes[0].AdPane != c.Token {
					t.Errorf("session %+v; want ad1 %s %s %s <store id> with pane label", s, c.Token, created.ID, r.ID)
				}
				if cols.State != store.StatePending || cols.PaneID != created.Panes[0].ID ||
					cols.RowVersion != before.RowVersion.(int64)+2 || len(restored) != 0 {
					t.Errorf("row {state %v, pane %v, row_version %v}, restores %v; want pending, %s, reset + identity write, none",
						cols.State, cols.PaneID, cols.RowVersion, restored, created.Panes[0].ID)
				}
				return
			}
			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			if k := e.rec.SocketCallsOf(kill)[0]; k.Target != created.ID {
				t.Errorf("kill targets %q; want the session id %q", k.Target, created.ID)
			}
			present := slices.ContainsFunc(e.rec.Sessions(r.Socket), func(s tmuxfix.SeedSession) bool { return s.ID == created.ID })
			if present != tc.killFail {
				t.Errorf("unlabelled session %s present %v; want present only when its kill failed", created.ID, present)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: r.Name, SessionID: created.ID, Ended: !tc.killFail,
				Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded, Launch: apitest.LaunchReuse},
			}), c.Token, e.storeID, tmuxfix.LabelValue(c.Token, created.ID, r.ID, e.storeID))
			if cols.State != store.StateEnded || len(restored) != 1 {
				t.Errorf("state %v, %s %v; want the restore run once", cols.State, rutRestored, restored)
			}
		})
	}
}

// rlFakeServer gives r's socket in test/fake-tmux the row's recorded server
// holding one unlabelled bystander session, as seedReusable leaves the
// Recorder.
func (e *killEnv) rlFakeServer(t *testing.T, r reuseRow) {
	t.Helper()
	srv, _ := e.rec.Server(r.Socket)
	(faketmuxfix.Tables{}).Write(t, r.Socket, faketmuxfix.Table{Server: &faketmuxfix.Server{PID: srv.PID, Start: srv.Start},
		Sessions: []faketmuxfix.Session{{ID: "$0", Created: srv.Start, Name: "bystander",
			Panes: []faketmuxfix.Pane{{ID: "%0", PID: e.newPID()}}}}})
}

// rlFakeTmuxClient is a Client on e's store driving test/fake-tmux, r's
// socket holding its server (rlFakeServer), with e's clock and process fake;
// it returns the fake's argv log.
func (e *killEnv) rlFakeTmuxClient(t *testing.T, r reuseRow) (*api.Client, string) {
	t.Helper()
	e.rlFakeServer(t, r)
	logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
	t.Setenv(faketmuxfix.EnvLog, logPath)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxCommand: faketmuxfix.Binary(t),
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, e.clock.Now)
	api.SetProcCheckerForTest(c, e.pc)
	return c, logPath
}

// rlSocketArgvs returns the logged invocations that name a socket, in order.
func rlSocketArgvs(t *testing.T, logPath string) [][]string {
	t.Helper()
	var out [][]string
	for _, a := range fakeTmuxArgvs(t, logPath) {
		if len(a) > 2 && a[1] == "-S" {
			out = append(out, a)
		}
	}
	return out
}

// rlEnvEntries counts argv's elements that start with prefix.
func rlEnvEntries(argv []string, prefix string) int {
	n := 0
	for _, a := range argv {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

// TestSpawnReuseCreateArgv: after the lookup on -S <socket>, one create there chains both labels
// (a # in the id doubled); a $ name (as a \ one) gets no chain and one label by id; the id's env entry appears once.
func TestSpawnReuseCreateArgv(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv.
	cases := []struct{ name, id, session string }{
		{"plain name", "", ""},
		{"dollar name", "", `a$b`},
		{"# in the id", "reuse#x##" + uuid.NewString()[:8], ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
			r := e.seedReusable(t, agentGone, reuseRowSpec{ID: tc.id, Age: time.Hour, Bare: true})
			name := tc.session
			if name == "" {
				name = "argv-" + uuid.NewString()[:8]
			}
			c, logPath := e.rlFakeTmuxClient(t, r)
			if _, err := c.Spawn(reuseParams(t, r, reuseRequest{Name: name})); err != nil {
				t.Fatalf("reuse: %v", err)
			}
			tok, _ := e.columns(t, r.ID).LaunchToken.(string)
			if !spawnTokenRE.MatchString(tok) || tok == r.Token {
				t.Fatalf("launch_token = %q; want a new 16-hex token (old %q)", tok, r.Token)
			}

			argvs, byID := rlSocketArgvs(t, logPath), tmux.NeedsLabelByID(name)
			if want := map[bool]int{false: 2, true: 3}[byID]; len(argvs) != want {
				t.Fatalf("socket invocations = %q; want %d", argvs, want)
			}
			if !isLookupOn(argvs[0], r.Socket) {
				t.Errorf("first socket invocation %q; want the lookup on -S %s", argvs[0], r.Socket)
			}
			create, target := argvs[1], "="+name+":"
			if !containsRun(create, []string{"-u", "-S", r.Socket, "new-session"}) || !containsRun(create, []string{"-s", name}) {
				t.Errorf("create argv %q; want new-session -s %q on -u -S %s", create, name, r.Socket)
			}
			if dash := slices.Index(create, "--"); dash < 0 || len(create)-dash-1 < 2 || create[dash+1] != "claude" {
				t.Errorf("create argv %q: want claude and at least one more element after -- (SR-3.8)", create)
			}
			if n := rlEnvEntries(create, "AGENT_DIRECTOR_INSTANCE_ID="+r.ID); n != 1 || rlEnvEntries(create, "AGENT_DIRECTOR_INSTANCE_ID=") != 1 ||
				strings.Contains(strings.Join(create, "\n"), "AGENT_DIRECTOR_LAUNCH_STARTED_AT") {
				t.Errorf("create argv %q: want AGENT_DIRECTOR_INSTANCE_ID=%s exactly once and no launch start", create, r.ID)
			}
			if !byID {
				for _, seq := range [][]string{
					{";", "set-option", "-F", "-t", target, "@ad_owner", tmuxfix.ChainLabelValue(tok, r.ID, e.storeID)},
					{";", "set-option", "-p", "-F", "-t", target, "@ad_pane", tmuxfix.ChainPaneLabelValue(tok)},
				} {
					if !containsRun(create, seq) {
						t.Errorf("create argv %q lacks the chained %q", create, seq)
					}
				}
				return
			}
			if slices.Contains(create, "@ad_owner") || slices.Contains(create, "@ad_pane") {
				t.Errorf("create argv %q chains a label for %q; want none", create, name)
			}
			label, sid := argvs[2], ""
			if i := slices.Index(label, "-t"); i > 0 && i+1 < len(label) {
				sid = label[i+1]
			}
			var paneID string
			for _, s := range (faketmuxfix.Tables{}).Read(t, r.Socket).Sessions {
				if s.ID == sid && len(s.Panes) > 0 {
					paneID = s.Panes[0].ID
				}
			}
			for _, seq := range [][]string{
				{"-S", r.Socket, "set-option", "-t", sid, "@ad_owner", tmuxfix.LabelValue(tok, sid, r.ID, e.storeID)},
				{"set-option", "-p", "-t", paneID, "@ad_pane", tmuxfix.PaneLabelValue(tok, paneID)},
			} {
				if !strings.HasPrefix(sid, "$") || paneID == "" || !containsRun(label, seq) {
					t.Errorf("label-by-id argv %q lacks %q", label, seq)
				}
			}
		})
	}
}

// TestSpawnReuseCreateMatchesPlainSpawn: reuse's create argv (settings, environment and command) equals a
// plain spawn's for the same request, but for the id and token, with AGENT_DIRECTOR_INSTANCE_ID once.
func TestSpawnReuseCreateMatchesPlainSpawn(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv.
	e := newKillEnv(t)
	e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
	c, logPath := e.rlFakeTmuxClient(t, r)
	q := reuseRequest{Name: "parity-" + uuid.NewString()[:8], Args: []string{"--model", "sonnet"},
		Env: map[string]string{"PARITY_VAR": "on"}, Labels: map[string]string{"team": "parity"}}
	plain := reuseParams(t, r, q)
	plain.ReuseFinished, plain.ClaudeInstanceID = false, "plain-"+uuid.NewString()[:8]
	if _, err := c.Spawn(plain); err != nil {
		t.Fatalf("plain spawn: %v", err)
	}
	e.rlFakeServer(t, r) // the plain spawn's session gone
	if _, err := c.Spawn(reuseParams(t, r, q)); err != nil {
		t.Fatalf("reuse: %v", err)
	}

	var creates [][]string
	for _, a := range rlSocketArgvs(t, logPath) {
		if slices.Contains(a, "new-session") {
			creates = append(creates, a)
		}
	}
	if len(creates) != 2 {
		t.Fatalf("creates = %q; want the plain spawn's, then the reuse's", creates)
	}
	// normalised is argv with id's and its launch token's text replaced.
	normalised := func(argv []string, id string) []string {
		tok, _ := e.columns(t, id).LaunchToken.(string)
		out := make([]string, len(argv))
		for i, a := range argv {
			out[i] = strings.NewReplacer(id, "<id>", tok, "<token>").Replace(a)
		}
		return out
	}
	if got, want := normalised(creates[1], r.ID), normalised(creates[0], plain.ClaudeInstanceID); !slices.Equal(got, want) {
		t.Errorf("reuse's create argv\n  %q\nwant the plain spawn's\n  %q", got, want)
	}
}

// TestSpawnReuseCeilingPaths (SR-13.2; AC-REUSE-16): in virtual time with no
// pipe-close wait, path (i) (lookup, a create whose chained label fails,
// then the label and the kill by id failing) charges Q + C + 2A, 10.5 s at
// the defaults; path (ii) (lookup, create answering "duplicate session",
// re-lookup) charges 2Q + C, 8 s, the longer once Q is raised above 2A.
func TestSpawnReuseCeilingPaths(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	c := config.Default().Tmux.EffectiveCreateTimeout()
	if got := q + c + 2*a; got != 10500*time.Millisecond || got > boundCap {
		t.Fatalf("Q + C + 2A at the defaults = %v; the Epic says 10.5 s, within %v", got, boundCap)
	}
	if got := 2*q + c; got != 8*time.Second || got > boundCap {
		t.Fatalf("2Q + C at the defaults = %v; the Epic says 8 s, within %v", got, boundCap)
	}
	raised := config.Tmux{QueryTimeoutMs: 2*config.DefaultActionTimeoutMs + config.DefaultQueryTimeoutMs}
	rq, written := raised.EffectiveQueryTimeout(), []apitest.TmuxSetting{
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, raised.QueryTimeoutMs)}
	if rq <= 2*a || 2*rq+c <= rq+c+2*a {
		t.Fatalf("raised Q = %v; want above 2A = %v, making path (ii) the longer", rq, 2*a)
	}
	cases := []struct {
		name   string
		q      time.Duration
		config []apitest.TmuxSetting // also written for the Client
		held   bool                  // path (ii); false: path (i)
	}{
		{"default Q/path i", q, nil, false},
		{"default Q/path ii", q, nil, true},
		{"Q above 2A/path i", rq, written, false},
		{"Q above 2A/path ii", rq, written, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: tc.q})
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), Held: tc.held})
			want, wantErr := tc.q+c+2*a, api.ErrTmuxSessionCreate
			calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession}
			if tc.held {
				want, wantErr, calls = 2*tc.q+c, api.ErrTmuxSessionConflict, []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
				e.arrangeHeld(t, r.resumeRow, heldSpec{Holder: holderForeign})
			} else {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
			}
			start := e.clock.Now()

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}), tc.config...)

			if elapsed := e.clock.Now().Sub(start); elapsed != want {
				t.Errorf("virtual time = %v; want %v", elapsed, want)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v; want %v", err, wantErr)
			}
			e.assertKillCalls(t, calls...)
			if st := e.columns(t, r.ID).State; st != store.StateEnded {
				t.Errorf("state = %v; want ended again (restored)", st)
			}
		})
	}
}
