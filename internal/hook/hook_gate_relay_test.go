package hook_test

// hook_gate_relay_test.go — AC-HOOK-01 continued (SR-22.9, SR-14; decisions
// A4, A8, A11): a relayed PermissionRequest the gate rejects, a relay timeout
// write whose gate stops holding, the content of ad.hook.ignored, and the
// fail-open rule when the trail cannot be written. Helpers are in
// hook_gate_test.go and hook_parent_fakes_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// gateEnvSecret is the value the hook's environment gives every variable
// Handle does not name; it must never reach the trail.
const gateEnvSecret = "env-value-must-not-leak-7f3a"

// gateRelayEnv is a relay-on hook environment for id whose other variables
// all read gateEnvSecret.
func gateRelayEnv(id string) func(string) string {
	return func(k string) string {
		switch k {
		case "AGENT_DIRECTOR_INSTANCE_ID":
			return id
		case hook.EnvRelayMode:
			return hook.RelayModeOn
		}
		return gateEnvSecret
	}
}

// linesAfter returns the trail lines named event for id written after before.
func linesAfter(t *testing.T, before int, event, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range readTrailLines(t, trailFile())[before:] {
		if row["event"] == event && row["claude_instance_id"] == id {
			out = append(out, row)
		}
	}
	return out
}

// assertNoRequests fails when id has any permission request row.
func assertNoRequests(t *testing.T, st *store.Store, id string) {
	t.Helper()
	rows, err := st.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("permission requests = %d; want none recorded", len(rows))
	}
}

// TestHookGateRelayedPermissionRequestFromAnotherParent: another process's
// relayed request records nothing, returns no decision, logs one event.
func TestHookGateRelayedPermissionRequestFromAnotherParent(t *testing.T) {
	const id = "gate-relay-foreign"
	st, _ := seedAgentRow(t, id, store.StateWorking)
	foreign := foreignParent(t, st, id)
	prior := mustGetSpawn(t, st, id)
	before := len(readTrailLines(t, trailFile()))
	now, restore := setupVirtualClock(t)
	defer restore()
	hc := hookConfig(envWith(id), foreign)
	hc.Cfg = config.Relay{TimeoutSeconds: 1}
	hc.Clock = &advancingClock{now: now}

	var stdout strings.Builder
	if err := hook.Handle(context.Background(), strings.NewReader(string(readPayloadFixture(t, "permission-request.json"))),
		&stdout, st, hc, newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v; want nil", err)
	}

	// SR-22.9: a relay hook the gate did not apply returns no decision and records no request.
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q; want empty (no decision)", stdout.String())
	}
	assertNoRequests(t, st, id)
	assertRowUnchanged(t, st, id, prior)
	assertStr(t, oneIgnored(t, before, id), "reason", store.HookReasonPIDMismatch)
	if n := len(linesAfter(t, before, "ad.relay_attempt.completed", id)); n != 0 {
		t.Errorf("ad.relay_attempt.completed lines = %d; want 0 (no relay)", n)
	}
}

// gateMoveClock is a virtual-time PollClock whose first Sleep runs move once,
// so the row changes while the relay polls.
type gateMoveClock struct {
	now   *time.Time
	move  func()
	moved bool
}

func (c *gateMoveClock) Sleep(_ context.Context, d time.Duration) {
	if !c.moved {
		c.moved = true
		c.move()
	}
	*c.now = c.now.Add(d)
}

// TestHookGateRelayTimeoutWriteAfterGateStopsHolding: a resume clears the pane
// mid-poll; the gated timeout write then writes nothing and logs no event (A8).
func TestHookGateRelayTimeoutWriteAfterGateStopsHolding(t *testing.T) {
	const id = "gate-relay-timeout"
	st, _ := seedAgentRow(t, id, store.StateWorking)
	agent := agentParent(t, st, id)
	before := len(readTrailLines(t, trailFile()))
	now, restore := setupVirtualClock(t)
	defer restore()

	var moved store.Spawn
	var moveErr error
	clock := &gateMoveClock{now: now, move: func() {
		if _, err := markMissingSameLife(st, id); err != nil {
			moveErr = err
			return
		}
		sp, err := st.GetSpawn(id)
		if err != nil {
			moveErr = err
			return
		}
		res, _, err := st.MoveToPending(id, sp.Snapshot, time.Now().UnixMilli(), gateLaunchToken, apitest.TestSocket, "")
		if err != nil || res != store.CondApplied {
			moveErr = err
			if err == nil {
				moveErr = errMoveNotApplied
			}
			return
		}
		moved, moveErr = st.GetSpawn(id)
	}}
	hc := hookConfig(envWith(id), agent)
	hc.Cfg = config.Relay{TimeoutSeconds: 1}
	hc.Clock = clock

	var stdout strings.Builder
	if err := hook.Handle(context.Background(), strings.NewReader(string(readPayloadFixture(t, "permission-request.json"))),
		&stdout, st, hc, newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v; want nil", err)
	}
	if !clock.moved || moveErr != nil {
		t.Fatalf("row move during the poll: ran=%v err=%v", clock.moved, moveErr)
	}

	// SR-22.9, decision A8: the timeout write's gate no longer holds; nothing written, no second event.
	assertRowUnchanged(t, st, id, moved)
	if moved.State != store.StatePending {
		t.Errorf("State after the move = %q; want pending", moved.State)
	}
	if n := len(hookIgnoredAfter(t, before, id)); n != 0 {
		t.Errorf("ad.hook.ignored lines = %d; want 0 (the INSERT applied; the timeout write logs only)", n)
	}
	if n := len(linesAfter(t, before, "ad.hook.fired", id)); n != 1 {
		t.Errorf("ad.hook.fired lines = %d; want 1", n)
	}
	if !strings.Contains(stdout.String(), `"deny"`) {
		t.Errorf("stdout = %q; want the timeout's deny envelope", stdout.String())
	}
}

// errMoveNotApplied reports a MoveToPending that did not apply.
var errMoveNotApplied = errors.New("MoveToPending did not apply")

// gateIgnoredKeys is ad.hook.ignored's key set: SR-14's fields, row_pane_pid
// (decision A4) and the trail envelope's event and ts; no launcher_pid (b.zde).
var gateIgnoredKeys = []string{
	"claude_instance_id", "event", "hook_event", "hook_session_id", "parent_command",
	"parent_pid", "reason", "row_pane_pid", "row_session_id", "source", "ts",
}

// assertKeySet fails when event's line does not have exactly the keys want (sorted).
func assertKeySet(t *testing.T, event string, line map[string]any, want []string) {
	t.Helper()
	keys := make([]string, 0, len(line))
	for k := range line {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("%s keys = %v; want %v", event, keys, want)
	}
}

// TestHookGateIgnoredEventContent: ad.hook.ignored has exactly SR-14's keys (+
// row_pane_pid), no payload or env content; one ad.hook.fired, no_change (A11).
func TestHookGateIgnoredEventContent(t *testing.T) {
	const rowSession = "sess-row-content"
	cases := []struct {
		name        string
		fixture     string
		parentName  string // "" = unreadable
		wantCommand any    // parent_command
		wantHookSID bool   // hook_session_id set from the payload
	}{
		{name: "relayed PermissionRequest", fixture: "permission-request.json", parentName: "claude", wantCommand: "claude", wantHookSID: true},
		{name: "PreToolUse, parent name unreadable", fixture: "pre-tool-use-bash.json", wantCommand: nil, wantHookSID: true},
		{name: "payload without transcript_path", parentName: "bash", wantCommand: "bash"},
	}
	st, dbPath := storefix.OpenTempStore(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "gate-content-" + string(rune('a'+i))
			seedGateRow(t, dbPath, id, store.StateWorking, rowSession)
			foreign := foreignParent(t, st, id)
			foreign.Name = tc.parentName
			payload := []byte(`{"hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"payload-text-9c1e"}`)
			if tc.fixture != "" {
				payload = readPayloadFixture(t, tc.fixture)
			}
			before := len(readTrailLines(t, trailFile()))

			fireGate(t, st, id, foreign, gateRelayEnv(id), payload)

			line := oneIgnored(t, before, id)
			// SR-14 (+ row_pane_pid, A4): exactly these keys.
			assertKeySet(t, "ad.hook.ignored", line, gateIgnoredKeys)
			assertStr(t, line, "source", "ad_hook")
			assertStr(t, line, "row_session_id", rowSession)
			if line["parent_command"] != tc.wantCommand {
				t.Errorf("parent_command = %v; want %v", line["parent_command"], tc.wantCommand)
			}
			if tc.wantHookSID {
				assertStr(t, line, "hook_session_id", payloadSessionID(t, payload))
			} else {
				assertNull(t, line, "hook_session_id")
			}
			assertNoLeak(t, line, payload)
			fired := linesAfter(t, before, "ad.hook.fired", id)
			if len(fired) != 1 {
				t.Fatalf("ad.hook.fired lines = %d; want 1", len(fired))
			}
			assertStr(t, fired[0], "upsert_outcome", string(store.UpsertNoChange))
		})
	}
}

// assertNoLeak fails when line carries an environment value or any payload
// string other than the session id (SR-14, SR-15).
func assertNoLeak(t *testing.T, line map[string]any, payload []byte) {
	t.Helper()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal line: %v", err)
	}
	text := string(b)
	if strings.Contains(text, gateEnvSecret) {
		t.Errorf("ad.hook.ignored carries an environment value: %s", text)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	for k, v := range fields {
		s, ok := v.(string)
		if !ok {
			switch v.(type) {
			case map[string]any, []any: // objects and arrays: never copied
				if nested, err := json.Marshal(v); err == nil && len(nested) > 2 && strings.Contains(text, string(nested)) {
					t.Errorf("ad.hook.ignored carries payload field %q: %s", k, text)
				}
			}
			continue
		}
		if k == "session_id" || k == "hook_event_name" || s == "" {
			continue // the session id and event name are SR-14 fields
		}
		if strings.Contains(text, s) {
			t.Errorf("ad.hook.ignored carries payload field %q (%q): %s", k, s, text)
		}
	}
	for _, s := range []string{"rm -rf", "node_modules", "tool_input", "payload-text-9c1e"} {
		if strings.Contains(text, s) {
			t.Errorf("ad.hook.ignored carries payload content %q: %s", s, text)
		}
	}
}

// gateFailOpenChildEnv marks the child process of
// TestHookGateIgnoredFailOpenWhenTrailUnwritable.
const gateFailOpenChildEnv = "AD_HOOK_GATE_TRAIL_FAIL_CHILD"

// TestHookGateIgnoredFailOpenWhenTrailUnwritable: with the trail unwritable an
// ignored hook still returns nil and changes nothing (run in a child process).
func TestHookGateIgnoredFailOpenWhenTrailUnwritable(t *testing.T) {
	if os.Getenv(gateFailOpenChildEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHookGateIgnoredFailOpenWhenTrailUnwritable$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
		cmd.Env = append(os.Environ(), gateFailOpenChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestHookGateIgnoredFailOpenWhenTrailUnwritable") {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".agent-director"), nil, 0o600); err != nil {
		t.Fatalf("block trail directory: %v", err)
	}
	t.Setenv("HOME", home)
	if err := trail.Emit(context.Background(), "ad.test.hook_gate_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, tc := range []struct {
		name    string
		fixture string
		env     func(id string) func(string) string
	}{
		{name: "Stop", fixture: "stop.json", env: func(id string) func(string) string { return envHook(id, "") }},
		{name: "relayed PermissionRequest", fixture: "permission-request.json", env: envWith},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "gate-failopen-" + strings.ReplaceAll(tc.name, " ", "-")
			st, _ := seedAgentRow(t, id, store.StateWorking)
			prior := mustGetSpawn(t, st, id)

			stdout := fireGate(t, st, id, foreignParent(t, st, id), tc.env(id), readPayloadFixture(t, tc.fixture))

			// SR-14: ad.hook.ignored is fail-open; a trail failure changes nothing.
			if stdout != "" {
				t.Errorf("stdout = %q; want empty", stdout)
			}
			assertRowUnchanged(t, st, id, prior)
			assertNoRequests(t, st, id)
		})
	}
}
