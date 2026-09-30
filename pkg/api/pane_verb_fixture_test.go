package api_test

// pane_verb_fixture_test.go extends the kill fixture (killEnv, killRow) for
// the pane verbs (read-pane now; send-keys and pause later; SR-20.2): the
// read-pane invocations, per-pane capture texts, extra session seeds, the
// recorded-call assertions and the "changes nothing" readers. It holds no tests.

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// paneWriteCalls are the call kinds that change tmux: sends, creates, kills and label writes.
var paneWriteCalls = []tmux.Call{tmux.CallSendText, tmux.CallSendEnter, tmux.CallCreate, tmux.CallKillPane,
	tmux.CallKillSession, tmux.CallSetLabel}

// readPane runs the exported api.ReadPane with e.store and e.rec; its server
// and pane checks use the production start-time reader, not e.pc.
func (e *killEnv) readPane(p api.ReadPaneParams) (api.ReadPaneResult, error) {
	return api.ReadPane(e.store, e.rec, p)
}

// readPaneClient runs Client.ReadPane on a new e.client (e.pc as its reader),
// for the cases where the server or a process check matters.
func (e *killEnv) readPaneClient(t *testing.T, p api.ReadPaneParams, settings ...apitest.TmuxSetting) (api.ReadPaneResult, error) {
	t.Helper()
	c, _ := e.client(t, settings...)
	return c.ReadPane(p)
}

// paneText is the capture text setPaneTexts gives paneID on socket.
func paneText(socket, paneID string) string { return "pane " + paneID + " on " + socket + "\n" }

// setPaneTexts gives every pane now on socket its own capture text
// (paneText), so a result shows which pane was read. Call it after seeding.
func (e *killEnv) setPaneTexts(socket string) {
	for _, s := range e.rec.Sessions(socket) {
		for _, p := range s.Panes {
			e.rec.SetCapture(socket, p.ID, paneText(socket, p.ID))
		}
	}
}

// newToken returns a well-formed launch token no row records.
func newToken() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:16] }

// pane is r's agent pane as seeded: its recorded pane id, agent pid and the row's pane label.
func (r killRow) pane() tmuxfix.SeedPane {
	return tmuxfix.SeedPane{ID: r.Spawn.Identity.PaneID, PID: r.AgentPID, AdPane: r.Token}
}

// seedOurs seeds r's current-labelled session under r.Name with panes (a
// moved, respawned or missing agent pane); for a row with no recorded pane,
// the pane carrying r's token becomes the agent's.
func (e *killEnv) seedOurs(t *testing.T, r *killRow, panes ...tmuxfix.SeedPane) {
	t.Helper()
	e.ensureServer(r)
	r.Session = e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name, Label: r.current(), Panes: panes})
	for _, p := range r.Session.Panes {
		if r.Spawn.Identity.PaneID == "" && p.AdPane == r.Token {
			e.setAgent(r, p.PID, apitest.LinuxProcStarttime)
		}
	}
	e.syncServers()
}

// seedLeftover seeds a leftover of an earlier launch of r with token under a
// new name: this store's label for r's id and one pane carrying token.
func (e *killEnv) seedLeftover(t *testing.T, r killRow, token string) tmuxfix.SeedSession {
	t.Helper()
	e.ensureServer(&r)
	return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "leftover-" + uuid.NewString()[:8],
		Label: tmuxfix.Valid(token, r.ID, r.StoreID), Panes: []tmuxfix.SeedPane{{AdPane: token}}})
}

// assertPaneCalls fails unless the socket-taking calls are exactly want in
// order, none is name-based and every call names its pane or session by id.
func (e *killEnv) assertPaneCalls(t *testing.T, want ...tmux.Call) {
	t.Helper()
	e.assertKillCalls(t, want...)
	for _, c := range e.rec.SocketCalls() {
		switch c.Call {
		case tmux.CallCapture, tmux.CallSendText, tmux.CallSendEnter:
			if !killPaneIDRe.MatchString(c.Target) {
				t.Errorf("%v targets %q; want a pane id", c.Call, c.Target)
			}
		}
	}
}

// assertCaptured fails unless exactly one capture was made, of paneID with nLines and ansi.
func (e *killEnv) assertCaptured(t *testing.T, paneID string, nLines int, ansi bool) {
	t.Helper()
	got := e.rec.SocketCallsOf(tmux.CallCapture)
	if len(got) != 1 {
		t.Fatalf("captures = %+v; want one of %s", got, paneID)
	}
	if c := got[0]; c.Target != paneID || c.NLines != nLines || c.ANSI != ansi {
		t.Errorf("capture of %q, n_lines %d, ansi %v; want %q, %d, %v", c.Target, c.NLines, c.ANSI, paneID, nLines, ansi)
	}
}

// assertNoCalls fails when a recorded socket-taking call is of any of kinds.
func (e *killEnv) assertNoCalls(t *testing.T, kinds ...tmux.Call) {
	t.Helper()
	for _, c := range e.rec.SocketCalls() {
		if slices.Contains(kinds, c.Call) {
			t.Errorf("recorded %v of %q; want no %v", c.Call, c.Target, kinds)
		}
	}
}

// assertRowUnchanged fails unless id's row reads back exactly as before
// (every column: the snapshot, row_version and the identity).
func (e *killEnv) assertRowUnchanged(t *testing.T, id string, before apitest.SpawnColumns) {
	t.Helper()
	if after := e.columns(t, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row %s changed:\n got %+v\nwant %+v", id, after, before)
	}
}

// trailMark is the trail's current record count, for assertNoTrailSince.
func trailMark(t *testing.T) int {
	t.Helper()
	return len(readAPITrailLines(t))
}

// assertNoTrailSince fails when a trail record of any event for id was
// written after mark.
func assertNoTrailSince(t *testing.T, mark int, id string) {
	t.Helper()
	for _, l := range readAPITrailLines(t)[mark:] {
		if l["claude_instance_id"] == id {
			t.Errorf("trail record %v for %s; want none", l["event"], id)
		}
	}
}
