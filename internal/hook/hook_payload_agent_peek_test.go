package hook_test

// b.n62: Claude/Codex event identification survives unrelated classifier
// errors; Cursor keeps its strict peek and classification stays strict.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

func TestPeekEventNameForAgentNonClassifierTypes(t *testing.T) {
	for _, agent := range []string{"", "claude", "codex", "cursor", "invalid"} {
		for _, key := range hook.ClaudeLenientPayloadFieldsForTest {
			t.Run(agent+"/"+key, func(t *testing.T) {
				raw := payloadWith(t, "permission-request.json", map[string]any{key: "compact"})
				if got := hook.PeekEventNameForAgent(agent, raw); got != "PermissionRequest" {
					t.Errorf("string control: peek = %q; want PermissionRequest", got)
				}
				for _, value := range nonStringPayloadValues {
					t.Run(value.name, func(t *testing.T) {
						raw := payloadWith(t, "permission-request.json", map[string]any{key: value.value})
						want := ""
						if agent == "" || agent == "claude" || agent == "codex" {
							want = "PermissionRequest"
						}
						if got := hook.PeekEventNameForAgent(agent, raw); got != want {
							t.Errorf("peek = %q; want %q", got, want)
						}
					})
				}
			})
		}
	}
}

func TestRelayNumericSessionIDUsesAgentPeekPolicy(t *testing.T) {
	for _, agent := range []string{"", "claude", "codex", "cursor"} {
		t.Run(agent, func(t *testing.T) {
			const id = "relay-payload-type"
			rowAgent := agent
			if rowAgent == "" {
				rowAgent = "claude"
			}
			st, _ := cxRelayRow(t, id, rowAgent, store.StateWorking)
			before := mustGetSpawn(t, st, id)
			hc := relayHC(id, cxRelayParent(t, st, id, rowAgent), 10*time.Second)
			hc.Agent = agent
			claude := rowAgent == "claude"
			if claude {
				atSleep(t, hc, 1, func() {
					if got := mustGetSpawn(t, st, id).State; got != store.StateCheckPermission {
						t.Errorf("state while polling = %q; want check_permission", got)
					}
					decideOnly(t, st, id, "allow")
				})
			}
			raw := payloadWith(t, "permission-request.json", map[string]any{"session_id": 12345, "origin": "main"})
			var out, logs bytes.Buffer
			if err := hook.Handle(context.Background(), bytes.NewReader(raw), &out, st, hc, log.New(&logs, "", 0)); err != nil {
				t.Fatalf("Handle = %v; want fail-open nil", err)
			}
			if claude {
				want := hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n"
				if out.String() != want {
					t.Errorf("stdout = %q; want %q", out.String(), want)
				}
				if strings.Contains(logs.String(), "hook: classify") {
					t.Errorf("Claude classification failed: %s", logs.String())
				}
				if req := onlyRequest(t, st, id); req.Decision != "allow" || req.DeliveredAt.IsZero() {
					t.Errorf("request = decision %q, delivered %v; want allow and acknowledged", req.Decision, req.DeliveredAt)
				}
				return
			}
			want := ""
			if agent == "codex" {
				want = hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
			}
			if out.String() != want {
				t.Errorf("strict decode stdout = %q; want %q", out.String(), want)
			}
			wantLog := "hook: classify (instance=" + id + "): json: cannot unmarshal number into Go struct field payload.session_id of type string\n"
			if logs.String() != wantLog {
				t.Errorf("log = %q; want %q", logs.String(), wantLog)
			}
			assertNoRequests(t, st, id)
			assertRowUnchanged(t, st, id, before)
		})
	}
}

// b.n62: a readable permission event must deny before recording when full
// classification fails, without changing the row (including row_version).
func TestRelayClassifierFailureDeniesKnownPermission(t *testing.T) {
	type testCase struct {
		name, agent, fixture, key, jsonType string
		value                               any
		fields                              map[string]any
		silent, relayOff, capacity          bool
	}
	var cases []testCase
	for _, key := range hook.StrictPayloadFieldsForTest {
		if key != "hook_event_name" {
			cases = append(cases, testCase{name: "claude/" + key, agent: "claude", key: key, value: 12345, jsonType: "number"})
		}
	}
	cases = append(cases,
		testCase{name: "default/tool_name", key: "tool_name", value: 12345, jsonType: "number"},
		testCase{name: "codex/tool_name", agent: "codex", fixture: "codex/permission-request-bash.json", key: "tool_name", value: 12345, jsonType: "number"},
		testCase{name: "codex/session_id", agent: "codex", fixture: "codex/permission-request-bash.json", key: "session_id", value: 12345, jsonType: "number"},
		testCase{name: "control/numeric-primary-with-alias", key: "hook_event_name", value: 12345, jsonType: "number", fields: map[string]any{"event_name": "PermissionRequest"}, silent: true},
		testCase{name: "control/pretool-with-wrong-alias", key: "event_name", value: 12345, jsonType: "number", fields: map[string]any{"hook_event_name": "PreToolUse"}, silent: true},
		testCase{name: "control/pretool-with-wrong-tool", key: "tool_name", value: 12345, jsonType: "number", fields: map[string]any{"hook_event_name": "PreToolUse", "event_name": "PermissionRequest"}, silent: true},
		testCase{name: "control/unknown-with-wrong-tool", key: "tool_name", value: 12345, jsonType: "number", fields: map[string]any{"hook_event_name": "Unknown"}, silent: true},
		testCase{name: "control/relay-off", key: "tool_name", value: 12345, jsonType: "number", relayOff: true, silent: true},
		testCase{name: "control/capacity", key: "tool_name", value: 12345, jsonType: "number", capacity: true, silent: true},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "relay-classify-error"
			agent := tc.agent
			if agent == "" {
				agent = "claude"
			}
			st, _ := cxRelayRow(t, id, agent, store.StateWorking)
			before := mustGetSpawn(t, st, id)
			hc := relayHC(id, cxRelayParent(t, st, id, agent), 10*time.Second)
			hc.Agent = tc.agent
			if tc.relayOff {
				hc.Env = envHook(id, hook.RelayModeOff)
			}
			if tc.capacity {
				hc.ContextSource = "statusLine"
			}
			fixture := tc.fixture
			if fixture == "" {
				fixture = "permission-request.json"
			}
			fields := tc.fields
			if fields == nil {
				fields = map[string]any{}
			}
			fields[tc.key] = tc.value
			raw := payloadWith(t, fixture, fields)
			var out, logs bytes.Buffer
			if err := hook.Handle(context.Background(), bytes.NewReader(raw), &out, st, hc, log.New(&logs, "", 0)); err != nil {
				t.Fatalf("Handle = %v; want nil", err)
			}
			want := ""
			if !tc.silent {
				want = hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
			}
			if out.String() != want {
				t.Errorf("stdout = %q; want %q", out.String(), want)
			}
			wantLog := fmt.Sprintf("hook: classify (instance=%s): json: cannot unmarshal %s into Go struct field payload.%s of type string\n", id, tc.jsonType, tc.key)
			if tc.capacity {
				wantLog = ""
			}
			if logs.String() != wantLog {
				t.Errorf("log = %q; want %q", logs.String(), wantLog)
			}
			assertNoRequests(t, st, id)
			assertRowUnchanged(t, st, id, before)
		})
	}
}

// b.n62 and b.45p: identify the authoritative event without trusting partial
// JSON decodes, changing precedence, or losing struct duplicate/null semantics.
func TestPeekEventNameForAgentEventIdentity(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"primary", `{"hook_event_name":"PermissionRequest"}`, "PermissionRequest"},
		{"primary-wins", `{"hook_event_name":"PreToolUse","event_name":"PermissionRequest"}`, "PreToolUse"},
		{"wrong-unused-alias", `{"hook_event_name":"PermissionRequest","event_name":12345}`, "PermissionRequest"},
		{"nonpermission-wrong-alias", `{"hook_event_name":"PreToolUse","event_name":true}`, "PreToolUse"},
		{"empty-primary", `{"hook_event_name":"","event_name":"PermissionRequest"}`, "PermissionRequest"},
		{"null-primary", `{"hook_event_name":null,"event_name":"PermissionRequest"}`, "PermissionRequest"},
		{"missing-primary", `{"event_name":"PermissionRequest"}`, "PermissionRequest"},
		{"no-name", `{}`, ""},
		{"empty-names", `{"hook_event_name":"","event_name":""}`, ""},
		{"null-names", `{"hook_event_name":null,"event_name":null}`, ""},
		{"wrong-alias", `{"event_name":12345}`, ""},
		{"unknown-name", `{"hook_event_name":"Unknown"}`, "Unknown"},
		{"case-primary", `{"HOOK_EVENT_NAME":"PreToolUse","event_name":"PermissionRequest"}`, "PreToolUse"},
		{"case-alias", `{"EVENT_NAME":"PermissionRequest"}`, "PermissionRequest"},
		{"duplicate-primary-null", `{"hook_event_name":"PreToolUse","hook_event_name":null,"event_name":"PermissionRequest"}`, "PreToolUse"},
		{"duplicate-primary-empty", `{"hook_event_name":"PreToolUse","hook_event_name":"","event_name":"PermissionRequest"}`, "PermissionRequest"},
		{"duplicate-alias-null", `{"event_name":"PermissionRequest","event_name":null}`, "PermissionRequest"},
		{"case-duplicate-last-wins", `{"hook_event_name":"PermissionRequest","HOOK_EVENT_NAME":"PreToolUse"}`, "PreToolUse"},
		{"duplicate-type-error", `{"hook_event_name":12345,"hook_event_name":"PermissionRequest"}`, ""},
		{"empty-input", ``, ""},
		{"null", `null`, ""},
		{"array", `[{"hook_event_name":"PermissionRequest"}]`, ""},
		{"number", `12345`, ""},
		{"bool", `true`, ""},
		{"string", `"PermissionRequest"`, ""},
		{"truncated", `{"hook_event_name":"PermissionRequest",`, ""},
		{"trailing-json", `{"hook_event_name":"PermissionRequest"}{}`, ""},
		{"trailing-junk", `{"hook_event_name":"PermissionRequest"}x`, ""},
	}
	for _, value := range nonStringPayloadValues {
		// Only the literal rows above need to preserve duplicate-key order.
		cases = append(cases, struct{ name, raw, want string }{
			"wrong-primary-" + value.name,
			string(payloadWith(t, "permission-request.json", map[string]any{"hook_event_name": value.value, "event_name": "PermissionRequest"})), "",
		})
	}
	for _, agent := range []string{"", "claude", "codex", "cursor", "invalid"} {
		t.Run(agent, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					raw := []byte(tc.raw)
					got := hook.PeekEventNameForAgent(agent, raw)
					want := tc.want
					if (agent == "cursor" || agent == "invalid") && (tc.name == "wrong-unused-alias" || tc.name == "nonpermission-wrong-alias") {
						want = ""
					}
					if got != want {
						t.Errorf("peek = %q; want %q", got, want)
					}
					if agent == "" && hook.PeekEventName(raw) != got {
						t.Errorf("default wrapper disagrees with agent-aware peek")
					}
					if res, err := hook.ClassifyEvent(agent, raw); err == nil && res.EventName != got {
						t.Errorf("classification event = %q; peek = %q", res.EventName, got)
					}
				})
			}
		})
	}
}
