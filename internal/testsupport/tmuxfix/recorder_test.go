package tmuxfix_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Shared fixtures for the Recorder tests: two sockets, a one-session
// Recorder and one invoker per socket-taking call kind.

const (
	sockA = "/tmp/tmux-test/a"
	sockB = "/tmp/tmux-test/b"
	agent = "agent-x"
)

// seeded returns a Recorder whose sockA server holds session $0 ("s0") with pane %0.
func seeded() *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().SeedSessions(sockA, sess("$0", "s0", tmux.Label{}, false, "%0"))
}

// sess builds a seeded session; paneIDs give one window-0 pane each (none: one auto pane).
func sess(id, name string, l tmux.Label, set bool, paneIDs ...string) tmuxfix.SeedSession {
	s := tmuxfix.SeedSession{ID: id, Name: name, Label: l, LabelSet: set}
	for i, p := range paneIDs {
		s.Panes = append(s.Panes, tmuxfix.SeedPane{Window: 0, Index: i, ID: p})
	}
	return s
}

// invoker makes one socket-taking call against seeded()'s session and pane.
type invoker struct {
	call tmux.Call
	do   func(r *tmuxfix.Recorder, sock string) error
}

// invokers covers every call kind; the Enter invoker sends text then Enter.
var invokers = []invoker{
	{tmux.CallLookup, func(r *tmuxfix.Recorder, s string) error { _, err := r.Lookup(s); return err }},
	{tmux.CallListPanes, func(r *tmuxfix.Recorder, s string) error { _, err := r.ListPanes(s); return err }},
	{tmux.CallKillPane, func(r *tmuxfix.Recorder, s string) error { return r.KillPane(s, "%0") }},
	{tmux.CallKillSession, func(r *tmuxfix.Recorder, s string) error { return r.KillSessionID(s, "$0") }},
	{tmux.CallSendText, func(r *tmuxfix.Recorder, s string) error { return r.SendKeysPane(s, "%0", "hi", false) }},
	{tmux.CallSendEnter, func(r *tmuxfix.Recorder, s string) error { return r.SendKeysPane(s, "%0", "hi", true) }},
	{tmux.CallSendKey, func(r *tmuxfix.Recorder, s string) error { return r.SendKeyPane(s, "%0", "C-u") }},
	{tmux.CallCapture, func(r *tmuxfix.Recorder, s string) error { _, err := r.CapturePaneID(s, "%0", 10, false); return err }},
	{tmux.CallCreate, func(r *tmuxfix.Recorder, s string) error {
		_, err := r.NewSession(s, "new", "/tmp", nil, []string{"claude"}, tmuxfix.Token, agent, tmuxfix.StoreID)
		return err
	}},
	{tmux.CallSetLabel, func(r *tmuxfix.Recorder, s string) error {
		return r.SetLabel(s, "$0", "%0", tmuxfix.Token, agent, tmuxfix.StoreID)
	}},
}

// callErr returns err as a *tmux.CallError, failing the test otherwise.
func callErr(t *testing.T, err error) *tmux.CallError {
	t.Helper()
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v (%T), want *tmux.CallError", err, err)
	}
	return ce
}

// paneCounts maps each of socket's session ids to its pane count.
func paneCounts(r *tmuxfix.Recorder, sock string) map[string]int {
	out := map[string]int{}
	for _, s := range r.Sessions(sock) {
		out[s.ID] = len(s.Panes)
	}
	return out
}

// lookup returns socket's lookup answer, failing the test on an error.
func lookup(t *testing.T, r *tmuxfix.Recorder, sock string) tmux.LookupAnswer {
	t.Helper()
	a, err := r.Lookup(sock)
	if err != nil {
		t.Fatalf("Lookup(%s): %v", sock, err)
	}
	return a
}

// TestRecorder_LookupLabels: each line's typed label under own labels and the
// scope values (server, then global-window, then own, then global).
func TestRecorder_LookupLabels(t *testing.T) {
	cur, old := tmuxfix.Valid(tmuxfix.Token, agent, tmuxfix.StoreID), tmuxfix.Valid(tmuxfix.OtherToken, agent, tmuxfix.StoreID)
	foreign := tmuxfix.Valid(tmuxfix.Token, "agent-y", tmuxfix.StoreID)
	none := tmux.Label{}
	owned := func(id string) tmuxfix.ScopeValue { return tmuxfix.ScopeValue{SessionID: id, Label: cur} }
	cases := []struct {
		name      string
		s0, s1    tmuxfix.SeedSession
		scope     map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue
		want      [2]tmux.Label
		wantScope bool
	}{
		{name: "valid-and-old", s0: sess("$0", "a", cur, false), s1: sess("$1", "b", old, false), want: [2]tmux.Label{cur, old}},
		{name: "foreign", s0: sess("$0", "a", foreign, false), s1: sess("$1", "b", none, false), want: [2]tmux.Label{foreign, none}},
		{name: "malformed-own-value", s0: sess("$0", "a", none, true), s1: sess("$1", "b", none, false), want: [2]tmux.Label{none, none}},
		{name: "borrowed-global", s0: sess("$0", "a", none, false), s1: sess("$1", "b", none, false),
			scope: map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue{tmuxfix.ScopeGlobal: owned("$0")}, want: [2]tmux.Label{cur, none}, wantScope: true},
		{name: "own-beats-global", s0: sess("$0", "a", old, false), s1: sess("$1", "b", none, true),
			scope: map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue{tmuxfix.ScopeGlobal: owned("$0")}, want: [2]tmux.Label{old, none}, wantScope: true},
		{name: "server-beats-own", s0: sess("$0", "a", old, false), s1: sess("$1", "b", foreign, false),
			scope: map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue{tmuxfix.ScopeServer: owned("$0")}, want: [2]tmux.Label{cur, none}, wantScope: true},
		{name: "global-window-beats-own", s0: sess("$0", "a", none, false), s1: sess("$1", "b", foreign, false),
			scope: map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue{tmuxfix.ScopeGlobalWindow: owned("$1")}, want: [2]tmux.Label{none, cur}, wantScope: true},
		{name: "server-beats-global-window", s0: sess("$0", "a", none, false), s1: sess("$1", "b", none, false),
			scope: map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue{tmuxfix.ScopeServer: owned("$1"), tmuxfix.ScopeGlobalWindow: owned("$0")},
			want:  [2]tmux.Label{none, cur}, wantScope: true},
		{name: "malformed-scope-value", s0: sess("$0", "a", none, false), s1: sess("$1", "b", cur, false),
			scope: map[tmuxfix.ScopeLevel]tmuxfix.ScopeValue{tmuxfix.ScopeGlobal: {}}, want: [2]tmux.Label{none, cur}, wantScope: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tmuxfix.NewRecorder().SeedSessions(sockA, tc.s0, tc.s1)
			for lvl, v := range tc.scope {
				r.SetScope(sockA, lvl, v)
			}
			a := lookup(t, r, sockA)
			if got := [2]tmux.Label{a.Sessions[0].Label, a.Sessions[1].Label}; got != tc.want {
				t.Errorf("labels = %+v, want %+v", got, tc.want)
			}
			if a.ScopeValue != tc.wantScope {
				t.Errorf("ScopeValue = %v, want %v", a.ScopeValue, tc.wantScope)
			}
		})
	}
}

// TestRecorder_LookupLabelShapes: each catalogue label shape's typed class is
// listed as seeded; a shape with a scope-section line reports ScopeValue.
func TestRecorder_LookupLabelShapes(t *testing.T) {
	for _, shape := range tmuxfix.LabelShapes() {
		t.Run(shape.Name, func(t *testing.T) {
			r := tmuxfix.NewRecorder().SeedSessions(sockA, sess(tmuxfix.LabelLineID, "a", shape.Want, true))
			if shape.ScopeValue {
				r.SetScope(sockA, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
			}
			a := lookup(t, r, sockA)
			if a.Sessions[0].Label != shape.Want || a.ScopeValue != shape.ScopeValue {
				t.Errorf("label %+v scope %v, want %+v scope %v", a.Sessions[0].Label, a.ScopeValue, shape.Want, shape.ScopeValue)
			}
		})
	}
}

// TestRecorder_Servers: no-server failures, and restart, re-bind and stop as
// seen by the lookup, new ids and Servers.
func TestRecorder_Servers(t *testing.T) {
	first, second := tmuxfix.Server{PID: 100, Start: 1000}, tmuxfix.Server{PID: 200, Start: 2000}
	cases := []struct {
		name        string
		act         func(r *tmuxfix.Recorder)
		wantFailure tmux.Failure // 0: the lookup answers from the new server
		wantOld     tmuxfix.ServerStatus
	}{
		{"restart", func(r *tmuxfix.Recorder) { r.RestartServer(sockA, second) }, 0,
			tmuxfix.ServerStatus{Server: first, Socket: sockA}},
		{"rebind", func(r *tmuxfix.Recorder) { r.RebindServer(sockA, second) }, 0,
			tmuxfix.ServerStatus{Server: first, Socket: sockA, Running: true}},
		{"stop", func(r *tmuxfix.Recorder) { r.StopServer(sockA) }, tmux.FailNoSocket,
			tmuxfix.ServerStatus{Server: first, Socket: sockA}},
		{"stop-stale-socket", func(r *tmuxfix.Recorder) { r.StopServer(sockA).SetNoServerFailure(sockA, tmux.FailNoServer) },
			tmux.FailNoServer, tmuxfix.ServerStatus{Server: first, Socket: sockA}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tmuxfix.NewRecorder().StartServer(sockA, first).
				SeedSessions(sockA, sess("$3", "s", tmux.Label{}, false, "%5")).
				SetScope(sockA, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
			tc.act(r)
			if got := r.Servers()[0]; got != tc.wantOld {
				t.Errorf("old server = %+v, want %+v", got, tc.wantOld)
			}
			if tc.wantFailure != 0 {
				_, err := r.Lookup(sockA)
				if ce := callErr(t, err); ce.Failure != tc.wantFailure || ce.Socket != sockA || ce.Call != tmux.CallLookup {
					t.Errorf("CallError = %+v, want %v on %s", ce, tc.wantFailure, sockA)
				}
				return
			}
			// b.47f: an empty server's answer still names it.
			if a := lookup(t, r, sockA); len(a.Sessions) != 0 || a.ServerPID != second.PID || a.ServerStart != second.Start || a.ScopeValue {
				t.Errorf("lookup after %s = %+v, want the new server, empty, with no scope value", tc.name, a)
			}
			reply, err := r.NewSession(sockA, "n", "/tmp", nil, nil, tmuxfix.Token, agent, tmuxfix.StoreID)
			if err != nil {
				t.Fatal(err)
			}
			want := tmux.CreateReply{SessionID: "$0", ServerPID: 200, ServerStart: 2000, PaneID: "%0", PanePID: reply.PanePID}
			if reply != want {
				t.Errorf("create reply = %+v, want %+v (ids counted again)", reply, want)
			}
			if got := r.Servers()[1]; got != (tmuxfix.ServerStatus{Server: second, Socket: sockA, Bound: true, Running: true}) {
				t.Errorf("new server = %+v", got)
			}
		})
	}
}

// TestRecorder_ListPanes: panes by session listing order then window and
// pane index, with their pane labels, from the named socket only.
func TestRecorder_ListPanes(t *testing.T) {
	r := tmuxfix.NewRecorder().
		SeedSessions(sockA,
			tmuxfix.SeedSession{ID: "$1", Name: "b", Panes: []tmuxfix.SeedPane{{Window: 1, Index: 0, ID: "%3", PID: 13}, {Window: 0, Index: 1, ID: "%2", PID: 12, AdPane: tmuxfix.OtherToken}, {Window: 0, Index: 0, ID: "%1", PID: 11}}},
			tmuxfix.SeedSession{ID: "$0", Name: "a", Panes: []tmuxfix.SeedPane{{ID: "%0", PID: 10, AdPane: tmuxfix.Token}}}).
		SeedSessions(sockB, tmuxfix.SeedSession{ID: "$0", Name: "x", Panes: []tmuxfix.SeedPane{{ID: "%9", PID: 99}}})
	got, err := r.ListPanes(sockA)
	if err != nil {
		t.Fatal(err)
	}
	want := []tmux.Pane{
		{SessionID: "$0", ID: "%0", PID: 10, AdPane: tmuxfix.Token},
		{SessionID: "$1", Window: 0, Index: 0, ID: "%1", PID: 11},
		{SessionID: "$1", Window: 0, Index: 1, ID: "%2", PID: 12, AdPane: tmuxfix.OtherToken},
		{SessionID: "$1", Window: 1, Index: 0, ID: "%3", PID: 13},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListPanes = %+v, want %+v", got, want)
	}
}

// TestRecorder_SendKeysPane: text then Enter as two recorded calls; no Enter
// without pressEnter or after a failed text call.
func TestRecorder_SendKeysPane(t *testing.T) {
	cases := []struct {
		name, pane string
		pressEnter bool
		want       []tmux.Call
	}{
		{"text-and-enter", "%0", true, []tmux.Call{tmux.CallSendText, tmux.CallSendEnter}},
		{"text-only", "%0", false, []tmux.Call{tmux.CallSendText}},
		{"unknown-pane", "%9", true, []tmux.Call{tmux.CallSendText}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := seeded()
			if err := r.SendKeysPane(sockA, tc.pane, "hello", tc.pressEnter); (err != nil) != (tc.pane == "%9") {
				t.Fatalf("SendKeysPane: %v", err)
			} else if err != nil && callErr(t, err).Call != tmux.CallSendText {
				t.Errorf("SendKeysPane err = %v; want a failed %v call", err, tmux.CallSendText)
			}
			var got []tmux.Call
			for _, c := range r.SocketCalls() {
				got = append(got, c.Call)
				if c.Target != tc.pane || c.Socket != sockA {
					t.Errorf("recorded %+v, want target %s on %s", c, tc.pane, sockA)
				}
			}
			if first := r.SocketCalls()[0]; !reflect.DeepEqual(got, tc.want) || first.Text != "hello" || first.PressEnter != tc.pressEnter {
				t.Errorf("calls = %v, text call %+v; want %v", got, first, tc.want)
			}
		})
	}
}

// TestRecorder_CapturePaneID: the pane's capture text whole (default empty),
// with its arguments recorded; an unknown pane fails.
func TestRecorder_CapturePaneID(t *testing.T) {
	r := tmuxfix.NewRecorder().
		SeedSessions(sockA, sess("$0", "a", tmux.Label{}, false, "%0", "%1")).
		SetCapture(sockA, "%0", "line1\nline2\n")
	for _, c := range []struct{ pane, want string }{{"%0", "line1\nline2\n"}, {"%1", ""}} {
		got, err := r.CapturePaneID(sockA, c.pane, 1, true)
		if err != nil || got != c.want {
			t.Errorf("CapturePaneID(%s) = %q, %v; want %q", c.pane, got, err, c.want)
		}
	}
	if c := r.SocketCallsOf(tmux.CallCapture)[0]; c.NLines != 1 || !c.ANSI || c.Target != "%0" {
		t.Errorf("recorded capture = %+v", c)
	}
	if _, err := r.CapturePaneID(sockA, "%9", 1, false); callErr(t, err).Failure != tmux.FailUnrecognized {
		t.Errorf("unknown pane error = %v", err)
	}
}

// readPaneRow seeds the row id (the apitest defaults: a socket, a pane and no
// server identity) and returns the lookup's view of it.
func readPaneRow(t *testing.T, id string) (dbPath string, launch tmux.Launch) {
	t.Helper()
	dbPath = filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, id, "waiting", "/tmp", "off", "", true); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck // read-only use
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	ri := row.Identity
	return dbPath, tmux.Launch{InstanceID: id, Token: ri.Token, StoreID: s.StoreID(), Socket: ri.Socket,
		ServerPID: ri.ServerPID, ServerStart: ri.ServerStart, ServerStarttime: ri.ServerStarttime}
}

// TestRecorder_HasSessionAnswersNotFound: the name-based HasSession records
// its call and answers not found even for a seeded name, with no socket call;
// Reset clears the record.
func TestRecorder_HasSessionAnswersNotFound(t *testing.T) {
	r := seeded()
	if got, err := r.HasSession("s0"); got || err != nil {
		t.Errorf("HasSession(s0) = %v, %v; want false, nil", got, err)
	}
	want := []tmuxfix.Call{{Kind: tmuxfix.CallHasSession, Name: "s0"}}
	if got := r.CallsOfKind(tmuxfix.CallHasSession); !reflect.DeepEqual(got, want) {
		t.Errorf("HasSession calls = %+v, want %+v", got, want)
	}
	if got := r.SocketCalls(); len(got) != 0 {
		t.Errorf("socket calls = %+v, want none", got)
	}
	r.Reset()
	if got := r.Calls(); len(got) != 0 {
		t.Errorf("calls after Reset = %+v, want none", got)
	}
}

// TestNewRecorderForResume: an empty listing, so the lookup on any socket
// lists no session and fails as no socket.
func TestNewRecorderForResume(t *testing.T) {
	a, err := tmuxfix.NewRecorderForResume().Lookup(sockA)
	if ce := callErr(t, err); ce.Failure != tmux.FailNoSocket || len(a.Sessions) != 0 {
		t.Errorf("Lookup = %+v, failure %v; want no session, %v", a, ce.Failure, tmux.FailNoSocket)
	}
}
