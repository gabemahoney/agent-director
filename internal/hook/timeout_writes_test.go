package hook_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// orderingWriter records how many decide and transition calls the store had
// seen when the first stdout byte arrived.
type orderingWriter struct {
	buf                     bytes.Buffer
	st                      *flakyRelayStore
	decidesAt, transitionAt int
	written                 bool
}

func (w *orderingWriter) Write(p []byte) (int, error) {
	if !w.written {
		w.written, w.decidesAt, w.transitionAt = true, len(w.st.decideArgs), len(w.st.transitionArgs)
	}
	return w.buf.Write(p)
}

// TestFailClosedTimeoutWritesDBBeforeStdout is the b.uer regression: on a
// relay polling timeout runRelay writes the deny/timeout decision and the
// transition back to working BEFORE the deny envelope reaches stdout, so an
// observer of the envelope always finds the matching DB state.
func TestFailClosedTimeoutWritesDBBeforeStdout(t *testing.T) {
	const id = "id-tout"
	st := &flakyRelayStore{getRows: []store.PermissionRow{{}}, getErrs: []error{nil}} // never decided
	now, restore := setupVirtualClock(t)
	defer restore()
	w := &orderingWriter{st: st}

	if err := hook.Handle(context.Background(), strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		w, st, hook.HandleConfig{Env: envWith(id), Cfg: config.Relay{TimeoutSeconds: 1}, Clock: &advancingClock{now: now}},
		newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(st.decideArgs) != 1 || st.decideArgs[0].InstanceID != id || st.decideArgs[0].Decision != "deny" ||
		st.decideArgs[0].Reason != store.DecisionReasonTimeout || st.decideArgs[0].RequestToken == "" {
		t.Errorf("decides = %+v; want one {%s <token> deny %s}", st.decideArgs, id, store.DecisionReasonTimeout)
	}
	// The first transition is the hook's own (check_permission); the timeout's is the second.
	want := []transitionCall{{id, store.StateCheckPermission, false}, {id, store.StateWorking, false}}
	if len(st.transitionArgs) != 2 || st.transitionArgs[0] != want[0] || st.transitionArgs[1] != want[1] {
		t.Errorf("transitions = %+v; want %+v", st.transitionArgs, want)
	}
	if !w.written || w.decidesAt != 1 || w.transitionAt != 2 {
		t.Errorf("at the first stdout byte (written %v): decides %d, transitions %d; want 1, 2 (DB before stdout)", w.written, w.decidesAt, w.transitionAt)
	}
	assertDenyEnvelope(t, &w.buf)
}
