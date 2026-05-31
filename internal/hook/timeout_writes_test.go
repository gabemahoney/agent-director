package hook_test

// Tests for the b.uer regression: on polling timeout, runRelay must
// write the deny decision and state transition to the DB BEFORE writing
// the deny envelope to stdout. This ensures an observer that reads the
// stdout envelope can always find the matching DB state without a race.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// orderingWriter wraps an *bytes.Buffer and captures a snapshot of
// the store's recorded call counts the instant the first byte arrives.
// This lets us assert that both DB writes happened BEFORE the stdout
// write without any concurrency.
type orderingWriter struct {
	buf *bytes.Buffer
	st  *flakyRelayStore

	// Populated on first Write call.
	decideCountAtWrite     int
	transitionCountAtWrite int
	capturedOnce           bool
}

func (w *orderingWriter) Write(p []byte) (int, error) {
	if !w.capturedOnce {
		w.capturedOnce = true
		w.decideCountAtWrite = len(w.st.decideArgs)
		w.transitionCountAtWrite = len(w.st.transitionArgs)
	}
	return w.buf.Write(p)
}

// TestFailClosedTimeoutWritesDBBeforeStdout drives runRelay to the
// polling-timeout branch using the virtual clock (zero wall-clock cost)
// and then asserts:
//   1. DecidePermissionRequest("id-tout", "deny", "timeout") was recorded.
//   2. ApplyHookTransition("id-tout", "working", false) was recorded.
//   3. Both calls were captured BEFORE the first stdout byte was written.
//   4. The stdout envelope is a valid deny envelope (SRD §6.4).
//
// Note: Handle calls ApplyHookTransition once BEFORE runRelay (to record
// the check_permission state transition from the PermissionRequest
// classification). The timeout path in runRelay adds a second call back
// to "working". The ordering assertion checks that BOTH the decide call
// and the working-transition are already recorded when the first stdout
// byte lands.
func TestFailClosedTimeoutWritesDBBeforeStdout(t *testing.T) {
	const instanceID = "id-tout"

	st := &flakyRelayStore{
		// Row stays undecided forever → polling loop hits deadline.
		getRows: []store.PermissionRow{{Decision: ""}},
		getErrs: []error{nil},
	}

	now, restore := setupVirtualClock(t)
	defer restore()
	clock := &advancingClock{now: now}

	raw := &bytes.Buffer{}
	w := &orderingWriter{buf: raw, st: st}

	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		w, st,
		hook.HandleConfig{
			Env:   envWith(instanceID),
			Cfg:   config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0, PermissionRequestCap: 0},
			Clock: clock,
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// --- Assert decide DB write happened ---

	if len(st.decideArgs) == 0 {
		t.Fatalf("DecidePermissionRequest was never called; expected one call with (deny, timeout)")
	}
	dc := st.decideArgs[0]
	if dc.InstanceID != instanceID || dc.Decision != "deny" || dc.Reason != store.DecisionReasonTimeout {
		t.Errorf("decideArgs[0] = %+v; want {%s deny %s}", dc, instanceID, store.DecisionReasonTimeout)
	}
	if dc.RequestToken == "" {
		t.Errorf("decideArgs[0].RequestToken is empty; want non-empty minted token")
	}

	// --- Assert working transition happened ---
	// Handle emits one ApplyHookTransition call for check_permission before
	// runRelay. runRelay's timeout path adds a second call back to working.
	// Find the working-state call regardless of its index.
	var foundWorking bool
	for _, tc := range st.transitionArgs {
		if tc.InstanceID == instanceID && tc.NewState == store.StateWorking && !tc.SoftRefresh {
			foundWorking = true
			break
		}
	}
	if !foundWorking {
		t.Fatalf("ApplyHookTransition(instanceID, %q, false) was never recorded; transitionArgs = %+v",
			store.StateWorking, st.transitionArgs)
	}

	// --- Assert ordering: DB writes before stdout ---

	if !w.capturedOnce {
		t.Fatalf("orderingWriter never received a Write call; stdout must emit an envelope")
	}
	// At the first stdout byte, both the decide write AND the working
	// transition must already be in the slices.
	if w.decideCountAtWrite < 1 {
		t.Errorf("DecidePermissionRequest count at first stdout byte = %d; want ≥1 (DB must be written before stdout)", w.decideCountAtWrite)
	}
	// The working transition is the 2nd ApplyHookTransition call; the first
	// (check_permission) happens before runRelay, so transitionCountAtWrite
	// must be ≥2 to confirm the working write landed before stdout.
	if w.transitionCountAtWrite < 2 {
		t.Errorf("ApplyHookTransition count at first stdout byte = %d; want ≥2 (working transition must precede stdout)", w.transitionCountAtWrite)
	}

	// --- Assert stdout is a valid deny envelope ---
	assertDenyEnvelope(t, raw)
}

// TestRelayTimeoutPersistsDenyDecision verifies that the deny decision
// written on timeout is the correct "deny"/"timeout" pair. Complements
// the ordering test above by checking the values are semantically right
// even when no row is present to update (best-effort write: errors are
// logged, not propagated), as well as the normal path where the store
// records them.
func TestRelayTimeoutPersistsDenyDecision(t *testing.T) {
	const instanceID = "id-persist"

	st := &flakyRelayStore{
		getRows: []store.PermissionRow{{Decision: ""}},
		getErrs: []error{nil},
	}

	now, restore := setupVirtualClock(t)
	defer restore()
	clock := &advancingClock{now: now}

	stdout := &bytes.Buffer{}
	if err := hook.Handle(context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Read"}`),
		stdout, st,
		hook.HandleConfig{
			Env:   envWith(instanceID),
			Cfg:   config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0, PermissionRequestCap: 0},
			Clock: clock,
		},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// At least one decide call must have decision=deny reason=timeout.
	var foundDeny bool
	for _, d := range st.decideArgs {
		if d.Decision == "deny" && d.Reason == store.DecisionReasonTimeout {
			foundDeny = true
			break
		}
	}
	if !foundDeny {
		t.Fatalf("no DecidePermissionRequest call with decision=deny reason=timeout; decideArgs = %+v", st.decideArgs)
	}

	// At least one ApplyHookTransition call must target working with SoftRefresh=false.
	var foundWorking bool
	for _, tr := range st.transitionArgs {
		if tr.NewState == store.StateWorking && !tr.SoftRefresh {
			foundWorking = true
			break
		}
	}
	if !foundWorking {
		t.Fatalf("ApplyHookTransition(instanceID, %q, false) was never recorded; transitionArgs = %+v",
			store.StateWorking, st.transitionArgs)
	}

	assertDenyEnvelope(t, stdout)
}
