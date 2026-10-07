package api_test

// spawn_reuse_launch_test.go covers reuse's launch after its reset (SR-3.5,
// SR-3.6, SR-10.4, SR-13.2; AC-LKP-17's reuse half, AC-REUSE-08): the one
// labelled create on the row's socket, the identity write and its guard, a
// lost reply adopted later, the launch timeout, the label step and a run from
// inside another session. The argv and parity cases are
// spawn_reuse_launch_argv_test.go; the fixture is spawn_reuse_fixture_test.go.

import (
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

// rlRecorded is the identity the write records for session s on socket: its
// server's pid, start and process start time, and its first pane's.
func rlRecorded(e *killEnv, socket string, s tmuxfix.SeedSession) []any {
	srv, _ := e.rec.Server(socket)
	return []any{int64(srv.PID), srv.Start, srv.ProcStart, s.Panes[0].ID, int64(s.Panes[0].PID), apitest.DarwinProcStarttime}
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

// TestSpawnReuseCreatesLabelledSessionOnRowSocket: the lookup and one create on the row's recorded
// socket (not the resolved one), the new name, token, id and store id; then the identity write.
func TestSpawnReuseCreatesLabelledSessionOnRowSocket(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, state string
		newName     bool
	}{
		{"ended row, recorded name", store.StateEnded, false},
		{"missing row, recorded name", store.StateMissing, false},
		{"ended row, new name", store.StateEnded, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			sock := filepath.Join(t.TempDir(), "row.sock")
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: tc.state, Age: time.Hour,
				Opts: []apitest.SpawnOption{apitest.WithTmuxSocket(sock)}})
			q := reuseRequest{}
			if tc.newName {
				q.Name = "new-" + uuid.NewString()[:8]
			}
			name := q.Name
			if name == "" {
				name = r.Name
			}
			rlNewPanesAlive(e)
			before, start := e.columns(t, r.ID), e.clock.Now().Add(boundQ)

			res, logs, err := e.reuse(t, reuseParams(t, r, q))
			if err != nil || res.ClaudeInstanceID != r.ID {
				t.Fatalf("reuse = %+v, %v; want success for %s", res, err, r.ID)
			}
			c := rlOneCreate(t, e, sock, tmux.CallLookup, tmux.CallCreate)
			if sock == e.defaultSocket || c.Target != name || c.Cwd != r.CWD || c.InstanceID != r.ID || c.StoreID != e.storeID {
				t.Errorf("create {name %q, cwd %q, id %q, store %q}; want {%q %q %q %q} on the row's socket",
					c.Target, c.Cwd, c.InstanceID, c.StoreID, name, r.CWD, r.ID, e.storeID)
			}
			if _, set := c.Envs["AGENT_DIRECTOR_LAUNCH_STARTED_AT"]; set || c.Envs["AGENT_DIRECTOR_INSTANCE_ID"] != r.ID {
				t.Errorf("create env %v; want AGENT_DIRECTOR_INSTANCE_ID=%s and no launch start", c.Envs, r.ID)
			}
			if len(c.Command) < 2 || c.Command[0] != "claude" {
				t.Errorf("create command %q; want claude and at least one more element (SR-3.8)", c.Command)
			}
			s := rplSessionNamed(t, e.rec, sock, storedFormOf(name))
			if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.Panes[0].AdPane != c.Token {
				t.Errorf("session label %+v, pane label %q; want ad1 %s <$N> %s %s, pane %s", s.Label, s.Panes[0].AdPane,
					c.Token, r.ID, e.storeID, c.Token)
			}
			cols := e.columns(t, r.ID)
			rlAssertNewLife(t, r, cols, c.Token, start)
			if cols.TmuxSocket != sock || cols.TmuxSessionName != name || cols.RowVersion != before.RowVersion.(int64)+2 {
				t.Errorf("row {socket %v, name %v, row_version %v}; want {%s %s %d}", cols.TmuxSocket, cols.TmuxSessionName,
					cols.RowVersion, sock, name, before.RowVersion.(int64)+2)
			}
			if got, want := identityCols(cols), rlRecorded(e, sock, s); !reflect.DeepEqual(got, want) {
				t.Errorf("identity columns = %#v; want %#v", got, want)
			}
			if logs != "" {
				t.Errorf("client log = %q; want nothing", logs)
			}
		})
	}
}

// TestSpawnReuseIdentityWrite: the identity write records the reply's server and pane only while
// the row is still this launch's; another versioned write first leaves it writing nothing, a hook first is ignored.
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
		// SR-22.9: the reset row records no pane until the identity write, so
		// the store's gated hook writes are ignored and the write applies.
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
			start := e.clock.Now().Add(boundQ)

			if _, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{})); err != nil || strings.Contains(logs, "WARN") {
				t.Fatalf("reuse = %v (log %q); want success and no WARN", err, logs)
			}
			c := rlOneCreate(t, e, r.Socket, tmux.CallLookup, tmux.CallCreate)
			s := rplSessionNamed(t, e.rec, r.Socket, r.Name)
			if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) {
				t.Errorf("session label %+v; want ad1 %s <$N> %s <store id>", s.Label, c.Token, r.ID)
			}
			cols := e.columns(t, r.ID)
			rlAssertNewLife(t, r, cols, c.Token, start)
			want := noIdentity
			if tc.recorded {
				want = rlRecorded(e, r.Socket, s)
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

// TestSpawnReuseLaunchTimeoutAndLostReply: a timed-out or non-zero unparseable create is ErrTmuxUnresponsive
// ("the row was reset") with no restore; exit 0 unparseable succeeds; each leaves the session labelled for send-keys to adopt.
func TestSpawnReuseLaunchTimeoutAndLostReply(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		script       tmuxfix.Script
		timeout      bool
		unrecognised bool
	}{
		{"create times out after creating", tmuxfix.Script{Failure: tmux.FailTimeout, Applied: true}, true, false},
		{"non-zero exit, unparseable reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1,
			HadStdout: true, Applied: true}, true, true},
		{"exit 0, unparseable reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0,
			HadStdout: true, Applied: true}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			rlNewPanesAlive(e)
			e.rec.Script(r.Socket, tc.script, tmux.CallCreate)
			before, start := e.columns(t, r.ID), e.clock.Now().Add(boundQ)

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
			c := rlOneCreate(t, e, r.Socket, tmux.CallLookup, tmux.CallCreate)
			if tc.timeout {
				assertLaunchSentinel(t, err, tmux.ErrTmuxUnresponsive)
				apitest.AssertDescription(t, err.Error(), apitest.DescLaunchTimeout(apitest.LaunchTimeout{
					InstanceID: r.ID, Timeout: boundC, Unrecognised: tc.unrecognised, RowReset: true,
				}), c.Token, e.storeID, r.Token)
			} else if err != nil {
				t.Fatalf("reuse: %v; want success with no identity", err)
			}
			cols := e.columns(t, r.ID)
			rlAssertNewLife(t, r, cols, c.Token, start)
			if !reflect.DeepEqual(identityCols(cols), noIdentity) || cols.RowVersion != before.RowVersion.(int64)+1 {
				t.Errorf("identity %#v, row_version %v; want none, the reset's %d", identityCols(cols), cols.RowVersion,
					before.RowVersion.(int64)+1)
			}
			if got := pendTrail(t, "ad.spawn.reuse_restored", r.ID); len(got) != 0 {
				t.Errorf("ad.spawn.reuse_restored = %v; want none (no restore)", got)
			}
			s := rplSessionNamed(t, e.rec, r.Socket, r.Name)
			if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.Panes[0].AdPane != c.Token {
				t.Fatalf("session %+v; want it labelled for %s with token %s", s, r.ID, c.Token)
			}

			// The next lookup, a send-keys allowed on the pending row, adopts the identity.
			e.rec.Reset()
			if _, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hello", AllowPending: true}); err != nil {
				t.Fatalf("send-keys: %v", err)
			}
			e.assertDelivered(t, r.Socket, s.Panes[0].ID, "hello")
			if got := e.adoption(t, r.ID); got.PaneID != s.Panes[0].ID || got.ServerPID == nil {
				t.Errorf("adoption %+v; want the server identity and pane %s", got, s.Panes[0].ID)
			}
		})
	}
}

// TestSpawnReuseLabelStep: a failed chained label is relabelled by id and the reuse succeeds; when the
// relabel fails too, the session is killed by id, ErrTmuxSessionCreate says so, and the restore runs.
func TestSpawnReuseLabelStep(t *testing.T) {
	t.Parallel()
	lookup, create, label, kill := tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	cases := []struct {
		name                string
		labelFail, killFail bool
		want                []tmux.Call
	}{
		{"relabel by id succeeds", false, false, []tmux.Call{lookup, create, label}},
		{"relabel fails, kill by id succeeds", true, false, []tmux.Call{lookup, create, label, kill}},
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
			restored := pendTrail(t, "ad.spawn.reuse_restored", r.ID)
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
			if _, present := e.rlSession(r.Socket, created.ID); present != tc.killFail {
				t.Errorf("unlabelled session %s present %v; want present only when its kill failed", created.ID, present)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: r.Name, SessionID: created.ID, Ended: !tc.killFail,
				Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded, Launch: apitest.LaunchReuse},
			}), c.Token, e.storeID, tmuxfix.LabelValue(c.Token, created.ID, r.ID, e.storeID))
			// The restore's content is spawn_reuse_restore_test.go's; here only that it ran.
			if cols.State != store.StateEnded || len(restored) != 1 {
				t.Errorf("state %v, ad.spawn.reuse_restored %v; want the restore run once", cols.State, restored)
			}
		})
	}
}

// rlSession returns the session id on socket, and whether it is there.
func (e *killEnv) rlSession(socket, id string) (tmuxfix.SeedSession, bool) {
	for _, s := range e.rec.Sessions(socket) {
		if s.ID == id {
			return s, true
		}
	}
	return tmuxfix.SeedSession{}, false
}

// TestSpawnReuseFromInsideAnotherSession: with TMUX naming another session's pane, reuse still
// creates on the row's socket, labels its new session, and leaves the other session's labels as they were.
func TestSpawnReuseFromInsideAnotherSession(t *testing.T) {
	// Serial: it sets TMUX, TMUX_PANE with t.Setenv.
	for name, onRowServer := range map[string]bool{"TMUX names the row server": true, "TMUX names another server": false} {
		t.Run(name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			otherTok := newToken()
			other := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "other-" + uuid.NewString()[:8],
				Label: tmuxfix.Valid(otherTok, "other-"+uuid.NewString()[:8], e.storeID), Panes: []tmuxfix.SeedPane{{AdPane: otherTok}}})
			tmuxSocket := filepath.Join(t.TempDir(), "elsewhere")
			if onRowServer {
				tmuxSocket = r.Socket
			}
			srv, _ := e.rec.Server(r.Socket)
			t.Setenv("TMUX", tmuxSocket+","+strconv.Itoa(srv.PID)+",0")
			t.Setenv("TMUX_PANE", other.Panes[0].ID)

			if _, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{})); err != nil {
				t.Fatalf("reuse: %v", err)
			}
			c := rlOneCreate(t, e, r.Socket, tmux.CallLookup, tmux.CallCreate)
			if s := rplSessionNamed(t, e.rec, r.Socket, r.Name); s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.ID == other.ID {
				t.Errorf("new session %+v; want its own, labelled ad1 %s <$N> %s <store id>", s, c.Token, r.ID)
			}
			if got, ok := e.rlSession(r.Socket, other.ID); !ok || !reflect.DeepEqual(got, other) {
				t.Errorf("the other session = %+v (present %v); want unchanged %+v", got, ok, other)
			}
		})
	}
}
