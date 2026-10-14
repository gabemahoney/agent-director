package hook_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The trusted verifier is mandatory; payload claims alone never authorize a write.
func TestCursorHandleRequiresIndependentVerifier(t *testing.T) {
	for _, event := range []string{"Stop", "Interrupt", "UserPromptSubmit"} {
		for _, held := range []bool{false, true} {
			for _, tc := range []struct {
				name                                                               string
				parentMismatch, startMismatch, sessionMismatch, connectionMismatch bool
				verifier                                                           string
				origin                                                             string
				child, retired                                                     bool
				calls                                                              int
				applies                                                            bool
			}{
				{name: "verified", verifier: "success", calls: 1, applies: true},
				{name: "missing verifier"},
				{name: "verification error", verifier: "error", calls: 1},
				{name: "replacement during verifier", verifier: "replacement", calls: 1},
				{name: "unknown origin", origin: "unknown", verifier: "success"},
				{name: "child origin", origin: "child", verifier: "success"},
				{name: "nested child", child: true, verifier: "success"},
				{name: "surviving report after retirement", retired: true, verifier: "success"},
				{name: "wrong immediate parent", parentMismatch: true, verifier: "success"},
				{name: "reused parent PID", startMismatch: true, verifier: "success"},
				{name: "wrong session", sessionMismatch: true, verifier: "success"},
				{name: "wrong connection", connectionMismatch: true, verifier: "success"},
			} {
				t.Run(fmt.Sprintf("%s/held=%v/%s", event, held, tc.name), func(t *testing.T) {
					id := "cursor-verifier"
					st, dbPath := seedAgentRow(t, id, store.StatePending,
						apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(ssgFreshPane))
					row := mustGetSpawn(t, st, id)
					b := store.CursorRuntime{InstanceID: id, Token: row.Identity.Token, Life: row.LifeNumber,
						GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime,
						RuntimePID: 12345, RuntimeStart: "synthetic-runtime-start", Connection: "synthetic-connection",
						InputPipe: "pipe:[1001]", OutputPipe: "pipe:[1002]", InputFD: 11, OutputFD: 12}
					if ok, err := st.RegisterCursorRuntime(row, b); err != nil || !ok {
						t.Fatalf("register runtime: %v, %v", ok, err)
					}
					if ok, err := st.SetCursorRuntimeSession(b, "synthetic-session"); err != nil || !ok {
						t.Fatalf("bind session: %v, %v", ok, err)
					}
					b.SessionID = "synthetic-session"
					if held {
						storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Hour)})
					}
					if tc.retired {
						if ok, err := st.MarkCursorRuntimeGone(b); err != nil || !ok {
							t.Fatalf("retire %v %v", ok, err)
						}
						b.Terminated = true
					}
					p := agentParent(t, st, id)
					p.Agent = store.AgentCursor
					if tc.parentMismatch {
						p = foreignParent(t, st, id)
						p.Agent = store.AgentCursor
					}
					if tc.startMismatch {
						p.Start += "-reused"
					}
					hc := hookConfig(envHook(id, ""), p)
					calls := 0
					mark := 0
					var replacement *store.Spawn
					path := writeTranscript(t, b.SessionID)
					if tc.verifier != "" {
						hc.VerifyCursor = func(actual *store.Store, got store.CursorRuntime, transcript string, ended bool) error {
							calls++
							if actual != st || !reflect.DeepEqual(got, b) || transcript != path || ended {
								t.Fatalf("incorrect verifier evidence: %+v, %q, ended=%v", got, transcript, ended)
							}
							if tc.verifier == "replacement" {
								if err := st.DeleteSpawn(id); err != nil {
									t.Fatal(err)
								}
								seedGateRow(t, dbPath, id, store.StateWaiting, "replacement-session", apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(ssgNewPane))
								fresh := mustGetSpawn(t, st, id)
								replacement = &fresh
								// The replacement seed emits its own legitimate transitions.
								mark = len(readTrailLines(t, trailFile()))
							}
							if tc.verifier == "error" {
								return errors.New("independent evidence unavailable")
							}
							return nil
						}
					}
					payload := map[string]any{"hook_event_name": event, "origin": "main",
						"session_id": b.SessionID, "cursor_connection": b.Connection, "transcript_path": path}
					if tc.origin != "" {
						payload["origin"] = tc.origin
					}
					if tc.child {
						payload["agent_id"] = "nested-agent"
					}
					if tc.sessionMismatch {
						payload["session_id"] = "other-session"
					}
					if tc.connectionMismatch {
						payload["cursor_connection"] = "other-connection"
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					before := mustGetSpawn(t, st, id)
					mark = len(readTrailLines(t, trailFile()))
					if out := ssgHandleWith(t, st, hc, string(raw), nil); out != "" {
						t.Fatalf("unexpected stdout: %q", out)
					}
					if calls != tc.calls {
						t.Fatalf("verifier called %d times; want %d", calls, tc.calls)
					}
					after := mustGetSpawn(t, st, id)
					if replacement != nil {
						before = *replacement
					}
					if tc.applies {
						expected := store.StateWaiting
						if event == "UserPromptSubmit" {
							expected = store.StateWorking
						}
						if held {
							expected = store.StateCheckPermission
						}
						if held && after.RowVersion <= before.RowVersion {
							t.Fatal("main held refresh did not advance row version")
						}
						if after.State != expected || after.SessionID != b.SessionID {
							t.Fatalf("verified prompt did not apply: %+v", after)
						}
					} else if !reflect.DeepEqual(before, after) {
						t.Fatal("refused report changed row")
					}
					if !tc.applies {
						for _, line := range readTrailLines(t, trailFile())[mark:] {
							if line["event"] == "ad.spawn.state_transition" && line["instance_id"] == id {
								t.Fatal("refused report emitted successful transition")
							}
						}
					}
					if replacement != nil {
						return
					}
					if held {
						request, err := st.GetPermissionRequest(id, storefix.TestRequestTokenA)
						if err != nil || request.MainTurnEndEligible || request.ProvenGone() || !request.AwaitsAnswer() {
							t.Fatalf("held provenance changed %+v %v", request, err)
						}
					}
					if got, err := st.ReadCursorRuntime(id); err != nil || !reflect.DeepEqual(got, b) {
						t.Fatalf("runtime binding changed: %+v, %v", got, err)
					}
				})
			}
		}
	}
}
