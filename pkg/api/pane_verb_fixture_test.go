package api_test

// pane_verb_fixture_test.go extends the kill fixture (killEnv, killRow) for
// the pane verbs (read-pane and send-keys; pause's own pieces are
// pause_fixture_test.go; SR-20.2): their
// invocations and repeatable runs, per-pane capture texts, extra session and
// pending-launch seeds, the recorded-call lists and assertions, the
// between-read-and-send writes and the "changes nothing" readers. It holds no tests.
// A later verb that acts on the agent's pane must extend this fixture and
// pause_fixture_test.go, not copy them.

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killStore is send-keys' store too.
var _ api.SendKeysStore = (*killStore)(nil)

// PermissionRequestsForSpawn returns failPermissionRequests' error, else delegates.
func (w *killStore) PermissionRequestsForSpawn(id string) ([]api.PermissionRow, error) {
	if w.permErr != nil {
		return nil, w.permErr
	}
	return w.st.PermissionRequestsForSpawn(id)
}

// failPermissionRequests makes every later relay-guard read return err
// (errInjectedStore when nil).
func (w *killStore) failPermissionRequests(err error) { w.permErr = orInjected(err) }

// paneWriteCalls are the call kinds that change tmux: sends, creates, kills and label writes.
var paneWriteCalls = []tmux.Call{tmux.CallSendText, tmux.CallSendEnter, tmux.CallCreate, tmux.CallKillPane,
	tmux.CallKillSession, tmux.CallSetLabel}

// The pane verbs' call sequences: a read (lookup, listing, capture), a
// delivery (lookup, listing, text, Enter) and a text call that failed (no Enter).
var (
	paneReadCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture}
	paneSendCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallSendText, tmux.CallSendEnter}
	paneTextCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallSendText}
)

// withFollowUp is calls then one follow-up lookup (a failed action's, SR-7.3), in a new slice.
func withFollowUp(calls []tmux.Call) []tmux.Call { return append(slices.Clone(calls), tmux.CallLookup) }

// verbRun is one verb call's answer and the socket-taking calls it added.
type verbRun[R any] struct {
	res   R
	err   error
	calls []tmuxfix.SocketCall
}

// runVerb runs call and returns its answer with the calls it added to e.rec.
func runVerb[R any](e *killEnv, call func() (R, error)) verbRun[R] {
	mark := len(e.rec.SocketCalls())
	res, err := call()
	return verbRun[R]{res: res, err: err, calls: e.rec.SocketCalls()[mark:]}
}

// assertSameRun fails unless again (the call re-issued) gave first's result,
// error text and calls.
func assertSameRun[R any](t *testing.T, again, first verbRun[R]) {
	t.Helper()
	if !reflect.DeepEqual(again.res, first.res) || errText(again.err) != errText(first.err) ||
		!reflect.DeepEqual(again.calls, first.calls) {
		t.Errorf("re-issued call = %+v, %v, calls %+v; want %+v, %v, calls %+v",
			again.res, again.err, again.calls, first.res, first.err, first.calls)
	}
}

// errText is err's text, "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

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

// readPaneRun runs readPaneClient with p as a verbRun.
func (e *killEnv) readPaneRun(t *testing.T, p api.ReadPaneParams) verbRun[api.ReadPaneResult] {
	t.Helper()
	return runVerb(e, func() (api.ReadPaneResult, error) { return e.readPaneClient(t, p) })
}

// sendKeysWindow is the relay window e.sendKeys passes: config's default,
// resolved as Client.SendKeys resolves it.
func sendKeysWindow() time.Duration {
	return time.Duration(config.Default().Relay.EffectiveTimeoutSeconds()) * time.Second
}

// sendKeys runs the exported api.SendKeys with e.store, e.rec, e.pc (its
// server and pane checks), sendKeysWindow and e.clock.Now.
func (e *killEnv) sendKeys(p api.SendKeysParams) (api.SendKeysResult, error) {
	return e.sendKeysAt(sendKeysWindow(), e.clock.Now(), p)
}

// sendKeysAt is sendKeys with the relay window and guard clock given, for a
// relay guard judged against permission requests stored at wall-clock time.
func (e *killEnv) sendKeysAt(window time.Duration, now time.Time, p api.SendKeysParams) (api.SendKeysResult, error) {
	return api.SendKeys(e.store, e.rec, e.pc, window, now, p)
}

// sendKeysClient runs Client.SendKeys on a new e.client, for the cases that
// need the trail or the config; logs is the Client's captured log.
func (e *killEnv) sendKeysClient(t *testing.T, p api.SendKeysParams, settings ...apitest.TmuxSetting) (res api.SendKeysResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.SendKeys(p)
	return res, buf.String(), err
}

// sendKeysRun runs sendKeys with p as a verbRun.
func (e *killEnv) sendKeysRun(p api.SendKeysParams) verbRun[api.SendKeysResult] {
	return runVerb(e, func() (api.SendKeysResult, error) { return e.sendKeys(p) })
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

// labelledPane is the id of the one pane of s carrying token (non-empty),
// failing unless exactly one does. labelledPane(t, s, s.Label.Token) is the
// pane of s carrying its label's token: Ours' agent pane or a leftover's.
func labelledPane(t *testing.T, s tmuxfix.SeedSession, token string) string {
	t.Helper()
	var ids []string
	for _, p := range s.Panes {
		if token != "" && p.AdPane == token {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("session %s (%s) panes carrying token %q = %v; want one", s.ID, s.Name, token, ids)
	}
	return ids[0]
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

// pendingKind is the launch a pending row is in (SR-7.1, SR-22.8).
type pendingKind int

const (
	pendingFresh   pendingKind = iota // a fresh spawn's launch
	pendingResumed                    // a resumed row's: its session id, transcript and an archived earlier session
	pendingReused                     // a reuse's, made by a real reuse (seedReusedPending): life + 1, a new token, no session id
)

// pendingKinds are the launch kinds send-keys meets on a pending row that a
// pendingSpec seeds; a reuse's (pendingReused) is made by seedPending alone.
func pendingKinds() []pendingKind { return []pendingKind{pendingFresh, pendingResumed} }

// String names k for subtest names; a reuse's carries "Reuse", so -run Reuse selects it.
func (k pendingKind) String() string {
	switch k {
	case pendingResumed:
		return "resumed row"
	case pendingReused:
		return "Reuse of a finished row"
	}
	return "fresh spawn"
}

// pendingShape is what a pending row's launch left.
type pendingShape int

const (
	pendingOurs      pendingShape = iota // the create reply recorded (server and pane); its current-labelled session up
	pendingLostReply                     // no server or pane recorded; its current-labelled session up, one pane carrying the token
	pendingLeftover                      // no server or pane recorded, no agent; only a session carrying r.old() (the launching process stopped before its create)
)

// pendingSpec is the killRowSpec of k's pending row with shape v, launch
// start at e.clock.Now; opts go last. A reuse's row has none: seedPending makes it.
func (e *killEnv) pendingSpec(k pendingKind, v pendingShape, opts ...apitest.SpawnOption) killRowSpec {
	spec := killRowSpec{State: store.StatePending,
		Opts: []apitest.SpawnOption{apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli())}}
	if k == pendingResumed {
		n := uuid.NewString()[:8]
		spec.SessionID = "sess-resumed-" + n
		spec.Opts = append(spec.Opts, apitest.WithLifeNumber(1), apitest.WithJsonlPath("/tmp/sess-resumed-"+n+".jsonl"),
			apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sess-earlier-" + n, Life: 1}))
	}
	switch v {
	case pendingLostReply:
		spec.NoPane, spec.NoServerIdentity = true, true
	case pendingLeftover:
		spec.NoPane, spec.NoServerIdentity, spec.NoSession, spec.Agent = true, true, true, agentNotRecorded
	}
	spec.Opts = append(spec.Opts, opts...)
	return spec
}

// seedPending seeds k's pending row with shape v (pendingSpec) and its
// session: the current-labelled one, or for pendingLeftover one labelled r.old().
// A reuse's row comes from seedReusedPending and takes no opts.
func (e *killEnv) seedPending(t *testing.T, k pendingKind, v pendingShape, opts ...apitest.SpawnOption) killRow {
	t.Helper()
	if k == pendingReused {
		if len(opts) > 0 {
			t.Fatalf("seedPending: a reuse's pending row takes no seed options (got %d)", len(opts))
		}
		return e.seedReusedPending(t, v)
	}
	r := e.seedRow(t, e.pendingSpec(k, v, opts...))
	if v == pendingLeftover {
		e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.old(), true))
	}
	return r
}

// sessionStartAfter applies a SessionStart (session id sessionID) to r's row
// as its own agent through the store's gated hook path (apitest.ApplyAgentHook,
// SR-22.9 rule (a)) when the first call of kind call returns. r must record
// its pane (a row with none ignores it: use rowWriteAfter). The test fails
// unless it ran and applied.
func (e *killEnv) sessionStartAfter(t *testing.T, call tmux.Call, r killRow, sessionID string) {
	t.Helper()
	if r.Spawn.Identity.PanePID <= 0 {
		t.Fatalf("row %s records no pane: a SessionStart is ignored (no_pane_recorded); use rowWriteAfter", r.ID)
	}
	e.onceAfter(t, call, "SessionStart", func() (bool, string) {
		a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionStart", sessionID)
		return a.Applied, a.Reason
	})
}

// rowWriteAfter changes r's row by a versioned store write that is not a
// hook (find-missing's liveness note: row_version +1, identity unchanged)
// when the first call of kind call returns; the stand-in for a SessionStart
// on a row with no recorded pane. The test fails unless it ran and applied.
func (e *killEnv) rowWriteAfter(t *testing.T, call tmux.Call, r killRow) {
	t.Helper()
	e.onceAfter(t, call, "versioned row write", func() (bool, string) {
		row, err := e.st.GetSpawn(r.ID)
		if err != nil {
			return false, err.Error()
		}
		res, err := e.st.SetLivenessNoteIfSameLife(r.ID, row.Snapshot, "changed between the row read and the send")
		if err != nil {
			return false, err.Error()
		}
		return res == store.CondApplied, fmt.Sprintf("condition result %d (CondApplied %d)", res, store.CondApplied)
	})
}

// onceAfter runs write when the first call of kind call returns and fails
// the test unless it ran (checked at cleanup) and reported applied.
func (e *killEnv) onceAfter(t *testing.T, call tmux.Call, what string, write func() (applied bool, why string)) {
	t.Helper()
	ran := false
	e.rec.AfterCall(call, func(tmuxfix.SocketCall, error) {
		if ran {
			return
		}
		ran = true
		if applied, why := write(); !applied {
			t.Errorf("%s after %v not applied: %s", what, call, why)
		}
	})
	t.Cleanup(func() {
		if !ran {
			t.Errorf("%s after %v never ran: no %v call was made", what, call, call)
		}
	})
}

// adoptionColumns is a row's adoption columns as stored (raw; nil for NULL)
// and its row_version, for "written once" and "unchanged" checks (SR-3.6).
type adoptionColumns struct {
	RowVersion                                                            int64
	ServerPID, ServerStarted, ServerStarttime, PaneID, PanePID, PaneStart any
}

// adoption reads id's adoption columns and row_version.
func (e *killEnv) adoption(t *testing.T, id string) adoptionColumns {
	t.Helper()
	c := e.columns(t, id)
	v, ok := c.RowVersion.(int64)
	if !ok {
		t.Fatalf("row %s row_version = %#v; want an integer", id, c.RowVersion)
	}
	return adoptionColumns{RowVersion: v, ServerPID: c.TmuxServerPID, ServerStarted: c.TmuxServerStarted,
		ServerStarttime: c.TmuxServerStarttime, PaneID: c.PaneID, PanePID: c.PanePID, PaneStart: c.PaneStarttime}
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

// assertNoTmuxCall fails when any tmux call, socket-taking or name-based, was made.
func (e *killEnv) assertNoTmuxCall(t *testing.T) {
	t.Helper()
	e.assertPaneCalls(t)
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

// assertTextSent fails unless exactly one text call was made, typing text
// (as delivered: CR stripped) into paneID on socket, with Enter asked for.
func (e *killEnv) assertTextSent(t *testing.T, socket, paneID, text string) {
	t.Helper()
	got := e.rec.SocketCallsOf(tmux.CallSendText)
	if len(got) != 1 {
		t.Fatalf("text calls = %+v; want one to %s", got, paneID)
	}
	if c := got[0]; c.Socket != socket || c.Target != paneID || c.Text != text || !c.PressEnter {
		t.Errorf("text call %q to %q on %s (Enter %v); want %q to %q on %s (Enter true)",
			c.Text, c.Target, c.Socket, c.PressEnter, text, paneID, socket)
	}
}

// assertDelivered fails unless the calls were exactly paneSendCalls: text
// typed into paneID on socket (assertTextSent), then one Enter to paneID.
func (e *killEnv) assertDelivered(t *testing.T, socket, paneID, text string) {
	t.Helper()
	e.assertPaneCalls(t, paneSendCalls...)
	e.assertTextSent(t, socket, paneID, text)
	if got := e.rec.SocketCallsOf(tmux.CallSendEnter); len(got) != 1 || got[0].Socket != socket || got[0].Target != paneID {
		t.Errorf("Enter calls = %+v; want one to %s on %s", got, paneID, socket)
	}
}

// assertNothingSent fails when a text or Enter call was made (the Recorder
// has no name-based send).
func (e *killEnv) assertNothingSent(t *testing.T) {
	t.Helper()
	e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter)
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
