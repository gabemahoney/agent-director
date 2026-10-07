package api_test

// spawn_launch_bound_test.go covers plain spawn's bounded launch (SR-3.5,
// SR-9.4, SR-13.2; AC-SPN-05, AC-SPN-06, AC-LKP-17's lost-reply half): a lost
// or unanswered create keeps its chained label and records no identity; a
// hung create returns ErrTmuxUnresponsive with the row pending; and the
// longest create path costs C + 2A (minted id) or Q + C + 2A (caller-supplied
// id) in virtual time. No test sleeps.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// The default call timeouts, from the internal/config constants (SR-13.1).
var (
	boundQ = time.Duration(config.DefaultQueryTimeoutMs) * time.Millisecond
	boundA = time.Duration(config.DefaultActionTimeoutMs) * time.Millisecond
	boundC = time.Duration(config.DefaultCreateTimeoutMs) * time.Millisecond
)

// boundCap is SR-13.2's bound on a single-row verb's time waiting on tmux at
// the defaults (AC-SPN-06).
const boundCap = 15 * time.Second

// boundHex16 is the launch token's form: 16 lowercase hex characters (SR-3.5).
var boundHex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

// boundEnv is a spawnEnv with a per-test tmux socket, the shared virtual
// clock bound to both the Client and the Recorder, and the store's id.
type boundEnv struct {
	spawnEnv
	clock   *tmuxfix.Clock
	start   time.Time
	socket  string
	storeID string
}

// newBoundEnv builds a boundEnv: TMUX_TMPDIR is a fresh directory with TMUX
// unset, and every Recorder call is charged its default timeout.
func newBoundEnv(t *testing.T) boundEnv {
	t.Helper()
	env := newSpawnEnv(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	start := time.Date(2026, 9, 29, 12, 0, 0, 123_000_000, time.UTC)
	clock := tmuxfix.NewClock(start)
	env.rec.WithVirtualTime(clock, tmux.Timeouts{})
	api.SetClockForTest(env.c, clock.Now)
	api.SetProcCheckerForTest(env.c, procfix.New())
	storeID, err := apitest.ReadStoreID(env.dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return boundEnv{spawnEnv: env, clock: clock, start: start, storeID: storeID,
		socket: filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")}
}

// spawn runs Spawn in its own goroutine, so an after-call hook may end it
// with runtime.Goexit; returned is false when it was ended that way.
func (e boundEnv) spawn(p api.SpawnParams) (res api.SpawnResult, err error, returned bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err = e.c.Spawn(p)
		returned = true
	}()
	<-done
	return res, err, returned
}

// callKinds returns the kinds of every recorded socket-taking call, in order.
func callKinds(rec *tmuxfix.Recorder) []tmux.Call {
	var out []tmux.Call
	for _, c := range rec.SocketCalls() {
		out = append(out, c.Call)
	}
	return out
}

// assertOnlyCatalogued fails unless err matches exactly the catalogued want.
func assertOnlyCatalogued(t *testing.T, err error, want string) {
	t.Helper()
	var got []string
	for _, e := range errnames.Catalog {
		if errors.Is(err, e.Err) {
			got = append(got, e.Name)
		}
	}
	if !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("err %v matches catalogued %q; want only %s", err, got, want)
	}
}

// assertPendingNoIdentity checks id's row is pending at version 0 with its
// launch start, token and socket, and no server or pane identity; it returns
// the token.
func assertPendingNoIdentity(t *testing.T, e boundEnv, id string, launchStart time.Time) string {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	if cols.State != store.StatePending || cols.RowVersion != int64(0) {
		t.Errorf("row state, row_version = %v, %v; want pending, 0", cols.State, cols.RowVersion)
	}
	if cols.LaunchStartedAt != launchStart.UnixMilli() {
		t.Errorf("launch_started_at = %v; want %d (the clock at insert)", cols.LaunchStartedAt, launchStart.UnixMilli())
	}
	token, _ := cols.LaunchToken.(string)
	if !boundHex16.MatchString(token) {
		t.Errorf("launch_token = %v; want 16 lowercase hex characters", cols.LaunchToken)
	}
	if cols.TmuxSocket != e.socket {
		t.Errorf("tmux_socket = %v; want %q", cols.TmuxSocket, e.socket)
	}
	identity := map[string]any{
		"tmux_server_pid": cols.TmuxServerPID, "tmux_server_started": cols.TmuxServerStarted,
		"tmux_server_starttime": cols.TmuxServerStarttime, "pane_id": cols.PaneID,
		"pane_pid": cols.PanePID, "pane_starttime": cols.PaneStarttime,
	}
	for col, v := range identity {
		if v != nil {
			t.Errorf("%s = %v; want NULL (no identity recorded)", col, v)
		}
	}
	return token
}

// TestSpawnLaunchBoundLostReplyKeepsLabel: a create that times out, gives an
// unparseable reply, or whose launch stops before the identity write leaves
// its session labelled and records no identity (SR-3.5, SR-9.4; AC-LKP-17).
func TestSpawnLaunchBoundLostReplyKeepsLabel(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name         string
		script       *tmuxfix.Script // the create's scripted result; nil answers from the table
		stop         bool            // an after-call hook on the create ends Spawn
		unresponsive bool            // ErrTmuxUnresponsive; else Spawn succeeds (or never returns)
		unrecognised bool            // the description's unrecognised-reply form
	}{
		{name: "create times out", script: &tmuxfix.Script{Failure: tmux.FailTimeout, Applied: true},
			unresponsive: true},
		{name: "non-zero exit with unparseable reply", script: &tmuxfix.Script{Failure: tmux.FailUnrecognized,
			ExitStatus: 1, HadStdout: true, Applied: true}, unresponsive: true, unrecognised: true},
		{name: "exit 0 with unparseable reply", script: &tmuxfix.Script{Failure: tmux.FailUnrecognized,
			ExitStatus: 0, HadStdout: true, Applied: true}},
		{name: "stop before the identity write", stop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBoundEnv(t)
			id := "bound-lost-" + uuid.NewString()[:8]
			if tc.script != nil {
				e.rec.Script(tmuxfix.AnySocket, *tc.script, tmux.CallCreate)
			}
			if tc.stop {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { runtime.Goexit() })
			}
			res, err, returned := e.spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})

			switch {
			case tc.stop:
				if returned {
					t.Fatalf("Spawn returned (%+v, %v); want it ended by the after-call hook", res, err)
				}
			case tc.unresponsive:
				if !errors.Is(err, api.ErrTmuxUnresponsive) {
					t.Fatalf("Spawn err = %v; want ErrTmuxUnresponsive", err)
				}
				assertOnlyCatalogued(t, err, "ErrTmuxUnresponsive")
			default:
				if err != nil || res.ClaudeInstanceID != id {
					t.Fatalf("Spawn = (%+v, %v); want success for %s", res, err, id)
				}
			}

			// The caller-supplied id makes its one scan lookup, then the one
			// create: no label by id and no kill.
			if got, want := callKinds(e.rec), []tmux.Call{tmux.CallLookup, tmux.CallCreate}; !reflect.DeepEqual(got, want) {
				t.Fatalf("tmux calls = %q; want %q", got, want)
			}
			token := assertPendingNoIdentity(t, e, id, e.start.Add(boundQ))
			if create := e.rec.SocketCallsOf(tmux.CallCreate)[0]; create.Socket != e.socket || create.Token != token {
				t.Errorf("create socket, token = %q, %q; want %q, %q", create.Socket, create.Token, e.socket, token)
			}

			sessions := e.rec.Sessions(e.socket)
			if len(sessions) != 1 {
				t.Fatalf("sessions on %s = %+v; want exactly one", e.socket, sessions)
			}
			s := sessions[0]
			if want := tmuxfix.Valid(token, id, e.storeID); !reflect.DeepEqual(s.Label, want) {
				t.Errorf("session label = %+v; want %+v (ad1 <token> <$N> <id> <store id>)", s.Label, want)
			}
			if len(s.Panes) != 1 || s.Panes[0].AdPane != token {
				t.Errorf("session panes = %+v; want one pane labelled with the launch token", s.Panes)
			}

			if tc.unresponsive {
				_, desc := errnames.Classify(err)
				apitest.AssertDescription(t, desc, apitest.DescLaunchTimeout(apitest.LaunchTimeout{
					InstanceID: id, Timeout: boundC, Unrecognised: tc.unrecognised, ExplicitSpawnID: true,
				}), token, e.storeID, tmuxfix.LabelValue(token, s.ID, id, e.storeID))
			}
		})
	}
}

// TestSpawnLaunchBoundHungCreate: a create charged its full default create
// timeout returns ErrTmuxUnresponsive, with the row pending and its launch start set.
func TestSpawnLaunchBoundHungCreate(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	e := newBoundEnv(t)
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallCreate)

	_, err, _ := e.spawn(api.SpawnParams{CWD: t.TempDir()})
	if !errors.Is(err, api.ErrTmuxUnresponsive) {
		t.Fatalf("Spawn err = %v; want ErrTmuxUnresponsive", err)
	}
	assertOnlyCatalogued(t, err, "ErrTmuxUnresponsive")
	if got := e.clock.Now().Sub(e.start); got != boundC {
		t.Errorf("virtual time charged = %v; want the default create timeout %v", got, boundC)
	}
	// A minted id makes no lookup: the create is the only call.
	calls := e.rec.SocketCalls()
	if len(calls) != 1 || calls[0].Call != tmux.CallCreate {
		t.Fatalf("tmux calls = %q; want exactly one create", callKinds(e.rec))
	}
	id := calls[0].InstanceID
	token := assertPendingNoIdentity(t, e, id, e.start)
	_, desc := errnames.Classify(err)
	apitest.AssertDescription(t, desc, apitest.DescLaunchTimeout(apitest.LaunchTimeout{InstanceID: id, Timeout: boundC}),
		token, e.storeID)
}

// TestSpawnLaunchBoundCeiling: the longest plain-spawn path (chained label,
// relabel and kill all failing) charges C + 2A, plus Q for a caller-supplied id's scan.
func TestSpawnLaunchBoundCeiling(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	const name = "bound-ceiling"
	cases := []struct {
		name     string
		id       string
		wantCall []tmux.Call
		want     time.Duration
	}{
		{"minted id", "", []tmux.Call{tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession},
			boundC + 2*boundA},
		{"caller-supplied id", "bound-ceiling-" + uuid.NewString()[:8],
			[]tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession},
			boundQ + boundC + 2*boundA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBoundEnv(t)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailLabel}, tmux.CallCreate).
				Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallSetLabel, tmux.CallKillSession)

			_, err, _ := e.spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id,
				TmuxSessionName: name, TmuxSessionNameSupplied: true})
			if !errors.Is(err, api.ErrTmuxSessionCreate) {
				t.Fatalf("Spawn err = %v; want ErrTmuxSessionCreate", err)
			}
			if got := callKinds(e.rec); !reflect.DeepEqual(got, tc.wantCall) {
				t.Fatalf("tmux calls = %q; want %q", got, tc.wantCall)
			}
			got := e.clock.Now().Sub(e.start)
			if got != tc.want {
				t.Errorf("virtual time charged = %v; want %v", got, tc.want)
			}
			if got > boundCap {
				t.Errorf("virtual time charged = %v; want at most %v (SR-13.2)", got, boundCap)
			}

			label := e.rec.SocketCallsOf(tmux.CallSetLabel)[0]
			_, desc := errnames.Classify(err)
			apitest.AssertDescription(t, desc, apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: name, SessionID: label.Target, PlainSpawn: true,
			}), label.Token, e.storeID)
			if cols, err := apitest.ReadSpawnColumns(e.dbPath, label.InstanceID); err != nil || cols.State != store.StatePending {
				t.Errorf("row %s = %v (err %v); want pending", label.InstanceID, cols.State, err)
			}
		})
	}
}
