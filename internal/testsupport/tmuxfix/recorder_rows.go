package tmuxfix

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// RowSessionOption adjusts the session SeedRowSession seeds (SR-20.3: another
// creation time, label or name).
type RowSessionOption func(*rowSession)

// rowSession holds SeedRowSession's option values.
type rowSession struct {
	created  int64
	name     string
	hasName  bool
	label    tmux.Label
	labelSet bool
	hasLabel bool
}

// WithRowSessionCreated gives the session's creation time, epoch seconds.
func WithRowSessionCreated(epoch int64) RowSessionOption {
	return func(o *rowSession) { o.created = epoch }
}

// WithRowSessionName gives the session's stored name instead of the row's
// session name (take $, \, '.' and ':' forms from StoredNames).
func WithRowSessionName(stored string) RowSessionOption {
	return func(o *rowSession) { o.name, o.hasName = stored, true }
}

// WithRowSessionLabel gives the session another label instead of the row's
// current one: Valid(OtherToken, id) for an old label, Valid(token, another
// id) for a foreign one, or a LabelShape's Want. set is SeedSession.LabelSet:
// true with a LabelNone label for a malformed or borrowed value, false for
// none (unset).
func WithRowSessionLabel(label tmux.Label, set bool) RowSessionOption {
	return func(o *rowSession) { o.label, o.labelSet, o.hasLabel = label, set, true }
}

// SeedRowSession makes the Recorder's table hold the session of the row
// instanceID of the store at dbPath, seeded through apitest.SeedSpawn
// (SR-20.2, SR-20.3), and returns it as stored. The session is on the
// row's socket, with the row's pane (a new pane when the row records none),
// the stored form of the row's session name, and the label valid for the
// row's id and launch token as read from the store, so a test never copies
// the token. Its creation time is the bound clock's current second
// (WithVirtualTime), else the wall clock's. When the socket has no server,
// one is started with the row's recorded server identity (new values for
// what the row does not record). Options give another creation time, label
// or name. The test fails when the row cannot be read, records no socket,
// records no well-formed token and no other label is given, or when its
// pane is already on the server (seed rows that share a socket with
// distinct panes through apitest.WithLaunchIdentity).
func (r *Recorder) SeedRowSession(t testing.TB, dbPath, instanceID string, opts ...RowSessionOption) SeedSession {
	t.Helper()
	row := readRow(t, dbPath, instanceID)
	o := rowSession{}
	for _, opt := range opts {
		opt(&o)
	}
	id := row.Identity
	if id.Socket == "" {
		t.Fatalf("tmuxfix.SeedRowSession: row %s records no tmux socket", instanceID)
	}
	seed := SeedSession{Name: storedName(row.TmuxSessionName), Created: o.created,
		Label: Valid(id.Token, instanceID), LabelSet: true}
	if o.hasName {
		seed.Name = o.name
	}
	if o.hasLabel {
		seed.Label, seed.LabelSet = o.label, o.labelSet
	} else if id.Token == "" {
		t.Fatalf("tmuxfix.SeedRowSession: row %s records no well-formed launch token; give WithRowSessionLabel", instanceID)
	}
	if id.PaneID != "" {
		seed.Panes = []SeedPane{{ID: id.PaneID, PID: id.PanePID}}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	srv := r.socket(id.Socket).server
	if srv == nil {
		srv = r.bind(id.Socket, Server{PID: id.ServerPID, Start: id.ServerStart, ProcStart: id.ServerStarttime})
	}
	if _, owner := srv.findPane(id.PaneID); id.PaneID != "" && owner != nil {
		t.Fatalf("tmuxfix.SeedRowSession: pane %s of row %s is already in session %s on %s",
			id.PaneID, instanceID, owner.id, id.Socket)
	}
	return r.addSession(srv, seed)
}

// readRow reads the row instanceID through the store, failing the test when
// it cannot.
func readRow(t testing.TB, dbPath, instanceID string) store.Spawn {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("tmuxfix.SeedRowSession: open store: %v", err)
	}
	defer s.Close() //nolint:errcheck // read-only use
	row, err := s.GetSpawn(instanceID)
	if err != nil {
		t.Fatalf("tmuxfix.SeedRowSession: read row %s: %v", instanceID, err)
	}
	return row
}
