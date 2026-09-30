package api_test

// kill_fixture_test.go is the shared kill test fixture (SR-20.2, SR-20.3,
// SR-20.6): killEnv (a real store behind the killStore wrapper, a
// tmuxfix.Recorder on a virtual clock, the procfix process-checker fake, the
// internal/config [tmux] defaults and a per-test TMUX_TMPDIR), the live-row
// factory (row, its own labelled session, its agent process), process and
// server call hooks, the trail readers and the call assertion. It holds no
// tests. HOME is left as TestMain set it, so the trail readers see kill's.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
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

// killStore is api.KillStore over a real store; the adoption write delegates
// unless failAdopt or refuseAdopt set its answer (nothing is written then).
type killStore struct {
	st       *store.Store
	adoptErr error
	adoptRes api.CondResult
}

// failAdopt makes every later adoption write return err (errInjectedStore when nil).
func (w *killStore) failAdopt(err error) { w.adoptErr = orInjected(err) }

// refuseAdopt makes every later adoption write return res (CondChanged or CondAbsent).
func (w *killStore) refuseAdopt(res api.CondResult) { w.adoptRes = res }

// GetSpawn delegates.
func (w *killStore) GetSpawn(id string) (api.Spawn, error) { return w.st.GetSpawn(id) }

// StoreID delegates.
func (w *killStore) StoreID() string { return w.st.StoreID() }

// AdoptIdentityIfUnchanged returns the injected answer, else delegates.
func (w *killStore) AdoptIdentityIfUnchanged(id string, examined api.RowSnapshot, li api.LaunchIdentity) (api.CondResult, error) {
	switch {
	case w.adoptErr != nil:
		return 0, w.adoptErr
	case w.adoptRes != 0:
		return w.adoptRes, nil
	}
	return w.st.AdoptIdentityIfUnchanged(id, examined, li)
}

// agentState is a seeded row's agent process in the fake; the zero value is alive.
type agentState int

const (
	agentAlive       agentState = iota // running with its recorded start time
	agentGone                          // no such process
	agentZombie                        // a zombie (reads as gone)
	agentUnreadable                    // its start time cannot be read
	agentNotRecorded                   // the row records no pid and no pane pid
)

// process is s as a procfix process with start time start.
func (s agentState) process(start string) procfix.Process {
	switch s {
	case agentGone:
		return procfix.Gone()
	case agentZombie:
		return procfix.Zombie()
	case agentUnreadable:
		return procfix.Unreadable()
	}
	return procfix.Alive(start)
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

// killRowSpec overrides seedRow's defaults; Opts are applied last, so they win.
type killRowSpec struct {
	ID               string     // default "kill-<8 hex>"
	State            string     // default waiting; any live or finished state
	Agent            agentState // default alive
	NoServerIdentity bool       // the row records no server identity (a lost create reply)
	NoPane           bool       // the row records no pane (a lost create reply)
	NoSession        bool       // seed no session (seedSession or seedTeamSession later)
	Teammates        int        // extra split panes in the row's session, each alive
	Opts             []apitest.SpawnOption
}

// killRow is a seeded row: Spawn as read back after seeding, Socket the
// socket kill uses (recorded, else e.defaultSocket), its agent process in
// the fake (AgentPID 0 when none), its teammates' pids and Session as seeded.
type killRow struct {
	ID, Name, Socket, Token, StoreID string
	Spawn                            api.Spawn
	Agent                            agentState
	AgentPID                         int
	AgentStart                       string
	TeammatePIDs                     []int
	Session                          tmuxfix.SeedSession
}

// current is the row's current label.
func (r killRow) current() tmux.Label { return tmuxfix.Valid(r.Token, r.ID, r.StoreID) }

// old is an earlier launch's label for the row (tmuxfix.OtherToken): a leftover.
func (r killRow) old() tmux.Label { return tmuxfix.Valid(tmuxfix.OtherToken, r.ID, r.StoreID) }

// otherStore is another store's valid label naming the row's id, with token.
func (r killRow) otherStore(token string) tmux.Label {
	return tmuxfix.Valid(token, r.ID, apitest.OtherStoreID(r.StoreID))
}

// foreign is this store's label with the row's token naming another row's id.
func (r killRow) foreign(id string) tmux.Label { return tmuxfix.Valid(r.Token, id, r.StoreID) }

// seedRow seeds, through apitest.SeedSpawn, a row with a full launch identity
// (token, apitest.TestSocket, the fixture server, the pane, the SessionStart
// pid and start time agreeing with the pane's), its agent process in the
// fake, and (unless NoSession) its current-labelled session in the Recorder.
// A later row in e gets its own pane id and pid.
func (e *killEnv) seedRow(t *testing.T, spec killRowSpec) killRow {
	t.Helper()
	id, state := spec.ID, spec.State
	if id == "" {
		id = "kill-" + uuid.NewString()[:8]
	}
	if state == "" {
		state = store.StateWaiting
	}
	li := store.LaunchIdentity{Token: strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Socket: apitest.TestSocket, ServerPID: killServerPID, ServerStart: killServerStart,
		ServerStarttime: apitest.LinuxProcStarttime, PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID,
		PaneStarttime: apitest.LinuxProcStarttime}
	if e.rows > 0 {
		li.PaneID, li.PanePID = "%"+strconv.Itoa(1000+e.rows), e.newPID()
	}
	e.rows++
	if spec.NoServerIdentity {
		li.ServerPID, li.ServerStart, li.ServerStarttime = 0, 0, ""
	}
	if spec.NoPane {
		li.PaneID = ""
	}
	if spec.NoPane || spec.Agent == agentNotRecorded {
		li.PanePID, li.PaneStarttime = 0, ""
	}
	opts := []apitest.SpawnOption{apitest.WithLaunchIdentity(li)}
	if li.PanePID > 0 {
		opts = append(opts, apitest.WithPID(li.PanePID), apitest.WithProcStarttime(li.PaneStarttime))
	}
	if _, err := apitest.SeedSpawn(e.dbPath, id, state, "", "off", "", false, append(opts, spec.Opts...)...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	row, err := e.st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	r := killRow{ID: id, Name: row.TmuxSessionName, Socket: row.Identity.Socket, Token: row.Identity.Token,
		StoreID: e.storeID, Spawn: row, Agent: spec.Agent}
	if r.Socket == "" {
		r.Socket = e.defaultSocket
	}
	agent := tmux.SelectAgentProcess(tmux.ProcIdentity{PID: row.PID, Starttime: row.ProcStarttime},
		tmux.ProcIdentity{PID: row.Identity.PanePID, Starttime: row.Identity.PaneStarttime})
	e.setAgent(&r, agent.Identity.PID, agent.Identity.Starttime)
	switch {
	case spec.Teammates > 0:
		e.seedTeamSession(t, &r, spec.Teammates)
	case !spec.NoSession:
		e.seedSession(t, &r)
	}
	e.syncServers()
	return r
}

// setAgent records pid and start as r's agent process and puts it in the
// fake in r.Agent's state; a pid of 0 (none recorded) is not put.
func (e *killEnv) setAgent(r *killRow, pid int, start string) {
	r.AgentPID, r.AgentStart = pid, start
	if pid > 0 {
		e.pc.Set(pid, r.Agent.process(start))
	}
}

// seedSession seeds r's own session through Recorder.SeedRowSession with
// opts (another label, name or creation time), on the fixture server when
// the socket has none. For a row with no recorded pane, the session's new
// pane is the agent's: its pid goes in the fake in r.Agent's state.
func (e *killEnv) seedSession(t *testing.T, r *killRow, opts ...tmuxfix.RowSessionOption) {
	t.Helper()
	e.ensureServer(r)
	r.Session = e.rec.SeedRowSession(t, e.dbPath, r.ID, opts...)
	e.adoptablePane(r)
	e.syncServers()
}

// seedTeamSession seeds r's current-labelled session with the agent's pane
// (window 0, pane 0, carrying the row's @ad_pane) and n teammate panes
// (no @ad_pane), each teammate alive in the fake. r.Name must be its own
// stored form (no $, \, '.' or ':').
func (e *killEnv) seedTeamSession(t *testing.T, r *killRow, n int) {
	t.Helper()
	e.ensureServer(r)
	panes := []tmuxfix.SeedPane{{ID: r.Spawn.Identity.PaneID, PID: r.Spawn.Identity.PanePID, AdPane: r.Token}}
	for i := 1; i <= n; i++ {
		pid := e.newPID()
		panes = append(panes, tmuxfix.SeedPane{Index: i, PID: pid})
		e.pc.Set(pid, procfix.Alive(apitest.LinuxProcStarttime))
		r.TeammatePIDs = append(r.TeammatePIDs, pid)
	}
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: r.Name, Label: r.current(), Panes: panes})
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Name == r.Name {
			r.Session = s
		}
	}
	e.adoptablePane(r)
	e.syncServers()
}

// adoptablePane makes the first pane of r's session r's agent process when
// the row records no pane (the pane adoption finds), alive with
// apitest.LinuxProcStarttime unless r.Agent says otherwise.
func (e *killEnv) adoptablePane(r *killRow) {
	if r.Spawn.Identity.PaneID != "" || r.Agent == agentNotRecorded || len(r.Session.Panes) == 0 {
		return
	}
	e.setAgent(r, r.Session.Panes[0].PID, apitest.LinuxProcStarttime)
}

// ensureServer binds a server on r.Socket when none is: the row's recorded
// server identity, else the fixture server's.
func (e *killEnv) ensureServer(r *killRow) {
	if _, ok := e.rec.Server(r.Socket); ok {
		return
	}
	srv := tmuxfix.Server{PID: killServerPID, Start: killServerStart, ProcStart: apitest.LinuxProcStarttime}
	if id := r.Spawn.Identity; id.ServerPID > 0 {
		srv = tmuxfix.Server{PID: id.ServerPID, Start: id.ServerStart, ProcStart: id.ServerStarttime}
	}
	e.rec.StartServer(r.Socket, srv)
}

// syncServers sets every server the Recorder has bound in the fake: a running
// one alive with its process start time, a stopped one gone. Call it after
// RestartServer, RebindServer or StopServer.
func (e *killEnv) syncServers() {
	for _, s := range e.rec.Servers() {
		if s.Running {
			e.pc.Set(s.PID, procfix.Alive(s.ProcStart))
		} else {
			e.pc.Set(s.PID, procfix.Gone())
		}
	}
}

// seedBystander seeds an unlabelled session with one pane on socket, so the
// server still holds a session after the row's is killed (the Recorder keeps
// an empty server bound; real tmux may exit).
func (e *killEnv) seedBystander(t *testing.T, socket string) tmuxfix.SeedSession {
	t.Helper()
	return e.seedOther(t, socket, tmuxfix.SeedSession{Name: "bystander-" + uuid.NewString()[:8]})
}

// seedViewer seeds an unlabelled session showing r's agent pane: another
// listing of it (a grouped session, a linked window, a viewer) when the pane
// is already on the server, else the pane's only listing.
func (e *killEnv) seedViewer(t *testing.T, r killRow) tmuxfix.SeedSession {
	t.Helper()
	pane := tmuxfix.SeedPane{ID: r.Spawn.Identity.PaneID, PID: r.AgentPID}
	for _, s := range e.rec.Sessions(r.Socket) {
		for _, p := range s.Panes {
			if p.ID == pane.ID {
				pane = tmuxfix.SeedPane{ID: p.ID, Shared: true}
			}
		}
	}
	e.ensureServer(&r)
	return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "viewer-" + uuid.NewString()[:8],
		Panes: []tmuxfix.SeedPane{pane}})
}

// seedOther seeds s on socket and returns it as stored.
func (e *killEnv) seedOther(t *testing.T, socket string, s tmuxfix.SeedSession) tmuxfix.SeedSession {
	t.Helper()
	e.rec.SeedSessions(socket, s)
	for _, got := range e.rec.Sessions(socket) {
		if got.Name == s.Name {
			return got
		}
	}
	t.Fatalf("session %q not seeded on %s", s.Name, socket)
	return tmuxfix.SeedSession{}
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
	var out []map[string]any
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == "kill" {
			out = append(out, l)
		}
	}
	return out
}

// assertKillCalls fails unless the Recorder's socket-taking calls are
// exactly want in order, no name-based call was made and every kill
// targets an id (assertKillsByID).
func (e *killEnv) assertKillCalls(t *testing.T, want ...tmux.Call) {
	t.Helper()
	var got []tmux.Call
	for _, c := range e.rec.SocketCalls() {
		got = append(got, c.Call)
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
