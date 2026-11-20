package hook_test

// busy_send_claude_test.go — busy send's Claude route A (Task t2.xis.oo.u8;
// Main Agent decisions 10 to 12) through hook.Handle on a real store: the
// owned FileChanged wake and, for Steer Turn, the owned parent PostToolBatch
// steer (Task t2.xis.wm.hg) are the only callbacks that consume the inbox, and
// only for the parent agent of the current Launch; Stop and the idle prompt
// only re-signal the wake endpoint; AD's own UserPromptSubmit sighting of the
// exact wake text is a wake's only take evidence, and the parent Stop ending
// the turn a steer went into is the steer's (E4 Main Agent decisions 1 and
// 2); SessionStart returns the endpoint to watch. The store's predicates (Turn
// Start counter, awaiting obligation, sighting and turn-end outcomes) are
// internal/store's (t3.xis.oo.u8.gr, t3.xis.wm.o7.cm), the coordinator's
// generic order and custody internal/busysend's (Task .xe); Steer Turn's
// limits, packing and prefix arithmetic are o7.cm's. The force's forced wake
// and sighting admission on this route are Task t2.xis.p8.zp's, over y2's real
// force claim and call end; y2's store matrix and call budgets are
// internal/store's and pkg/api's.

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
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
	// now is every callback's clock (HandleConfig.Now; nil: the wall clock)
	// and seam the store it runs on (nil: st).
	now  func() time.Time
	seam hook.HookStore
	self func() store.ProcessIdentity // nil: wakeHelper; distinct hook invocations may replace it
}

// newWakeEnv seeds the row in state with opts.
func newWakeEnv(t *testing.T, state string, opts ...apitest.SpawnOption) *wakeEnv {
	t.Helper()
	id := "wake-" + strings.ReplaceAll(t.Name(), "/", "-")
	st, db := ssgSeed(t, id, state, wakeSession, opts...)
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
	return newResumedWakeEnvWithHeld(t, nil, nil)
}

// newResumedWakeEnvWithHeld carries texts from the ended Launch and configures
// its new Launch before the first SessionStart (including a captured wake job).
func newResumedWakeEnvWithHeld(t *testing.T, texts []string, configure func(*wakeEnv, *hook.HandleConfig)) *wakeEnv {
	t.Helper()
	id := "wake-" + strings.ReplaceAll(t.Name(), "/", "-")
	st, db := ssgSeed(t, id, store.StateEnded, wakeSession, apitest.WithJsonlPath("/x/"+wakeSession+".jsonl"),
		apitest.WithLaunchIdentity(ssgOldPane))
	if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	e := &wakeEnv{st: st, db: db, id: id}
	for i, text := range texts {
		e.admit(t, fmt.Sprint(i), text)
	}
	e.resume(t, ssgNewPane, configure)
	return e
}

// resume moves e's ended row to a new Launch on pane in the same session, as
// resume's move and identity write do, and reports its agent in
// (SessionStart resume), so the new Launch records its own route and creates
// its endpoint; configure, when set, changes that SessionStart's config.
func (e *wakeEnv) resume(t *testing.T, pane store.LaunchIdentity, configure func(*wakeEnv, *hook.HandleConfig)) {
	t.Helper()
	res, moved, err := e.st.MoveToPendingWithExit(e.id, mustGetSpawn(t, e.st, e.id).Snapshot, time.Now().UnixMilli(), pane.Token, pane.Socket, "",
		store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, e.st, e.id))
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want applied", res, err)
	}
	if res, err := e.st.RecordLaunchIdentity(e.id, moved, pane.Token, pane); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity = %v, %v; want applied", res, err)
	}
	e.token, e.parent = pane.Token, agentParent(t, e.st, e.id)
	if e.endpoint, err = store.BusySendWakePath(e.st.StoreID(), e.token); err != nil {
		t.Fatal(err)
	}
	start := e.payload(t, "session-start-resume.json", map[string]any{"transcript_path": writeTranscript(t, wakeSession)})
	hc := hookConfig(envHook(e.id, ""), e.parent)
	hc.Self = wakeHelper
	if configure != nil {
		configure(e, &hc)
	}
	var stdout bytes.Buffer
	if err := hook.Handle(context.Background(), bytes.NewReader(start), &stdout, e.st, hc, newSilentLogger()); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	f, err := e.st.ReadBusySendRouteFacts(context.Background(), e.id, store.HookGate{})
	if err != nil || f.Kind != store.BusySendLaunchResume || !f.Covered || f.WakePath != e.endpoint || f.State != store.StateWaiting ||
		f.TurnStarts != 0 {
		t.Fatalf("resumed route facts = %+v, %v; want a waiting covered resume Launch with its own route and no Turn Start", f, err)
	}
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
	var stdout bytes.Buffer
	err := e.handleTo(ctx, p, payload, &stdout, stderr)
	return stdout.String(), err
}

// handleTo is handle onto stdout as given: a steer writes only to a stream
// that takes a write deadline, as the CLI's stdout for PostToolBatch does.
func (e *wakeEnv) handleTo(ctx context.Context, p hookParent, payload []byte, stdout, stderr io.Writer) error {
	hc := hookConfig(envHook(e.id, ""), p)
	hc.Self, hc.Stderr, hc.Now = wakeHelper, stderr, e.now
	if e.self != nil {
		hc.Self = e.self
	}
	var st hook.HookStore = e.st
	if e.seam != nil {
		st = e.seam
	}
	return hook.Handle(ctx, bytes.NewReader(payload), stdout, st, hc, newSilentLogger())
}

// tick moves every later callback's clock d on, starting from the wall clock
// the first time; tick(0) only freezes it.
func (e *wakeEnv) tick(d time.Duration) {
	at := time.Now()
	if e.now != nil {
		at = e.now()
	}
	at = at.Add(d)
	e.now = func() time.Time { return at }
}

// steerWith fires the parent's PostToolBatch from the agent onto w as its
// stdout and returns what it wrote there: Handle returns nil (exit 0, never
// ErrRewake) and writes nothing on stderr, whatever it wrote.
func (e *wakeEnv) steerWith(t *testing.T, w *wakeStderr) string {
	t.Helper()
	var stderr bytes.Buffer
	if err := e.handleTo(context.Background(), e.parent, e.payload(t, "post-tool-batch.json", nil), w, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("PostToolBatch Handle = %v, stderr %q; want nil and nothing on stderr", err, stderr.String())
	}
	return w.String()
}

// steer fires the parent's PostToolBatch onto a stdout that takes every byte
// and returns the one prompt it carries (steerContext).
func (e *wakeEnv) steer(t *testing.T) string {
	t.Helper()
	return steerContext(t, e.steerWith(t, &wakeStderr{keep: -1}))
}

// steerContext is the one prompt a steer's whole output carries, "" for no
// output: exactly one JSON object, its newline and nothing after, whose only
// key is hookSpecificOutput holding exactly hookEventName PostToolBatch and
// additionalContext (no decision, permission decision, continue or stop
// control). Decoding gives the prompt back byte for byte.
func steerContext(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}\n") {
		t.Fatalf("steer output %q; want one JSON object ended by one newline", out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var env map[string]json.RawMessage
	var inner map[string]string
	if err := dec.Decode(&env); err != nil || len(env) != 1 || json.Unmarshal(env["hookSpecificOutput"], &inner) != nil ||
		len(inner) != 2 || inner["hookEventName"] != hook.EventNamePostToolBatch {
		t.Fatalf("steer output %q (%v); want only hookSpecificOutput with hookEventName PostToolBatch and additionalContext", out, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("steer output %q continues after its envelope (%v); want EOF", out, err)
	}
	prompt, ok := inner["additionalContext"]
	if !ok {
		t.Fatalf("steer output %q has no additionalContext", out)
	}
	return prompt
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

// admit admits text as confirmed Message request in the Agent Default mode
// (Wait Turn), failing on a refusal.
func (e *wakeEnv) admit(t *testing.T, request, text string) {
	t.Helper()
	e.admitAs(t, request, text, "", true)
}

// admitAs admits text as Message request in mode ("": the Agent Default),
// confirmed or not, failing on a refusal. An unconfirmed Message carries no
// pair, so it has no Result to read: request only names it in failures.
func (e *wakeEnv) admitAs(t *testing.T, request, text string, mode store.BusySendMode, confirmed bool) {
	t.Helper()
	req := store.BusySendAdmissionRequest{InstanceID: e.id, Text: []byte(text), Confirmed: &confirmed}
	if confirmed {
		req.CallerUUID, req.DeliveryRequestID = wakeCaller, request
	}
	if mode != "" {
		req.SendMode = &mode
	}
	if _, err := e.st.AdmitBusySend(req); err != nil {
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

// force claims every Held Message into a force whose call enters now, through
// y2's real claim with the API call's 12 s active phase and 3 s cleanup (E5
// decision 1), as force-delivery does, and sets every later callback's clock
// a second into that call (inCall).
func (e *wakeEnv) force(t *testing.T) store.BusySendForceClaim {
	t.Helper()
	l, err := e.st.GetBusySendPublishedLaunch(e.id)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.st.ClaimBusySendForce(context.Background(), store.BusySendForceClaimRequest{InstanceID: e.id, Launch: l,
		ForceID: uuid.NewString(), OwnerCallID: uuid.NewString(), ActiveBudget: 12 * time.Second, CleanupBudget: 3 * time.Second,
		At: time.Now()})
	if err != nil || c.Handover == nil || c.Force.Outcome != store.BusySendForceRunning {
		t.Fatalf("force claim %+v, %v; want a running force", c, err)
	}
	e.inCall(c, time.Second)
	return c
}

// inCall sets every later callback's clock to d after c's call entered.
func (e *wakeEnv) inCall(c store.BusySendForceClaim, d time.Duration) {
	at := time.UnixMilli(c.Force.StartedAtMillis).Add(d)
	e.now = func() time.Time { return at }
}

// forceResult is c's force as get-force reads it.
func (e *wakeEnv) forceResult(t *testing.T, c store.BusySendForceClaim) store.BusySendForce {
	t.Helper()
	l, err := e.st.ReadBusySendForce(c.Force.ForceID)
	if err != nil || l.ForceResult == nil {
		t.Fatalf("ReadBusySendForce = %+v, %v; want found", l, err)
	}
	return *l.ForceResult
}

// endCall records c's call end as the API call does when its active phase
// runs out, 12 s after its entry.
func (e *wakeEnv) endCall(t *testing.T, c store.BusySendForceClaim) store.BusySendForce {
	t.Helper()
	end, err := e.st.EndBusySendForceCall(context.Background(), c.Ref, time.UnixMilli(c.Force.StartedAtMillis).Add(12*time.Second))
	if err != nil || !end.Ended {
		t.Fatalf("EndBusySendForceCall = %+v, %v; want the call's end recorded", end, err)
	}
	return end.Force
}

// pause is the real conditional pause's claim attempt (SR-6.5) on e's current
// Launch, for a new call with a minute of budget left.
func (e *wakeEnv) pause(t *testing.T) store.BusySendPauseDecision {
	t.Helper()
	l, err := e.st.GetBusySendPublishedLaunch(e.id)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.st.ClaimBusySendPauseWithin(context.Background(), store.BusySendPauseRequest{InstanceID: e.id, LaunchID: l.LaunchID,
		LaunchToken: l.LaunchToken, OwnerCallID: uuid.NewString(), Deadline: time.Now().Add(time.Minute)}, 10*time.Second)
	if err != nil {
		t.Fatalf("ClaimBusySendPauseWithin: %v", err)
	}
	return d
}

// assertRaced fails unless each request's Result is Final sent_unconfirmed
// with the sticky wait_turn_raced (every Claude wake is race-exposed, SR-3.3a,
// ahead of handed_over_not_proven) on e's Launch and session.
func (e *wakeEnv) assertRaced(t *testing.T, requests ...string) {
	t.Helper()
	e.assertSent(t, store.BusySendReasonWaitTurnRaced, requests...)
}

// assertSent fails unless each request's Result is Final sent_unconfirmed
// with reason on e's Launch and session.
func (e *wakeEnv) assertSent(t *testing.T, reason store.BusySendResultReason, requests ...string) {
	t.Helper()
	for _, request := range requests {
		r := e.result(t, request)
		if r.Class != store.BusySendClassSentUnconfirmed || !r.Final || r.Reason != string(reason) ||
			r.LaunchToken == nil || *r.LaunchToken != e.token || r.SessionID != wakeSession {
			t.Errorf("%s Result = %+v; want Final sent_unconfirmed %s on the captured Launch and session", request, r, reason)
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

// wakeStderr is a hook's error stream, or a steer's stdout, that takes a
// write deadline, as the route requires (R2-2), and keeps the last one set:
// before runs once at its first write; each write keeps keep bytes (all when
// negative) and returns err.
type wakeStderr struct {
	bytes.Buffer
	before   func()
	keep     int
	err      error
	deadline time.Time
}

func (w *wakeStderr) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	return nil
}

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
	e.assertHeld(t, "m3", store.BusySendWaitHandoverInProgress)

	// R2-1: the exact text from the right transcript under a contradicting
	// payload session_id is no sighting.
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(first),
		"session_id": "0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d"}))
	if o := e.observe(t); o.AwaitingTurnStart != 1 {
		t.Errorf("awaiting after a sighting under a contradicting payload session = %d; want 1", o.AwaitingTurnStart)
	}
	turns := mustGetSpawn(t, e.st, e.id).TurnStarts
	touched := e.wakeMtime(t)
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(first)}))
	if got := mustGetSpawn(t, e.st, e.id); got.TurnStarts != turns+1 || got.State != store.StateWorking {
		t.Errorf("after the sighting: Turn Starts %d, state %s; want %d, working", got.TurnStarts, got.State, turns+1)
	}
	if o := e.observe(t); o.AwaitingTurnStart != 0 {
		t.Errorf("awaiting after the exact sighting = %d; want 0", o.AwaitingTurnStart)
	}
	e.assertRaced(t, "m1", "m2")
	// An ordinary group's next prompt waits for a later turn end, never a
	// sighting (decision 13; only a force advances on one, SR-3.4).
	if touched() {
		t.Error("the sighting touched the wake endpoint; want only the next turn end to")
	}

	e.fire(t, e.payload(t, "stop.json", nil))
	if !touched() {
		t.Error("the parent's Stop with Messages Held left the wake endpoint untouched")
	}
	if got, want := e.wake(t), "[agent-director: message 1 of 1]\n"+c; got != want {
		t.Errorf("the next wake wrote %d bytes; want m3 alone (%d bytes)", len(got), len(want))
	}
	e.assertRaced(t, "m3")
	e.assertHeld(t, "late", store.BusySendWaitHandoverInProgress)
}

// failRouteFacts is the store with its nth route-facts read failing, after the
// callback's own authority read (the first) succeeded; fired records it. With
// before set, that read instead runs before and then reads: a causal barrier.
// With read set, each force prompt read first runs it with the read's context,
// and readErr keeps what the read then returned. steps records a parent
// Stop's take-evidence steps as Handle reaches them: "read" (its entry read,
// ReadBusySendTurnEndPrompts), "write" (its own gated lifecycle write) and
// "record" (RecordBusySendTurnEnd), each with the step's request (the zero one
// for "write"); turnEnd, when set, runs first, and an error it returns replaces
// the store's call (errNotApplied: a write that did not apply). commits keeps
// each issue commit's error; issued records refs only for successful commits.
type failRouteFacts struct {
	*store.Store
	n       int
	fired   bool
	before  func()
	read    func(context.Context)
	readErr error
	steps   []string
	turnEnd func(step string, te store.BusySendTurnEnd) error
	commits []error
	issued  []store.BusySendPromptRef
}

func (s *failRouteFacts) CommitBusySendIssue(ctx context.Context, req store.BusySendIssueRequest) (store.BusySendIssue, error) {
	out, err := s.Store.CommitBusySendIssue(ctx, req)
	s.commits = append(s.commits, err)
	if err == nil {
		s.issued = append(s.issued, req.Ref)
	}
	return out, err
}

// errNotApplied makes failRouteFacts' lifecycle write one whose gate did not
// hold: nothing written, no error.
var errNotApplied = errors.New("the write did not apply")

func (s *failRouteFacts) step(name string, te store.BusySendTurnEnd) error {
	s.steps = append(s.steps, name)
	if s.turnEnd == nil {
		return nil
	}
	return s.turnEnd(name, te)
}

func (s *failRouteFacts) ReadBusySendTurnEndPrompts(ctx context.Context, te store.BusySendTurnEnd) ([]store.BusySendTurnEndPrompt, error) {
	if err := s.step("read", te); err != nil {
		return nil, err
	}
	return s.Store.ReadBusySendTurnEndPrompts(ctx, te)
}

func (s *failRouteFacts) ApplyHookTransitionResult(id string, gate store.HookGate, state string, soft bool, event, path string,
	present bool) (store.UpsertOutcome, store.HookApplied, error) {
	switch err := s.step("write", store.BusySendTurnEnd{}); {
	case errors.Is(err, errNotApplied):
		return store.UpsertNoChange, store.HookApplied{}, nil
	case err != nil:
		return store.UpsertError, store.HookApplied{}, err
	}
	return s.Store.ApplyHookTransitionResult(id, gate, state, soft, event, path, present)
}

func (s *failRouteFacts) RecordBusySendTurnEnd(ctx context.Context, te store.BusySendTurnEnd) (store.BusySendTurnEndResult, error) {
	if err := s.step("record", te); err != nil {
		return store.BusySendTurnEndResult{}, err
	}
	return s.Store.RecordBusySendTurnEnd(ctx, te)
}

// noTurnEndSeam is the store without the turn-end take-evidence seam: its
// fields hide the store's two methods of it.
type noTurnEndSeam struct {
	*store.Store
	ReadBusySendTurnEndPrompts, RecordBusySendTurnEnd struct{}
}

func (s *failRouteFacts) ReadBusySendForcePrompt(ctx context.Context, id string) (store.BusySendForcePrompt, error) {
	if s.read != nil {
		s.read(ctx)
	}
	p, err := s.Store.ReadBusySendForcePrompt(ctx, id)
	s.readErr = err
	return p, err
}

func (s *failRouteFacts) ReadBusySendRouteFacts(ctx context.Context, id string, gate store.HookGate) (store.BusySendRouteFacts, error) {
	if s.n--; s.n == 0 {
		s.fired = true
		if s.before == nil {
			return store.BusySendRouteFacts{}, errors.New("injected route-facts read failure")
		}
		s.before()
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
// issues nothing. While a force's call runs (SR-3.4, SR-3.9), the forced wake
// skips only the turn, Dialog and compaction waits: each case marked force
// also refuses it, issuing none of the force's frozen prompt. Each case marked
// steer also refuses the parent's PostToolBatch steer of a Steer Turn Message
// (SR-3.4, SR-3.1; M2 P-C4.1, P-C4.6): a foreground or background child's
// callback (agent_id, the payload fixtures' own, though it reports the
// parent's session and process parent), another process, session or Launch,
// a payload session that contradicts its transcript, a failed facts read, no
// startup seam, no issuer identity or a stdout that takes no write deadline
// consume nothing, write no context on stdout and exit 0; neither does a
// Dialog or startup precedence, which a steer still waits on. No case logs an
// unknown event, and the steer writes no row.
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
		plain     bool   // the output stream (stderr, a steer's stdout) takes no write deadline
		noStartup bool   // no HandleConfig.ClaudeStartupPrompt
		resumed   bool   // the row is on a resumed Launch (newResumedWakeEnv)
		logged    string // a diagnostic the hook logs; "" checks none
		waits     []store.BusySendWaitReason
		force     bool   // also run with a force's call running
		steer     string // also run as the parent's PostToolBatch from this fixture ("": none)
		noWake    bool   // run only as the steer
	}{
		{name: "another process's callback", parent: func(t *testing.T, e *wakeEnv) hookParent { return foreignParent(t, e.st, e.id) }, force: true,
			steer: "post-tool-batch.json"},
		{name: "a subagent's callback", fields: map[string]any{"agent_id": "a5b92f0e7d3c6184", "agent_type": "Explore"}, force: true},
		{name: "a foreground child's callback", steer: "post-tool-batch-subagent.json", noWake: true},
		{name: "a background child's callback", steer: "post-tool-batch-background.json", noWake: true},
		{name: "another watched path", fields: map[string]any{"file_path": "/work/demo/.envrc"}, force: true},
		{name: "uncovered Launch", prep: func(t *testing.T, e *wakeEnv) {
			if err := apitest.SeedBusySendCoverage(e.db, e.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
				t.Fatal(err)
			}
		}, force: true, steer: "post-tool-batch.json"},
		{name: "ended Launch", prep: func(t *testing.T, e *wakeEnv) {
			e.fire(t, e.payload(t, "session-end-prompt-input-exit.json", nil))
		}, waits: []store.BusySendWaitReason{store.BusySendWaitLaunchEnded}, force: true, steer: "post-tool-batch.json"},
		{name: "another session (F2)", fields: map[string]any{"session_id": "0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d",
			"transcript_path": "/x/0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d.jsonl"}, force: true, steer: "post-tool-batch.json"},
		{name: "a payload session contradicting its transcript", fields: map[string]any{"session_id": "0d1c2b3a-4f5e-4d6c-8b7a-9e8f7a6b5c4d"},
			steer: "post-tool-batch.json"},
		{name: "no session (F2)", fields: map[string]any{"session_id": nil, "transcript_path": nil}},
		{name: "running turn", state: store.StateWorking, waits: []store.BusySendWaitReason{store.BusySendWaitTurnRunning}},
		{name: "open Dialog", state: store.StateCheckPermission, prep: func(t *testing.T, e *wakeEnv) {
			if _, err := apitest.SeedPermissionRequest(e.db, e.id, "Bash"); err != nil {
				t.Fatal(err)
			}
		}, waits: []store.BusySendWaitReason{store.BusySendWaitDialog}, steer: "post-tool-batch.json"},
		{name: "agent starting", state: store.StatePending, waits: []store.BusySendWaitReason{store.BusySendWaitAgentStarting},
			steer: "post-tool-batch.json"},
		{name: "startup prompt not yet taken", opts: startup, waits: []store.BusySendWaitReason{store.BusySendWaitAgentStarting}, force: true,
			steer: "post-tool-batch.json"},
		{name: "facts unreadable at first (R2-5)", failAt: 1, logged: "hook: busy-send route facts", force: true, steer: "post-tool-batch.json"},
		{name: "facts unreadable at eligibility", failAt: 2, waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown},
			steer: "post-tool-batch.json"},
		{name: "facts unreadable at authorize", failAt: 3, waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown},
			steer: "post-tool-batch.json"},
		{name: "no helper identity", noSelf: true, force: true, steer: "post-tool-batch.json"},
		{name: "output that takes no write deadline (R2-2)", plain: true, logged: "takes no write deadline",
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}, force: true, steer: "post-tool-batch.json"},
		{name: "no startup-prompt reader on a first turn (R2-7)", noStartup: true,
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}, force: true},
		{name: "no startup-prompt reader after a Turn Start (R3-1)", noStartup: true, opts: []apitest.SpawnOption{apitest.WithTurnStarts(1)},
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}, steer: "post-tool-batch.json"},
		{name: "no startup-prompt reader on a resumed Launch (R3-1)", noStartup: true, resumed: true,
			waits: []store.BusySendWaitReason{store.BusySendWaitReadinessUnknown}},
	}
	for _, tc := range cases {
		for _, mode := range []string{"wake", "forced wake", "steer"} {
			if mode == "wake" && tc.noWake || mode == "forced wake" && !tc.force || mode == "steer" && tc.steer == "" {
				continue
			}
			forced, steer := mode == "forced wake", mode == "steer"
			name := tc.name
			switch mode {
			case "forced wake":
				name += ", a force's call running"
			case "steer":
				name += ", a steer"
			}
			t.Run(name, func(t *testing.T) {
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
				if steer {
					e.admitAs(t, "m1", "hello", store.BusySendSteerTurn, true)
				} else {
					e.admit(t, "m1", "hello")
				}
				admitted := e.result(t, "m1")
				var force store.BusySendForceClaim
				if forced {
					force = e.force(t)
				}
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
				hc.Self, hc.Now = wakeHelper, e.now
				if tc.noSelf {
					hc.Self = nil
				}
				if tc.noStartup {
					hc.ClaudeStartupPrompt = nil
				}
				var plain, logs bytes.Buffer
				// Both streams take a write deadline, as the CLI gives the owned
				// hooks theirs; plain replaces the one the callback writes to.
				stdout, stderr := &wakeStderr{keep: -1}, &wakeStderr{keep: -1}
				var out io.Writer = stdout
				hc.Stderr = stderr
				payload := e.fileChanged(t, tc.fields)
				switch {
				case steer:
					payload = e.payload(t, tc.steer, tc.fields)
					if tc.plain {
						out = &plain
					}
				case tc.plain:
					hc.Stderr = &plain
				}
				if err := hook.Handle(context.Background(), bytes.NewReader(payload), out, st, hc, log.New(&logs, "", 0)); err != nil {
					t.Errorf("Handle = %v; want nil (exit 0)", err)
				}
				if stdout.Len() != 0 || stderr.Len() != 0 || plain.Len() != 0 {
					t.Errorf("wrote stdout %q, stderr %q, %q; want nothing", stdout.String(), stderr.String(), plain.String())
				}
				if failing.fired != (tc.failAt > 0) {
					t.Errorf("injected route-facts failure fired = %t; want %t", failing.fired, tc.failAt > 0)
				}
				if !strings.Contains(logs.String(), tc.logged) || strings.Contains(logs.String(), "unknown event") {
					t.Errorf("hook log %q; want it to name %q and no unknown event", logs.String(), tc.logged)
				}
				assertRowUnchanged(t, e.st, e.id, row)
				if after, err := e.st.OpenPermissionRequestsForSpawn(e.id); err != nil || len(after) != len(requests) {
					t.Errorf("open permission requests %d → %d (%v); want unchanged", len(requests), len(after), err)
				}
				if forced {
					// The frozen prompt stays unissued: still claimed, or released by Launch End.
					if h, err := e.st.ReadBusySendHandover(context.Background(), force.Handover.HandoverID); err != nil ||
						!slices.Contains([]store.BusySendEmission{store.BusySendEmissionNone, store.BusySendEmissionReleased}, h.Prompts[0].Emission) {
						t.Errorf("the force's prompt = %+v, %v; want never issued", h.Prompts, err)
					}
					return
				}
				waits := tc.waits
				if waits == nil {
					waits = []store.BusySendWaitReason{admitted.Wait.Reason}
				}
				e.assertHeld(t, "m1", waits...)
				// An uncovered Launch shows no delivery facts (nil).
				if o, err := e.st.ReadBusySendObservation(e.id, time.Second); err != nil || (o != nil && (o.HandoverCount != 0 || o.AwaitingTurnStart != 0)) {
					t.Errorf("delivery facts = %+v, %v; want no hand-over claimed or issued", o, err)
				}
			})
		}
	}
}

// TestClaudeWakeOutputBoundary (M2 P-C4.2, P-C4.3; SR-2.4, SR-3.5): at the
// wake's first byte the prompt's issue and its awaiting obligation are
// durable. Only a whole write is complete output (exit 2, Final); a short
// write is partial and a failed one uncertain, both non-Final, never retried
// by a later wake, their obligation kept. The agent's sighting arriving before
// the completion record admits but leaves custody issued until written. A
// sighting and Stop cannot turn incomplete output into order. An authorized
// writer can still emit after logical End, but its late completion grants no
// order or job authority (SR-6.2); this is not physical Exit evidence.
// A steer's output (M2 P-C4.2, P-C4.3; SR-2.5, SR-3.4) is its one stdout
// envelope, under the Job deadline: intent and the obligation are durable at
// its first byte, and only the whole envelope, its newline included, is
// complete (Final handed_over_not_proven, never wait_turn_raced), returning the
// prefix's never-issued prompt to Held for the next parent boundary. A short
// or failed envelope keeps the barrier and its obligation: neither a later
// boundary nor, after the turn's Stop, a wake retries it or issues the
// remainder.
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
			custody := func(want string) {
				t.Helper()
				facts, err := apitest.ReadBusySendFacts(e.db, e.id)
				if err != nil || len(facts.Messages) == 0 || facts.Messages[0].Place != want {
					t.Fatalf("wake custody %+v, %v; want %s", facts.Messages, err, want)
				}
			}
			w := &wakeStderr{keep: tc.keep, err: tc.err}
			w.before = func() {
				if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 0 {
					t.Errorf("at the first byte: awaiting %d, Held %d; want the issued prompt's obligation 1, Held 0", o.AwaitingTurnStart, o.HeldCount)
				}
				if tc.sight {
					e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
					if o := e.observe(t); o.AwaitingTurnStart != 0 {
						t.Errorf("sighting before output: awaiting %d; want 0", o.AwaitingTurnStart)
					}
				}
				custody("issued") // Admission alone cannot supply missing output.
			}
			got := e.wakeWith(t, w)
			if tc.sight {
				if got != body {
					t.Errorf("wrote %q; want %q", got, body)
				}
				e.assertRaced(t, "m1")
				custody("closed") // The completion records order, before any Stop.
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
			e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
			e.fire(t, e.payload(t, "stop.json", nil))
			custody("issued")
			if o := e.observe(t); o.AwaitingTurnStart != 0 {
				t.Errorf("after sighting: awaiting %d; want 0 despite retained custody", o.AwaitingTurnStart)
			}
			e.admit(t, "m2", "later")
			if got := e.wake(t); got != "" {
				t.Errorf("sighting and Stop released incomplete output: %q", got)
			}
			e.assertHeld(t, "m2", store.BusySendWaitHandoverInProgress)
		})
		t.Run(tc.name+", a steer", func(t *testing.T) {
			e := newWakeEnv(t, store.StateWorking)
			a, b := strings.Repeat("a", 5000), strings.Repeat("b", 5000)
			e.admitAs(t, "m1", a, store.BusySendSteerTurn, true)
			e.admitAs(t, "m2", b, store.BusySendSteerTurn, true)
			w := &wakeStderr{keep: tc.keep, err: tc.err}
			w.before = func() {
				if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 0 {
					t.Errorf("at the first byte: awaiting %d, Held %d; want the issued prompt's obligation 1, Held 0", o.AwaitingTurnStart, o.HeldCount)
				}
			}
			began := time.Now()
			got := e.steerWith(t, w)
			if w.deadline.Before(began) || w.deadline.After(time.Now().Add(time.Minute)) {
				t.Errorf("write deadline %v; want the Job's, within a minute of the callback", w.deadline)
			}
			if !tc.sight {
				if r := e.result(t, "m1"); r.Final || r.Class != store.BusySendClassSentUnconfirmed || r.PartialTransport != tc.partial {
					t.Errorf("Result = %+v; want non-Final sent_unconfirmed, partial transport %t", r, tc.partial)
				}
				again := e.steer(t)
				// The turn's Stop is no evidence for output never complete.
				e.fire(t, e.payload(t, "stop.json", nil))
				if wake := e.wake(t); again != "" || wake != "" {
					t.Errorf("a later boundary wrote %q and a wake after the Stop %q; want no retry and no remainder behind the barrier", again, wake)
				}
				if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 1 {
					t.Errorf("awaiting %d, Held %d; want the obligation kept and m2 Held", o.AwaitingTurnStart, o.HeldCount)
				}
				return
			}
			if got := steerContext(t, got); got != "[agent-director: message 1 of 1]\n"+a {
				t.Fatalf("steer wrote %d bytes of context; want m1 alone", len(got))
			}
			e.assertSent(t, store.BusySendReasonHandedOverNotProven, "m1")
			if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 1 {
				t.Errorf("awaiting %d, Held %d; want m1's obligation and m2 returned to Held", o.AwaitingTurnStart, o.HeldCount)
			}
			if got := e.steer(t); got != "[agent-director: message 1 of 1]\n"+b {
				t.Errorf("the next boundary wrote %d bytes of context; want the remainder m2", len(got))
			}
		})
	}
	for _, end := range []bool{false, true} {
		t.Run(fmt.Sprintf("sighted force, End before output %t", end), func(t *testing.T) {
			e := newWakeEnv(t, store.StateWorking)
			e.admit(t, "m1", "hello")
			c := e.force(t)
			const body = "[agent-director: message 1 of 1]\nhello"
			w := &wakeStderr{keep: 10, err: io.ErrShortWrite}
			wantOutput, wantTransport := body[:10], store.BusySendTransportPartial
			if end {
				w.keep, w.err = -1, nil
				wantOutput, wantTransport = body, store.BusySendTransportWritten
			}
			var final store.BusySendResult
			var fenced store.BusySendHandover
			w.before = func() {
				e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
				h, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
				if err != nil || !h.Open || len(h.Prompts) != 1 || h.Prompts[0].Transport != "" || h.Prompts[0].OrderKind != "" ||
					e.observe(t).AwaitingTurnStart != 0 {
					t.Fatalf("sighting alone: %+v, %v; want admitted with output and order still missing", h, err)
				}
				if end {
					// The writer is already authorized. Logical End does not stop
					// its bytes; no synthetic physical Exit is supplied here.
					e.fire(t, e.payload(t, "session-end-prompt-input-exit.json", nil))
				} else if f := e.endCall(t, c); f.Outcome != store.BusySendForceHandedOver || f.Reason != store.BusySendForceAdmitted {
					t.Fatalf("call end with sighting but no output: %+v; want handed_over admitted", f)
				}
				final = e.result(t, "m1")
				if final.Final != end {
					t.Fatalf("before output: %+v; want Final=%t only from Launch End", final, end)
				}
				fenced, err = e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := e.wakeWith(t, w); got != wantOutput {
				t.Fatalf("authorized writer emitted %q; want %q", got, wantOutput)
			}
			if !end {
				final = e.result(t, "m1")
				if final.Final || !final.PartialTransport {
					t.Fatalf("admitted force with short output: %+v; want non-Final and partial", final)
				}
			}
			for i := range 2 {
				if i > 0 {
					e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
				}
				h, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
				if err != nil || len(h.Prompts) != 1 || h.Prompts[0].Transport != wantTransport || h.Prompts[0].OrderKind != "" {
					t.Fatalf("late completion/sighting: %+v, %v; want %s without order", h, err, wantTransport)
				}
				if h.Revision != fenced.Revision || h.PromptCursor != fenced.PromptCursor || h.Open != fenced.Open {
					t.Errorf("late callback changed fenced job: %+v; want revision/cursor/custody from %+v", h, fenced)
				}
				facts, err := apitest.ReadBusySendFacts(e.db, e.id)
				if err != nil || len(facts.Messages) != 1 || facts.Messages[0].Place != "issued" {
					t.Fatalf("custody after completion/sighting: %+v, %v; want issued", facts.Messages, err)
				}
				fence := storefix.ReadLaunchFence(t, e.st, c.Handover.Destination.LaunchID)
				if fence.Ended != end || len(fence.Prompts) != 1 || fence.Prompts[0].Fenced != end {
					t.Fatalf("Launch fence: %+v; want ended/fenced=%t, one retained prompt", fence, end)
				}
				if got := e.result(t, "m1"); !reflect.DeepEqual(got, final) {
					t.Errorf("late callback changed Result: %+v; want %+v", got, final)
				}
				progress, err := e.st.ReadBusySendForceProgress(context.Background(), c.Force.ForceID)
				if err != nil || progress.Outcome == store.BusySendForceRunning || progress.Next != -1 {
					t.Errorf("old force job: %+v, %v; want finished with no next prompt", progress, err)
				}
				if got := e.wake(t); got != "" {
					t.Fatalf("late callback revived the old job: %q", got)
				}
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
	e.assertHeld(t, "m2", store.BusySendWaitHandoverInProgress)
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

// Regression b.kii / PM1: the real coordinator and hook must agree on the
// captured Launch's measure, including overlapping literal outputs counted
// only against the original body. These synthetic snapshots qualify no provider.
func TestClaudeStoredPromptMeasure(t *testing.T) {
	for _, boundary := range []string{"wait", "steer", "force"} {
		t.Run(boundary, func(t *testing.T) {
			caps := storefix.BusySendLaterCapabilities()
			caps.Modes[0].MessageBytes, caps.Modes[0].PromptBytes = 20, 160
			caps.Modes[1].MessageBytes, caps.Modes[1].PromptBytes = 20, 90
			e := newWakeEnv(t, store.StateWaiting, apitest.WithBusySendCapabilities(caps))
			seam := &failRouteFacts{Store: e.st}
			e.seam = seam
			mode := store.BusySendWaitTurn
			if boundary != "wait" {
				e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": "existing turn"}))
			}
			if boundary == "steer" {
				mode = store.BusySendSteerTurn
			}
			text := strings.Repeat("<&", 8)
			e.admitAs(t, "first", text, mode, true)
			e.admitAs(t, "tail", "tail", mode, true)
			var force store.BusySendForceClaim
			if boundary == "force" {
				force = e.force(t)
			}
			output := e.wake
			if boundary == "steer" {
				output = e.steer
			}
			const header = "[agent-director: message 1 of 1]\n"
			body := header + text
			if got := output(t); got != body {
				t.Fatalf("stored %s output = %q; want exactly one whole prompt %q", boundary, got, body)
			}
			if len(seam.issued) != 1 {
				t.Fatalf("issue commits = %d; want one", len(seam.issued))
			}
			h, err := e.st.ReadBusySendHandover(context.Background(), seam.issued[0].HandoverID)
			if err != nil || len(h.Prompts) != 2 {
				t.Fatalf("stored partition = %+v, %v; want two frozen whole prompts", h, err)
			}
			// Literal expected rendering: '<' produces '&lt;', whose '&'
			// is not escaped again. The budget mode adds exactly 32 bytes.
			rendered := int64(len("[later]\n" + header + strings.Repeat("&lt;&amp;", 8) + "\n[/later]"))
			tailBytes := int64(len("[later]\n" + header + "tail\n[/later]"))
			if boundary == "steer" {
				rendered, tailBytes = int64(len(body))+32, int64(len(header+"tail"))+32
			}
			for i, want := range []struct {
				body  string
				bytes int64
			}{{body, rendered}, {header + "tail", tailBytes}} {
				if p := h.Prompts[i]; string(p.Body) != want.body || p.RenderedBytes != want.bytes {
					t.Errorf("frozen prompt %d = %q, %d bytes; want %q, %d bytes", i, p.Body, p.RenderedBytes, want.body, want.bytes)
				}
			}
			facts, err := apitest.ReadBusySendFacts(e.db, e.id)
			if err != nil || len(facts.Messages) != 2 {
				t.Fatalf("Message facts = %+v, %v", facts, err)
			}
			wantPlace := "held"
			if boundary == "force" {
				wantPlace = "claimed"
			}
			if tail := facts.Messages[1]; tail.Place != wantPlace || !tail.Reserved || string(tail.Text) != "tail" || tail.Seq != 2 ||
				tail.SendMode != string(mode) || string(facts.Messages[0].Text) != text {
				t.Errorf("Message custody = %+v; want original text/mode and reserved tail %s at sequence 2", facts.Messages, wantPlace)
			}
			if boundary == "steer" {
				if got := output(t); got != header+"tail" {
					t.Fatalf("next tool boundary wrote %q; want the whole tail", got)
				}
			} else if got := output(t); got != "" {
				t.Fatalf("unsighted wake repeated %q; want no output", got)
			}
			if boundary == "force" {
				if ended := e.endCall(t, force); ended.Outcome != store.BusySendForcePending {
					t.Errorf("force ended %+v; want pending under Claude's local sighting policy", ended)
				}
			}
		})
	}
}

// TestClaudeForcedWake (SR-3.4, SR-4.2, SR-3.9; M2 P-C4.3): while a force's
// call runs, the agent's own FileChanged is that force's forced wake in any
// turn or Dialog state, with every Held Message already claimed by
// the force: its only output is the frozen prompt, byte for byte, on stderr,
// exit 2 (ErrRewake) and nothing on stdout, and no row, Dialog or permission
// request changes. At its first byte the prompt's intent is durable and its
// Message forced; a callback coalesced into it, and a later one before the
// prompt's sighting, writes nothing. Its write deadline is the force's
// recorded call deadline (y2's clip, M1 ForceFencedAtCallEnd), never an
// ordinary wake's budget.
func TestClaudeForcedWake(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		dialog      bool
	}{
		{name: "idle", state: store.StateWaiting},
		{name: "a running turn", state: store.StateWorking},
		{name: "a permission Dialog", state: store.StateCheckPermission, dialog: true},
		{name: "a question", state: store.StateAskUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, tc.state)
			if tc.dialog {
				if _, err := apitest.SeedPermissionRequest(e.db, e.id, "Bash"); err != nil {
					t.Fatal(err)
				}
			}
			e.admit(t, "m1", "hello")
			c := e.force(t)
			if o := e.observe(t); o.HeldCount != 0 {
				t.Fatalf("Held %d after the force's claim; want 0", o.HeldCount)
			}
			row := mustGetSpawn(t, e.st, e.id)
			requests, err := e.st.OpenPermissionRequestsForSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			const body = "[agent-director: message 1 of 1]\nhello"
			w := &wakeStderr{keep: -1}
			w.before = func() {
				if o := e.observe(t); o.AwaitingTurnStart != 1 || !e.result(t, "m1").Forced {
					t.Errorf("at the first byte: awaiting %d, m1 %+v; want the issued prompt's obligation and m1 forced", o.AwaitingTurnStart, e.result(t, "m1"))
				}
				if got := e.wakeWith(t, &wakeStderr{keep: -1}); got != "" {
					t.Errorf("a callback coalesced into the forced wake wrote %q; want nothing", got)
				}
			}
			left, began := c.Handover.JobDeadline.Sub(e.now()), time.Now()
			if got := e.wakeWith(t, w); got != body {
				t.Fatalf("forced wake wrote %q; want %q", got, body)
			}
			if w.deadline.Before(began.Add(left)) || w.deadline.After(time.Now().Add(left)) {
				t.Errorf("write deadline %v; want the force's call deadline, %v from the callback's clock", w.deadline, left)
			}
			if got := e.wake(t); got != "" {
				t.Errorf("a later callback before the sighting wrote %q; want nothing", got)
			}
			assertRowUnchanged(t, e.st, e.id, row)
			if after, err := e.st.OpenPermissionRequestsForSpawn(e.id); err != nil || !reflect.DeepEqual(after, requests) {
				t.Errorf("open permission requests %+v, %v; want unchanged %+v", after, err, requests)
			}
		})
	}
}

// TestClaudeForcedWakeBoundaries (SR-3.2, SR-3.4, SR-4.3; M1
// ForceFencedAtCallEnd, NoOrphanForceClaim; M2 P-C4.3): a forced wake of a
// two-prompt force that does not write prompt 0 whole is never replayed and
// supplies no admission or negative. A partial write keeps prompt 1 back even
// once prompt 0 is sighted (admission alone); an unacknowledged issue commit
// writes nothing; a callback past the force's 12 s issue phase but within its
// call writes nothing and leaves the claim to the call; and one held at its
// last look while the call ends consumes the call's fence: nothing is written
// and the returned Messages are not delivered by that callback. A wake 100 ms
// before the issue phase ends reads the prompt under those 100 ms, never a
// fresh wake budget (code review F1): a read delayed past them is cancelled,
// writing and issuing nothing, and the whole invocation ends at the call's
// recorded deadline even when the clock it reads later steps back. When the
// call's caller is lost after prompt 0 was written whole (no call end; SR-3.1,
// SR-3.2, E5 decision 7; product review F1), the agent's exact sighting after
// the recorded deadline both admits prompt 0 and finishes the force:
// handed_over admitted, b Held again unforced in its place, a's Final Result
// unchanged and the wake touched for b. The same sighting before the deadline
// releases nothing, and a SessionStart past it leaves the force pending with b
// Held.
func TestClaudeForcedWakeBoundaries(t *testing.T) {
	a, b := strings.Repeat("a", 5000), strings.Repeat("b", 5000)
	first := "[agent-director: message 1 of 1]\n" + a
	for _, tc := range []struct {
		name     string
		at       time.Duration // the callback's clock after the call's entry
		keep     int
		err      error
		barrier  string        // at the route's last look: "lost ack" or "call end"; at the prompt read: "delayed read" or "clock back"
		event    string        // then, with no call end recorded: "sighting" (prompt 0 whole) or "SessionStart"
		eventAt  time.Duration // the event's clock after the call's entry
		touch    bool          // the event touches the wake
		wrote    string
		outcome  store.BusySendForceOutcome
		reason   store.BusySendForceReason // "": not checked
		held     int64
		awaiting int64
	}{
		{name: "a short write, then its sighting", at: time.Second, keep: 10, err: io.ErrShortWrite, event: "sighting", eventAt: time.Second,
			wrote: first[:10], outcome: store.BusySendForceRunning},
		{name: "a failed write", at: time.Second, keep: 0, err: errors.New("write |1: broken pipe"), outcome: store.BusySendForceRunning,
			awaiting: 1},
		{name: "an unacknowledged issue commit", at: time.Second, keep: -1, barrier: "lost ack", outcome: store.BusySendForceRunning,
			awaiting: 1},
		{name: "after the issue phase", at: 13 * time.Second, keep: -1, outcome: store.BusySendForceRunning},
		{name: "the call ends at the last look", at: time.Second, keep: -1, barrier: "call end", outcome: store.BusySendForceRefused, held: 2},
		{name: "a prompt read delayed past the issue phase", at: 11900 * time.Millisecond, keep: -1, barrier: "delayed read",
			outcome: store.BusySendForceRunning},
		{name: "the clock steps back during the prompt read", at: 11900 * time.Millisecond, keep: -1, barrier: "clock back", wrote: first,
			outcome: store.BusySendForceRunning, awaiting: 1},
		{name: "the caller lost, then a sighting past the call's deadline", at: time.Second, keep: -1, wrote: first, event: "sighting",
			eventAt: 16 * time.Second, touch: true, outcome: store.BusySendForceHandedOver, reason: store.BusySendForceAdmitted, held: 1},
		{name: "the caller lost, then a sighting within the call", at: time.Second, keep: -1, wrote: first, event: "sighting",
			eventAt: 14 * time.Second, outcome: store.BusySendForceRunning},
		{name: "the caller lost, then a SessionStart past the call's deadline", at: time.Second, keep: -1, wrote: first, event: "SessionStart",
			eventAt: 16 * time.Second, touch: true, outcome: store.BusySendForcePending, reason: store.BusySendForceAwaitingAdmission, held: 1,
			awaiting: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, store.StateWorking)
			e.admit(t, "a", a)
			e.admit(t, "b", b)
			c := e.force(t)
			now := time.UnixMilli(c.Force.StartedAtMillis).Add(tc.at)
			e.now = func() time.Time { return now }
			issueLeft, callLeft := c.Handover.IssueDeadline.Sub(now), time.UnixMilli(c.Force.CallDeadlineMillis).Sub(now)
			var lost atomic.Bool
			var began, readAt time.Time
			seam := &failRouteFacts{Store: e.st, n: 2, before: func() {
				switch tc.barrier {
				case "lost ack":
					lost.Store(true)
				case "call end":
					e.endCall(t, c)
				}
			}, read: func(ctx context.Context) {
				readAt = time.Now()
				if d, ok := ctx.Deadline(); !ok || d.Before(began.Add(issueLeft)) || d.After(readAt.Add(issueLeft)) {
					t.Errorf("the prompt read's context ends at %v (%t); want the %v left of the original issue phase", d, ok, issueLeft)
					return
				}
				switch tc.barrier {
				case "delayed read":
					<-ctx.Done()
				case "clock back":
					now = time.UnixMilli(c.Force.StartedAtMillis).Add(time.Second)
				}
			}}
			e.seam = seam
			stop, err := store.ObserveSQLiteForTest(e.st, func(d driver.Driver) driver.Driver {
				return sqliteobserve.Wrap(d, sqliteobserve.Hooks{AfterCommit: func(context.Context) error {
					if lost.CompareAndSwap(true, false) {
						return errors.New("the issue commit's acknowledgement was lost")
					}
					return nil
				}})
			})
			if err != nil {
				t.Fatal(err)
			}
			w := &wakeStderr{keep: tc.keep, err: tc.err}
			began = time.Now()
			got := e.wakeWith(t, w)
			if err := stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			looked := tc.at < 12*time.Second && tc.barrier != "delayed read"
			if got != tc.wrote || seam.fired != looked || lost.Load() {
				t.Fatalf("forced wake wrote %q (last look reached %t, ack still to lose %t); want %q", got, seam.fired, lost.Load(), tc.wrote)
			}
			switch tc.barrier {
			case "delayed read":
				if seam.readErr == nil {
					t.Error("the delayed prompt read returned no error; want it cancelled at the issue phase's end")
				}
			case "clock back":
				if w.deadline.Before(began.Add(callLeft)) || w.deadline.After(readAt.Add(callLeft)) {
					t.Errorf("write deadline %v; want the call's recorded deadline, %v from the wake's start", w.deadline, callLeft)
				}
			}
			e.seam = nil
			if tc.event != "" {
				now = time.UnixMilli(c.Force.StartedAtMillis).Add(tc.eventAt)
				before, touched := e.result(t, "a"), e.wakeMtime(t)
				payload := e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(first)})
				if tc.event == "SessionStart" {
					payload = e.payload(t, "session-start-compact.json", nil)
				}
				var stderr bytes.Buffer
				if out, err := e.handle(context.Background(), e.parent, payload, &stderr); err != nil || stderr.Len() != 0 ||
					tc.event == "sighting" && out != "" {
					t.Errorf("%s = stdout %q, stderr %q, %v; want no output but SessionStart's watch path", tc.event, out, stderr.String(), err)
				}
				if got := touched(); got != tc.touch {
					t.Errorf("%s touched the wake: %t; want %t", tc.event, got, tc.touch)
				}
				if r := e.result(t, "a"); !reflect.DeepEqual(r, before) || tc.touch && !r.Final {
					t.Errorf("a after the %s = %+v; want its Result unchanged %+v, Final once the force is finished", tc.event, r, before)
				}
			}
			if tc.held == 1 {
				facts, err := apitest.ReadBusySendFacts(e.db, e.id)
				if r := e.result(t, "b"); err != nil || r.Class != store.BusySendClassHeld || r.Forced || facts.Messages[1].Place != "held" ||
					facts.Messages[1].Seq != 2 {
					t.Errorf("b = %+v (%+v, %v); want Held again in its accepted place, unforced", r, facts.Messages, err)
				}
			}
			if tc.outcome == store.BusySendForceRunning && tc.barrier != "delayed read" {
				if again := e.wake(t); again != "" {
					t.Errorf("a later forced wake wrote %d bytes; want no replay and no next prompt", len(again))
				}
			}
			if f := e.forceResult(t, c); f.Outcome != tc.outcome || tc.reason != "" && f.Reason != tc.reason {
				t.Errorf("force %+v; want %s %s", f, tc.outcome, tc.reason)
			}
			if o := e.observe(t); o.HeldCount != tc.held || o.AwaitingTurnStart != tc.awaiting {
				t.Errorf("Held %d, awaiting %d; want %d, %d", o.HeldCount, o.AwaitingTurnStart, tc.held, tc.awaiting)
			}
		})
	}
}

// TestClaudeForceSightingAfterCallReturn (SR-3.5, SR-4.3, SR-4.5; M1
// ForceAdmissionSound and ForceOutcomeStable at the real sighting; M2 P-C4.4):
// an idle force whose prompt was written whole but not sighted when its call
// ended is pending awaiting_admission, its carried Result Final. A Stop, a
// FileChanged after the call, the agent's transcript recording the prompt
// and a store reopen admit nothing; AD's own later UserPromptSubmit sighting
// of the exact text makes it handed_over admitted, its Result unchanged, and a
// repeated sighting changes nothing. Confirm From Transcripts, on or off, is
// read back as set and changes none of it (no transcript reader, decision 4).
func TestClaudeForceSightingAfterCallReturn(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirm_from_transcripts %t", confirm), func(t *testing.T) {
			e := newWakeEnv(t, store.StateWaiting)
			defaults := store.BusySendAgentDefaults{SendMode: store.BusySendWaitTurn, ConfirmFromTranscripts: confirm}
			if err := apitest.SeedBusySendDefaults(e.db, e.id, defaults); err != nil {
				t.Fatal(err)
			}
			e.admit(t, "m1", "hello")
			c := e.force(t)
			const body = "[agent-director: message 1 of 1]\nhello"
			if got := e.wake(t); got != body {
				t.Fatalf("forced wake wrote %q; want %q", got, body)
			}
			transcript := filepath.Join(t.TempDir(), wakeSession+".jsonl")
			record, err := json.Marshal(map[string]any{"type": "user", "sessionId": wakeSession,
				"message": map[string]any{"role": "user", "content": claudeWake(body)}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(transcript, append(record, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			e.fire(t, e.payload(t, "stop.json", map[string]any{"transcript_path": transcript}))
			ended := e.endCall(t, c)
			final := e.result(t, "m1")
			if ended.Outcome != store.BusySendForcePending || ended.Reason != store.BusySendForceAwaitingAdmission || !final.Final || !final.Forced {
				t.Fatalf("call end %s %s, m1 %+v; want pending awaiting_admission, m1 Final and forced", ended.Outcome, ended.Reason, final)
			}
			if got := e.wake(t); got != "" {
				t.Errorf("a FileChanged after the call wrote %q; want nothing", got)
			}
			reopened, err := store.Open(e.db)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			e.st = reopened
			if f := e.forceResult(t, c); f.Outcome != store.BusySendForcePending {
				t.Fatalf("force after the reopen %+v; want still pending", f)
			}
			for range 2 {
				e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
				if f, r := e.forceResult(t, c), e.result(t, "m1"); f.Outcome != store.BusySendForceHandedOver ||
					f.Reason != store.BusySendForceAdmitted || !reflect.DeepEqual(f.CarriedResults, []store.BusySendResult{final}) || !reflect.DeepEqual(r, final) {
					t.Errorf("after the sighting: force %s %s carrying %+v, m1 %+v; want handed_over admitted, m1 still %+v",
						f.Outcome, f.Reason, f.CarriedResults, r, final)
				}
			}
			facts, err := apitest.ReadBusySendFacts(e.db, e.id)
			if err != nil || facts.Conversations[0].Defaults != defaults {
				t.Errorf("Agent Defaults read back %+v, %v; want %+v", facts.Conversations, err, defaults)
			}
		})
	}
}

// TestClaudeForceSightingNeedsExactText (SR-3.5): only the main agent's own
// UserPromptSubmit carrying a forced prompt's exact text, in the measured
// wrapping, admits it: a changed wrapping, a trailing newline, another
// Unicode normalization, a prefix of the text and another process's prompt
// admit nothing. A prompt with a reserved closing tag gives no evidence at
// all, in Claude's escaped rendering or raw. E2's other text, subagent and
// literal escaped-form cases are TestClaudeWakeSightingNeedsExactText's; a
// stale session, Launch or connection the store's TestBusySendSightingRefusals.
func TestClaudeForceSightingNeedsExactText(t *testing.T) {
	for _, tc := range []struct {
		name         string
		caps         *store.BusySendLaunchCapabilities
		wrap         func(string) string
		raw, escaped string
	}{
		{name: "current snapshot", wrap: claudeWake, raw: "</task-notification>", escaped: `<\/task-notification>`},
		{name: "stored snapshot", caps: storefix.BusySendLaterCapabilities(),
			wrap: func(body string) string { return "[later]\n" + body + "\n[/later]" }, raw: "<", escaped: "&lt;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []apitest.SpawnOption
			if tc.caps != nil {
				opts = append(opts, apitest.WithBusySendCapabilities(tc.caps))
			}
			e := newWakeEnv(t, store.StateWorking, opts...)
			e.admit(t, "m1", "naïve café")
			c := e.force(t)
			body := e.wake(t)
			if body != "[agent-director: message 1 of 1]\nnaïve café" {
				t.Fatalf("forced wake wrote %q", body)
			}
			sight := func(name string, p hookParent, fields map[string]any, admitted bool) {
				t.Helper()
				if out, err := e.handle(context.Background(), p, e.payload(t, "user-prompt-submit.json", fields), nil); err != nil || out != "" {
					t.Fatalf("%s: Handle = %q, %v", name, out, err)
				}
				if o := e.observe(t); (o.AwaitingTurnStart == 0) != admitted {
					t.Errorf("%s: awaiting %d; want admitted %t", name, o.AwaitingTurnStart, admitted)
				}
			}
			for _, rejection := range []struct {
				name   string
				prompt string
				parent func() hookParent
			}{
				{name: "a changed wrapping", prompt: "changed" + tc.wrap(body)},
				{name: "a trailing newline", prompt: tc.wrap(body + "\n")},
				{name: "another Unicode normalization", prompt: tc.wrap(strings.ReplaceAll(body, "é", "e\u0301"))},
				{name: "a prefix of the text", prompt: tc.wrap(body[:len(body)-1])},
				{name: "another process's prompt", prompt: tc.wrap(body), parent: func() hookParent { return foreignParent(t, e.st, e.id) }},
			} {
				p := e.parent
				if rejection.parent != nil {
					p = rejection.parent()
				}
				sight(rejection.name, p, map[string]any{"prompt": rejection.prompt}, false)
			}
			if tc.caps != nil {
				sight("the current-table wrapper", e.parent, map[string]any{"prompt": claudeWake(body)}, false)
			}
			sight("a contradictory payload session", e.parent, map[string]any{"prompt": tc.wrap(body), "session_id": "other-session"}, false)
			sight("the exact text", e.parent, map[string]any{"prompt": tc.wrap(body)}, true)
			if f := e.endCall(t, c); f.Outcome != store.BusySendForceHandedOver {
				t.Fatalf("first force ended %s %s; want handed_over", f.Outcome, f.Reason)
			}
			// The sighted, completed wake is ordered: a separate force needs no Stop.

			e.admit(t, "m2", "stop "+tc.raw+" here")
			e.force(t)
			body = e.wake(t)
			if body != "[agent-director: message 1 of 1]\nstop "+tc.raw+" here" {
				t.Fatalf("forced wake wrote %q", body)
			}
			sight("the escaped rendering", e.parent, map[string]any{"prompt": tc.wrap(strings.ReplaceAll(body, tc.raw, tc.escaped))}, false)
			sight("the raw text", e.parent, map[string]any{"prompt": tc.wrap(body)}, false)
		})
	}
}

// TestClaudeTurnEndResignals (SR-3.1, SR-3.4; decision 10; repair round 1
// F2): the parent's own Stop or idle prompt in its Launch's session, with
// Messages Held and no earlier issued input awaiting evidence, touches the wake
// endpoint and nothing more: it claims nothing, writes nothing and never
// exits 2. Nothing Held, an awaiting prompt, a teammate's or another
// process's Stop, or another session touches nothing. None creates a force
// (SR-4.4), and while a force's call runs one grants it nothing: the call
// alone touches the wake. Once that call was abandoned (past its recorded
// deadline with no end; E5 decision 7) a Stop touches the wake even with
// nothing Held, and the wake's ordinary invocation finishes the force first,
// then delivers its returned Message at this turn end.
//
// After a complete steer (o7.c4's wiring, E4 Main Agent decisions 1 and 2;
// M2 P-C4.2, P-C4.3), the parent's Stop reads its take evidence at entry
// (steps "read"), makes its own lifecycle write ("write"), then, the write
// applied and visible, records it ("record") at its entry observation, never a
// time read later, and only then re-signals: the steer's obligation is cleared
// and the Held remainder's wake touched, its Final Result unchanged, and
// nothing written. Output observed in the Stop's own millisecond, a write that
// does not apply or fails, a failed evidence write or no evidence seam
// records nothing, so the remainder's wake is not touched; neither does newer
// steered output completed during the write. A subagent's Stop, SubagentStop,
// the idle prompt, a soft SessionEnd and a Stop-like unknown event never
// supply that evidence.
func TestClaudeTurnEndResignals(t *testing.T) {
	held := func(t *testing.T, e *wakeEnv) { e.admit(t, "m1", "hello") }
	forced := func(at time.Duration) func(t *testing.T, e *wakeEnv) {
		return func(t *testing.T, e *wakeEnv) {
			held(t, e)
			e.inCall(e.force(t), at)
		}
	}
	// steered issues a complete Steer Turn prompt, s1, at the parent's tool
	// boundary, returning s2 to Held, and moves the clock gap on.
	steered := func(gap time.Duration) func(t *testing.T, e *wakeEnv) {
		return func(t *testing.T, e *wakeEnv) {
			e.admitAs(t, "s1", strings.Repeat("a", 5000), store.BusySendSteerTurn, true)
			e.admitAs(t, "s2", strings.Repeat("b", 5000), store.BusySendSteerTurn, true)
			e.tick(0)
			if e.steer(t) == "" {
				t.Fatal("the steer wrote nothing")
			}
			e.tick(gap)
		}
	}
	fail := func(at string, err error) func(t *testing.T, e *wakeEnv, step string) error {
		return func(t *testing.T, e *wakeEnv, step string) error {
			if step == at {
				return err
			}
			return nil
		}
	}
	cases := []struct {
		name    string
		state   string
		fixture string
		fields  map[string]any
		prep    func(t *testing.T, e *wakeEnv)
		foreign bool
		touch   bool
		then    func(t *testing.T, e *wakeEnv)
		// steer checks the callback's take evidence for prep's steer: the
		// awaiting count after it, and its evidence steps when steps is set.
		steer    bool
		awaiting int64
		steps    []string
		turnEnd  func(t *testing.T, e *wakeEnv, step string) error // runs at each evidence step
		noSeam   bool                                              // the store has no turn-end evidence seam
		claims   bool                                              // turnEnd claims and issues meanwhile
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
		{name: "Stop while a force's call runs", state: store.StateWorking, fixture: "stop.json", prep: forced(time.Second)},
		{name: "Stop after a force's call was abandoned", state: store.StateWorking, fixture: "stop.json",
			prep: forced(15*time.Second + time.Millisecond), touch: true, then: func(t *testing.T, e *wakeEnv) {
				if got, want := e.wake(t), "[agent-director: message 1 of 1]\nhello"; got != want {
					t.Errorf("the next wake wrote %q; want the returned Message %q by ordinary delivery", got, want)
				}
				if r := e.result(t, "m1"); r.Forced {
					t.Errorf("m1 %+v; want delivered unforced", r)
				}
			}},
		{name: "Stop after a complete steer, its remainder Held", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			touch: true, steer: true, steps: []string{"read", "write", "record"}, then: func(t *testing.T, e *wakeEnv) {
				e.assertSent(t, store.BusySendReasonHandedOverNotProven, "s1")
			}},
		{name: "Stop in the steer's own millisecond", state: store.StateWorking, fixture: "stop.json", prep: steered(0),
			steer: true, awaiting: 1, steps: []string{"read", "write"}},
		{name: "Stop whose write does not apply", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			steer: true, awaiting: 1, steps: []string{"read", "write"}, turnEnd: fail("write", errNotApplied)},
		{name: "Stop whose write fails", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			steer: true, awaiting: 1, steps: []string{"read", "write"}, turnEnd: fail("write", errors.New("injected write failure"))},
		{name: "Stop whose evidence write fails", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			steer: true, awaiting: 1, steps: []string{"read", "write", "record"}, turnEnd: fail("record", errors.New("injected evidence failure"))},
		{name: "Stop with no evidence seam", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			steer: true, awaiting: 1, noSeam: true},
		{name: "Stop with newer steered output during its write", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			steer: true, awaiting: 1, steps: []string{"read", "write", "record"}, claims: true,
			turnEnd: func(t *testing.T, e *wakeEnv, step string) error {
				if step == "write" {
					e.tick(time.Second)
					if got := e.steer(t); got != "[agent-director: message 1 of 1]\n"+strings.Repeat("b", 5000) {
						t.Errorf("the steer during the Stop wrote %d bytes of context; want s2", len(got))
					}
					e.tick(time.Second)
				}
				return nil
			}},
		{name: "a subagent's Stop after a steer", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			fields: map[string]any{"agent_id": "a5b92f0e7d3c6184", "agent_type": "Explore"}, steer: true, awaiting: 1},
		{name: "SubagentStop after a steer", state: store.StateWorking, fixture: "subagent-stop.json", prep: steered(time.Second),
			steer: true, awaiting: 1},
		{name: "idle prompt after a steer", state: store.StateWorking, fixture: "notification.json", prep: steered(time.Second),
			steer: true, awaiting: 1},
		{name: "soft SessionEnd after a steer", state: store.StateWorking, fixture: "session-end-clear.json", prep: steered(time.Second),
			steer: true, awaiting: 1},
		{name: "a Stop-like unknown event after a steer", state: store.StateWorking, fixture: "stop.json", prep: steered(time.Second),
			fields: map[string]any{"hook_event_name": "StopFailure"}, steer: true, awaiting: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, tc.state)
			if tc.prep != nil {
				tc.prep(t, e)
			}
			before, forces := e.observe(t), storefix.ReadBusySendCounts(t, e.db).Forces
			p := e.parent
			if tc.foreign {
				p = foreignParent(t, e.st, e.id)
			}
			var seam *failRouteFacts
			switch {
			case tc.noSeam:
				e.seam = &noTurnEndSeam{Store: e.st}
			case tc.steer:
				entry := e.now()
				seam = &failRouteFacts{Store: e.st, turnEnd: func(step string, te store.BusySendTurnEnd) error {
					if step != "write" && !te.At.Equal(entry) {
						t.Errorf("%s at %v; want the Stop's entry observation %v", step, te.At, entry)
					}
					if step == "record" {
						if got := mustGetSpawn(t, e.st, e.id).State; got != store.StateWaiting {
							t.Errorf("at the evidence write the row is %s; want the Stop's applied write visible", got)
						}
					}
					if tc.turnEnd != nil {
						return tc.turnEnd(t, e, step)
					}
					return nil
				}}
				e.seam = seam
			}
			touched := e.wakeMtime(t)
			var stderr bytes.Buffer
			if out, err := e.handle(context.Background(), p, e.payload(t, tc.fixture, tc.fields), &stderr); err != nil || out != "" || stderr.Len() != 0 {
				t.Errorf("Handle = stdout %q, stderr %q, %v; want nothing, nil", out, stderr.String(), err)
			}
			e.seam = nil
			if got := touched(); got != tc.touch {
				t.Errorf("wake endpoint touched = %t; want %t", got, tc.touch)
			}
			if after := e.observe(t); !tc.claims && (after.HeldCount != before.HeldCount || after.HandoverCount != before.HandoverCount) {
				t.Errorf("Held %d → %d, hand-overs %d → %d; want nothing claimed", before.HeldCount, after.HeldCount,
					before.HandoverCount, after.HandoverCount)
			}
			if after := e.observe(t); tc.steer && after.AwaitingTurnStart != tc.awaiting {
				t.Errorf("awaiting %d → %d; want %d", before.AwaitingTurnStart, after.AwaitingTurnStart, tc.awaiting)
			}
			if seam != nil && tc.steps != nil && !slices.Equal(seam.steps, tc.steps) {
				t.Errorf("take-evidence steps %v; want %v", seam.steps, tc.steps)
			}
			if n := storefix.ReadBusySendCounts(t, e.db).Forces; n != forces {
				t.Errorf("%d forces → %d; want none created", forces, n)
			}
			if tc.then != nil {
				tc.then(t, e)
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
		prompt                     bool
	}{
		{"covered", true, true, true, false},
		{"startup prompt goes first (Q5)", true, true, true, true},
		{"uncovered", false, true, true, false},
		{"no session (F2)", true, false, true, false},
		{"no endpoint preparer (R2-7)", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "wake-" + strings.ReplaceAll(t.Name(), "/", "-")
			args := `["--model","opus"]`
			if tc.prompt {
				args = `["--model","opus","first turn"]`
			}
			st, db := ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane), apitest.WithRawAgentArgs(args))
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
			scheduled := 0
			hc.ScheduleClaudeStartupWake = func(hook.ClaudeStartupWake) error {
				scheduled++
				return nil
			}
			if !tc.preparer {
				hc.PrepareBusySendWake = nil
			}
			if got := ssgHandleWith(t, st, hc, payload, newSilentLogger()); got != want {
				t.Errorf("SessionStart stdout = %q; want %q", got, want)
			}
			wantScheduled := 0
			if want != "" && !tc.prompt {
				wantScheduled = 1
			}
			if scheduled != wantScheduled {
				t.Errorf("startup jobs = %d; want %d", scheduled, wantScheduled)
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

// Q5 / t2.xis.oo.67: discard every pre-watch hint, release only the finite
// startup job, then drive FileChanged. No subsequent send or Stop rescues it.
// Neither startup nor resume creates a force (SR-4.4).
func TestClaudeStartupWakeDeliversHeldInOrder(t *testing.T) {
	for _, resumed := range []bool{true, false} {
		t.Run(fmt.Sprintf("resume=%v", resumed), func(t *testing.T) {
			now := time.Now()
			clock := &advancingClock{now: &now}
			var jobs []hook.ClaudeStartupWake
			configure := func(e *wakeEnv, hc *hook.HandleConfig) {
				hc.Now = clock.Now
				hc.ScheduleClaudeStartupWake = func(job hook.ClaudeStartupWake) error {
					jobs = append(jobs, job)
					return nil
				}
			}
			var e *wakeEnv
			if resumed {
				// These Messages were accepted on the old, ended Launch.
				e = newResumedWakeEnvWithHeld(t, []string{"first", "second"}, configure)
			} else {
				e = newWakeEnv(t, store.StatePending, apitest.WithTurnStarts(0), apitest.WithRawAgentArgs(`["--model","opus"]`))
				hc := hookConfig(envHook(e.id, ""), e.parent)
				configure(e, &hc)
				e.admit(t, "0", "first")
				e.admit(t, "1", "second")
				var out bytes.Buffer
				if err := hook.Handle(context.Background(), bytes.NewReader(e.payload(t, "session-start-startup.json", map[string]any{
					"transcript_path": writeTranscript(t, wakeSession)})), &out, e.st, hc, newSilentLogger()); err != nil {
					t.Fatal(err)
				}
				want := `{"hookSpecificOutput":{"hookEventName":"SessionStart","watchPaths":["` + e.endpoint + `"]}}` + "\n"
				if out.String() != want {
					t.Fatalf("SessionStart = %q; want %q", out.String(), want)
				}
			}
			if len(jobs) != 1 {
				t.Fatalf("scheduled %d startup jobs; want one", len(jobs))
			}
			if o := e.observe(t); o.HeldCount != 2 || o.HandoverCount != 0 {
				t.Fatalf("before watch registration = %+v; want two Held, no hand-over", o)
			}
			// Consume SessionStart output and install the watch only now. Aging
			// the file discards admission's touch and SessionStart preparation.
			touched := e.wakeMtime(t)
			exited := false
			clock.runAt, clock.run = 1, func() {
				if touched() {
					t.Error("startup job touched before SessionStart exit")
				}
				exited = true
			}
			if err := hook.RunClaudeStartupWake(context.Background(), e.st, jobs[0], func() bool { return exited }, clock, clock.Now, newSilentLogger()); err != nil {
				t.Fatal(err)
			}
			if !touched() || now.Sub(jobs[0].Deadline.Add(-5*time.Second)) < 250*time.Millisecond || !now.Before(jobs[0].Deadline) {
				t.Fatalf("post-watch hint touched=%v, finished=%v, deadline=%v", touched(), now, jobs[0].Deadline)
			}
			if o := e.observe(t); o.HeldCount != 2 || o.HandoverCount != 0 {
				t.Fatalf("startup hint delivered without FileChanged: %+v", o)
			}
			want := "[agent-director: message 1 of 2]\nfirst\n\n[agent-director: message 2 of 2]\nsecond"
			if got := e.wake(t); got != want {
				t.Fatalf("FileChanged = %q; want %q", got, want)
			}
			e.assertRaced(t, "0", "1")
			if o := e.observe(t); o.HeldCount != 0 || o.HandoverCount != 0 || o.AwaitingTurnStart != 1 {
				t.Fatalf("after FileChanged = %+v; want Final Results and one hand-over awaiting exact take evidence", o)
			}
			if got := e.wake(t); got != "" {
				t.Fatalf("repeated FileChanged duplicated prompt %q", got)
			}
			e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(want)}))
			if o := e.observe(t); o.AwaitingTurnStart != 0 || o.HandoverCount != 0 {
				t.Fatalf("after exact take evidence = %+v; want awaiting cleared and Results still Final", o)
			}
			if got := e.wake(t); got != "" {
				t.Errorf("wake after take duplicated prompt %q", got)
			}
			if n := storefix.ReadBusySendCounts(t, e.db).Forces; n != 0 {
				t.Errorf("startup delivery created %d forces; want none (SR-4.4)", n)
			}
		})
	}
}

// Startup recovery revalidates its Launch, gate, coverage and session after
// waiting; failure leaves custody untouched and does not recreate endpoints.
func TestClaudeStartupWakeRevalidatesAndExpires(t *testing.T) {
	for _, name := range []string{"stale token", "wrong parent", "wrong session", "uncovered during settle", "ended during settle", "missing endpoint", "hook never exits", "watch budget exhausted", "cancelled", "unreadable facts"} {
		t.Run(name, func(t *testing.T) {
			e := newWakeEnv(t, store.StateWaiting)
			e.admit(t, "held", "hello")
			touched := e.wakeMtime(t)
			now := time.Now()
			clock := &advancingClock{now: &now}
			job := hook.ClaudeStartupWake{InstanceID: e.id, LaunchToken: e.token, SessionID: wakeSession,
				ParentPID: e.parent.PID, ParentStart: e.parent.Start, Deadline: now.Add(5 * time.Second)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var st hook.HookStore = e.st
			wantErr := false
			row := mustGetSpawn(t, e.st, e.id)
			duringWait := name == "uncovered during settle" || name == "ended during settle"
			transitioned := false
			switch name {
			case "stale token":
				job.LaunchToken = "superseded"
			case "wrong parent":
				job.ParentPID++
			case "wrong session":
				job.SessionID = "another-session"
			case "uncovered during settle", "ended during settle":
				// TR2: begin eligible and mutate the real store from inside the
				// settle sleep. A route read moved before that wait would retain
				// authority and incorrectly touch the endpoint after this change.
				f, err := e.st.ReadBusySendRouteFacts(ctx, e.id, store.HookGate{ParentPID: job.ParentPID, ParentStart: job.ParentStart})
				if err != nil || !f.GateHolds || !f.Covered || f.Ended || f.State != store.StateWaiting ||
					f.Destination.LaunchToken != job.LaunchToken || f.Destination.SessionID != job.SessionID {
					t.Fatalf("initial startup authority = %+v, %v; want eligible", f, err)
				}
				clock.runAt, clock.run = 1, func() {
					if touched() {
						t.Error("startup job touched before its settle completed")
					}
					if name == "uncovered during settle" {
						if err := apitest.SeedBusySendCoverage(e.db, e.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
							t.Fatal(err)
						}
					} else {
						e.fire(t, e.payload(t, "session-end-prompt-input-exit.json", nil))
					}
					transitioned = true
					row = mustGetSpawn(t, e.st, e.id)
				}
			case "missing endpoint":
				if err := os.Remove(e.endpoint); err != nil {
					t.Fatal(err)
				}
				wantErr = true
			case "hook never exits":
				wantErr = true
			case "watch budget exhausted":
				job.Deadline = now.Add(200 * time.Millisecond)
				wantErr = true
			case "cancelled":
				cancel()
				wantErr = true
			case "unreadable facts":
				st = &failRouteFacts{Store: e.st, n: 1}
			}
			var logs bytes.Buffer
			err := hook.RunClaudeStartupWake(ctx, st, job, func() bool { return name != "hook never exits" }, clock, clock.Now, log.New(&logs, "", 0))
			if (err != nil) != wantErr {
				t.Errorf("startup wake = %v; want error=%v", err, wantErr)
			}
			if duringWait && !transitioned {
				t.Fatal("authority transition did not run during the settle wait")
			}
			if name == "missing endpoint" {
				if _, err := os.Stat(e.endpoint); !os.IsNotExist(err) {
					t.Errorf("startup job recreated endpoint: %v", err)
				}
			} else if touched() {
				t.Error("refused startup job touched the endpoint")
			}
			if name == "hook never exits" && !now.Equal(job.Deadline) {
				t.Errorf("never-exiting hook stopped at %v; want its finite deadline %v", now, job.Deadline)
			}
			if now.After(job.Deadline) {
				t.Error("startup job exceeded its budget")
			}
			assertRowUnchanged(t, e.st, e.id, row)
			if r := e.result(t, "held"); r.Final || r.Class != store.BusySendClassHeld {
				t.Errorf("startup job changed Held custody: %+v", r)
			}
			if name == "unreadable facts" && !strings.Contains(logs.String(), "injected route-facts read failure") {
				t.Errorf("read failure not diagnosed: %q", logs.String())
			}
		})
	}
}

// Regression t2.xis.w3.9f (b.kii, capture 4; SR-2.5/O4): complete output of
// a busy-issued forced wake must not let a later steer pass it. Its exact
// sighting supplies the missing order fact, even before any parent Stop.
func TestClaudeSteerWaitsForBusyWakeSighting(t *testing.T) {
	e := newWakeEnv(t, store.StateWaiting)
	// Consume two earlier Messages so A and B have capture 4's sequences 3
	// and 4. Finish that prefix's turn before starting the turn A interrupts.
	e.admit(t, "prefix-1", "first")
	e.admit(t, "prefix-2", "second")
	const prefix = "[agent-director: message 1 of 2]\nfirst\n\n[agent-director: message 2 of 2]\nsecond"
	if got := e.wake(t); got != prefix {
		t.Fatalf("prefix wake wrote %q; want %q", got, prefix)
	}
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(prefix)}))
	e.tick(time.Millisecond)
	e.fire(t, e.payload(t, "stop.json", nil))
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": "existing parent turn"}))
	if row, o := mustGetSpawn(t, e.st, e.id), e.observe(t); row.State != store.StateWorking || o.AwaitingTurnStart != 0 || o.HeldCount != 0 {
		t.Fatalf("before A: state %s, awaiting %d, Held %d; want working, 0, 0", row.State, o.AwaitingTurnStart, o.HeldCount)
	}

	e.admit(t, "A", "OVERLAP-WAKE")
	c := e.force(t)
	const wake = "[agent-director: message 1 of 1]\nOVERLAP-WAKE"
	if got := e.wake(t); got != wake {
		t.Fatalf("busy forced wake wrote %q; want A alone %q", got, wake)
	}
	h, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
	if err != nil || len(h.Prompts) != 1 || h.Boundary != store.BusySendBoundaryForce ||
		h.Prompts[0].Emission != store.BusySendEmissionIssued || h.Prompts[0].Transport != store.BusySendTransportWritten {
		t.Fatalf("A hand-over %+v, %v; want one issued forced wake with complete output", h, err)
	}
	t.Logf("A complete output: transport=%s, order=%s", h.Prompts[0].Transport, h.Prompts[0].OrderKind)
	// The live capture's steer came after the finite force call returned.
	// A still has no sighting; ending the call must not clear its barrier.
	if ended := e.endCall(t, c); ended.Outcome != store.BusySendForcePending {
		t.Fatalf("force call ended %+v; want pending without A's sighting", ended)
	}
	e.inCall(c, 20*time.Second)
	e.admitAs(t, "B", "STEER", store.BusySendSteerTurn, true)
	facts, err := apitest.ReadBusySendFacts(e.db, e.id)
	if err != nil || len(facts.Messages) != 4 || facts.Messages[2].RequestID.String != "A" || facts.Messages[2].Seq != 3 ||
		facts.Messages[3].RequestID.String != "B" || facts.Messages[3].Seq != 4 {
		t.Fatalf("Messages %+v, %v; want A at sequence 3 before B at sequence 4", facts.Messages, err)
	}
	if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 1 {
		t.Fatalf("before the tool boundary: awaiting %d, Held %d; want A awaiting, B Held", o.AwaitingTurnStart, o.HeldCount)
	}

	// No UserPromptSubmit for A and no Stop since its complete output.
	got := e.steer(t)
	if r := e.result(t, "B"); r.Class != store.BusySendClassHeld || r.Final {
		t.Errorf("B released before take evidence for busy-issued wake A: Result %+v; want non-Final Held", r)
	}
	if got != "" {
		t.Fatalf("PostToolBatch issued early steer %q before take evidence for busy-issued wake A; want no output", got)
	}

	// Capture 5's separate regression owns the Stop/sibling continuation.
	// Here a sighting releases order while capture 4's parent turn still runs.
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(wake)}))
	e.assertHeld(t, "B", store.BusySendWaitReadinessUnknown) // The force's settlement refreshes Held waits.
	e.tick(time.Second)
	const steer = "[agent-director: message 1 of 1]\nSTEER"
	if got := e.steer(t); got != steer {
		t.Fatalf("PostToolBatch after A's sighting wrote %q; want B alone %q", got, steer)
	}
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "B")
	if got := e.steer(t); got != "" {
		t.Errorf("repeated PostToolBatch wrote %q; want B issued once", got)
	}
}

// Regression b.kii / t2.xis.w3.ok: complete output finalizes Results and frees
// capacity, but order additionally needs an exact sighting. Stops leave an
// unsighted wake owning custody; after sighting, Stop only schedules the next
// ordinary wake. Force outcome and awaiting follow admission independently.
func TestClaudeWakeOrderIsSeparateFromAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		forced  bool
		sighted bool
	}{
		{name: "ordinary wake, no sighting"},
		{name: "ordinary wake, sighted", sighted: true},
		{name: "forced wake, no sighting", forced: true},
		{name: "forced wake, sighted", forced: true, sighted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := store.StateWaiting
			if tc.forced {
				state = store.StateWorking
			}
			e := newWakeEnv(t, state)
			e.tick(0)
			e.admit(t, "A", "first wake")
			var force store.BusySendForceClaim
			if tc.forced {
				force = e.force(t)
			}
			const body = "[agent-director: message 1 of 1]\nfirst wake"
			if got := e.wake(t); got != body {
				t.Fatalf("wake = %q; want %q", got, body)
			}
			if tc.forced {
				e.assertSent(t, store.BusySendReasonHandedOverNotProven, "A")
				if f := e.endCall(t, force); f.Outcome != store.BusySendForcePending {
					t.Fatalf("force call end = %+v; want pending", f)
				}
				e.inCall(force, 20*time.Second)
			} else {
				e.assertRaced(t, "A")
			}
			final := e.result(t, "A")
			awaiting := int64(1)
			checkA := func(place string) {
				t.Helper()
				facts, err := apitest.ReadBusySendFacts(e.db, e.id)
				if err != nil || len(facts.Messages) == 0 || facts.Messages[0].Place != place || facts.Messages[0].Reserved {
					t.Fatalf("A facts %+v, %v; want %s with capacity freed", facts.Messages, err, place)
				}
				if o := e.observe(t); o.AwaitingTurnStart != awaiting {
					t.Errorf("awaiting %d; want %d independently of order", o.AwaitingTurnStart, awaiting)
				}
				if got := e.result(t, "A"); !reflect.DeepEqual(got, final) {
					t.Errorf("A Result changed: %+v; want %+v", got, final)
				}
				if tc.forced {
					outcome, reason := store.BusySendForcePending, store.BusySendForceAwaitingAdmission
					order := store.BusySendOrderKind("")
					if place == "closed" {
						outcome, reason = store.BusySendForceHandedOver, store.BusySendForceAdmitted
						order = store.BusySendOrderOrderedAcceptance
					}
					if f := e.forceResult(t, force); f.Outcome != outcome || f.Reason != reason {
						t.Errorf("force %+v; want %s %s", f, outcome, reason)
					}
					h, err := e.st.ReadBusySendHandover(context.Background(), force.Handover.HandoverID)
					if err != nil || len(h.Prompts) != 1 || h.Prompts[0].OrderKind != order || h.Open != (place == "issued") {
						t.Fatalf("A hand-over %+v, %v; want order %q, custody %s", h, err, order, place)
					}
				}
			}
			checkA("issued") // Complete output alone is Final, but owns order.
			place := "issued"
			if tc.sighted {
				e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(body)}))
				awaiting, place = 0, "closed"
				checkA(place) // Both facts release A before any Stop.
			}
			e.admitAs(t, "B", "later steer", store.BusySendSteerTurn, true)
			e.admit(t, "W", "later wait")
			if !tc.sighted {
				for range 2 {
					e.tick(time.Second)
					e.fire(t, e.payload(t, "stop.json", nil))
					if got := e.wake(t); got != "" {
						t.Fatalf("Stop without sighting allowed ordinary wake %q", got)
					}
					checkA(place)
				}
				e.assertHeld(t, "B", store.BusySendWaitHandoverInProgress)
				e.assertHeld(t, "W", store.BusySendWaitHandoverInProgress)
				launch, err := e.st.GetBusySendPublishedLaunch(e.id)
				if err != nil {
					t.Fatal(err)
				}
				separate, err := e.st.ClaimBusySendForce(context.Background(), store.BusySendForceClaimRequest{InstanceID: e.id, Launch: launch,
					ForceID: uuid.NewString(), OwnerCallID: uuid.NewString(), ActiveBudget: 12 * time.Second, CleanupBudget: 3 * time.Second, At: e.now()})
				if err != nil || separate.Handover != nil || separate.Force.Outcome != store.BusySendForceRefused ||
					separate.Force.Reason != store.BusySendForceHandoverInProgress {
					t.Fatalf("separate force %+v, %v; want refused handover_in_progress", separate, err)
				}
				return
			}
			if got := e.wake(t); got != "" {
				t.Fatalf("ordinary wake during the sighted turn wrote %q", got)
			}
			e.tick(time.Second)
			e.fire(t, e.payload(t, "stop.json", nil))
			checkA(place)
			const rest = "[agent-director: message 1 of 2]\nlater steer\n\n[agent-director: message 2 of 2]\nlater wait"
			if got := e.wake(t); got != rest {
				t.Fatalf("wake after sighting and Stop = %q; want %q", got, rest)
			}
			e.assertRaced(t, "B", "W")
		})
	}
}

// Regression b.kii / t2.xis.w3.ok, SR-3.4: a running force advances through a
// new hook issuer only after its predecessor has recorded order and admission.
func TestClaudeRunningForceAdvancesAfterWakeOrder(t *testing.T) {
	e := newWakeEnv(t, store.StateWorking)
	a, b := strings.Repeat("a", 5000), strings.Repeat("b", 5000)
	e.admit(t, "a", a)
	e.admit(t, "b", b)
	c := e.force(t)
	first := "[agent-director: message 1 of 1]\n" + a
	if got := e.wake(t); got != first {
		t.Fatalf("first force prompt = %q; want a alone", got)
	}
	final := e.result(t, "a")
	if got := e.wake(t); got != "" {
		t.Fatalf("force advanced without sighting: %q", got)
	}
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(first)}))
	ordered, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
	if err != nil || len(ordered.Prompts) != 2 || ordered.Prompts[0].OrderKind != store.BusySendOrderOrderedAcceptance ||
		ordered.Prompts[1].Emission != store.BusySendEmissionNone || e.observe(t).AwaitingTurnStart != 0 {
		t.Fatalf("before issuer turnover: %+v, %v; want a ordered and admitted, b never issued", ordered, err)
	}
	e.self = func() store.ProcessIdentity {
		p := wakeHelper()
		p.PID++
		p.Starttime = "4243"
		return p
	}
	e.tick(time.Millisecond)
	second := "[agent-director: message 1 of 1]\n" + b
	if got := e.wake(t); got != second {
		t.Fatalf("same force after sighting, before Stop = %q; want b alone", got)
	}
	h, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
	if err != nil || len(h.Prompts) != 2 || !h.Open || h.Prompts[0].OrderKind != store.BusySendOrderOrderedAcceptance || h.Prompts[1].OrderKind != "" ||
		h.Prompts[0].Transport != store.BusySendTransportWritten || h.Prompts[1].Transport != store.BusySendTransportWritten {
		t.Fatalf("force handover %+v, %v; want both completed, only a ordered", h, err)
	}
	if got := e.wake(t); got != "" {
		t.Fatalf("repeated force wake replayed %q", got)
	}
	if f := e.endCall(t, c); f.Outcome != store.BusySendForcePending {
		t.Fatalf("call end %+v; want pending for b's admission", f)
	}
	e.inCall(c, 20*time.Second)
	if got := e.wake(t); got != "" {
		t.Fatalf("wake after call end replayed %q", got)
	}
	e.fire(t, e.payload(t, "stop.json", nil))
	if o, f := e.observe(t), e.forceResult(t, c); o.AwaitingTurnStart != 1 || f.Outcome != store.BusySendForcePending {
		t.Fatalf("after Stop: awaiting %d, force %+v; want b awaiting, force pending", o.AwaitingTurnStart, f)
	}
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(second)}))
	if f := e.forceResult(t, c); f.Outcome != store.BusySendForceHandedOver || f.Reason != store.BusySendForceAdmitted {
		t.Errorf("after b's sighting: force %+v; want handed_over admitted", f)
	}
	h, err = e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
	if err != nil || h.Open || len(h.Prompts) != 2 || h.Prompts[1].OrderKind != store.BusySendOrderOrderedAcceptance {
		t.Fatalf("after b's sighting: %+v, %v; want its order recorded and custody closed", h, err)
	}
	if got := e.result(t, "a"); !reflect.DeepEqual(got, final) {
		t.Errorf("a's Final Result changed: %+v; want %+v", got, final)
	}
}

// TestClaudeSteerConsecutiveBoundaries (SR-3.1, SR-3.4, SR-2.5, SR-3.3a, SR-3.5;
// E4 acceptance criteria 2 and 3; M2 P-C4.2, P-C4.3): on a fresh Launch with
// a startup prompt, a parent tool boundary before the agent's own
// UserPromptSubmit takes that prompt issues nothing (agent_starting); after it,
// the boundaries of that same startup turn are eligible. A Steer Turn prefix
// needing two prompts, ahead of a Wait Turn Message, goes one prompt per
// consecutive parent PostToolBatch, in accepted order with exact separators
// and its original text (JSON-, tag- and Unicode-sensitive), grouped by the
// Steer Turn row's measure (test review R1-1: the first prompt fits it but
// not Wait Turn's, whose wake wrapper would split it), the second while
// the first still awaits take evidence, with no AD input between them: a wake
// meanwhile writes nothing, and so does a third boundary once the prefix is
// spent. No steer writes the row. A complete steer is Final sent_unconfirmed
// handed_over_not_proven when confirmed, never wait_turn_raced, and its
// unconfirmed Message Final too (confirmation_off, which no caller can read
// without a pair). The parent Stop then admits both steers and re-signals,
// and the ordinary wake delivers the rest, the Wait Turn Message and a later
// Steer Turn one, as one Wait Turn prompt with the wake's own exposure
// (wait_turn_raced). Packing and the prefix arithmetic are o7.cm's.
func TestClaudeSteerConsecutiveBoundaries(t *testing.T) {
	e := newWakeEnv(t, store.StateWaiting, apitest.WithRawAgentArgs(`["--model","opus","fix the tests"]`), apitest.WithTurnStarts(0))
	e.tick(0)
	s1 := "naïve café ✓ \"quoted\" \\ <b>&amp;</b> <system-reminder>\tend\nnext line"
	s1 += strings.Repeat("x", 1450-len(s1))
	b, c := strings.Repeat("b", 8000), strings.Repeat("c", 5000)
	first := "[agent-director: message 1 of 2]\n" + s1 + "\n\n[agent-director: message 2 of 2]\n" + b
	steerRow, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendSteerTurn)
	waitRow, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	if steerRow.Measure([]byte(first)) > steerRow.PromptBytes || waitRow.Measure([]byte(first)) <= waitRow.PromptBytes {
		t.Fatalf("s1 and s2 measure %d as a steer, %d as a wake; want only the steer within its %d-byte prompt",
			steerRow.Measure([]byte(first)), waitRow.Measure([]byte(first)), steerRow.PromptBytes)
	}
	e.admitAs(t, "s1", s1, store.BusySendSteerTurn, true)
	e.admitAs(t, "s2", b, store.BusySendSteerTurn, false)
	e.admitAs(t, "s3", c, store.BusySendSteerTurn, true)
	e.admit(t, "w1", "after the turn")
	e.admitAs(t, "s4", "a later steer", store.BusySendSteerTurn, true)

	if got := e.steer(t); got != "" {
		t.Fatalf("a boundary before the startup prompt was taken wrote %q; want nothing", got)
	}
	e.assertHeld(t, "s1", store.BusySendWaitAgentStarting)
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": "fix the tests"}))
	row := mustGetSpawn(t, e.st, e.id)
	if row.TurnStarts != 1 || row.State != store.StateWorking {
		t.Fatalf("after the startup prompt: Turn Starts %d, state %s; want 1, working", row.TurnStarts, row.State)
	}

	e.tick(time.Second)
	if got := e.steer(t); got != first {
		t.Fatalf("the first boundary of the startup turn wrote %d bytes of context; want s1 and s2 as one prompt (%d bytes)", len(got), len(first))
	}
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "s1")
	// s2, unconfirmed, has no pair and so no readable Result: handed over
	// with s1 and Final (no non-Final hand-over left); its reason is
	// confirmation_off by the store's mapping (o7.cm, E2), and the hand-over's
	// one race exposure is s1's, never wait_turn_raced.
	facts, err := apitest.ReadBusySendFacts(e.db, e.id)
	if err != nil || facts.Messages[1].Confirmed || facts.Messages[1].Place != "closed" {
		t.Errorf("s2 facts %+v, %v; want unconfirmed and handed over", facts.Messages, err)
	}
	if o := e.observe(t); o.HandoverCount != 0 {
		t.Errorf("non-Final hand-overs %d; want s1's and s2's Results Final", o.HandoverCount)
	}
	e.tick(time.Second)
	if got := e.wake(t); got != "" {
		t.Errorf("a wake between the boundaries wrote %q; want nothing", got)
	}
	e.tick(time.Second)
	if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 3 {
		t.Fatalf("before the second boundary: awaiting %d, Held %d; want the first steer awaiting, s3, w1 and s4 Held", o.AwaitingTurnStart, o.HeldCount)
	}
	if got, want := e.steer(t), "[agent-director: message 1 of 1]\n"+c; got != want {
		t.Fatalf("the second boundary wrote %d bytes of context; want s3 alone (%d)", len(got), len(want))
	}
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "s3")
	e.tick(time.Second)
	if got := e.steer(t); got != "" {
		t.Errorf("a third boundary wrote %q; want nothing: the next Message waits for turn end", got)
	}
	assertRowUnchanged(t, e.st, e.id, row)
	if o := e.observe(t); o.AwaitingTurnStart != 2 || o.HeldCount != 2 {
		t.Fatalf("before the turn's end: awaiting %d, Held %d; want both steers awaiting, w1 and s4 Held", o.AwaitingTurnStart, o.HeldCount)
	}

	e.tick(time.Second)
	touched := e.wakeMtime(t)
	e.fire(t, e.payload(t, "stop.json", nil))
	if o := e.observe(t); !touched() || o.AwaitingTurnStart != 0 {
		t.Fatalf("the parent's Stop: touched %t, awaiting %d; want the wake re-signalled with both steers admitted", touched(), o.AwaitingTurnStart)
	}
	rest := "[agent-director: message 1 of 2]\nafter the turn\n\n[agent-director: message 2 of 2]\na later steer"
	if got := e.wake(t); got != rest {
		t.Fatalf("the wake wrote %q; want w1 and s4 as one prompt %q", got, rest)
	}
	e.assertRaced(t, "w1", "s4")
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "s1", "s3")
}

// TestClaudeSteerKeepsItsBinding (M2 P-C4.1, P-C4.6, P-C4.7; SR-2.4, SR-3.9):
// a steer is pinned to the destination its callback authenticated. When the
// Launch is replaced after that, by a resume reusing the provider session and
// the same process identity (so the gate still holds), before the steer's
// eligibility read or its last look before the issue commit, the callback
// adopts nothing: it writes no context and its Message stays Held, never
// issued, for the new Launch's own parent boundary to deliver. A Dialog
// opening between eligibility and the last look refuses too: the request
// stays open and unanswered, and nothing is written. A pause claimed then is
// TestClaudePauseFence's. A route or connection changes only with a new
// Launch (its route identity is its Launch id, its connection generation 0).
func TestClaudeSteerKeepsItsBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     int // the route-facts read the change runs before
		dialog bool
	}{
		{name: "the Launch replaced before eligibility", at: 2},
		{name: "the Launch replaced before the last look", at: 3},
		{name: "a Dialog opened before the last look", at: 3, dialog: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The replacement Launch uses today's table; the captured route
			// must not adopt its destination or its different measure.
			e := newWakeEnv(t, store.StateWorking, apitest.WithBusySendCapabilities(storefix.BusySendLaterCapabilities()))
			e.admitAs(t, "m1", "hello", store.BusySendSteerTurn, true)
			callback, token := e.parent, e.token
			seam := &failRouteFacts{Store: e.st, n: tc.at}
			seam.before = func() {
				e.seam = nil
				if tc.dialog {
					if _, err := apitest.SeedPermissionRequest(e.db, e.id, "Bash"); err != nil {
						t.Fatal(err)
					}
					return
				}
				e.fire(t, e.payload(t, "session-end-prompt-input-exit.json", nil))
				e.resume(t, ssgPane("4444444444444444", callback.PID), nil)
				if e.parent != callback {
					t.Fatalf("the new Launch's process %+v; want the callback's %+v, so its gate still holds", e.parent, callback)
				}
			}
			e.seam = seam
			if got := e.steer(t); got != "" || !seam.fired {
				t.Fatalf("steer wrote %q (change made %t); want nothing", got, seam.fired)
			}
			if o := e.observe(t); o.AwaitingTurnStart != 0 || o.HeldCount != 1 {
				t.Errorf("awaiting %d, Held %d; want m1 never issued and Held", o.AwaitingTurnStart, o.HeldCount)
			}
			if tc.dialog {
				e.assertHeld(t, "m1", store.BusySendWaitDialog)
				if open, err := e.st.OpenPermissionRequestsForSpawn(e.id); err != nil || len(open) != 1 {
					t.Errorf("open permission requests %d, %v; want the Dialog's, unanswered", len(open), err)
				}
				return
			}
			if r := e.result(t, "m1"); r.Final || r.Class != store.BusySendClassHeld || e.token == token {
				t.Fatalf("m1 %+v on Launch %s; want Held on a new Launch", r, e.token)
			}
			if got := e.steer(t); got != "[agent-director: message 1 of 1]\nhello" {
				t.Errorf("the new Launch's own boundary wrote %q; want m1", got)
			}
			e.assertSent(t, store.BusySendReasonHandedOverNotProven, "m1")
		})
	}
}

// TestClaudeSteerRaces (M2 P-C4.2, P-C4.3; SR-2.2, SR-3.3; E4 Main Agent
// decision 2): while a steer writes its envelope, a send admitted then joins
// nothing and causes no further output, and its wake writes nothing; a parent
// Stop then, before the steer's completion is recorded, supplies no take
// evidence and touches nothing, and the wake after it neither replays the
// steer nor delivers the later Message ahead of its evidence. A later parent
// Stop admits the steer and re-signals the wake, which delivers that Message.
// o7.cm owns both store transactions' serializations.
func TestClaudeSteerRaces(t *testing.T) {
	e := newWakeEnv(t, store.StateWorking)
	e.tick(0)
	e.admitAs(t, "s1", "first steer", store.BusySendSteerTurn, true)
	w := &wakeStderr{keep: -1}
	w.before = func() {
		e.admitAs(t, "late", "second steer", store.BusySendSteerTurn, true)
		e.tick(time.Second)
		touched := e.wakeMtime(t)
		e.fire(t, e.payload(t, "stop.json", nil))
		if got := e.wake(t); touched() || got != "" {
			t.Errorf("a Stop during the steer's write touched the wake (%t), which wrote %q; want neither", touched(), got)
		}
	}
	if got := steerContext(t, e.steerWith(t, w)); got != "[agent-director: message 1 of 1]\nfirst steer" {
		t.Fatalf("steer wrote %q; want s1 alone", got)
	}
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "s1")
	if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 1 {
		t.Fatalf("awaiting %d, Held %d; want s1 awaiting evidence, late Held", o.AwaitingTurnStart, o.HeldCount)
	}
	if got := e.wake(t); got != "" {
		t.Errorf("a wake after the steer completed wrote %q; want nothing before its evidence", got)
	}
	e.tick(time.Second)
	touched := e.wakeMtime(t)
	e.fire(t, e.payload(t, "stop.json", nil))
	if o := e.observe(t); !touched() || o.AwaitingTurnStart != 0 {
		t.Fatalf("a later Stop: touched %t, awaiting %d; want s1 admitted and the wake re-signalled", touched(), o.AwaitingTurnStart)
	}
	if got := e.wake(t); got != "[agent-director: message 1 of 1]\nsecond steer" {
		t.Errorf("the wake wrote %q; want late", got)
	}
}

// TestClaudeSteerForces (SR-3.4, SR-4.2, SR-4.3; E5 decision 7; M2 P-C4.4): a
// parent PostToolBatch never carries a force's authority. While a force's
// call runs, its hand-over bars the steer's claim: nothing is written, the
// force's prompt stays unissued and a later Steer Turn Message waits
// handover_in_progress. Once the call is abandoned, the boundary, as the
// agent's eligible event, finishes the force before its own waits, even when
// a Dialog or startup precedence then blocks it and when its stdout could take
// no output: the force's Messages are Held again, unforced, nothing is issued
// and no forced wake writes. E5 owns the force outcome matrix.
func TestClaudeSteerForces(t *testing.T) {
	startup := []apitest.SpawnOption{apitest.WithRawAgentArgs(`["--model","opus","fix the tests"]`), apitest.WithTurnStarts(0)}
	for _, tc := range []struct {
		name   string
		state  string
		opts   []apitest.SpawnOption
		dialog bool
		at     time.Duration // the callback's clock after the call's entry
		plain  bool          // stdout takes no write deadline
		wait   store.BusySendWaitReason
	}{
		{name: "a force's call running", state: store.StateWorking, at: time.Second, wait: store.BusySendWaitHandoverInProgress},
		{name: "an abandoned force, then a Dialog", state: store.StateCheckPermission, dialog: true, at: 15*time.Second + time.Millisecond,
			wait: store.BusySendWaitDialog},
		{name: "an abandoned force, then the startup prompt's precedence, no output possible", state: store.StateWaiting, opts: startup,
			at: 15*time.Second + time.Millisecond, plain: true, wait: store.BusySendWaitAgentStarting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, tc.state, tc.opts...)
			if tc.dialog {
				if _, err := apitest.SeedPermissionRequest(e.db, e.id, "Bash"); err != nil {
					t.Fatal(err)
				}
			}
			e.admitAs(t, "m1", "hello", store.BusySendSteerTurn, true)
			c := e.force(t)
			e.admitAs(t, "m2", "next", store.BusySendSteerTurn, true)
			e.inCall(c, tc.at)
			var plain bytes.Buffer
			stdout := &wakeStderr{keep: -1}
			var out io.Writer = stdout
			if tc.plain {
				out = &plain
			}
			if err := e.handleTo(context.Background(), e.parent, e.payload(t, "post-tool-batch.json", nil), out, nil); err != nil ||
				stdout.Len() != 0 || plain.Len() != 0 {
				t.Fatalf("PostToolBatch = %v, stdout %q%q; want nil and nothing written", err, stdout.String(), plain.String())
			}
			if o := e.observe(t); o.AwaitingTurnStart != 0 {
				t.Errorf("awaiting %d; want nothing issued", o.AwaitingTurnStart)
			}
			f := e.forceResult(t, c)
			if tc.at < 15*time.Second {
				h, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
				if f.Outcome != store.BusySendForceRunning || err != nil || h.Prompts[0].Emission != store.BusySendEmissionNone {
					t.Errorf("force %s, its prompt %+v (%v); want running, never issued", f.Outcome, h.Prompts, err)
				}
				e.assertHeld(t, "m2", tc.wait)
				return
			}
			if r := e.result(t, "m1"); f.Outcome == store.BusySendForceRunning || r.Forced {
				t.Errorf("force %s, m1 %+v; want the force finished and m1 unforced", f.Outcome, r)
			}
			e.assertHeld(t, "m1", tc.wait)
		})
	}
}

// TestClaudePauseFence (SR-6.5, SR-2.4; M2 P-C4.6, P-C4.7 issue side): the
// real conditional pause's standing claim on the waiting agent fences both
// owned callbacks at their shared issue commit (BusySendGuardPause), which
// each reaches:
//   - the ordinary FileChanged wake, the one handler and commit that the
//     parent Stop (TestClaudeTurnEndResignals), the idle prompt and admission
//     (TestClaudeWakeDeliversHeldPrompts) and the startup job
//     (TestClaudeStartupWakeDeliversHeldInOrder) only notify, its hand-over
//     claimed before the claim lands at its last look;
//   - a parent PostToolBatch steer in a turn the agent started after the
//     claim. (A steer prepared before the claim cannot reach the fence: the
//     turn's Stop that makes the agent claimable changes the permission
//     epoch, which its commit refuses first.)
//
// Neither writes a wake body on stderr or context on stdout, an intent or an
// issuer; the Messages stay Held in accepted order, waiting readiness_unknown,
// and the claim still stands. Force prompts and the intent-first order are
// internal/busysend's (TestCoordinatorPauseFencesForcePrompts,
// TestCoordinatorPauseLosesToIssuedInput); the claim's matrix internal/store's.
func TestClaudePauseFence(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode store.BusySendMode
		at   int // the route-facts read the claim runs before; 0: before the callback
		fire func(*testing.T, *wakeEnv) string
	}{
		{name: "a wake prepared before the claim", at: 3, fire: func(t *testing.T, e *wakeEnv) string { return e.wake(t) }},
		{name: "a steer in a turn started after the claim", mode: store.BusySendSteerTurn, fire: func(t *testing.T, e *wakeEnv) string {
			e.fire(t, e.payload(t, "user-prompt-submit.json", nil))
			return e.steer(t)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWakeEnv(t, store.StateWaiting)
			e.admitAs(t, "m1", "first", tc.mode, true)
			e.admitAs(t, "m2", "second", tc.mode, true)
			l, err := e.st.GetBusySendPublishedLaunch(e.id)
			if err != nil {
				t.Fatal(err)
			}
			held, err := apitest.ReadBusySendFacts(e.db, e.id)
			if err != nil {
				t.Fatal(err)
			}
			fence := storefix.ReadLaunchFence(t, e.st, l.LaunchID)
			var claim store.BusySendPauseDecision
			seam := &failRouteFacts{Store: e.st, n: tc.at}
			if tc.at == 0 {
				claim = e.pause(t)
			} else {
				seam.before = func() { claim = e.pause(t) }
			}
			e.seam = seam
			got := tc.fire(t, e)
			e.seam = nil
			if claim.Kind != store.BusySendPauseDecisionClaimed || got != "" || seam.fired != (tc.at > 0) {
				t.Fatalf("pause claim %+v (at the last look %t), then the callback wrote %q; want claimed, then nothing", claim, seam.fired, got)
			}
			var guard *store.BusySendGuardError
			if len(seam.commits) != 1 || !errors.As(seam.commits[0], &guard) || guard.Failure != store.BusySendGuardPause {
				t.Errorf("issue commits %v; want one, refused by the pause fence", seam.commits)
			}
			if got := storefix.ReadLaunchFence(t, e.st, l.LaunchID); !reflect.DeepEqual(got, fence) {
				t.Errorf("Launch fence %+v; want unchanged %+v: no intent, no issuer", got, fence)
			}
			facts, err := apitest.ReadBusySendFacts(e.db, e.id)
			if err != nil || len(facts.Messages) != 2 {
				t.Fatalf("facts %+v, %v", facts.Messages, err)
			}
			for i, m := range facts.Messages {
				if m.ID != held.Messages[i].ID || m.Place != "held" {
					t.Errorf("Message %d %+v; want %s Held in its accepted place", i, m, held.Messages[i].ID)
				}
			}
			e.assertHeld(t, "m1", store.BusySendWaitReadinessUnknown)
			e.assertHeld(t, "m2", store.BusySendWaitReadinessUnknown, store.BusySendWaitEarlierMessage)
			if o := e.observe(t); o.AwaitingTurnStart != 0 || o.HeldCount != 2 {
				t.Errorf("awaiting %d, Held %d; want nothing issued, both Held", o.AwaitingTurnStart, o.HeldCount)
			}
			if again := e.pause(t); again.Cause != store.BusySendPauseInProgress || again.Claim == nil || again.Claim.Ref != claim.Claim.Ref {
				t.Errorf("a later pause %+v; want the standing claim's pause_in_progress", again)
			}
		})
	}
}
