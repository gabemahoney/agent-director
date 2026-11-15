package hook_test

// busy_send_claude_test.go — busy send's Claude route A (Task t2.xis.oo.u8;
// Main Agent decisions 10 to 12) through hook.Handle on a real store: the
// owned FileChanged wake is the only callback that consumes the inbox, and
// only for the parent agent of the current Launch; Stop and the idle prompt
// only re-signal its wake endpoint; AD's own UserPromptSubmit sighting of the
// exact wake text is the only take evidence; SessionStart returns the endpoint
// to watch. The store's predicates (Turn Start counter, awaiting obligation,
// sighting outcomes) are internal/store's (t3.xis.oo.u8.gr), the coordinator's
// generic order and custody internal/busysend's (Task .xe).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// wakeCaller is the Caller UUID of every Message these tests admit.
const wakeCaller = "3b9e1c4a-7d2f-4e8b-9a6c-1f0e2d3c4b5a"

// wakeSession is the main agent's session: user-prompt-submit.json's.
const wakeSession = "5acdf0e3-8b8c-47c8-83d6-870c0b0cebd1"

// wakeHelper is the hook process's own identity (HandleConfig.Self).
func wakeHelper() store.ProcessIdentity {
	return store.ProcessIdentity{PID: 4242, Starttime: "4242", PIDNamespace: "pid:[4026531836]"}
}

// claudeWake is body in Claude Code's measured asyncRewake wrapping (Research
// t1.p8z.9e), as the agent's UserPromptSubmit hook receives a wake.
func claudeWake(body string) string {
	return "<task-notification>\n<summary>Stop hook feedback</summary>\n</task-notification>\n<system-reminder>\n" +
		`Stop hook blocking error from command "FileChanged": ` + body + "\n</system-reminder>"
}

// wakeEnv is a covered Claude row in session wakeSession whose current Launch
// records route A at its own wake endpoint (store.BusySendWakePath under
// TestMain's HOME, created by spawn.PrepareBusySendWake), and its own agent.
type wakeEnv struct {
	st              *store.Store
	db, id          string
	endpoint, token string
	parent          hookParent
}

// newWakeEnv seeds the row in state with opts.
func newWakeEnv(t *testing.T, state string, opts ...apitest.SpawnOption) *wakeEnv {
	t.Helper()
	id := "wake-" + strings.ReplaceAll(t.Name(), "/", "-")
	st, db := ssgSeed(t, id, state, wakeSession, opts...)
	if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	l, err := st.GetBusySendPublishedLaunch(id)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := spawn.PrepareBusySendWake(st.StoreID(), l.LaunchToken)
	if err != nil {
		t.Fatalf("PrepareBusySendWake: %v", err)
	}
	if err := apitest.SeedBusySendRoute(db, id, string(store.BusySendRouteClaudeHooks), wake); err != nil {
		t.Fatal(err)
	}
	return &wakeEnv{st: st, db: db, id: id, endpoint: wake, token: l.LaunchToken, parent: agentParent(t, st, id)}
}

// newResumedWakeEnv is a waiting covered Claude row on a resumed Launch with no
// Turn Start yet: an ended row whose conversation is covered, moved and
// identified as resume does it (as ssgResumed), so the new Launch records its
// own route, then reported in by its agent's SessionStart, which creates the
// endpoint.
func newResumedWakeEnv(t *testing.T) *wakeEnv {
	t.Helper()
	id := "wake-" + strings.ReplaceAll(t.Name(), "/", "-")
	st, db := ssgSeed(t, id, store.StateEnded, wakeSession, apitest.WithJsonlPath("/x/"+wakeSession+".jsonl"),
		apitest.WithLaunchIdentity(ssgOldPane))
	if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	res, moved, err := st.MoveToPendingWithExit(id, mustGetSpawn(t, st, id).Snapshot, time.Now().UnixMilli(), ssgNewPane.Token, ssgNewPane.Socket, "", store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, st, id))
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want applied", res, err)
	}
	if res, err := st.RecordLaunchIdentity(id, moved, ssgNewPane.Token, ssgNewPane); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity = %v, %v; want applied", res, err)
	}
	e := &wakeEnv{st: st, db: db, id: id, token: ssgNewPane.Token, parent: agentParent(t, st, id)}
	if e.endpoint, err = store.BusySendWakePath(st.StoreID(), e.token); err != nil {
		t.Fatal(err)
	}
	start := e.payload(t, "session-start-resume.json", map[string]any{"transcript_path": writeTranscript(t, wakeSession)})
	if _, err := e.handle(context.Background(), e.parent, start, nil); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	f, err := st.ReadBusySendRouteFacts(context.Background(), id, store.HookGate{})
	if err != nil || f.Kind != store.BusySendLaunchResume || !f.Covered || f.WakePath != e.endpoint || f.State != store.StateWaiting ||
		f.TurnStarts != 0 {
		t.Fatalf("resumed route facts = %+v, %v; want a waiting covered resume Launch with its own route and no Turn Start", f, err)
	}
	return e
}

// payload is fixture in e's session with fields set (nil removes one).
func (e *wakeEnv) payload(t *testing.T, fixture string, fields map[string]any) []byte {
	t.Helper()
	f := map[string]any{"session_id": wakeSession, "transcript_path": "/x/" + wakeSession + ".jsonl"}
	maps.Copy(f, fields)
	return payloadWith(t, fixture, f)
}

// fileChanged is the owned wake's callback for e's endpoint, with fields set.
func (e *wakeEnv) fileChanged(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	f := map[string]any{"file_path": e.endpoint}
	maps.Copy(f, fields)
	return e.payload(t, "file-changed.json", f)
}

// handle runs one Handle of payload from p with the hook's own identity and
// stderr, and returns its stdout and error.
func (e *wakeEnv) handle(ctx context.Context, p hookParent, payload []byte, stderr io.Writer) (string, error) {
	hc := hookConfig(envHook(e.id, ""), p)
	hc.Self, hc.Stderr = wakeHelper, stderr
	var stdout bytes.Buffer
	err := hook.Handle(ctx, bytes.NewReader(payload), &stdout, e.st, hc, newSilentLogger())
	return stdout.String(), err
}

// fire runs payload from the agent with no stderr expected: it must return nil
// and write nothing on stdout.
func (e *wakeEnv) fire(t *testing.T, payload []byte) {
	t.Helper()
	if out, err := e.handle(context.Background(), e.parent, payload, nil); err != nil || out != "" {
		t.Fatalf("Handle(%s) = stdout %q, %v; want nothing, nil", hook.PeekEventName(payload), out, err)
	}
}

// wakeWith fires the owned wake from the agent onto w and returns what it
// wrote there: Handle returns ErrRewake exactly when it wrote one whole
// prompt, else nil, and never writes stdout (no decision envelope).
func (e *wakeEnv) wakeWith(t *testing.T, w *wakeStderr) string {
	t.Helper()
	out, err := e.handle(context.Background(), e.parent, e.fileChanged(t, nil), w)
	if out != "" {
		t.Errorf("FileChanged stdout = %q; want nothing", out)
	}
	if err != nil && !errors.Is(err, hook.ErrRewake) {
		t.Fatalf("FileChanged Handle = %v; want nil or ErrRewake", err)
	}
	if whole := w.Len() > 0 && w.err == nil && w.keep < 0; errors.Is(err, hook.ErrRewake) != whole {
		t.Errorf("FileChanged Handle = %v after writing %d bytes (error %v); want ErrRewake only for a whole prompt", err, w.Len(), w.err)
	}
	return w.String()
}

// wake fires the owned wake onto a writer that takes every byte.
func (e *wakeEnv) wake(t *testing.T) string {
	t.Helper()
	return e.wakeWith(t, &wakeStderr{keep: -1})
}

// admit admits text as confirmed Message request, failing on a refusal.
func (e *wakeEnv) admit(t *testing.T, request, text string) {
	t.Helper()
	confirmed := true
	if _, err := e.st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: e.id, Text: []byte(text), CallerUUID: wakeCaller,
		DeliveryRequestID: request, Confirmed: &confirmed}); err != nil {
		t.Fatalf("admit %s: %v", request, err)
	}
}

// result is Message request's stored Result.
func (e *wakeEnv) result(t *testing.T, request string) store.BusySendResult {
	t.Helper()
	l, err := e.st.ReadBusySendResult(wakeCaller, request)
	if err != nil || l.Result == nil {
		t.Fatalf("ReadBusySendResult(%s) = %+v, %v", request, l, err)
	}
	return *l.Result
}

// observe is get's delivery facts for e's row.
func (e *wakeEnv) observe(t *testing.T) store.BusySendObservation {
	t.Helper()
	o, err := e.st.ReadBusySendObservation(e.id, time.Second)
	if err != nil || o == nil {
		t.Fatalf("ReadBusySendObservation = %v, %v", o, err)
	}
	return *o
}

// assertRaced fails unless each request's Result is Final sent_unconfirmed
// with the sticky wait_turn_raced (every Claude wake is race-exposed, SR-3.3a,
// ahead of handed_over_not_proven) on e's Launch and session.
func (e *wakeEnv) assertRaced(t *testing.T, requests ...string) {
	t.Helper()
	for _, request := range requests {
		r := e.result(t, request)
		if r.Class != store.BusySendClassSentUnconfirmed || !r.Final || r.Reason != string(store.BusySendReasonWaitTurnRaced) ||
			r.LaunchToken == nil || *r.LaunchToken != e.token || r.SessionID != wakeSession {
			t.Errorf("%s Result = %+v; want Final sent_unconfirmed wait_turn_raced on the captured Launch and session", request, r)
		}
	}
}

// assertHeld fails unless request is still Held, non-Final, waiting one of waits.
func (e *wakeEnv) assertHeld(t *testing.T, request string, waits ...store.BusySendWaitReason) {
	t.Helper()
	r := e.result(t, request)
	if r.Class != store.BusySendClassHeld || r.Final || r.Wait == nil || !slices.Contains(waits, r.Wait.Reason) {
		t.Errorf("%s Result = %+v (wait %+v); want Held, waiting %v", request, r, r.Wait, waits)
	}
}

// wakeStderr is a hook's error stream that takes a write deadline, as the
// route requires (R2-2): before runs once at its first write; each write
// keeps keep bytes (all when negative) and returns err.
type wakeStderr struct {
	bytes.Buffer
	before func()
	keep   int
	err    error
}

func (w *wakeStderr) SetWriteDeadline(time.Time) error { return nil }

func (w *wakeStderr) Write(p []byte) (int, error) {
	if f := w.before; f != nil {
		w.before = nil
		f()
	}
	n := len(p)
	if w.keep >= 0 && w.keep < n {
		n = w.keep
	}
	w.Buffer.Write(p[:n])
	return n, w.err
}

// wakeMtime sets e's endpoint's times far back and returns a check of whether
// they changed since.
func (e *wakeEnv) wakeMtime(t *testing.T) (touched func() bool) {
	t.Helper()
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(e.endpoint, old, old); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		info, err := os.Stat(e.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		return !info.ModTime().Equal(old)
	}
}

// TestClaudeWakeDeliversHeldPrompts (SR-2.2 to SR-2.5, SR-3.3 to SR-3.5;
// decision 10): the agent's own FileChanged for its Launch's endpoint writes
// one whole prompt on stderr and exits 2 (ErrRewake), never a decision
// envelope or a state write: the Held prefix in accepted order with exact
// separators, frozen at its cutoff, one prompt per wake, with an 8,000-byte
// Message fitting a prompt alone. A send admitted and a wake fired while it
// writes join nothing and write nothing. A repeated or coalesced wake writes
// nothing while that prompt has no take evidence; the agent's exact sighting
// of it (UserPromptSubmit, which also counts a Turn Start; not one whose
// payload session_id contradicts its transcript's) lifts that, and the
// parent's Stop re-signals the endpoint so the next wake delivers the rest.
func TestClaudeWakeDeliversHeldPrompts(t *testing.T) {
	e := newWakeEnv(t, store.StateWaiting)
	b, c, d := strings.Repeat("b", 8000), strings.Repeat("c", 8000), strings.Repeat("d", 8000)
	e.admit(t, "m1", "first")
	e.admit(t, "m2", b)
	e.admit(t, "m3", c)
	row := mustGetSpawn(t, e.st, e.id)

	w := &wakeStderr{keep: -1}
	w.before = func() {
		e.admit(t, "late", d)
		if got := e.wakeWith(t, &wakeStderr{keep: -1}); got != "" {
			t.Errorf("a wake racing the first wrote %q; want nothing", got)
		}
	}
	first := "[agent-director: message 1 of 2]\nfirst\n\n[agent-director: message 2 of 2]\n" + b
	if got := e.wakeWith(t, w); got != first {
		t.Fatalf("first wake wrote %d bytes; want the Held prefix m1, m2 as one prompt (%d bytes)", len(got), len(first))
	}
	assertRowUnchanged(t, e.st, e.id, row)
	e.assertRaced(t, "m1", "m2")
	if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 2 {
		t.Errorf("after the first wake: awaiting %d, Held %d; want 1, 2 (m3 released, late past the cutoff)", o.AwaitingTurnStart, o.HeldCount)
	}

	if got := e.wake(t); got != "" {
		t.Errorf("a repeated wake before any sighting wrote %d bytes; want nothing", len(got))
	}
	e.assertHeld(t, "m3", store.BusySendWaitReadinessUnknown)

	// R2-1: the exact text from the right transcript under a contradicting
	// payload session_id is no sighting.
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(first),
		"session_id": "0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d"}))
	if o := e.observe(t); o.AwaitingTurnStart != 1 {
		t.Errorf("awaiting after a sighting under a contradicting payload session = %d; want 1", o.AwaitingTurnStart)
	}
	turns := mustGetSpawn(t, e.st, e.id).TurnStarts
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(first)}))
	if got := mustGetSpawn(t, e.st, e.id); got.TurnStarts != turns+1 || got.State != store.StateWorking {
		t.Errorf("after the sighting: Turn Starts %d, state %s; want %d, working", got.TurnStarts, got.State, turns+1)
	}
	if o := e.observe(t); o.AwaitingTurnStart != 0 {
		t.Errorf("awaiting after the exact sighting = %d; want 0", o.AwaitingTurnStart)
	}
	e.assertRaced(t, "m1", "m2")

	touched := e.wakeMtime(t)
	e.fire(t, e.payload(t, "stop.json", nil))
	if !touched() {
		t.Error("the parent's Stop with Messages Held left the wake endpoint untouched")
	}
	if got, want := e.wake(t), "[agent-director: message 1 of 1]\n"+c; got != want {
		t.Errorf("the next wake wrote %d bytes; want m3 alone (%d bytes)", len(got), len(want))
	}
	e.assertRaced(t, "m3")
	e.assertHeld(t, "late", store.BusySendWaitReadinessUnknown, store.BusySendWaitEarlierMessage)
}

// failRouteFacts is the store with its nth route-facts read failing, after the
// callback's own authority read (the first) succeeded; fired records it.
type failRouteFacts struct {
	*store.Store
	n     int
	fired bool
}

func (s *failRouteFacts) ReadBusySendRouteFacts(ctx context.Context, id string, gate store.HookGate) (store.BusySendRouteFacts, error) {
	if s.n--; s.n == 0 {
		s.fired = true
		return store.BusySendRouteFacts{}, errors.New("injected route-facts read failure")
	}
	return s.Store.ReadBusySendRouteFacts(ctx, id, gate)
}

// TestClaudeWakeRefusals (SR-3.3, SR-3.4; decision 10; repair round 1 F2,
// F3): a wake callback that is not the parent agent's for its current
// Launch's endpoint, or whose agent is not ready, writes nothing, exits 0,
// changes no row and leaves the Message Held with its wait. The not-ready
// causes are recorded (a startup prompt goes first in the Launch's order and
// its own UserPromptSubmit is its only evidence); an open Dialog stays open
// and unanswered. A route-facts read that fails first (logged), at
// eligibility or at the last look before the issue commit claims nothing or
// releases its claim. A stream that takes no write deadline (logged), or no
// startup-prompt reader on any turn of any Launch (code repairs 2 and 3),
// issues nothing.
func TestClaudeWakeRefusals(t *testing.T) {
	startup := []apitest.SpawnOption{apitest.WithRawAgentArgs(`["--model","opus","fix the tests"]`), apitest.WithTurnStarts(0)}
	cases := []struct {
		name      string
		state     string
		opts      []apitest.SpawnOption
		prep      func(t *testing.T, e *wakeEnv)
		fields    map[string]any
		parent    func(t *testing.T, e *wakeEnv) hookParent
		failAt    int // the route-facts read that fails; 0 for none
		noSelf    bool
		plain     bool   // stderr takes no write deadline
		noStartup bool   // no HandleConfig.ClaudeStartupPrompt
		resumed   bool   // the row is on a resumed Launch (newResumedWakeEnv)
		logged    string // a diagnostic the hook logs; "" checks none
		waits     []store.BusySendWaitReason
	}{
		{name: "another process's callback", parent: func(t *testing.T, e *wakeEnv) hookParent { return foreignParent(t, e.st, e.id) }},
		{name: "a subagent's callback", fields: map[string]any{"agent_id": "a5b92f0e7d3c6184", "agent_type": "Explore"}},
		{name: "another watched path", fields: map[string]any{"file_path": "/work/demo/.envrc"}},
		{name: "uncovered Launch", prep: func(t *testing.T, e *wakeEnv) {
			if err := apitest.SeedBusySendCoverage(e.db, e.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "another session (F2)", fields: map[string]any{"session_id": "0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d",
			"transcript_path": "/x/0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d.jsonl"}},
		{name: "no session (F2)", fields: map[string]any{"session_id": nil, "transcript_path": nil}},
		{name: "running turn", state: store.StateWorking, waits: []store.BusySendWaitReason{store.BusySendWaitTurnRunning}},
		{name: "open Dialog", state: store.StateCheckPermission, prep: func(t *testing.T, e *wakeEnv) {
			if _, err := apitest.SeedPermissionRequest(e.db, e.id, "Bash"); err != nil {
				t.Fatal(err)
			}
		}, waits: []store.BusySendWaitReason{store.BusySendWaitDialog}},
		{name: "agent starting", state: store.StatePending, waits: []store.BusySendWaitReason{store.BusySendWaitAgentStarting}},
		{name: "startup prompt not yet taken", opts: startup, waits: []store.BusySendWaitReason{store.BusySendWaitAgentStarting}},
		{name: "facts unreadable at first (R2-5)", failAt: 1, logged: "hook: busy-send route facts"},
		{name: "facts unreadable at eligibility", failAt: 2, waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
		{name: "facts unreadable at authorize", failAt: 3, waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
		{name: "no helper identity", noSelf: true},
		{name: "output that takes no write deadline (R2-2)", plain: true, logged: "takes no write deadline",
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
		{name: "no startup-prompt reader on a first turn (R2-7)", noStartup: true,
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
		{name: "no startup-prompt reader after a Turn Start (R3-1)", noStartup: true, opts: []apitest.SpawnOption{apitest.WithTurnStarts(1)},
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
		{name: "no startup-prompt reader on a resumed Launch (R3-1)", noStartup: true, resumed: true,
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			if state == "" {
				state = store.StateWaiting
			}
			var e *wakeEnv
			if tc.resumed {
				e = newResumedWakeEnv(t)
			} else {
				e = newWakeEnv(t, state, tc.opts...)
			}
			e.admit(t, "m1", "hello")
			admitted := e.result(t, "m1")
			if tc.prep != nil {
				tc.prep(t, e)
			}
			p := e.parent
			if tc.parent != nil {
				p = tc.parent(t, e)
			}
			var st hook.HookStore = e.st
			failing := &failRouteFacts{Store: e.st, n: tc.failAt}
			if tc.failAt > 0 {
				st = failing
			}
			requests, err := e.st.OpenPermissionRequestsForSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			row := mustGetSpawn(t, e.st, e.id)
			hc := hookConfig(envHook(e.id, ""), p)
			hc.Self = wakeHelper
			if tc.noSelf {
				hc.Self = nil
			}
			if tc.noStartup {
				hc.ClaudeStartupPrompt = nil
			}
			var plain, stdout, logs bytes.Buffer
			stderr := &wakeStderr{keep: -1}
			hc.Stderr = stderr
			if tc.plain {
				hc.Stderr = &plain
			}
			if err := hook.Handle(context.Background(), bytes.NewReader(e.fileChanged(t, tc.fields)), &stdout, st, hc, log.New(&logs, "", 0)); err != nil {
				t.Errorf("Handle = %v; want nil (exit 0)", err)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 || plain.Len() != 0 {
				t.Errorf("wrote stdout %q, stderr %q%q; want nothing", stdout.String(), stderr.String(), plain.String())
			}
			if failing.fired != (tc.failAt > 0) {
				t.Errorf("injected route-facts failure fired = %t; want %t", failing.fired, tc.failAt > 0)
			}
			if !strings.Contains(logs.String(), tc.logged) {
				t.Errorf("hook log %q; want it to name %q", logs.String(), tc.logged)
			}
			waits := tc.waits
			if waits == nil {
				waits = []store.BusySendWaitReason{admitted.Wait.Reason}
			}
			e.assertHeld(t, "m1", waits...)
			assertRowUnchanged(t, e.st, e.id, row)
			// An uncovered Launch shows no delivery facts (nil).
			if o, err := e.st.ReadBusySendObservation(e.id, time.Second); err != nil || (o != nil && (o.HandoverCount != 0 || o.AwaitingTurnStart != 0)) {
				t.Errorf("delivery facts = %+v, %v; want no hand-over claimed or issued", o, err)
			}
			if after, err := e.st.OpenPermissionRequestsForSpawn(e.id); err != nil || len(after) != len(requests) {
				t.Errorf("open permission requests %d → %d (%v); want unchanged", len(requests), len(after), err)
			}
		})
	}
}

// TestClaudeWakeOutputBoundary (M2 P-C4.2, P-C4.3; SR-2.4, SR-3.5): at the
// wake's first byte the prompt's issue and its awaiting obligation are
// durable. Only a whole write is complete output (exit 2, Final); a short
// write is partial and a failed one uncertain, both non-Final, never retried
// by a later wake, their obligation kept. The agent's sighting arriving before
// the completion record still binds, and the completion then secures order.
func TestClaudeWakeOutputBoundary(t *testing.T) {
	cases := []struct {
		name    string
		keep    int
		err     error
		sight   bool
		partial bool
	}{
		{name: "whole write, sighting first", keep: -1, sight: true},
		{name: "short write", keep: 10, err: io.ErrShortWrite, partial: true},
		{name: "failed write", keep: 0, err: errors.New("write |1: broken pipe")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, store.StateWaiting)
			e.admit(t, "m1", "hello")
			const body = "[agent-director: message 1 of 1]\nhello"
			w := &wakeStderr{keep: tc.keep, err: tc.err}
			w.before = func() {
				if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 0 {
					t.Errorf("at the first byte: awaiting %d, Held %d; want the issued prompt's obligation 1, Held 0", o.AwaitingTurnStart, o.HeldCount)
				}
				if tc.sight {
					e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
				}
			}
			got := e.wakeWith(t, w)
			if tc.sight {
				if got != body {
					t.Errorf("wrote %q; want %q", got, body)
				}
				e.assertRaced(t, "m1")
				if o := e.observe(t); o.AwaitingTurnStart != 0 {
					t.Errorf("awaiting = %d; want 0 after the sighting", o.AwaitingTurnStart)
				}
				return
			}
			if r := e.result(t, "m1"); r.Final || r.Class != store.BusySendClassSentUnconfirmed || r.PartialTransport != tc.partial {
				t.Errorf("Result = %+v; want non-Final sent_unconfirmed, partial transport %t", r, tc.partial)
			}
			if got := e.wake(t); got != "" {
				t.Errorf("a later wake wrote %q; want no retry", got)
			}
			if o := e.observe(t); o.AwaitingTurnStart != 1 {
				t.Errorf("awaiting = %d; want the obligation kept", o.AwaitingTurnStart)
			}
		})
	}
}

// stallStderr is a hook's error stream over a pipe's write end, deadline
// capable as the CLI gives the FileChanged hook: it counts the writes still in
// flight and the bytes they reported written.
type stallStderr struct {
	f        *os.File
	inFlight atomic.Int32
	wrote    atomic.Int64
}

func (s *stallStderr) Write(p []byte) (int, error) {
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	n, err := s.f.Write(p)
	s.wrote.Add(int64(n))
	return n, err
}

func (s *stallStderr) SetWriteDeadline(t time.Time) error { return s.f.SetWriteDeadline(t) }

// TestClaudeWakeWriteIsBounded (repair round 1 F4): a wake whose stderr cannot
// take its prompt stops writing at the Job deadline (here its caller's) and
// exits 0 with no writer left: once the pipe's only write end is closed,
// draining it finds exactly what the write reported before the hook returned,
// never the prompt. The prompt stays issued, non-Final and awaiting evidence:
// never complete, never re-emitted.
func TestClaudeWakeWriteIsBounded(t *testing.T) {
	e := newWakeEnv(t, store.StateWaiting)
	e.admit(t, "m1", "hello")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := w.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	full := 0
	for chunk := make([]byte, 4096); ; {
		n, err := w.Write(chunk)
		if full += n; err != nil {
			break // the pipe is full
		}
	}
	if err := w.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	stderr := &stallStderr{f: w}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, err := e.handle(ctx, e.parent, e.fileChanged(t, nil), stderr)
		if out != "" {
			t.Errorf("stdout %q; want nothing", out)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Handle = %v; want nil (exit 0) for output cut at its deadline", err)
		}
	case <-time.After(10 * time.Second):
		go func() { _, _ = io.Copy(io.Discard, r) }() // let the write finish so the hook returns
		<-done
		t.Fatal("the wake was still writing 9 s past its Job deadline; want its write bounded")
	}
	if n := stderr.inFlight.Load(); n != 0 {
		t.Fatalf("%d writes still in flight after the hook returned; want none", n)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(r); err != nil || len(rest) != full+int(stderr.wrote.Load()) || bytes.Contains(rest, []byte("hello")) {
		t.Errorf("drained %d bytes, %v; want only the %d filling the pipe and the %d the write reported, never the prompt",
			len(rest), err, full, stderr.wrote.Load())
	}
	if res := e.result(t, "m1"); res.Final {
		t.Errorf("Result = %+v; want non-Final: the output was never complete", res)
	}
	if o := e.observe(t); o.AwaitingTurnStart != 1 {
		t.Errorf("awaiting = %d; want the issued prompt's obligation kept", o.AwaitingTurnStart)
	}
	if got := e.wake(t); got != "" {
		t.Errorf("a later wake wrote %q; want no re-emission", got)
	}
}

// TestClaudeWakeSightingNeedsExactText (SR-3.3, SR-3.5): only the agent's own
// UserPromptSubmit carrying the wake's exact text is take evidence. A turn
// start with other text, a subagent's prompt with the exact text, and a text
// whose escaping cannot be undone exactly (the body holds the escaped form
// literally) leave the obligation, so the next wake writes nothing.
func TestClaudeWakeSightingNeedsExactText(t *testing.T) {
	e := newWakeEnv(t, store.StateWaiting)
	e.admit(t, "m1", "keep &lt;/system-reminder> literal")
	body := e.wake(t)
	if body != "[agent-director: message 1 of 1]\nkeep &lt;/system-reminder> literal" {
		t.Fatalf("wake wrote %q", body)
	}
	turns := mustGetSpawn(t, e.st, e.id).TurnStarts
	for _, tc := range []struct {
		name   string
		fields map[string]any
		turn   int64
	}{
		{"a turn start with other text", map[string]any{"prompt": "hello"}, 1},
		{"a subagent's prompt", map[string]any{"prompt": claudeWake(body), "agent_id": "a5b92f0e7d3c6184", "agent_type": "Explore"}, 0},
		{"escaping that cannot be undone", map[string]any{"prompt": claudeWake(body)}, 1},
	} {
		e.fire(t, e.payload(t, "user-prompt-submit.json", tc.fields))
		turns += tc.turn
		if got := mustGetSpawn(t, e.st, e.id).TurnStarts; got != turns {
			t.Errorf("%s: Turn Starts %d; want %d", tc.name, got, turns)
		}
		if o := e.observe(t); o.AwaitingTurnStart != 1 {
			t.Errorf("%s: awaiting %d; want 1, no evidence", tc.name, o.AwaitingTurnStart)
		}
	}
	e.fire(t, e.payload(t, "stop.json", nil))
	e.admit(t, "m2", "next")
	if got := e.wake(t); got != "" {
		t.Errorf("a wake with the earlier prompt unsighted wrote %q; want nothing", got)
	}
	e.assertHeld(t, "m2", store.BusySendWaitReadinessUnknown, store.BusySendWaitEarlierMessage)
}

// TestClaudeWakeMeasuresEscaping (SR-1.6, SR-2.3; repair round 1 F1, Main
// Agent decision 12 as the MI updated it): the complete-prompt measure is
// Claude's whole rendering, its escaping of reserved closing tags included,
// within 9,600 bytes. The worst valid Message, 8,000 bytes nearly all
// `</system-reminder>`, is admitted and goes alone: its escaped render is
// exactly 9,534 bytes. Two Messages that fit one prompt raw but pass 9,600
// bytes escaped are two prompts, so the wake writes the first alone.
func TestClaudeWakeMeasuresEscaping(t *testing.T) {
	limits, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	e := newWakeEnv(t, store.StateWaiting)
	worst := strings.Repeat("</system-reminder>", 444) + "12345678" // 8,000 bytes
	e.admit(t, "worst", worst)
	body := "[agent-director: message 1 of 1]\n" + worst
	if got := e.wake(t); got != body {
		t.Errorf("wake wrote %d bytes; want the 8,000-byte Message alone (%d bytes)", len(got), len(body))
	}
	escaped := claudeWake(strings.ReplaceAll(body, "</system-reminder>", "&lt;/system-reminder>"))
	if limits.PromptBytes != 9600 || len(escaped) != 9534 || limits.Measure([]byte(body)) != 9534 {
		t.Errorf("prompt limit %d, escaped render %d, measure %d; want 9600, 9534, 9534",
			limits.PromptBytes, len(escaped), limits.Measure([]byte(body)))
	}

	e = newWakeEnv(t, store.StateWaiting)
	tags := strings.Repeat("</system-reminder>", 234) // 4,212 bytes, 4,914 escaped
	e.admit(t, "m1", tags)
	e.admit(t, "m2", tags)
	if got, want := e.wake(t), "[agent-director: message 1 of 1]\n"+tags; got != want {
		t.Errorf("wake wrote %d bytes; want m1 alone (%d bytes): together they pass 9,600 escaped", len(got), len(want))
	}
}

// TestClaudeTurnEndResignals (SR-3.1, SR-3.4; decision 10; repair round 1
// F2): the parent's own Stop or idle prompt in its Launch's session, with
// Messages Held and no earlier issued input awaiting evidence, touches the wake
// endpoint and nothing more: it claims nothing, writes nothing and never
// exits 2. Nothing Held, an awaiting prompt, a teammate's or another
// process's Stop, or another session touches nothing.
func TestClaudeTurnEndResignals(t *testing.T) {
	held := func(t *testing.T, e *wakeEnv) { e.admit(t, "m1", "hello") }
	cases := []struct {
		name    string
		state   string
		fixture string
		fields  map[string]any
		prep    func(t *testing.T, e *wakeEnv)
		foreign bool
		touch   bool
	}{
		{name: "Stop with Messages Held", state: store.StateWorking, fixture: "stop.json", prep: held, touch: true},
		{name: "idle prompt with Messages Held", state: store.StateWorking, fixture: "notification.json", prep: held, touch: true},
		{name: "nothing Held", state: store.StateWorking, fixture: "stop.json"},
		{name: "earlier prompt awaiting evidence", state: store.StateWaiting, fixture: "stop.json", prep: func(t *testing.T, e *wakeEnv) {
			e.admit(t, "m1", "hello")
			if e.wake(t) == "" {
				t.Fatal("the first wake wrote nothing")
			}
			e.admit(t, "m2", "next")
			e.fire(t, e.payload(t, "user-prompt-submit.json", nil)) // a turn, not the wake's text
		}},
		{name: "a teammate's Stop", state: store.StateWorking, fixture: "stop.json", prep: held,
			fields: map[string]any{"agent_id": "ahelper-5c1e9a7b3d2f8406", "agent_type": "helper"}},
		{name: "another process's Stop", state: store.StateWorking, fixture: "stop.json", prep: held, foreign: true},
		{name: "another session (F2)", state: store.StateWorking, fixture: "stop.json", prep: held, fields: map[string]any{
			"session_id": "0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d", "transcript_path": "/x/0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d.jsonl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, tc.state)
			if tc.prep != nil {
				tc.prep(t, e)
			}
			before := e.observe(t)
			p := e.parent
			if tc.foreign {
				p = foreignParent(t, e.st, e.id)
			}
			touched := e.wakeMtime(t)
			var stderr bytes.Buffer
			if out, err := e.handle(context.Background(), p, e.payload(t, tc.fixture, tc.fields), &stderr); err != nil || out != "" || stderr.Len() != 0 {
				t.Errorf("Handle = stdout %q, stderr %q, %v; want nothing, nil", out, stderr.String(), err)
			}
			if got := touched(); got != tc.touch {
				t.Errorf("wake endpoint touched = %t; want %t", got, tc.touch)
			}
			if after := e.observe(t); after.HeldCount != before.HeldCount || after.HandoverCount != before.HandoverCount {
				t.Errorf("Held %d → %d, hand-overs %d → %d; want nothing claimed", before.HeldCount, after.HeldCount,
					before.HandoverCount, after.HandoverCount)
			}
		})
	}
}

// TestClaudeSessionStartWatchPath (SR-3.4; repair round 1 F2): a covered
// Launch's own main SessionStart creates its wake endpoint, an empty private
// file, and returns it in watchPaths as its only stdout. An uncovered
// Launch's, one with no session, or a hook with no endpoint preparer
// (HandleConfig.PrepareBusySendWake; R2-7) writes and creates nothing.
func TestClaudeSessionStartWatchPath(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		covered, session, preparer bool
	}{
		{"covered", true, true, true},
		{"uncovered", false, true, true},
		{"no session (F2)", true, false, true},
		{"no endpoint preparer (R2-7)", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "wake-" + strings.ReplaceAll(t.Name(), "/", "-")
			st, db := ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane))
			wake, err := store.BusySendWakePath(st.StoreID(), ssgFreshPane.Token)
			if err != nil {
				t.Fatal(err)
			}
			if tc.covered {
				if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
					t.Fatal(err)
				}
				if err := apitest.SeedBusySendRoute(db, id, string(store.BusySendRouteClaudeHooks), wake); err != nil {
					t.Fatal(err)
				}
			}
			payload, _, _ := ssgPayload(t, "startup")
			if !tc.session {
				payload = `{"hook_event_name":"SessionStart","source":"startup"}`
			}
			want := ""
			if tc.covered && tc.session && tc.preparer {
				want = `{"hookSpecificOutput":{"hookEventName":"SessionStart","watchPaths":["` + wake + `"]}}` + "\n"
			}
			hc := hookConfig(envHook(id, ""), agentParent(t, st, id))
			if !tc.preparer {
				hc.PrepareBusySendWake = nil
			}
			if got := ssgHandleWith(t, st, hc, payload, newSilentLogger()); got != want {
				t.Errorf("SessionStart stdout = %q; want %q", got, want)
			}
			info, err := os.Lstat(wake)
			switch {
			case want == "" && !os.IsNotExist(err):
				t.Errorf("wake endpoint = %v, %v; want none created", info, err)
			case want != "" && (err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0):
				t.Errorf("wake endpoint = %v, %v; want an empty regular file, mode 0600", info, err)
			case want != "":
				if dir, err := os.Stat(filepath.Dir(wake)); err != nil || dir.Mode().Perm() != 0o700 {
					t.Errorf("launch directory = %v, %v; want mode 0700", dir, err)
				}
			}
		})
	}
}
