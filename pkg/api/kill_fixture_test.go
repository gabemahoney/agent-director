package api_test

// kill_fixture_test.go is the shared kill test fixture (SR-20.2, SR-20.3,
// SR-20.6): killEnv (a real store behind the killStore wrapper, a
// tmuxfix.Recorder on a virtual clock, the procfix process-checker fake, the
// internal/config [tmux] defaults and a per-test TMUX_TMPDIR), process and
// server call hooks, the trail readers and the call assertion; the live-row
// factory is kill_fixture_rows_test.go. It holds no tests. HOME is left as
// TestMain set it, so the trail readers see kill's. Later verb Epics
// (read-pane, send-keys, pause, resume, reuse, kill's finished-row opt-in)
// extend this fixture instead of writing their own.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killClockStart is where the fixture's virtual clock starts.
var killClockStart = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// The fixture's tmux server: rows record it unless NoServerIdentity, and it
// runs in the process fake with apitest.LinuxProcStarttime.
var (
	killServerPID   = apitest.TestPanePID + 1
	killServerStart = killClockStart.Add(-time.Hour).Unix()
)

// Kills must name ids (SR-6.1): "%N" for a pane, "$N" for a session.
var (
	killPaneIDRe    = regexp.MustCompile(`^%[0-9]+$`)
	killSessionIDRe = regexp.MustCompile(`^\$[0-9]+$`)
)

// killStore is api.KillStore (and the pane verbs' stores) over a real store;
// the adoption write delegates unless failAdopt or refuseAdopt set its
// answer (nothing is written then); adoptTries counts every adoption write
// attempted, stateReads every state read (pause's wait polls); permErr is
// failPermissionRequests', stateErr failStateReads'.
type killStore struct {
	st         *store.Store
	adoptErr   error
	adoptRes   api.CondResult
	adoptTries int
	stateReads int
	permErr    error
	stateErr   error
}

// failAdopt makes every later adoption write return err (errInjectedStore when nil).
func (w *killStore) failAdopt(err error) { w.adoptErr = orInjected(err) }

// refuseAdopt makes every later adoption write return res (CondChanged or CondAbsent).
func (w *killStore) refuseAdopt(res api.CondResult) { w.adoptRes = res }

// GetSpawn delegates.
func (w *killStore) GetSpawn(id string) (api.Spawn, error) { return w.st.GetSpawn(id) }

// StoreID delegates.
func (w *killStore) StoreID() string { return w.st.StoreID() }

// AdoptIdentityIfUnchanged counts the attempt, then returns the injected
// answer, else delegates.
func (w *killStore) AdoptIdentityIfUnchanged(id string, examined api.RowSnapshot, li api.LaunchIdentity) (api.CondResult, error) {
	w.adoptTries++
	switch {
	case w.adoptErr != nil:
		return 0, w.adoptErr
	case w.adoptRes != 0:
		return w.adoptRes, nil
	}
	return w.st.AdoptIdentityIfUnchanged(id, examined, li)
}

// killEnv is what a kill test drives: st (a real store on dbPath) behind
// store, rec on clock's virtual time, pc the process fake, cfg the [tmux]
// defaults Kill is given, sleep kill's pause (clock.Advance by default) and
// defaultSocket the socket a row with none recorded resolves to.
type killEnv struct {
	dbPath        string
	st            *store.Store
	store         *killStore
	rec           *tmuxfix.Recorder
	clock         *tmuxfix.Clock
	pc            *procfix.Checker
	cfg           config.Tmux
	sleep         func(time.Duration)
	storeID       string
	defaultSocket string
	rows, nextPID int
}

// newKillEnv builds a killEnv over a fresh store, with TMUX unset and a
// per-test TMUX_TMPDIR whose per-user socket directory exists.
func newKillEnv(t *testing.T) *killEnv {
	t.Helper()
	tmpdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX") //nolint:errcheck
	if err := os.MkdirAll(userSocketDir(tmpdir), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.InitStore(dbPath); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	storefix.RegisterStorePath(t, st, dbPath) // storefix's permission-request seeders take st
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	clock := tmuxfix.NewClock(killClockStart)
	return &killEnv{dbPath: dbPath, st: st, store: &killStore{st: st}, clock: clock, pc: procfix.New(),
		rec: tmuxfix.NewRecorder().WithVirtualTime(clock, tmux.Timeouts{}), cfg: config.Default().Tmux,
		sleep: clock.Advance, storeID: storeID, defaultSocket: filepath.Join(userSocketDir(tmpdir), "default"),
		nextPID: apitest.TestPanePID + 10}
}

// kill runs the exported api.Kill on id with e.store, e.rec, e.pc, e.cfg's
// effective durations, e.clock.Now and e.sleep.
func (e *killEnv) kill(id string) (api.KillResult, error) {
	return api.Kill(e.store, e.rec, e.pc, e.cfg.EffectiveStartingSession(), e.cfg.EffectiveStoppingWindow(),
		e.cfg.EffectiveKillExitWait(), e.clock.Now, e.sleep, api.KillParams{ClaudeInstanceID: id})
}

// client builds a Client on e's store file with a config file holding
// settings, e.rec as its tmux client, e's clock, fake and sleep, and a
// captured logger (returned).
func (e *killEnv) client(t *testing.T, settings ...apitest.TmuxSetting) (*api.Client, *bytes.Buffer) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath, settings...)
	logs := &bytes.Buffer{}
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, Logger: log.New(logs, "", 0),
		TmuxClient: e.rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, e.clock.Now)
	api.SetProcCheckerForTest(c, e.pc)
	api.SetSleepForTest(c, func(d time.Duration) { e.sleep(d) })
	return c, logs
}

// columns reads id's row raw through apitest.ReadSpawnColumns.
func (e *killEnv) columns(t *testing.T, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols
}

// newPID returns a pid no other fixture process uses.
func (e *killEnv) newPID() int {
	e.nextPID++
	return e.nextPID
}

// setAfterCall sets each of pids to p in the fake whenever a tmux call of kind
// call returns, e.g. the pane kill ending the agent.
func (e *killEnv) setAfterCall(call tmux.Call, p procfix.Process, pids ...int) {
	e.rec.AfterCall(call, func(tmuxfix.SocketCall, error) {
		for _, pid := range pids {
			e.pc.Set(pid, p)
		}
	})
}

// setAfterWaiting makes e.sleep set each of pids to p once d of sleep has
// passed in total, for a process that exits during kill's wait.
func (e *killEnv) setAfterWaiting(d time.Duration, p procfix.Process, pids ...int) {
	prev, slept := e.sleep, time.Duration(0)
	e.sleep = func(x time.Duration) {
		prev(x)
		if slept += x; slept >= d {
			for _, pid := range pids {
				e.pc.Set(pid, p)
			}
		}
	}
}

// serverExitsWhenEmpty stops socket's server (gone in the fake) when a pane
// or session kill leaves it with no session, as tmux exits.
func (e *killEnv) serverExitsWhenEmpty(socket string) {
	exit := func(c tmuxfix.SocketCall, _ error) {
		if _, ok := e.rec.Server(socket); ok && c.Socket == socket && len(e.rec.Sessions(socket)) == 0 {
			e.rec.StopServer(socket)
			e.syncServers()
		}
	}
	e.rec.AfterCall(tmux.CallKillPane, exit).AfterCall(tmux.CallKillSession, exit)
}

// killCalled returns id's ad.kill.called trail records, in write order.
func killCalled(t *testing.T, id string) []map[string]any {
	t.Helper()
	return pendTrail(t, "ad.kill.called", id)
}

// killDisagrees returns id's ad.provenance.disagree records written by kill.
func killDisagrees(t *testing.T, id string) []map[string]any {
	t.Helper()
	return verbDisagrees(t, "kill", id)
}

// assertKillCalls fails unless the Recorder's socket-taking calls are
// exactly want in order (as recordedCall gives them), no name-based call was
// made and every kill targets an id (assertKillsByID).
func (e *killEnv) assertKillCalls(t *testing.T, want ...tmux.Call) {
	t.Helper()
	var got []tmux.Call
	for _, c := range e.rec.SocketCalls() {
		got = append(got, recordedCall(c))
	}
	if !slices.Equal(got, want) {
		t.Errorf("tmux calls = %v; want %v", got, want)
	}
	if calls := e.rec.Calls(); len(calls) != 0 {
		t.Errorf("name-based tmux calls = %v; want none", calls)
	}
	e.assertKillsByID(t)
}

// assertKillsByID fails when a recorded pane kill names no pane id or a
// session kill no session id (never a session name).
func (e *killEnv) assertKillsByID(t *testing.T) {
	t.Helper()
	for _, c := range e.rec.SocketCalls() {
		if (c.Call == tmux.CallKillPane && !killPaneIDRe.MatchString(c.Target)) ||
			(c.Call == tmux.CallKillSession && !killSessionIDRe.MatchString(c.Target)) {
			t.Errorf("%v targets %q; want an id", c.Call, c.Target)
		}
	}
}
