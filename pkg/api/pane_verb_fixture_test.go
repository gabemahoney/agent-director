package api_test

// pane_verb_fixture_test.go extends the kill fixture (killEnv, killRow) for
// the pane verbs (read-pane, send-keys, pause; SR-20.2): their invocations
// and repeatable runs, the cross-verb adapter (paneVerb) the shared tables run
// each verb through, per-pane capture texts, extra session and pending-launch
// seeds (a reuse's included), the recorded-call lists and assertions, the between-read-and-send writes, the "changes nothing"
// readers, and pause's own pieces (its call sequences with the line cleared
// before /exit, b.9o4, the wait's poll seam, the SessionEnd that ends the row,
// the failing state read). It holds no tests. A later verb that acts on the
// agent's pane extends this fixture, not copies it.

import (
	"context"
	"errors"
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

// paneClearKey is the key pause sends to the agent's pane before typing
// /exit (b.9o4): C-u, by name, which Claude Code binds to deleting from the
// cursor to the start of the line.
const paneClearKey = "C-u"

// callClearLine stands, in the call lists below, for a key send of paneClearKey.
const callClearLine = tmux.CallSendKey + " " + paneClearKey

// recordedCall is c's kind as the call lists compare it: callClearLine for a
// key send of paneClearKey, else c.Call.
func recordedCall(c tmuxfix.SocketCall) tmux.Call {
	if c.Key == paneClearKey {
		return callClearLine
	}
	return c.Call
}

// paneWriteCalls are the call kinds that change tmux: sends, creates, kills and label writes.
var paneWriteCalls = []tmux.Call{tmux.CallSendText, tmux.CallSendEnter, callClearLine, tmux.CallCreate,
	tmux.CallKillPane, tmux.CallKillSession, tmux.CallSetLabel}

// The pane verbs' call sequences: a read (lookup, listing, capture), a
// delivery (lookup, listing, text, Enter) and a text call that failed (no
// Enter). pause's, which clear the line first, are below.
var (
	paneReadCalls   = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture}
	paneSendCalls   = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallSendText, tmux.CallSendEnter}
	paneTextCalls   = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallSendText}
	paneListedCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes}
	paneLookupCalls = []tmux.Call{tmux.CallLookup}
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

// skaText is the text the shared tables send (LF kept, no CR).
const skaText = "run the tests\nthen report"

// skaParams is send-keys on r with skaText.
func skaParams(r killRow) api.SendKeysParams {
	return api.SendKeysParams{ClaudeInstanceID: r.ID, Text: skaText}
}

// rppLines is the n_lines the shared tables read with (not the default), with ANSI on.
const rppLines = 7

// paneVerb is one pane verb as the shared tables run it: read-pane (through
// a Client, rppLines lines, ANSI on), send-keys (skaText) or pause (to its
// wait, pauseToWait). keys marks send-keys and pause, which type, write a
// lost reply's identity once (SR-3.6) and write disagree records.
type paneVerb struct {
	name string
	verb apitest.PaneVerb
	keys bool
	gone string // the gone error's name
	// run runs the verb on r and returns the text read (read-pane) and its error.
	run func(t *testing.T, e *killEnv, r killRow) (string, error)
	// acted fails unless the run (out, err) acted on pane by id with exactly the verb's calls.
	acted func(t *testing.T, e *killEnv, r killRow, pane, out string, err error)
}

// paneVerbs are read-pane, send-keys and pause.
func paneVerbs() []paneVerb { return []paneVerb{readPaneVerb(), sendKeysVerb(), pauseVerb()} }

// keysVerbs are send-keys and pause.
func keysVerbs() []paneVerb { return []paneVerb{sendKeysVerb(), pauseVerb()} }

// readPaneVerb reads with every pane's own text set (setPaneTexts).
func readPaneVerb() paneVerb {
	return paneVerb{name: "read-pane", verb: apitest.PaneReadPane, gone: "ErrTmuxCaptureFailed",
		run: func(t *testing.T, e *killEnv, r killRow) (string, error) {
			e.setPaneTexts(r.Socket)
			res, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: rppLines, ANSI: true})
			return res.Pane, err
		},
		acted: func(t *testing.T, e *killEnv, r killRow, pane, out string, err error) {
			t.Helper()
			if err != nil || out != paneText(r.Socket, pane) {
				t.Errorf("ReadPane = %q, %v; want pane %s's text", out, err, pane)
			}
			e.assertPaneCalls(t, paneReadCalls...)
			e.assertCaptured(t, pane, rppLines, true)
		}}
}

// sendKeysVerb sends skaText through api.SendKeys.
func sendKeysVerb() paneVerb {
	return paneVerb{name: "send-keys", verb: apitest.PaneSendKeys, keys: true, gone: "ErrTmuxSendKeys",
		run: func(_ *testing.T, e *killEnv, r killRow) (string, error) {
			_, err := e.sendKeys(skaParams(r))
			return "", err
		},
		acted: func(t *testing.T, e *killEnv, r killRow, pane, _ string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("SendKeys: %v; want delivery to %s", err, pane)
			}
			e.assertDelivered(t, r.Socket, pane, skaText)
		}}
}

// pauseVerb pauses through api.Pause up to its wait (pauseToWait).
func pauseVerb() paneVerb {
	return paneVerb{name: "pause", verb: apitest.PanePause, keys: true, gone: "ErrTmuxSendKeys",
		run: func(_ *testing.T, e *killEnv, r killRow) (string, error) { return "", e.pauseToWait(r) },
		acted: func(t *testing.T, e *killEnv, r killRow, pane, _ string, err error) {
			t.Helper()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Pause = %v; want /exit delivered to %s, then the wait", err, pane)
			}
			e.assertExitDelivered(t, r.Socket, pane)
		}}
}

// pauseToWait runs api.Pause on r with a context cancelled when an Enter call
// returns: a delivered /exit's wait returns context.Canceled at once, so no
// process-wide knob is needed and the test may run in parallel.
func (e *killEnv) pauseToWait(r killRow) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.rec.AfterCall(tmux.CallSendEnter, func(tmuxfix.SocketCall, error) { cancel() })
	_, err := e.pauseWithin(ctx, pauseTimeoutSeconds, pauseParams(r))
	return err
}

// paneWant is a shared-table case's answer: pane, the pane the verb acts on by
// id; else the refusal errName (one name) with desc's case for the verb (no
// value of forbid in it), after calls, nothing acted on.
type paneWant struct {
	pane    string
	errName string
	desc    func(v apitest.PaneVerb) apitest.DescCase
	calls   []tmux.Call
	forbid  []string
}

// paneNotFound is the pane-not-found conflict naming session name after the listing.
func paneNotFound(r killRow, name string, lostReply bool) paneWant {
	return paneWant{errName: "ErrTmuxSessionConflict", calls: paneListedCalls, desc: func(v apitest.PaneVerb) apitest.DescCase {
		return apitest.DescPaneNotFound(apitest.PaneNotFound{Verb: v, InstanceID: r.ID, Name: name, LostReply: lostReply})
	}}
}

// paneGoneWant is the verb's gone error after the lookup alone.
func paneGoneWant(v paneVerb, r killRow) paneWant {
	return paneWant{errName: v.gone, calls: paneLookupCalls, desc: func(pv apitest.PaneVerb) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: pv, InstanceID: r.ID, Name: r.Name})
	}}
}

// check runs v on r, fails unless it answers want and returns the run. The
// row is left as it was, unless v writes a lost reply's identity (keys: a
// server identity the row lacks, or the one token pane it acts on; that
// write is TestKeysVerbsAdoptionWrite's); read-pane writes no trail record for r.
func (v paneVerb) check(t *testing.T, e *killEnv, r killRow, want paneWant) verbRun[string] {
	t.Helper()
	before, mark := e.columns(t, r.ID), trailMark(t)
	run := runVerb(e, func() (string, error) { return v.run(t, e, r) })
	if want.pane != "" {
		v.acted(t, e, r, want.pane, run.res, run.err)
	} else {
		v.assertRefused(t, e, r, run.err, want)
	}
	if id := r.Spawn.Identity; !v.keys || (id.ServerPID > 0 && (id.PaneID != "" || want.pane == "")) {
		e.assertRowUnchanged(t, r.ID, before)
	}
	if !v.keys {
		assertNoTrailSince(t, mark, r.ID)
	}
	return run
}

// assertRefused fails unless err is want's one name and description for v,
// the calls are exactly want.calls, nothing was captured, typed or cleared,
// and no wait polled.
func (v paneVerb) assertRefused(t *testing.T, e *killEnv, r killRow, err error, want paneWant) {
	t.Helper()
	assertOneName(t, err, want.errName)
	if err != nil && want.desc != nil {
		apitest.AssertDescription(t, err.Error(), want.desc(v.verb),
			append([]string{r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID)}, want.forbid...)...)
	}
	e.assertNoCalls(t, tmux.CallCapture, tmux.CallSendText, tmux.CallSendEnter, callClearLine)
	e.assertPaneCalls(t, want.calls...)
	if e.store.stateReads != 0 {
		t.Errorf("state reads = %d; want no wait after a refusal", e.store.stateReads)
	}
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

// otherPane is a pane that is not the agent's: a new id and pid, no pane label.
func (e *killEnv) otherPane() tmuxfix.SeedPane { return tmuxfix.SeedPane{PID: e.newPID()} }

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

// pendingKind is the launch a pending row is in (SR-7.1, SR-22.8): a fresh
// spawn's (seeded) or a reuse's, made by a real reuse (seedReusedPending:
// life + 1, a new token, no session id). The verbs never branch on the life,
// so a resumed row's launch is not a kind of its own.
type pendingKind int

const (
	pendingFresh  pendingKind = iota // a fresh spawn's launch
	pendingReused                    // a reuse's, made by a real reuse
)

// String names k for subtest names; a reuse's carries "Reuse", so -run Reuse selects it.
func (k pendingKind) String() string {
	if k == pendingReused {
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

// pendingSpec is the killRowSpec of a fresh spawn's pending row with shape v,
// launch start at e.clock.Now; opts go last.
func (e *killEnv) pendingSpec(v pendingShape, opts ...apitest.SpawnOption) killRowSpec {
	spec := killRowSpec{State: store.StatePending,
		Opts: []apitest.SpawnOption{apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli())}}
	switch v {
	case pendingLostReply:
		spec.NoPane, spec.NoServerIdentity = true, true
	case pendingLeftover:
		spec.NoPane, spec.NoServerIdentity, spec.NoSession, spec.Agent = true, true, true, agentNotRecorded
	}
	spec.Opts = append(spec.Opts, opts...)
	return spec
}

// seedPending seeds k's pending row with shape v and its session: the
// current-labelled one, or for pendingLeftover one labelled r.old().
func (e *killEnv) seedPending(t *testing.T, k pendingKind, v pendingShape) killRow {
	t.Helper()
	if k == pendingReused {
		return e.seedReusedPending(t, v)
	}
	r := e.seedRow(t, e.pendingSpec(v))
	if v == pendingLeftover {
		e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.old(), true))
	}
	return r
}

// seedReusedPending makes a pending row of shape v by a real reuse of a
// finished row whose agent is gone, then drops the reuse's recorded tmux calls
// (Recorder.Reset), so a test's call checks see only its own verb's.
//   - pendingOurs: the labelled create recorded, its pane's agent alive (reusePending);
//   - pendingLostReply: the create made the labelled session but its reply was lost:
//     no server or pane recorded, the token pane's agent alive;
//   - pendingLeftover: the create timed out making nothing (reuseTimesOut), then a
//     session labelled with the earlier life's token came up under the recorded name.
func (e *killEnv) seedReusedPending(t *testing.T, v pendingShape) killRow {
	t.Helper()
	var r reuseRow
	switch v {
	case pendingLostReply:
		e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, Applied: true, Times: 1}, tmux.CallCreate)
		r = e.reusePending(t, agentAlive, reuseRowSpec{}, reuseRequest{})
		e.adoptablePane(&r.killRow)
	case pendingLeftover:
		r = e.seedReusable(t, agentGone, reuseRowSpec{})
		earlier := r.Token
		r = e.reuseTimesOut(t, r, reuseRequest{}, false)
		r.Agent = agentNotRecorded
		e.seedSession(t, &r.killRow, tmuxfix.WithRowSessionLabel(tmuxfix.Valid(earlier, r.ID, r.StoreID), true))
	default:
		r = e.reusePending(t, agentAlive, reuseRowSpec{}, reuseRequest{})
	}
	e.rec.Reset()
	return r.killRow
}

// seedPendingNoSession is k's pending row with shape v and no session: a
// pendingSpec row seeded with NoSession, or a reuse's row with its session
// killed by id (the call dropped with Recorder.Reset).
func (e *killEnv) seedPendingNoSession(t *testing.T, k pendingKind, v pendingShape) killRow {
	t.Helper()
	if k != pendingReused {
		spec := e.pendingSpec(v)
		spec.NoSession = true
		return e.seedRow(t, spec)
	}
	r := e.seedReusedPending(t, v)
	if r.Session.ID != "" {
		if err := e.rec.KillSessionID(r.Socket, r.Session.ID); err != nil {
			t.Fatalf("KillSessionID(%s, %s): %v", r.Socket, r.Session.ID, err)
		}
	}
	e.rec.Reset()
	r.Session = tmuxfix.SeedSession{}
	return r
}

// reuseTimesOut reuses r (from seedReusable, its agent gone) with q through
// Client.Spawn, the create timing out (made: it made its labelled session
// anyway), failing unless that is ErrTmuxUnresponsive with the reset row left
// pending; it returns r as the reuse left it (reusedAs).
func (e *killEnv) reuseTimesOut(t *testing.T, r reuseRow, q reuseRequest, made bool) reuseRow {
	t.Helper()
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout, Applied: made, Times: 1}, tmux.CallCreate)
	if _, logs, err := e.reuse(t, reuseParams(t, r, q)); !errors.Is(err, api.ErrTmuxUnresponsive) {
		t.Fatalf("reuse of %s = %v (log %q); want ErrTmuxUnresponsive", r.ID, err, logs)
	}
	return e.reusedAs(t, r)
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
		switch recordedCall(c) {
		case tmux.CallCapture, tmux.CallSendText, tmux.CallSendEnter, callClearLine:
			if !killPaneIDRe.MatchString(c.Target) {
				t.Errorf("%v targets %q; want a pane id", recordedCall(c), c.Target)
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

// assertEnterSent fails unless exactly one Enter call was made, to paneID on socket.
func (e *killEnv) assertEnterSent(t *testing.T, socket, paneID string) {
	t.Helper()
	if got := e.rec.SocketCallsOf(tmux.CallSendEnter); len(got) != 1 || got[0].Socket != socket || got[0].Target != paneID {
		t.Errorf("Enter calls = %+v; want one to %s on %s", got, paneID, socket)
	}
}

// assertDelivered fails unless the calls were exactly paneSendCalls: text
// typed into paneID on socket (assertTextSent), then one Enter to paneID.
func (e *killEnv) assertDelivered(t *testing.T, socket, paneID, text string) {
	t.Helper()
	e.assertPaneCalls(t, paneSendCalls...)
	e.assertTextSent(t, socket, paneID, text)
	e.assertEnterSent(t, socket, paneID)
}

// assertEnterOnly fails unless the calls were the lookup, the pane listing
// and one Enter to paneID on socket, with nothing typed: a text call, if
// one is made, types "" into paneID (b.9o4, send-keys with empty text).
func (e *killEnv) assertEnterOnly(t *testing.T, socket, paneID string) {
	t.Helper()
	var got []tmux.Call
	for _, c := range e.rec.SocketCalls() {
		if c.Call == tmux.CallSendText && c.Text == "" && c.Socket == socket && c.Target == paneID {
			continue
		}
		got = append(got, recordedCall(c))
	}
	if want := []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallSendEnter}; !slices.Equal(got, want) {
		t.Errorf("tmux calls, an empty text call aside = %v; want %v", got, want)
	}
	e.assertEnterSent(t, socket, paneID)
}

// assertNothingSent fails when a text, Enter or line-clear call was made
// (the Recorder has no name-based send).
func (e *killEnv) assertNothingSent(t *testing.T) {
	t.Helper()
	e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter, callClearLine)
}

// assertNoCalls fails when a recorded socket-taking call is of any of kinds
// (as recordedCall gives it).
func (e *killEnv) assertNoCalls(t *testing.T, kinds ...tmux.Call) {
	t.Helper()
	for _, c := range e.rec.SocketCalls() {
		if slices.Contains(kinds, recordedCall(c)) {
			t.Errorf("recorded %v of %q; want no %v", recordedCall(c), c.Target, kinds)
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

// killStore is pause's store too.
var _ api.PauseStore = (*killStore)(nil)

// GetSpawnState counts the read, then returns failStateReads' error, else
// delegates.
func (w *killStore) GetSpawnState(id string) (string, error) {
	w.stateReads++
	if w.stateErr != nil {
		return "", w.stateErr
	}
	return w.st.GetSpawnState(id)
}

// failStateReads makes every later state read (pause's wait polls) return
// err (errInjectedStore when nil).
func (w *killStore) failStateReads(err error) { w.stateErr = orInjected(err) }

// exitText is what pause types into the agent's pane.
const exitText = "/exit"

// pause's call sequences (b.9o4): it clears the agent's input line
// (callClearLine) before typing /exit, so a delivery is lookup, listing,
// line clear, /exit and Enter, a failed line clear types nothing after it,
// and a failed /exit call makes no Enter.
var (
	pauseSendCalls  = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, callClearLine, tmux.CallSendText, tmux.CallSendEnter}
	pauseTextCalls  = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, callClearLine, tmux.CallSendText}
	pauseClearCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, callClearLine}
)

// pauseTimeoutSeconds is the wait e.pause allows: a row nothing ends stays
// waiting and the wait ends in ErrPauseTimeout after this real-time second.
const pauseTimeoutSeconds = 1

// fastPausePolls makes the wait poll every millisecond of real sleep
// (api.SetPauseTestKnobs) until cleanup restores the cadence; not for parallel tests.
func fastPausePolls(t *testing.T) {
	t.Helper()
	interval, sleep := api.PauseTestKnobs()
	api.SetPauseTestKnobs(time.Millisecond, time.Sleep)
	t.Cleanup(func() { api.SetPauseTestKnobs(interval, sleep) })
}

// pauseParams is pause on r.
func pauseParams(r killRow) api.PauseParams { return api.PauseParams{ClaudeInstanceID: r.ID} }

// pause runs the exported api.Pause with a background context, e.store,
// e.rec, e.pc (its server and pane checks) and pauseTimeoutSeconds.
func (e *killEnv) pause(p api.PauseParams) (api.PauseResult, error) {
	return e.pauseWithin(context.Background(), pauseTimeoutSeconds, p)
}

// pauseWithin is pause with ctx and the wait's timeout given.
func (e *killEnv) pauseWithin(ctx context.Context, timeoutSeconds int, p api.PauseParams) (api.PauseResult, error) {
	return api.Pause(ctx, e.store, e.rec, e.pc, timeoutSeconds, p)
}

// pauseClient runs Client.Pause (background context, config's pause timeout)
// on a new e.client, for the trail or [tmux] config cases; logs is its log.
func (e *killEnv) pauseClient(t *testing.T, p api.PauseParams, settings ...apitest.TmuxSetting) (res api.PauseResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Pause(context.Background(), p)
	return res, buf.String(), err
}

// endAsAgent ends r's row as its own agent (a SessionEnd through
// apitest.ApplyAgentHook, the row's claude_session_id; SR-22.9) and reports
// whether it applied, and why not. The row must record its pane by then.
func (e *killEnv) endAsAgent(t *testing.T, r killRow) (bool, string) {
	t.Helper()
	row, err := e.st.GetSpawn(r.ID)
	if err != nil {
		return false, err.Error()
	}
	a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", row.ClaudeSessionID)
	return a.Applied, a.Reason
}

// endAfterEnter ends r's row as its own agent (endAsAgent) when the first
// Enter call returns. The test fails unless it ran and applied.
func (e *killEnv) endAfterEnter(t *testing.T, r killRow) {
	t.Helper()
	e.onceAfter(t, tmux.CallSendEnter, "SessionEnd", func() (bool, string) { return e.endAsAgent(t, r) })
}

// endAtFirstWait ends r's row as its own agent (endAsAgent) at the pause
// wait's first sleep (api.SetPauseTestKnobs), so only a wait that sleeps sees
// it; cleanup restores the knobs and fails the test unless it ran and
// applied. Not for parallel tests.
func (e *killEnv) endAtFirstWait(t *testing.T, r killRow) {
	t.Helper()
	interval, sleep := api.PauseTestKnobs()
	ran := false
	api.SetPauseTestKnobs(time.Millisecond, func(d time.Duration) {
		if ran {
			time.Sleep(d)
			return
		}
		ran = true
		if applied, why := e.endAsAgent(t, r); !applied {
			t.Errorf("SessionEnd at the wait's first sleep not applied: %s", why)
		}
	})
	t.Cleanup(func() {
		api.SetPauseTestKnobs(interval, sleep)
		if !ran {
			t.Errorf("SessionEnd at the wait's first sleep never ran: the wait never slept")
		}
	})
}

// assertExitDelivered fails unless the calls were exactly pauseSendCalls:
// paneID's input line cleared on socket (assertLineCleared), exitText typed
// into it, then one Enter to it.
func (e *killEnv) assertExitDelivered(t *testing.T, socket, paneID string) {
	t.Helper()
	e.assertPaneCalls(t, pauseSendCalls...)
	e.assertExitTyped(t, socket, paneID)
	e.assertEnterSent(t, socket, paneID)
}

// assertExitTyped fails unless paneID's input line was cleared on socket
// (assertLineCleared) and exactly one text call then typed exitText into
// it, with Enter asked for (the failed-action cases too).
func (e *killEnv) assertExitTyped(t *testing.T, socket, paneID string) {
	t.Helper()
	e.assertLineCleared(t, socket, paneID)
	e.assertTextSent(t, socket, paneID, exitText)
}

// assertLineCleared fails unless exactly one key send was made, paneClearKey
// to paneID on socket, and it came before every text call (b.9o4).
func (e *killEnv) assertLineCleared(t *testing.T, socket, paneID string) {
	t.Helper()
	var keys []tmuxfix.SocketCall
	textFirst := false
	for _, c := range e.rec.SocketCalls() {
		switch {
		case c.Key != "":
			keys = append(keys, c)
		case c.Call == tmux.CallSendText && len(keys) == 0:
			textFirst = true
		}
	}
	if len(keys) != 1 {
		t.Fatalf("key sends = %+v; want one, %s to %s on %s", keys, paneClearKey, paneID, socket)
	}
	if c := keys[0]; c.Key != paneClearKey || c.Socket != socket || c.Target != paneID {
		t.Errorf("key send %q to %q on %s; want %s to %s on %s", c.Key, c.Target, c.Socket, paneClearKey, paneID, socket)
	}
	if textFirst {
		t.Errorf("a text call came before the line was cleared; want %s first", paneClearKey)
	}
}

// pauseDisagrees returns id's ad.provenance.disagree records written by pause.
func pauseDisagrees(t *testing.T, id string) []map[string]any {
	t.Helper()
	return verbDisagrees(t, "pause", id)
}
