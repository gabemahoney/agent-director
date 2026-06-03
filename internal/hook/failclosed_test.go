package hook_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// SRD §6.4 says every code path between "permission hook starts" and
// "envelope written" must end in a written envelope when relay_mode=on.
// This file pins one test per failure mode.

// flakyRelayStore is a HookStore double with programmable behavior for
// the relay-side methods. The state-tracking calls succeed by default
// so we can isolate the failure under test.
type flakyRelayStore struct {
	transitionErr  error
	sessionErr     error
	upsertErr      error
	decideErr      error
	getRows        []store.PermissionRow
	getErrs        []error
	idx            atomic.Int32

	// Recorded calls — inspected by tests that assert call ordering.
	decideArgs     []decideCall
	transitionArgs []transitionCall
}

type decideCall struct {
	InstanceID   string
	RequestToken string
	Decision     string
	Reason       string
}

type transitionCall struct {
	InstanceID  string
	NewState    string
	SoftRefresh bool
}

func (f *flakyRelayStore) ApplyHookTransition(instanceID, newState string, softRefresh bool) error {
	f.transitionArgs = append(f.transitionArgs, transitionCall{instanceID, newState, softRefresh})
	return f.transitionErr
}
func (f *flakyRelayStore) SetSessionID(string, string) error {
	return f.sessionErr
}
func (f *flakyRelayStore) UpsertOpenPermissionRequest(_, _, _, _ string, _ int) error {
	return f.upsertErr
}
func (f *flakyRelayStore) DecidePermissionRequest(instanceID, requestToken, decision, reason string) (bool, error) {
	f.decideArgs = append(f.decideArgs, decideCall{instanceID, requestToken, decision, reason})
	if f.decideErr != nil {
		return false, f.decideErr
	}
	return true, nil
}
func (f *flakyRelayStore) GetPermissionRequest(_, _ string) (store.PermissionRow, error) {
	n := f.idx.Add(1) - 1
	if int(n) >= len(f.getRows) {
		// Sticky last entry.
		i := len(f.getRows) - 1
		if i < 0 {
			return store.PermissionRow{}, sql.ErrNoRows
		}
		return f.getRows[i], f.getErrs[i]
	}
	return f.getRows[n], f.getErrs[n]
}

// envWith returns a func(string)string that sets RELAY_MODE=on and
// AGENT_DIRECTOR_INSTANCE_ID=id; everything else empty.
func envWith(id string) func(string) string {
	return func(k string) string {
		switch k {
		case hook.EnvRelayMode:
			return hook.RelayModeOn
		case "AGENT_DIRECTOR_INSTANCE_ID":
			return id
		}
		return ""
	}
}

// assertDenyEnvelope parses stdout and confirms a single deny envelope
// in the SRD §6.3 nested shape.
func assertDenyEnvelope(t *testing.T, stdout *bytes.Buffer) {
	t.Helper()
	if stdout.Len() == 0 {
		t.Fatalf("stdout empty — relay-on path must always emit an envelope (SRD §6.4)")
	}
	var env struct {
		HookSpecificOutput struct {
			Decision struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw=%s", err, stdout.String())
	}
	if env.HookSpecificOutput.Decision.Behavior != "deny" {
		t.Fatalf("behavior = %q; want deny\nraw=%s",
			env.HookSpecificOutput.Decision.Behavior, stdout.String())
	}
}

func newSilentLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// 1. Invalid AGENT_DIRECTOR_INSTANCE_ID (missing) → deny envelope.
func TestFailClosedMissingInstanceID(t *testing.T) {
	stdout := &bytes.Buffer{}
	env := func(k string) string {
		if k == hook.EnvRelayMode {
			return hook.RelayModeOn
		}
		return "" // AGENT_DIRECTOR_INSTANCE_ID empty
	}
	st := &flakyRelayStore{}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest"}`),
		stdout, st,
		hook.HandleConfig{Env: env, Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 2. Invalid AGENT_DIRECTOR_INSTANCE_ID (contains slash) → deny.
func TestFailClosedInvalidInstanceID(t *testing.T) {
	stdout := &bytes.Buffer{}
	env := func(k string) string {
		switch k {
		case hook.EnvRelayMode:
			return hook.RelayModeOn
		case "AGENT_DIRECTOR_INSTANCE_ID":
			return "id/with/slash"
		}
		return ""
	}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest"}`),
		stdout, &flakyRelayStore{},
		hook.HandleConfig{Env: env, Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 3. Malformed payload → silent exit. We cannot determine the event
// name from an unparseable payload, so the b.45p gate forces fail-open
// (silent) rather than risk emitting a permission-shaped deny envelope
// from a non-PermissionRequest process. The legitimate PermissionRequest
// sibling process gets a parseable payload and exercises its own
// fail-closed branch separately.
func TestFailClosedMalformedPayload(t *testing.T) {
	stdout := &bytes.Buffer{}
	if err := hook.Handle(context.Background(),
		strings.NewReader("not json at all"),
		stdout, &flakyRelayStore{},
		hook.HandleConfig{Env: envWith("id-1"), Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout non-empty on malformed payload (unknown event): %q", stdout.String())
	}
}

// 4. UPSERT failure (DB unreachable) → deny.
func TestFailClosedUpsertFailure(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{upsertErr: errors.New("disk full")}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{Env: envWith("id-1"), Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 5. ApplyHookTransition failure → deny envelope.
func TestFailClosedApplyHookTransitionFailure(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{transitionErr: errors.New("db gone")}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{Env: envWith("id-1"), Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 6. Polling timeout → deny envelope (relay's own failure mode).
func TestFailClosedPollingTimeout(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{
		// All reads return an undecided row.
		getRows: []store.PermissionRow{{Decision: ""}},
		getErrs: []error{nil},
	}
	// Virtual clock drives the 1s timeout without burning wall-clock.
	now, restore := setupVirtualClock(t)
	defer restore()
	clock := &advancingClock{now: now}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{
			Env:   envWith("id-1"),
			Cfg:   config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
			Clock: clock,
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 7. ctx-cancel mid-poll → deny envelope.
func TestFailClosedContextCancelDuringPoll(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{
		getRows: []store.PermissionRow{{Decision: ""}},
		getErrs: []error{nil},
	}
	now, restore := setupVirtualClock(t)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel synchronously after the 2nd virtual sleep — no goroutine,
	// no wall-clock wait.
	clock := &advancingClock{now: now, cancel: cancel, cancelAfter: 2}
	if err := hook.Handle(ctx,
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{
			Env:   envWith("id-1"),
			Cfg:   config.Relay{TimeoutSeconds: 60, PollBaseMs: 50, PollJitterMs: 0},
			Clock: clock,
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 8. Row preempted during poll (sql.ErrNoRows) → deny envelope.
func TestFailClosedRowPreemptedDuringPoll(t *testing.T) {
	stdout := &bytes.Buffer{}
	// First read returns the open row; second read returns sql.ErrNoRows
	// as if a later DELETE-INSERT replaced our row out from under us.
	st := &flakyRelayStore{
		getRows: []store.PermissionRow{
			{Decision: ""}, // first poll: still undecided
			{},             // second: sticky entry — but err in getErrs[1]
		},
		getErrs: []error{nil, sql.ErrNoRows},
	}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{
			Env:   envWith("id-1"),
			Cfg:   config.Relay{TimeoutSeconds: 30, PollBaseMs: 0, PollJitterMs: 0},
			Clock: fastClock{},
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 9. Polling exhausts read-retry budget → deny envelope.
func TestFailClosedReadRetryBudgetExhausted(t *testing.T) {
	stdout := &bytes.Buffer{}
	rows := make([]store.PermissionRow, 10)
	errs := make([]error, 10)
	for i := range errs {
		errs[i] = errors.New("flaky db")
	}
	st := &flakyRelayStore{getRows: rows, getErrs: errs}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{
			Env:   envWith("id-1"),
			Cfg:   config.Relay{TimeoutSeconds: 30, PollBaseMs: 0, PollJitterMs: 0},
			Clock: fastClock{},
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, stdout)
}

// 10. Non-relay event with RELAY_MODE=on emits NOTHING (no envelope) —
// the fail-closed boundary is scoped to PermissionRequest events.
// State-tracking events stay fail-open per SRD §3.2.
func TestRelayActiveNonPermissionRequestDoesNotEmitEnvelope(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"SessionStart"}`),
		stdout, st,
		hook.HandleConfig{Env: envWith("id-1"), Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout non-empty on non-relay event: %q", stdout.String())
	}
}

// b.45p regression: a PreToolUse process with RELAY_MODE=on that hits
// an ApplyHookTransition failure MUST NOT emit a deny envelope. Claude
// Code routes hook stdout by fd → tool_use_id, so an envelope leaked
// from PreToolUse would be applied to the in-flight tool and race the
// legitimate PermissionRequest sibling process.
func TestPreToolUseWithRelayOn_OnApplyHookTransitionFailure_EmitsNothing(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{transitionErr: errors.New("BUSY")}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{Env: envWith("id-1"), Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("PreToolUse process leaked envelope on transition failure: %q", stdout.String())
	}
}

// errReader is a stdin double whose Read always errors, simulating a
// pipe closing or kernel I/O failure on the hook's stdin.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("simulated stdin failure") }

// b.45p regression: a stdin-read failure happens BEFORE we can peek the
// event name. With the read-payload-first restructure, we genuinely
// don't know the event type, so the b.45p-safe default is silent exit.
func TestPreToolUseWithRelayOn_OnPayloadReadFailure_EmitsNothing(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{}
	if err := hook.Handle(context.Background(),
		errReader{},
		stdout, st,
		hook.HandleConfig{Env: envWith("id-1"), Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("payload-read failure leaked envelope: %q", stdout.String())
	}
}

// b.45p regression: a PreToolUse process whose AGENT_DIRECTOR_INSTANCE_ID
// is missing must not emit a deny envelope. The pre-fix code would have
// emitted one based solely on RELAY_MODE=on, leaking into the
// PermissionRequest sibling's fd.
func TestPreToolUseWithRelayOn_OnResolveInstanceIDFailure_EmitsNothing(t *testing.T) {
	stdout := &bytes.Buffer{}
	// Env returns RELAY_MODE=on but no instance id.
	env := func(k string) string {
		if k == hook.EnvRelayMode {
			return hook.RelayModeOn
		}
		return ""
	}
	st := &flakyRelayStore{}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{Env: env, Cfg: config.Relay{TimeoutSeconds: 1}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("missing instance id on PreToolUse leaked envelope: %q", stdout.String())
	}
}

// 11. Happy-path: relay-on + PermissionRequest + decided row → allow
// envelope. Pinned here so the fail-closed scaffolding above doesn't
// accidentally mask a real allow-path regression.
func TestRelayHappyPathAllow(t *testing.T) {
	stdout := &bytes.Buffer{}
	st := &flakyRelayStore{
		getRows: []store.PermissionRow{
			{Decision: "allow", DecisionReason: "trusted"},
		},
		getErrs: []error{nil},
	}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		stdout, st,
		hook.HandleConfig{
			Env: envWith("id-1"),
			Cfg: config.Relay{TimeoutSeconds: 5, PollBaseMs: 0, PollJitterMs: 0},
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"behavior":"allow"`) {
		t.Errorf("expected allow envelope; got %s", out)
	}
	if !strings.Contains(out, `"message":"trusted"`) {
		t.Errorf("reason lost: %s", out)
	}
}
