package tmuxfix

import (
	"sort"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The Recorder's per-socket session tables (SRD SR-20.3). Each socket has at
// most one bound server; a server holds sessions, a session holds panes.
// Everything is typed: labels are tmux.Label values as the production
// client would classify them, never raw @ad_owner text.
//
// One pane may be listed in several sessions (SeedPane.Shared), as tmux
// shows a pane of a grouped session, a linked window or a viewing session
// (SR-3.4, SR-6.1): the pane listing shows it once per session listing it,
// a pane kill removes it from every one, and a session kill (or a session
// hook) removes only that session's listing, so the pane lives while any
// session still lists it.

// Server is a tmux server's identity: the #{pid} and #{start_time} the
// lookup and the create reply show, and the server process's start time as
// a start-time reader would read it (same form as proc_starttime), for a
// process-checker fake.
type Server struct {
	// PID is the server's #{pid}.
	PID int
	// Start is the server's #{start_time}, epoch seconds.
	Start int64
	// ProcStart is the server process's start time; the Recorder never
	// reports it through the tmux calls.
	ProcStart string
}

// ServerStatus is one server the Recorder has bound, for a process-checker
// fake: Running stays true for a server whose socket another server took
// (RebindServer) and turns false when it stops (RestartServer, StopServer).
type ServerStatus struct {
	Server
	// Socket is the socket the server was bound to.
	Socket string
	// Bound reports that the server is the one answering on Socket.
	Bound bool
	// Running reports that the server process is still running.
	Running bool
}

// SeedPane is one pane of a seeded session (SR-3.7): window index, pane
// index, pane id "%N", pane pid and pane label.
type SeedPane struct {
	Window, Index int
	// ID is the pane id; "" assigns the server's next "%N".
	ID string
	// PID is the pane pid; 0 assigns a fresh pid above Linux's PID_MAX_LIMIT,
	// which no process-start-time reader finds.
	PID int
	// AdPane is the pane label as the pane listing classifies it
	// (tmux.Pane.AdPane; WD 2026-09-29c): the launch token of the pane's own
	// "<token> <pane id>" value, or "" for none (a split pane, a teammate) or
	// for a malformed or borrowed value. Typed, never raw text.
	AdPane string
	// Shared lists a pane the server already holds in another session in
	// this session too: a grouped session, a linked window or a viewing
	// session (SR-3.4). ID must name that pane, and at most once per
	// session; Window and Index are this session's own. PID and AdPane
	// are the pane's own: zero and "" take them from the pane, other
	// values must match it. Seeding panics otherwise. Stored listings
	// (Sessions) report Shared false: tmux keeps no owning session, and
	// every listing is the same pane.
	Shared bool
}

// SeedSession is one session of a socket's table.
type SeedSession struct {
	// ID is the session id; "" assigns the server's next "$N".
	ID string
	// Name is the stored name, the bytes tmux lists (SR-3.10); take $, \,
	// '.' and ':' forms from StoredNames.
	Name string
	// Created is #{session_created}, epoch seconds; 0 takes the bound
	// clock's current second (WithVirtualTime), else the wall clock's.
	Created int64
	// Label is the session's own label as the lookup classifies it: Valid
	// (token, id, store id) for a current, old (OtherToken), foreign
	// (another id) or another store's (OtherStoreID) label, or a
	// LabelShape's Want.
	Label tmux.Label
	// LabelSet reports that the session carries its own @ad_owner value
	// even though Label is LabelNone (a malformed or borrowed value); a
	// LabelValid label is always set. Only an unset session inherits a
	// global scope value (see ScopeLevel).
	LabelSet bool
	// Panes are the session's panes; none gives one pane, window 0 index 0,
	// with a new id and pid.
	Panes []SeedPane
}

// ScopeLevel names one of the three scope reads of the lookup (SR-3.4).
type ScopeLevel int

// The scope levels, in the order of the lookup's reads. As in tmux 3.2a's
// format lookup, a session's listed #{@ad_owner} is the server value when
// set, else the global-window value when set, else the session's own
// value when set, else the global value (provenance-fresh F2, F9b).
const (
	// ScopeGlobal is a global session option (set-option -g).
	ScopeGlobal ScopeLevel = iota + 1
	// ScopeServer is a server option (set-option -s).
	ScopeServer
	// ScopeGlobalWindow is a global window option (set-option -gw).
	ScopeGlobalWindow
)

// ScopeValue is a scope-level @ad_owner value in typed form: the session id
// the value embeds and the label it reads as on that session's line. On
// any other session's line it reads as LabelNone (a borrowed value). A
// value embedding no session id (malformed) has SessionID "" and a
// LabelNone Label.
type ScopeValue struct {
	SessionID string
	Label     tmux.Label
}

// socketState is one socket's table.
type socketState struct {
	server   *serverState              // bound server; nil when none answers
	noServer tmux.Failure              // failure of a call with no server; 0 means FailNoSocket
	scope    map[ScopeLevel]ScopeValue // the server's; cleared when it stops or is replaced
}

// serverState is one server and its sessions.
type serverState struct {
	Server
	socket   string
	bound    bool
	running  bool
	sessions []*sessionState
	nextSess int // next $N
	nextPane int // next %N
}

// sessionState is one session of a server.
type sessionState struct {
	id       string
	name     string
	created  int64
	label    tmux.Label
	labelSet bool
	panes    []SeedPane
}

// paneKey identifies a capture text: a socket and a pane id.
type paneKey struct{ socket, pane string }

// autoPIDBase is where auto-assigned pids start: above Linux's
// PID_MAX_LIMIT (4194304), so no start-time reader finds such a process.
const autoPIDBase = 5_000_000

// socket returns the socket's state, creating an empty one. Callers hold
// r.mu.
func (r *Recorder) socket(socket string) *socketState {
	if r.sockets == nil {
		r.sockets = make(map[string]*socketState)
	}
	st, ok := r.sockets[socket]
	if !ok {
		st = &socketState{scope: make(map[ScopeLevel]ScopeValue)}
		r.sockets[socket] = st
	}
	return st
}

// autoPID returns a fresh pid above PID_MAX_LIMIT. Callers hold r.mu.
func (r *Recorder) autoPID() int {
	if r.nextPID < autoPIDBase {
		r.nextPID = autoPIDBase
	}
	r.nextPID++
	return r.nextPID
}

// nowSecond returns the bound clock's current second, or the wall clock's
// when no clock is bound. Callers hold r.mu.
func (r *Recorder) nowSecond() int64 {
	if r.clock != nil {
		return r.clock.Now().Unix()
	}
	return time.Now().Unix()
}

// bind binds a new server with identity s to socket, filling a zero PID
// and Start. Callers hold r.mu and have unbound any previous server.
func (r *Recorder) bind(socket string, s Server) *serverState {
	if s.PID == 0 {
		s.PID = r.autoPID()
	}
	if s.Start == 0 {
		s.Start = r.nowSecond()
	}
	srv := &serverState{Server: s, socket: socket, bound: true, running: true}
	r.socket(socket).server = srv
	r.servers = append(r.servers, srv)
	return srv
}

// serverFor returns socket's bound server, starting one with a new
// identity when none is bound (as a create does). Callers hold r.mu.
func (r *Recorder) serverFor(socket string) *serverState {
	if srv := r.socket(socket).server; srv != nil {
		return srv
	}
	return r.bind(socket, Server{})
}

// StartServer binds a server with identity s to socket; a zero PID or Start
// is filled (a fresh pid, the current second). It panics when a server is
// already bound there: use RestartServer or RebindServer.
func (r *Recorder) StartServer(socket string, s Server) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.socket(socket).server != nil {
		panic("tmuxfix: StartServer: a server is already bound to " + socket)
	}
	r.bind(socket, s)
	return r
}

// RestartServer stops socket's server (its sessions and scope values go and
// its process no longer runs) and binds a new, empty server with identity s: new pid, start
// time, and session and pane ids counted from $0 and %0 again, as a new tmux
// server numbers them.
func (r *Recorder) RestartServer(socket string, s Server) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.socket(socket)
	if old := st.server; old != nil {
		old.bound, old.running = false, false
	}
	clear(st.scope)
	r.bind(socket, s)
	return r
}

// RebindServer binds a new, empty server with identity s (no sessions or
// scope values) to socket while
// the previous one keeps running unbound: its identity stays in Servers
// with Running true, but no call reaches it.
func (r *Recorder) RebindServer(socket string, s Server) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.socket(socket)
	if old := st.server; old != nil {
		old.bound = false
	}
	clear(st.scope)
	r.bind(socket, s)
	return r
}

// StopServer stops socket's server: every later call on socket fails with
// the socket's no-server failure (SetNoServerFailure).
func (r *Recorder) StopServer(socket string) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.socket(socket)
	if st.server != nil {
		st.server.bound, st.server.running = false, false
		st.server = nil
	}
	clear(st.scope)
	return r
}

// EmptyServer removes every session of socket's bound server, recording no
// call: the server keeps running, bound, with its identity, scope values and
// id counters, as a tmux server with exit-empty off does once its last session
// ends (b.47f). It panics when no server is bound there.
func (r *Recorder) EmptyServer(socket string) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	srv := r.socket(socket).server
	if srv == nil {
		panic("tmuxfix: EmptyServer: no server is bound to " + socket)
	}
	srv.sessions = nil
	return r
}

// SetNoServerFailure sets the failure of a call on socket while no server is
// bound there: tmux.FailNoSocket (the default: no socket file) or
// tmux.FailNoServer (a stale socket file). Its CallError carries the socket.
func (r *Recorder) SetNoServerFailure(socket string, f tmux.Failure) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.socket(socket).noServer = f
	return r
}

// Servers returns every server the Recorder has bound, in binding order, for
// a process-checker fake.
func (r *Recorder) Servers() []ServerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ServerStatus, 0, len(r.servers))
	for _, s := range r.servers {
		out = append(out, ServerStatus{Server: s.Server, Socket: s.socket, Bound: s.bound, Running: s.running})
	}
	return out
}

// Server returns the identity of the server bound to socket, and whether one
// is bound.
func (r *Recorder) Server(socket string) (Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if srv := r.socket(socket).server; srv != nil {
		return srv.Server, true
	}
	return Server{}, false
}

// SeedSessions adds sessions to socket's server, starting a server with a new
// identity when none is bound. Empty ids and pane ids take the server's next
// ones, and later ids are counted past every seeded one. It panics on a
// session or pane id the server already holds, except a SeedPane.Shared
// pane, which must be held already (seed its first session earlier, in
// this call or before).
func (r *Recorder) SeedSessions(socket string, sessions ...SeedSession) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	srv := r.serverFor(socket)
	for _, s := range sessions {
		r.addSession(srv, s)
	}
	return r
}

// addSession adds one seeded session to srv and returns it as stored.
// Callers hold r.mu.
func (r *Recorder) addSession(srv *serverState, s SeedSession) SeedSession {
	if s.ID == "" {
		s.ID = "$" + strconv.Itoa(srv.nextSess)
	}
	if srv.findSession(s.ID) != nil {
		panic("tmuxfix: session " + s.ID + " already exists on " + srv.socket)
	}
	srv.nextSess = nextAfter(srv.nextSess, s.ID)
	if s.Created == 0 {
		s.Created = r.nowSecond()
	}
	if len(s.Panes) == 0 {
		s.Panes = []SeedPane{{}}
	}
	panes := make([]SeedPane, 0, len(s.Panes))
	for _, p := range s.Panes {
		if p.Shared {
			panes = append(panes, srv.sharedPane(p, panes))
			continue
		}
		if p.ID == "" {
			p.ID = "%" + strconv.Itoa(srv.nextPane)
		}
		if _, owner := srv.findPane(p.ID); owner != nil {
			panic("tmuxfix: pane " + p.ID + " already exists on " + srv.socket +
				" (SeedPane.Shared lists it in another session)")
		}
		srv.nextPane = nextAfter(srv.nextPane, p.ID)
		if p.PID == 0 {
			p.PID = r.autoPID()
		}
		panes = append(panes, p)
	}
	s.Panes = panes
	srv.sessions = append(srv.sessions, &sessionState{
		id: s.ID, name: s.Name, created: s.Created, label: s.Label,
		labelSet: s.LabelSet || s.Label.Kind == tmux.LabelValid, panes: panes,
	})
	s.LabelSet = s.LabelSet || s.Label.Kind == tmux.LabelValid
	return s
}

// sharedPane returns the listing of the held pane p.ID that a SeedPane.Shared
// entry p seeds, with the pane's own PID and AdPane; seen are the listings
// already seeded for the same session. It panics on an unheld pane, a
// second listing in one session, or a PID or AdPane other than the pane's.
func (srv *serverState) sharedPane(p SeedPane, seen []SeedPane) SeedPane {
	i, owner := srv.findPane(p.ID)
	if p.ID == "" || owner == nil {
		panic("tmuxfix: shared pane " + strconv.Quote(p.ID) + " is not on " + srv.socket)
	}
	for _, q := range seen {
		if q.ID == p.ID {
			panic("tmuxfix: shared pane " + p.ID + " is listed twice in one session on " + srv.socket)
		}
	}
	held := owner.panes[i]
	if (p.PID != 0 && p.PID != held.PID) || (p.AdPane != "" && p.AdPane != held.AdPane) {
		panic("tmuxfix: shared pane " + p.ID + " on " + srv.socket + " gives a pid or pane label other than the pane's")
	}
	p.PID, p.AdPane, p.Shared = held.PID, held.AdPane, false
	return p
}

// nextAfter returns next advanced past id's number ("$N" or "%N").
func nextAfter(next int, id string) int {
	if len(id) < 2 {
		return next
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < next {
		return next
	}
	return n + 1
}

// SetScope sets socket's scope value at level; a bound server is not
// needed. Any set level makes the lookup's ScopeValue true. Scope values are
// the server's options: RestartServer, RebindServer and StopServer clear
// them.
func (r *Recorder) SetScope(socket string, level ScopeLevel, v ScopeValue) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.socket(socket).scope[level] = v
	return r
}

// ClearScope unsets socket's scope value at level.
func (r *Recorder) ClearScope(socket string, level ScopeLevel) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.socket(socket).scope, level)
	return r
}

// SetCapture sets the text CapturePaneID returns for paneID on socket
// (default ""). The text is returned whole, whatever nLines asks for.
func (r *Recorder) SetCapture(socket, paneID, text string) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.captures == nil {
		r.captures = make(map[paneKey]string)
	}
	r.captures[paneKey{socket, paneID}] = text
	return r
}

// Sessions returns socket's bound server's sessions as stored, in listing
// order, with their own labels (no scope value applied); nil with no
// server.
func (r *Recorder) Sessions(socket string) []SeedSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	srv := r.socket(socket).server
	if srv == nil {
		return nil
	}
	var out []SeedSession
	for _, s := range srv.listed() {
		out = append(out, s.seed())
	}
	return out
}

// seed returns s as a SeedSession copy.
func (s *sessionState) seed() SeedSession {
	return SeedSession{ID: s.id, Name: s.name, Created: s.created, Label: s.label,
		LabelSet: s.labelSet, Panes: append([]SeedPane(nil), s.panes...)}
}

// listed returns the server's sessions in tmux's listing order: by stored
// name, byte order (tmux keeps sessions in a tree sorted by name).
func (srv *serverState) listed() []*sessionState {
	out := append([]*sessionState(nil), srv.sessions...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// findSession returns the session with id, or nil.
func (srv *serverState) findSession(id string) *sessionState {
	for _, s := range srv.sessions {
		if s.id == id {
			return s
		}
	}
	return nil
}

// findPane returns the index of the pane with id in the first session
// listing it, and that session, or nil.
func (srv *serverState) findPane(id string) (int, *sessionState) {
	for _, s := range srv.sessions {
		for i, p := range s.panes {
			if p.ID == id {
				return i, s
			}
		}
	}
	return 0, nil
}

// removePane removes the pane with id from every session listing it,
// removing each session left with no pane, and reports whether any listed
// it.
func (srv *serverState) removePane(id string) bool {
	found := false
	for _, s := range append([]*sessionState(nil), srv.sessions...) {
		for i, p := range s.panes {
			if p.ID != id {
				continue
			}
			s.panes = append(s.panes[:i:i], s.panes[i+1:]...)
			found = true
			if len(s.panes) == 0 {
				srv.removeSession(s.id)
			}
			break
		}
	}
	return found
}

// setAdPane sets the pane label of the pane with id on every session
// listing it, and reports whether any listed it.
func (srv *serverState) setAdPane(id, token string) bool {
	found := false
	for _, s := range srv.sessions {
		for i := range s.panes {
			if s.panes[i].ID == id {
				s.panes[i].AdPane, found = token, true
			}
		}
	}
	return found
}

// removeSession removes the session with id.
func (srv *serverState) removeSession(id string) {
	for i, s := range srv.sessions {
		if s.id == id {
			srv.sessions = append(srv.sessions[:i], srv.sessions[i+1:]...)
			return
		}
	}
}

// listedLabel is the label the lookup shows on s's line under the socket's
// scope values (see ScopeLevel).
func (st *socketState) listedLabel(s *sessionState) tmux.Label {
	inherited := func(v ScopeValue) tmux.Label {
		if v.SessionID != "" && v.SessionID == s.id {
			return v.Label
		}
		return tmux.Label{}
	}
	if v, ok := st.scope[ScopeServer]; ok {
		return inherited(v)
	}
	if v, ok := st.scope[ScopeGlobalWindow]; ok {
		return inherited(v)
	}
	if s.labelSet {
		return s.label
	}
	if v, ok := st.scope[ScopeGlobal]; ok {
		return inherited(v)
	}
	return tmux.Label{}
}

// storedName returns the name tmux stores for a name given to new-session:
// the replay catalogue's recorded stored form (StoredNames) when it holds
// the name, else the name unchanged. The Recorder never derives a stored
// form itself.
func storedName(raw string) string {
	for _, n := range StoredNames() {
		if n.Raw == raw {
			return n.Stored
		}
	}
	return raw
}
