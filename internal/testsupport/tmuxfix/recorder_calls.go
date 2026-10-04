package tmuxfix

import (
	"sort"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The Recorder's socket-taking calls (SRD SR-20.3, Appendix F.1, F.3), with
// exactly *tmux.Client's signatures. Each call is recorded, answered from a
// script or from the socket's table, and then passes through virtual time,
// session hooks and after-call hooks (recorder_hooks.go). Every failure is a
// *tmux.CallError whose Call names the call kind.
//
// Table answers: a call on a socket with no bound server fails with the
// socket's no-server failure (SetNoServerFailure; tmux.FailNoSocket by
// default). A kill, send, capture or label of a pane or session id the
// server does not hold is FailUnrecognized with no FirstLine and exit
// status 1 (tmux's "can't find" replies; script a catalogue entry's
// FirstLine to carry one). The server stays bound when its last session is
// killed; StopServer models its exit. A pane listed in several sessions
// (SeedPane.Shared) is one pane to every call: the pane listing shows it
// once per session, a pane kill or pane label reaches every listing, and
// a session kill removes one listing only.

// SocketCall is one recorded socket-taking call.
type SocketCall struct {
	// Call is the call kind; SendKeysPane records CallSendText and, when it
	// makes one, CallSendEnter; SendKeyPane records CallSendKey.
	Call tmux.Call
	// Socket is the socket the call names.
	Socket string
	// Target is the session id (session kill, label by id), the pane id
	// (pane kill, sends, capture) or the name (create); "" for the lookup
	// and the pane listing.
	Target string
	// PaneID is SetLabel's pane id, the pane its pane label is set on (WD
	// 2026-09-29c); "" for every other call.
	PaneID string
	// Text and PressEnter are SendKeysPane's arguments (on the text call).
	Text       string
	PressEnter bool
	// Key is the key a key send names, which tmux reads as a key and never
	// types literally (send-keys -t <pane id> <key>), for example pause's
	// C-u before /exit (b.9o4); "" on every other call. A key send is
	// recorded under its own call kind, never the text or Enter call's.
	Key string
	// NLines and ANSI are CapturePaneID's arguments.
	NLines int
	ANSI   bool
	// Cwd, Envs and Command are NewSession's arguments.
	Cwd     string
	Envs    map[string]string
	Command []string
	// Token, InstanceID and StoreID are NewSession's and SetLabel's label
	// arguments (the session label's second, fourth and fifth fields; WD
	// 2026-09-29 STORE); Token is also the pane label's first field.
	Token, InstanceID, StoreID string
}

// effect applies a call's table effect on the socket's table and returns
// its answer, or a table-derived failure. s is the call's script (zero when
// none answers it). Callers hold r.mu.
type effect func(st *socketState, s Script) (any, *tmux.CallError)

// do runs one socket-taking call: record it, answer it from a script or the
// table, then apply virtual time, session hooks and after-call hooks, in
// that order (see recorder_hooks.go), and return. Every call but the create
// needs a bound server; a create starts one.
func (r *Recorder) do(c SocketCall, eff effect) (any, error) {
	r.mu.Lock()
	r.socketCalls = append(r.socketCalls, c)
	st := r.socket(c.Socket)
	hasServer := st.server != nil || c.Call == tmux.CallCreate
	s, scripted := r.takeScript(c.Socket, c.Call)
	var ans any
	var fail *tmux.CallError
	switch {
	case scripted && s.Failure != 0:
		labelStep := s.Failure == tmux.FailLabel && c.Call == tmux.CallCreate
		if hasServer && (s.Applied || labelStep) {
			ans, _ = eff(st, s)
		}
		if !labelStep {
			ans = nil
		}
		fail = r.scriptedError(c, s)
	case !hasServer:
		fail = noServerError(c, st)
	default:
		ans, fail = eff(st, s)
	}
	var err error
	if fail != nil {
		err = fail
	}
	hooks := r.afterCall(c)
	r.mu.Unlock()
	for _, h := range hooks {
		h(c, err)
	}
	return ans, err
}

// noServerError is the failure of a call on a socket with no bound server.
func noServerError(c SocketCall, st *socketState) *tmux.CallError {
	f := st.noServer
	if f == 0 {
		f = tmux.FailNoSocket
	}
	return &tmux.CallError{Call: c.Call, Failure: f, Socket: c.Socket, ExitStatus: 1}
}

// notFound is the failure of a call naming an id the server does not hold.
func notFound(call tmux.Call) *tmux.CallError {
	return &tmux.CallError{Call: call, Failure: tmux.FailUnrecognized, ExitStatus: 1}
}

// Lookup answers the one-call lookup from socket's table: the bound server's
// identity, with or without sessions (the lookup's identity line; LFR H5;
// b.47f), the sessions in listing order (by stored name) with each line's
// label under the scope values, and ScopeValue.
func (r *Recorder) Lookup(socket string) (tmux.LookupAnswer, error) {
	ans, err := r.do(SocketCall{Call: tmux.CallLookup, Socket: socket}, func(st *socketState, _ Script) (any, *tmux.CallError) {
		a := tmux.LookupAnswer{ServerPID: st.server.PID, ServerStart: st.server.Start}
		for _, s := range st.server.listed() {
			a.Sessions = append(a.Sessions, tmux.Session{ID: s.id, Created: s.created, Name: s.name,
				Label: st.listedLabel(s)})
		}
		a.ScopeValue = len(st.scope) > 0
		return a, nil
	})
	a, _ := ans.(tmux.LookupAnswer)
	return a, err
}

// ListPanes answers the pane listing from socket's table: every pane of the
// server, sessions in listing order, panes by window then pane index, each
// with its SeedPane.AdPane. A pane listed in several sessions
// (SeedPane.Shared) is shown once per session, with that session's id,
// window and index, as list-panes -a shows it.
func (r *Recorder) ListPanes(socket string) ([]tmux.Pane, error) {
	ans, err := r.do(SocketCall{Call: tmux.CallListPanes, Socket: socket}, func(st *socketState, _ Script) (any, *tmux.CallError) {
		var out []tmux.Pane
		for _, s := range st.server.listed() {
			panes := append([]SeedPane(nil), s.panes...)
			sort.SliceStable(panes, func(i, j int) bool {
				if panes[i].Window != panes[j].Window {
					return panes[i].Window < panes[j].Window
				}
				return panes[i].Index < panes[j].Index
			})
			for _, p := range panes {
				out = append(out, tmux.Pane{SessionID: s.id, Window: p.Window, Index: p.Index, ID: p.ID, PID: p.PID,
					AdPane: p.AdPane})
			}
		}
		return out, nil
	})
	panes, _ := ans.([]tmux.Pane)
	return panes, err
}

// KillPane removes the pane paneID from socket's table, from every session
// listing it (SeedPane.Shared); killing a session's last pane removes the
// session.
func (r *Recorder) KillPane(socket, paneID string) error {
	_, err := r.do(SocketCall{Call: tmux.CallKillPane, Socket: socket, Target: paneID}, func(st *socketState, _ Script) (any, *tmux.CallError) {
		if !st.server.removePane(paneID) {
			return nil, notFound(tmux.CallKillPane)
		}
		return nil, nil
	})
	return err
}

// KillSessionID removes the session sessionID from socket's table. A pane it
// shares with another session (SeedPane.Shared) stays listed there: the
// pane goes only when no session lists it.
func (r *Recorder) KillSessionID(socket, sessionID string) error {
	_, err := r.do(SocketCall{Call: tmux.CallKillSession, Socket: socket, Target: sessionID}, func(st *socketState, _ Script) (any, *tmux.CallError) {
		if st.server.findSession(sessionID) == nil {
			return nil, notFound(tmux.CallKillSession)
		}
		st.server.removeSession(sessionID)
		return nil, nil
	})
	return err
}

// sendEffect is a send's table effect: the pane paneID must be in the
// socket's table, else call fails as not found.
func sendEffect(paneID string, call tmux.Call) effect {
	return func(st *socketState, _ Script) (any, *tmux.CallError) {
		if _, s := st.server.findPane(paneID); s == nil {
			return nil, notFound(call)
		}
		return nil, nil
	}
}

// SendKeysPane records the text call and, when pressEnter is set and the
// text call succeeded, the Enter call, mirroring the production client. The
// pane must be in socket's table.
func (r *Recorder) SendKeysPane(socket, paneID, text string, pressEnter bool) error {
	textCall := SocketCall{Call: tmux.CallSendText, Socket: socket, Target: paneID, Text: text, PressEnter: pressEnter}
	if _, err := r.do(textCall, sendEffect(paneID, tmux.CallSendText)); err != nil || !pressEnter {
		return err
	}
	_, err := r.do(SocketCall{Call: tmux.CallSendEnter, Socket: socket, Target: paneID}, sendEffect(paneID, tmux.CallSendEnter))
	return err
}

// SendKeyPane records one key send, tmux.CallSendKey with Key key (pause's
// C-u before /exit, b.9o4), mirroring the production client. The pane must
// be in socket's table.
func (r *Recorder) SendKeyPane(socket, paneID, key string) error {
	_, err := r.do(SocketCall{Call: tmux.CallSendKey, Socket: socket, Target: paneID, Key: key},
		sendEffect(paneID, tmux.CallSendKey))
	return err
}

// CapturePaneID returns the pane's capture text (SetCapture; "" by default)
// whole, whatever nLines asks for. The pane must be in socket's table.
func (r *Recorder) CapturePaneID(socket, paneID string, nLines int, ansi bool) (string, error) {
	c := SocketCall{Call: tmux.CallCapture, Socket: socket, Target: paneID, NLines: nLines, ANSI: ansi}
	ans, err := r.do(c, func(st *socketState, _ Script) (any, *tmux.CallError) {
		if _, s := st.server.findPane(paneID); s == nil {
			return nil, notFound(tmux.CallCapture)
		}
		return r.captures[paneKey{socket, paneID}], nil
	})
	text, _ := ans.(string)
	return text, err
}

// SetLabel sets the session's label to the valid label for token,
// instanceID and storeID (as the lookup classifies "ad1 <token> <$N> <id>
// <store id>" on its own line; WD 2026-09-29 STORE), then the pane label of
// the pane paneID, anywhere on the server, to token (WD 2026-09-29c), in
// that order, as tmux runs the two steps: a session id the server does not
// hold changes nothing, and a pane id it does not hold leaves the session
// labelled; either fails. A pane listed in several sessions
// (SeedPane.Shared) takes the pane label on every listing.
func (r *Recorder) SetLabel(socket, sessionID, paneID, token, instanceID, storeID string) error {
	c := SocketCall{Call: tmux.CallSetLabel, Socket: socket, Target: sessionID, PaneID: paneID, Token: token,
		InstanceID: instanceID, StoreID: storeID}
	_, err := r.do(c, func(st *socketState, _ Script) (any, *tmux.CallError) {
		s := st.server.findSession(sessionID)
		if s == nil {
			return nil, notFound(tmux.CallSetLabel)
		}
		s.label, s.labelSet = Valid(token, instanceID, storeID), true
		if !st.server.setAdPane(paneID, token) {
			return nil, notFound(tmux.CallSetLabel)
		}
		return nil, nil
	})
	return err
}

// NewSession adds a session named name to socket's table, starting a server
// with a new identity when none is bound, and returns its create reply: a
// new session id, the server identity and a new first pane (window 0,
// pane 0). The stored name is the catalogue's stored form of name
// (StoredNames), else name. The session is labelled valid for token,
// instanceID and storeID (the five-field label; WD 2026-09-29 STORE), and
// its pane's AdPane is token (the pane label; WD 2026-09-29c), exactly when
// the production client chains the labels (!tmux.NeedsLabelByID(name)). A
// stored name the server already holds is FailDuplicate and adds nothing. A
// scripted FailLabel adds the session and its pane with neither label and
// returns its reply with the error; with Script.Applied, any other scripted
// failure adds the session (labelled by the same rule) and returns no reply.
func (r *Recorder) NewSession(socket, name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string) (tmux.CreateReply, error) {
	c := SocketCall{Call: tmux.CallCreate, Socket: socket, Target: name, Cwd: cwd, Envs: envs,
		Command: command, Token: token, InstanceID: instanceID, StoreID: storeID}
	ans, err := r.do(c, r.createEffect(socket, name, token, instanceID, storeID))
	reply, _ := ans.(tmux.CreateReply)
	return reply, err
}

// createEffect is NewSession's table effect. It runs with or without a
// bound server (a create starts one); a scripted FailLabel leaves the new
// session and its pane unlabelled. Callers hold r.mu.
func (r *Recorder) createEffect(socket, name, token, instanceID, storeID string) effect {
	return func(_ *socketState, s Script) (any, *tmux.CallError) {
		srv := r.serverFor(socket)
		stored := storedName(name)
		for _, have := range srv.sessions {
			if have.name == stored {
				return nil, &tmux.CallError{Call: tmux.CallCreate, Failure: tmux.FailDuplicate, ExitStatus: 1}
			}
		}
		seed := SeedSession{Name: stored}
		if !tmux.NeedsLabelByID(name) && s.Failure != tmux.FailLabel {
			seed.Label = Valid(token, instanceID, storeID)
			seed.Panes = []SeedPane{{AdPane: token}}
		}
		added := r.addSession(srv, seed)
		p := added.Panes[0]
		return tmux.CreateReply{SessionID: added.ID, ServerPID: srv.PID, ServerStart: srv.Start,
			PaneID: p.ID, PanePID: p.PID}, nil
	}
}

// SocketCalls returns a copy of every recorded socket-taking call, in call
// order.
func (r *Recorder) SocketCalls() []SocketCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SocketCall(nil), r.socketCalls...)
}

// SocketCallsOf returns the recorded socket-taking calls of kind call, in
// call order.
func (r *Recorder) SocketCallsOf(call tmux.Call) []SocketCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []SocketCall
	for _, c := range r.socketCalls {
		if c.Call == call {
			out = append(out, c)
		}
	}
	return out
}
