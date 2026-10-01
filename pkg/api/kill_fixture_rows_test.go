package api_test

// kill_fixture_rows_test.go is the kill fixture's row factory (SR-20.2): the
// row spec and seeded row, the agent process states, and the row, session,
// server and other-session seeds. It holds no tests.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

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

// killRowSpec overrides seedRow's defaults; Opts are applied last, so they win.
type killRowSpec struct {
	ID               string     // default "kill-<8 hex>"
	State            string     // default waiting; any live or finished state
	Agent            agentState // default alive
	NoServerIdentity bool       // the row records no server identity (a lost create reply)
	NoPane           bool       // the row records no pane (a lost create reply)
	NoSession        bool       // seed no session (seedSession or seedTeamSession later)
	Teammates        int        // extra split panes in the row's session, each alive
	RelayOn          bool       // relay_mode on (default off)
	SessionID        string     // the row's claude_session_id (default none)
	CWD              string     // the row's cwd (default none; a resumable row needs one)
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
	relay := "off"
	if spec.RelayOn {
		relay = "on"
	}
	if _, err := apitest.SeedSpawn(e.dbPath, id, state, spec.CWD, relay, spec.SessionID, false, append(opts, spec.Opts...)...); err != nil {
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
