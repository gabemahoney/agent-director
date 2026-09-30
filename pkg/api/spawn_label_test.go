package api_test

// spawn_label_test.go covers plain spawn's failed label step (SR-3.5, SR-1.4,
// SR-9.4; AC-LKP-17): a failed chained label is relabelled by id; when that
// fails too, the session is killed by id and spawn returns ErrTmuxSessionCreate.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// labelServerPID is the scripted tmux server's pid in the label-step tests.
const labelServerPID = 4242

var labelTokenRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// labelStepEnv is a spawnEnv on an isolated socket whose server already holds
// a bystander session, with a fixed clock and a scripted start-time reader.
type labelStepEnv struct {
	spawnEnv
	socket    string
	bystander tmuxfix.SeedSession
	clock     *tmuxfix.Clock
	pc        *procfix.Checker
	storeID   string
}

// newLabelStepEnv builds a labelStepEnv; the bystander makes the new session
// $1 with pane %1, so targets cannot match by default.
func newLabelStepEnv(t *testing.T) labelStepEnv {
	t.Helper()
	env := newSpawnEnv(t)
	if err := os.WriteFile(filepath.Join(env.home, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	e := labelStepEnv{spawnEnv: env, pc: procfix.New(),
		socket: filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default"),
		clock:  tmuxfix.NewClock(time.Date(2026, 9, 29, 12, 0, 0, 456_000_000, time.UTC))}
	e.rec.StartServer(e.socket, tmuxfix.Server{PID: labelServerPID})
	e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: "bystander"})
	e.bystander = e.rec.Sessions(e.socket)[0]
	e.pc.Set(labelServerPID, procfix.Alive(procstarttimefix.LinuxProcStarttime))
	api.SetClockForTest(e.c, e.clock.Now)
	api.SetProcCheckerForTest(e.c, e.pc)
	if e.storeID, err = apitest.ReadStoreID(e.dbPath); err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return e
}

// sessionByID returns the session id on e's socket, or false when it is gone.
func (e labelStepEnv) sessionByID(id string) (tmuxfix.SeedSession, bool) {
	for _, s := range e.rec.Sessions(e.socket) {
		if s.ID == id {
			return s, true
		}
	}
	return tmuxfix.SeedSession{}, false
}

// otherTmuxSentinels are the tmux sentinels a label-step error must not match.
var otherTmuxSentinels = []error{
	tmux.ErrTmuxNotAvailable, tmux.ErrTmuxUnresponsive, tmux.ErrTmuxSessionConflict,
	tmux.ErrTmuxKillFailed, tmux.ErrTmuxListPanesFailed, tmux.ErrTmuxSendKeys, tmux.ErrTmuxCaptureFailed,
}

// TestSpawnLabelStepFailure: a failed chained label is relabelled once by the
// reply's session and pane ids; a failed relabel kills the session by id.
func TestSpawnLabelStepFailure(t *testing.T) {
	create, label, kill := tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	cases := []struct {
		name      string
		labelFail bool
		killFail  bool
		wantCalls []tmux.Call
	}{
		{"relabel by id succeeds", false, false, []tmux.Call{create, label}},
		{"relabel fails, kill by id succeeds", true, false, []tmux.Call{create, label, kill}},
		{"relabel fails, kill by id fails", true, true, []tmux.Call{create, label, kill}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLabelStepEnv(t)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create)
			if tc.labelFail {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, label)
			}
			if tc.killFail {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, kill)
			}
			var created tmuxfix.SeedSession
			e.rec.AfterCall(create, func(tmuxfix.SocketCall, error) {
				created = e.rec.Sessions(e.socket)[1]
				e.pc.Set(created.Panes[0].PID, procfix.Alive(procstarttimefix.DarwinProcStarttime))
			})
			name := "lbl-" + uuid.NewString()[:8]
			wantStart := e.clock.Now().UnixMilli()

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), TmuxSessionName: name, TmuxSessionNameSupplied: true})

			calls := e.rec.SocketCalls()
			kinds := make([]tmux.Call, len(calls))
			for i, c := range calls {
				kinds[i] = c.Call
				if c.Socket != e.socket {
					t.Errorf("call %d (%s) socket = %q; want %q", i, c.Call, c.Socket, e.socket)
				}
			}
			if !reflect.DeepEqual(kinds, tc.wantCalls) {
				t.Fatalf("tmux calls = %v; want %v", kinds, tc.wantCalls)
			}
			c0, l := calls[0], calls[1]
			id, tok := c0.InstanceID, c0.Token
			pane := created.Panes[0]
			if created.ID != "$1" || created.LabelSet || pane.AdPane != "" {
				t.Errorf("session after the create = %+v; want $1 with no label", created)
			}
			if l.Target != created.ID || l.PaneID != pane.ID {
				t.Errorf("label by id targets {%q %q}; want the reply's {%q %q}", l.Target, l.PaneID, created.ID, pane.ID)
			}
			if !labelTokenRE.MatchString(tok) || c0.StoreID != e.storeID || l.Token != tok || l.InstanceID != id || l.StoreID != e.storeID {
				t.Errorf("create {%q %q %q}, label by id {%q %q %q}; want a 16-hex token, %q and store %q on both",
					c0.Token, c0.InstanceID, c0.StoreID, l.Token, l.InstanceID, l.StoreID, id, e.storeID)
			}
			if len(calls) == 3 && calls[2].Target != created.ID {
				t.Errorf("kill targets %q; want the session id %q", calls[2].Target, created.ID)
			}
			if _, ok := e.sessionByID(e.bystander.ID); !ok {
				t.Errorf("bystander session %s is gone; want it untouched", e.bystander.ID)
			}

			row, rerr := apitest.ReadSpawnColumns(e.dbPath, id)
			if rerr != nil {
				t.Fatalf("ReadSpawnColumns(%q): %v", id, rerr)
			}
			if row.State != store.StatePending || row.LaunchStartedAt != wantStart || row.LaunchToken != tok || row.TmuxSocket != e.socket {
				t.Errorf("row = {state %v, launch_started_at %v, token %v, socket %v}; want {%s, %d, %s, %s}",
					row.State, row.LaunchStartedAt, row.LaunchToken, row.TmuxSocket, store.StatePending, wantStart, tok, e.socket)
			}

			if !tc.labelFail {
				assertLabelStepRelabelled(t, e, err, row, created, tok, id)
				return
			}
			assertLabelStepUnlabelled(t, e, err, row, created, name, !tc.killFail)
			apitest.AssertDescription(t, err.Error(), apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: name, SessionID: created.ID, Ended: !tc.killFail, PlainSpawn: true,
			}), tok, e.storeID, tmuxfix.LabelValue(tok, created.ID, id, e.storeID))
		})
	}
}

// assertLabelStepRelabelled checks a relabelled spawn: success, the five-field
// and pane labels in place, and the launch identity written.
func assertLabelStepRelabelled(t *testing.T, e labelStepEnv, err error, row apitest.SpawnColumns, created tmuxfix.SeedSession, tok, id string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	s, ok := e.sessionByID(created.ID)
	if !ok || s.Label != tmuxfix.Valid(tok, id, e.storeID) || s.Panes[0].AdPane != tok {
		t.Errorf("session %s = %+v (present %v); want label ad1 %s %s %s %s and pane label %s",
			created.ID, s, ok, tok, created.ID, id, e.storeID, tok)
	}
	pane := created.Panes[0]
	if row.RowVersion != int64(1) || row.TmuxServerPID != int64(labelServerPID) ||
		row.TmuxServerStarttime != procstarttimefix.LinuxProcStarttime || row.PaneID != pane.ID ||
		row.PanePID != int64(pane.PID) || row.PaneStarttime != procstarttimefix.DarwinProcStarttime {
		t.Errorf("row identity = {row_version %v, server %v %v, pane %v %v %v}; want {1, %d %s, %s %d %s}",
			row.RowVersion, row.TmuxServerPID, row.TmuxServerStarttime, row.PaneID, row.PanePID, row.PaneStarttime,
			labelServerPID, procstarttimefix.LinuxProcStarttime, pane.ID, pane.PID, procstarttimefix.DarwinProcStarttime)
	}
}

// assertLabelStepUnlabelled checks a spawn whose relabel failed:
// ErrTmuxSessionCreate only, the session gone when ended, and no identity.
func assertLabelStepUnlabelled(t *testing.T, e labelStepEnv, err error, row apitest.SpawnColumns, created tmuxfix.SeedSession, name string, ended bool) {
	t.Helper()
	if !errors.Is(err, api.ErrTmuxSessionCreate) {
		t.Fatalf("Spawn err = %v; want ErrTmuxSessionCreate", err)
	}
	for _, other := range otherTmuxSentinels {
		if errors.Is(err, other) {
			t.Errorf("err %v also matches %v", err, other)
		}
	}
	s, present := e.sessionByID(created.ID)
	if present == ended || (present && s.LabelSet) {
		t.Errorf("session %s (%q) present %v, labelled %v; want present %v and unlabelled", created.ID, name, present, s.LabelSet, !ended)
	}
	identity := []any{row.TmuxServerPID, row.TmuxServerStarted, row.TmuxServerStarttime, row.PaneID, row.PanePID, row.PaneStarttime}
	if row.RowVersion != int64(0) || !reflect.DeepEqual(identity, make([]any, len(identity))) {
		t.Errorf("row_version %v, identity %v; want 0 and all NULL", row.RowVersion, identity)
	}
}
