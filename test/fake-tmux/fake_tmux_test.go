package main_test

// End-to-end tests of the production internal/tmux client over the rebuilt
// fake (SR-20.3): every call kind runs as a subprocess against a per-socket
// table, each case on its own socket. Reply texts, labels and stored names
// come from the replay catalogue (tmuxfix) only.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Short bounds: generous enough for a loaded sandbox, far below the defaults'
// worst case when something hangs.
const (
	callTimeout = 5 * time.Second
	hangTimeout = time.Second
)

// Seeded server identity.
const (
	seedPID   = 4242
	seedStart = 1790549182
)

// tables is the in-process table location: beside each socket, no variable.
var tables = faketmuxfix.Tables{}

// newClient builds the production client over the fake with a create bound.
func newClient(t *testing.T, create time.Duration) *tmux.Client {
	t.Helper()
	return tmux.New(faketmuxfix.Binary(t), tmux.Timeouts{
		Query: callTimeout, Action: callTimeout, Create: create, WaitDelay: 100 * time.Millisecond,
	})
}

// newSocket returns a fresh socket path, so every case has its own table.
func newSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sock")
}

// seeded is a two-session table: alpha ($0, labelled, panes %0 %1) and beta
// ($1, unlabelled, pane %2).
func seeded() faketmuxfix.Table {
	return faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: seedPID, Start: seedStart},
		Sessions: []faketmuxfix.Session{
			{ID: "$1", Created: seedStart + 2, Name: "beta", Panes: []faketmuxfix.Pane{{ID: "%2", PID: 502}}},
			{ID: "$0", Created: seedStart + 1, Name: "alpha", Label: tmuxfix.LabelValue(tmuxfix.Token, "$0", "agent-a", tmuxfix.StoreID),
				Panes: []faketmuxfix.Pane{{ID: "%0", PID: 500}, {Index: 1, ID: "%1", PID: 501}}},
		},
	}
}

// seed writes seeded() on a fresh socket and returns the socket.
func seed(t *testing.T) string {
	t.Helper()
	socket := newSocket(t)
	tables.Write(t, socket, seeded())
	return socket
}

// create runs a chained or by-id create of name with instance id on socket.
func create(t *testing.T, c *tmux.Client, socket, name, id string) (tmux.CreateReply, error) {
	t.Helper()
	return createIn(t, c, socket, name, id, tmuxfix.StoreID)
}

// createIn is create with the given store id.
func createIn(t *testing.T, c *tmux.Client, socket, name, id, storeID string) (tmux.CreateReply, error) {
	t.Helper()
	return c.NewSession(socket, name, t.TempDir(), nil, []string{"claude"}, tmuxfix.Token, id, storeID)
}

// fiveFields is the @ad_owner value written for the session: ad1 <token> <$N> <id> <store id>.
func fiveFields(sessionID, id, storeID string) string {
	return "ad1 " + tmuxfix.Token + " " + sessionID + " " + id + " " + storeID
}

// rawLabel returns the table's stored @ad_owner value of session id on socket.
func rawLabel(t *testing.T, socket, id string) string {
	t.Helper()
	for _, s := range tables.Read(t, socket).Sessions {
		if s.ID == id {
			return s.Label
		}
	}
	t.Fatalf("table for %s has no session %s", socket, id)
	return ""
}

// invoke makes one call of kind call against seeded()'s targets.
func invoke(t *testing.T, c *tmux.Client, socket string, call tmux.Call) error {
	t.Helper()
	var err error
	switch call {
	case tmux.CallLookup:
		_, err = c.Lookup(socket)
	case tmux.CallListPanes:
		_, err = c.ListPanes(socket)
	case tmux.CallKillPane:
		err = c.KillPane(socket, "%0")
	case tmux.CallKillSession:
		err = c.KillSessionID(socket, "$0")
	case tmux.CallSendText:
		err = c.SendKeysPane(socket, "%0", "hello", false)
	case tmux.CallSendEnter:
		err = c.SendKeysPane(socket, "%0", "hello", true)
	case tmux.CallCapture:
		_, err = c.CapturePaneID(socket, "%0", 24, false)
	case tmux.CallCreate:
		_, err = create(t, c, socket, "fresh", "agent-new")
	case tmux.CallSetLabel:
		err = c.SetLabel(socket, "$1", "%2", tmuxfix.Token, "agent-b", tmuxfix.StoreID)
	default:
		t.Fatalf("no invocation for call kind %q", call)
	}
	return err
}

// lookup returns socket's lookup answer, failing the test on an error.
func lookup(t *testing.T, c *tmux.Client, socket string) tmux.LookupAnswer {
	t.Helper()
	return must[tmux.LookupAnswer](t)(c.Lookup(socket))
}

// listPanes returns socket's pane listing, failing the test on an error.
func listPanes(t *testing.T, c *tmux.Client, socket string) []tmux.Pane {
	t.Helper()
	return must[[]tmux.Pane](t)(c.ListPanes(socket))
}

// must returns a call's value, failing the test on its error.
func must[V any](t *testing.T) func(V, error) V {
	return func(v V, err error) V {
		t.Helper()
		if err != nil {
			t.Fatalf("%v", err)
		}
		return v
	}
}

// assertFailure checks err against want (0: success) for call, and the
// entry's Socket and FirstLine where they apply.
func assertFailure(t *testing.T, err error, call tmux.Call, want tmux.Failure, e tmuxfix.Entry) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("want success, got %v", err)
		}
		return
	}
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("want *tmux.CallError %v, got %v", want, err)
	}
	if ce.Call != call || ce.Failure != want {
		t.Fatalf("got %q %v, want %q %v (%v)", ce.Call, ce.Failure, call, want, err)
	}
	if e.Socket != "" && ce.Socket != e.Socket {
		t.Errorf("Socket = %q, want %q", ce.Socket, e.Socket)
	}
	if want == tmux.FailUnrecognized && ce.FirstLine != e.FirstLine {
		t.Errorf("FirstLine = %q, want %q", ce.FirstLine, e.FirstLine)
	}
}

// ids maps xs to their ids.
func ids[X any](xs []X, id func(X) string) []string {
	var out []string
	for _, x := range xs {
		out = append(out, id(x))
	}
	return out
}

// readLog parses a FAKE_TMUX_LOG file into records of argv elements.
func readLog(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var recs [][]string
	for _, rec := range strings.SplitAfter(string(data), "---\n") {
		if rec == "" {
			continue
		}
		if !strings.HasSuffix(rec, "\n---\n") {
			t.Fatalf("log record not terminated by ---: %q", rec)
		}
		recs = append(recs, strings.Split(strings.TrimSuffix(rec, "\n---\n"), "\n"))
	}
	return recs
}

// TestEveryCallKindSucceeds makes each of the nine calls against a seeded table.
func TestEveryCallKindSucceeds(t *testing.T) {
	c := newClient(t, callTimeout)
	for _, call := range tmuxfix.AllCalls() {
		t.Run(string(call), func(t *testing.T) {
			if err := invoke(t, c, seed(t), call); err != nil {
				t.Fatalf("%s: %v", call, err)
			}
		})
	}
}

// TestLookupAnswersFromTable checks sessions, typed labels, scope borrowing
// and server fields; a socket with no table answers empty.
func TestLookupAnswersFromTable(t *testing.T) {
	borrowed := func(set func(*faketmuxfix.Scope, string)) *faketmuxfix.Table {
		tb := faketmuxfix.Table{
			Server:   &faketmuxfix.Server{PID: seedPID, Start: seedStart},
			Sessions: []faketmuxfix.Session{{ID: "$1", Created: seedStart, Name: "bare", Panes: []faketmuxfix.Pane{{ID: "%1"}}}},
		}
		set(&tb.Scope, tmuxfix.LabelValue(tmuxfix.Token, "$0", "agent-x", tmuxfix.StoreID))
		return &tb
	}
	borrowedWant := tmux.LookupAnswer{ServerPID: seedPID, ServerStart: seedStart, ScopeValue: true,
		Sessions: []tmux.Session{{ID: "$1", Created: seedStart, Name: "bare"}}}
	seededTable := seeded()
	type lookupCase struct {
		name  string
		table *faketmuxfix.Table
		want  tmux.LookupAnswer
	}
	cases := []lookupCase{
		{name: "no table", want: tmux.LookupAnswer{}},
		{name: "sessions valid and none", table: &seededTable, want: tmux.LookupAnswer{
			ServerPID: seedPID, ServerStart: seedStart, Sessions: []tmux.Session{
				{ID: "$0", Created: seedStart + 1, Name: "alpha", Label: tmuxfix.Valid(tmuxfix.Token, "agent-a", tmuxfix.StoreID)},
				{ID: "$1", Created: seedStart + 2, Name: "beta"},
			}}},
		{name: "borrowed global", want: borrowedWant,
			table: borrowed(func(s *faketmuxfix.Scope, v string) { s.Global = v })},
		{name: "borrowed server", want: borrowedWant,
			table: borrowed(func(s *faketmuxfix.Scope, v string) { s.Server = v })},
		{name: "borrowed global window", want: borrowedWant,
			table: borrowed(func(s *faketmuxfix.Scope, v string) { s.GlobalWindow = v })},
	}
	for _, shape := range tmuxfix.LabelShapes() {
		want := shape.Entry().Lookup
		s := want.Sessions[0]
		cases = append(cases, lookupCase{name: "label " + shape.Name, want: want, table: &faketmuxfix.Table{
			Server: &faketmuxfix.Server{PID: want.ServerPID, Start: want.ServerStart},
			Sessions: []faketmuxfix.Session{{ID: s.ID, Created: s.Created, Name: s.Name, Label: shape.Value,
				Panes: []faketmuxfix.Pane{{ID: "%1"}}}},
		}})
	}
	c := newClient(t, callTimeout)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := newSocket(t)
			if tc.table != nil {
				tables.Write(t, socket, *tc.table)
			}
			if got := lookup(t, c, socket); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Lookup = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCreateChainLabelsOwnID checks a chained create stores the five-field
// label naming its own id and the caller's store id, and lists it valid, and
// lists its pane label; a '#' or spaces in the instance id and another
// store's id included.
func TestCreateChainLabelsOwnID(t *testing.T) {
	shapeID := map[string]string{}
	for _, s := range tmuxfix.LabelShapes() {
		shapeID[s.Name] = s.Want.InstanceID
	}
	cases := []struct{ name, session, id, storeID string }{
		{"plain id", "agent-a", "agent-a", tmuxfix.StoreID},
		{"hash in id", "agent-h", shapeID["valid-hash"], tmuxfix.StoreID},
		{"spaces in id", "agent-s", shapeID["valid-spaces"], tmuxfix.StoreID},
		{"other store", "agent-o", "agent-o", tmuxfix.OtherStoreID},
	}
	c := newClient(t, callTimeout)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.id == "" {
				t.Fatalf("the catalogue lacks the shape's instance id")
			}
			socket := newSocket(t)
			reply := must[tmux.CreateReply](t)(createIn(t, c, socket, tc.session, tc.id, tc.storeID))
			if rawLabel(t, socket, reply.SessionID) != fiveFields(reply.SessionID, tc.id, tc.storeID) {
				t.Errorf("session %s: stored label is not ad1 <token> <$N> <id> <store id>", reply.SessionID)
			}
			want := tmux.LookupAnswer{ServerPID: reply.ServerPID, ServerStart: reply.ServerStart}
			got := lookup(t, c, socket)
			if len(got.Sessions) == 1 {
				want.Sessions = []tmux.Session{{ID: reply.SessionID, Created: got.Sessions[0].Created,
					Name: tc.session, Label: tmuxfix.Valid(tmuxfix.Token, tc.id, tc.storeID)}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Lookup = %+v, want %+v", got, want)
			}
			wantPanes := []tmux.Pane{{SessionID: reply.SessionID, ID: reply.PaneID, PID: reply.PanePID, AdPane: tmuxfix.Token}}
			if panes := listPanes(t, c, socket); !reflect.DeepEqual(panes, wantPanes) {
				t.Errorf("ListPanes = %+v, want %+v", panes, wantPanes)
			}
		})
	}
}

// TestCreateByIDNameThenSetLabel checks a '$' or '\' name is created
// unlabelled under its stored form and SetLabel then labels it by id with
// the caller's store id (another store's for '\').
func TestCreateByIDNameThenSetLabel(t *testing.T) {
	c := newClient(t, callTimeout)
	storeIDs := map[string]string{`$`: tmuxfix.StoreID, `\`: tmuxfix.OtherStoreID}
	for _, marker := range []string{`$`, `\`} {
		storeID := storeIDs[marker]
		var n tmuxfix.StoredName
		for _, cand := range tmuxfix.StoredNames() {
			if cand.LabelByID && strings.Contains(cand.Raw, marker) && n.Raw == "" {
				n = cand
			}
		}
		t.Run(n.Raw, func(t *testing.T) {
			socket := newSocket(t)
			reply := must[tmux.CreateReply](t)(create(t, c, socket, n.Raw, "agent-d"))
			label := func() tmux.Label {
				ans := lookup(t, c, socket)
				if len(ans.Sessions) != 1 || ans.Sessions[0].ID != reply.SessionID || ans.Sessions[0].Name != n.Stored {
					t.Fatalf("Lookup sessions = %+v, want one %s named %q", ans.Sessions, reply.SessionID, n.Stored)
				}
				return ans.Sessions[0].Label
			}
			if got := label(); got != (tmux.Label{}) {
				t.Fatalf("label before SetLabel = %+v, want none", got)
			}
			if err := c.SetLabel(socket, reply.SessionID, reply.PaneID, tmuxfix.Token, "agent-d", storeID); err != nil {
				t.Fatalf("SetLabel: %v", err)
			}
			if rawLabel(t, socket, reply.SessionID) != fiveFields(reply.SessionID, "agent-d", storeID) {
				t.Errorf("session %s: stored label is not ad1 <token> <$N> <id> <store id>", reply.SessionID)
			}
			if got, want := label(), tmuxfix.Valid(tmuxfix.Token, "agent-d", storeID); got != want {
				t.Errorf("label after SetLabel = %+v, want %+v", got, want)
			}
		})
	}
}

// TestCreateOutcomes checks the injected create outcomes through the client:
// with the create's effect the lookup shows the labelled session, without it
// nothing is created.
func TestCreateOutcomes(t *testing.T) {
	bare := tmuxfix.Find(tmuxfix.Replies(""), "reply/nonzero-empty-stderr")
	unparseable := tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-unparseable")
	unparseableNonzero := tmuxfix.Find(tmuxfix.CreateReplies(), "create/unparseable-nonzero")
	labelFailed := tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-then-label-failed")
	cases := []struct {
		name    string
		inj     faketmuxfix.Injection
		entry   tmuxfix.Entry
		want    tmux.Failure
		timeout time.Duration
		// wantLabel is the created session's label; nil means no session.
		wantLabel *tmux.Label
	}{
		{name: "effect then hang", inj: faketmuxfix.Hang(tmux.CallCreate).WithEffect(),
			want: tmux.FailTimeout, timeout: hangTimeout, wantLabel: ptr(tmuxfix.Valid(tmuxfix.Token, "agent-o", tmuxfix.StoreID))},
		{name: "effect then exit 0 unparseable", inj: faketmuxfix.Reply(tmux.CallCreate, unparseable).WithEffect(),
			entry: unparseable, want: unparseable.Want[tmux.CallCreate], wantLabel: ptr(tmuxfix.Valid(tmuxfix.Token, "agent-o", tmuxfix.StoreID))},
		{name: "effect then non-zero unparseable", inj: faketmuxfix.Reply(tmux.CallCreate, unparseableNonzero).WithEffect(),
			entry: unparseableNonzero, want: unparseableNonzero.Want[tmux.CallCreate], wantLabel: ptr(tmuxfix.Valid(tmuxfix.Token, "agent-o", tmuxfix.StoreID))},
		{name: "non-zero with no output creates nothing", inj: faketmuxfix.ExitCode(tmux.CallCreate, bare.Exit),
			entry: bare, want: bare.Want[tmux.CallCreate]},
		{name: "chained label step fails", inj: faketmuxfix.ChainFails(),
			want: labelFailed.Want[tmux.CallCreate], wantLabel: &tmux.Label{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			timeout := callTimeout
			if tc.timeout != 0 {
				timeout = tc.timeout
			}
			c := newClient(t, timeout)
			socket := newSocket(t)
			tables.Inject(t, socket, tc.inj)
			_, err := create(t, c, socket, "agent-o", "agent-o")
			assertFailure(t, err, tmux.CallCreate, tc.want, tc.entry)
			ans := lookup(t, c, socket)
			switch {
			case tc.wantLabel == nil && len(ans.Sessions) != 0:
				t.Errorf("Lookup sessions = %+v, want none", ans.Sessions)
			case tc.wantLabel != nil && (len(ans.Sessions) != 1 || ans.Sessions[0].Label != *tc.wantLabel):
				t.Errorf("Lookup sessions = %+v, want one labelled %+v", ans.Sessions, *tc.wantLabel)
			}
		})
	}
}

// ptr returns a pointer to l.
func ptr(l tmux.Label) *tmux.Label { return &l }

// TestKillsChangeNextAnswers checks pane and session kills show in the next
// lookup and pane listing.
func TestKillsChangeNextAnswers(t *testing.T) {
	c := newClient(t, callTimeout)
	cases := []struct {
		name         string
		kill         func(socket string) error
		wantSessions []string
		wantPanes    []string
	}{
		{name: "none", kill: func(string) error { return nil },
			wantSessions: []string{"$0", "$1"}, wantPanes: []string{"%0", "%1", "%2"}},
		{name: "pane of two", kill: func(s string) error { return c.KillPane(s, "%1") },
			wantSessions: []string{"$0", "$1"}, wantPanes: []string{"%0", "%2"}},
		{name: "last pane removes session", kill: func(s string) error { return c.KillPane(s, "%2") },
			wantSessions: []string{"$0"}, wantPanes: []string{"%0", "%1"}},
		{name: "session", kill: func(s string) error { return c.KillSessionID(s, "$0") },
			wantSessions: []string{"$1"}, wantPanes: []string{"%2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := seed(t)
			if err := tc.kill(socket); err != nil {
				t.Fatalf("kill: %v", err)
			}
			sessions := ids(lookup(t, c, socket).Sessions, func(s tmux.Session) string { return s.ID })
			if !reflect.DeepEqual(sessions, tc.wantSessions) {
				t.Errorf("sessions = %v, want %v", sessions, tc.wantSessions)
			}
			panes := ids(listPanes(t, c, socket), func(p tmux.Pane) string { return p.ID })
			if !reflect.DeepEqual(panes, tc.wantPanes) {
				t.Errorf("panes = %v, want %v", panes, tc.wantPanes)
			}
		})
	}
	t.Run("listing fields", func(t *testing.T) {
		want := []tmux.Pane{{SessionID: "$0", ID: "%0", PID: 500},
			{SessionID: "$0", Index: 1, ID: "%1", PID: 501}, {SessionID: "$1", ID: "%2", PID: 502}}
		if got := listPanes(t, c, seed(t)); !reflect.DeepEqual(got, want) {
			t.Errorf("ListPanes = %+v, want %+v", got, want)
		}
	})
}

// TestSendAndCaptureLogged checks text, Enter and capture by pane id work and
// are logged with -u -S <socket> first.
func TestSendAndCaptureLogged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
	t.Setenv(faketmuxfix.EnvLog, logPath)
	c := newClient(t, callTimeout)
	plain := tmuxfix.Find(tmuxfix.Captures(), "capture/P1").Stdout
	ansi := tmuxfix.Find(tmuxfix.Captures(), "capture/ansi").Stdout
	socket := newSocket(t)
	tb := seeded()
	alpha := &tb.Sessions[1] // panes %0 and %1
	alpha.Panes[0].Capture, alpha.Panes[1].Capture = plain, ansi
	tables.Write(t, socket, tb)

	if err := c.SendKeysPane(socket, "%0", "hello there", true); err != nil {
		t.Fatalf("SendKeysPane: %v", err)
	}
	for _, cc := range []struct {
		pane string
		ansi bool
		want string
	}{{"%0", false, plain}, {"%1", true, ansi}} {
		if got, err := c.CapturePaneID(socket, cc.pane, 24, cc.ansi); err != nil || got != cc.want {
			t.Errorf("CapturePaneID(%s) = %q, %v; want %q", cc.pane, got, err, cc.want)
		}
	}

	recs := readLog(t, logPath)
	wantCmds := []struct{ cmd, pane string }{{"send-keys", "%0"}, {"send-keys", "%0"}, {"capture-pane", "%0"}, {"capture-pane", "%1"}}
	if len(recs) != len(wantCmds) {
		t.Fatalf("log has %d records, want %d: %q", len(recs), len(wantCmds), recs)
	}
	for i, w := range wantCmds {
		rec := recs[i]
		if len(rec) < 5 || !reflect.DeepEqual(rec[1:4], []string{"-u", "-S", socket}) || rec[4] != w.cmd {
			t.Errorf("record %d = %q, want -u -S %s %s first", i, rec, socket, w.cmd)
			continue
		}
		if !strings.Contains(strings.Join(rec, "\n"), "\n-t\n"+w.pane+"\n") {
			t.Errorf("record %d = %q, want target %s", i, rec, w.pane)
		}
	}
}

// TestSendTextAfterDoubleDash checks the fake accepts socket-form
// `send-keys -t %N -l -- <text>` for a text tmux would otherwise read as
// flags or as a command separator, logging the text as the client sent it (a
// final ";" escaped as `\;`).
func TestSendTextAfterDoubleDash(t *testing.T) {
	c := newClient(t, callTimeout)
	for _, tc := range []struct{ text, sent string }{
		{"-x", "-x"},
		{"--", "--"},
		{";", `\;`},
		{"a;", `a\;`},
		{"a ;", `a \;`},
		{`a\;`, `a\\;`},
		{";a", ";a"},
		{"a;b", "a;b"},
		{"-x;", `-x\;`},
	} {
		t.Run(tc.text, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
			t.Setenv(faketmuxfix.EnvLog, logPath)
			socket := seed(t)
			if err := c.SendKeysPane(socket, "%0", tc.text, false); err != nil {
				t.Fatalf("SendKeysPane(%q): %v", tc.text, err)
			}
			recs := readLog(t, logPath)
			want := []string{"-u", "-S", socket, "send-keys", "-t", "%0", "-l", "--", tc.sent}
			if len(recs) != 1 || !reflect.DeepEqual(recs[0][1:], want) {
				t.Errorf("log = %q, want one record with argv %q", recs, want)
			}
		})
	}
}

// TestInjectedRepliesYieldTypedKind injects each catalogue reply once on one
// call kind it is recorded for; permission and duplicate replies on the create.
func TestInjectedRepliesYieldTypedKind(t *testing.T) {
	c := newClient(t, callTimeout)
	// Names and call kinds do not depend on the socket; each case rebuilds
	// its entry for its own socket, as the fake does.
	for i, named := range append(tmuxfix.Replies(""), tmuxfix.CreateReplies()...) {
		calls := named.Calls()
		call := calls[i%len(calls)]
		if named.Name == tmuxfix.SocketDenied("").Name || strings.HasPrefix(named.Name, tmuxfix.Duplicate("").Name) {
			call = tmux.CallCreate
		}
		t.Run(named.Name+" on "+string(call), func(t *testing.T) {
			socket := seed(t)
			e, ok := faketmuxfix.ResolveEntry(named.Name, socket)
			if !ok {
				t.Fatalf("no catalogue entry %q", named.Name)
			}
			tables.Inject(t, socket, faketmuxfix.Reply(call, e))
			assertFailure(t, invoke(t, c, socket, call), call, e.Want[call], e)
		})
	}
}

// TestSocketsIsolated checks tables and injections on one socket leave
// another's answers alone.
func TestSocketsIsolated(t *testing.T) {
	c := newClient(t, callTimeout)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, s := range []string{a, b} {
		if _, err := create(t, c, s, "agent-i", "agent-i"); err != nil {
			t.Fatalf("NewSession on %s: %v", s, err)
		}
	}
	noServer := tmuxfix.NoServer(b)
	tables.Inject(t, b, faketmuxfix.Reply(tmux.CallLookup, noServer))
	if ans := lookup(t, c, a); len(ans.Sessions) != 1 || ans.Sessions[0].Label != tmuxfix.Valid(tmuxfix.Token, "agent-i", tmuxfix.StoreID) {
		t.Errorf("Lookup(a) sessions = %+v, want one labelled session", ans.Sessions)
	}
	_, err := c.Lookup(b)
	assertFailure(t, err, tmux.CallLookup, noServer.Want[tmux.CallLookup], noServer)
}
