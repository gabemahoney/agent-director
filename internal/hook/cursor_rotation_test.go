package hook_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Handler-level synthetic evidence owns payload/direct-parent authentication;
// the real pane test separately uses the built CLI and actual kernel verifier.
func TestCursorRotationHandleAuthenticatesPreviousDeadlineAndIndependentEvidence(t *testing.T) {
	for _, mode := range []string{"verified", "missing rotation verifier", "rotation evidence lost", "ordinary evidence lost", "wrong previous", "expired", "unbounded deadline", "malformed deadline", "wrong parent", "child", "archive failure"} {
		t.Run(mode, func(t *testing.T) {
			id := "cursor-rotation"
			st, path := seedAgentRow(t, id, store.StatePending, apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(ssgFreshPane))
			row := mustGetSpawn(t, st, id)
			b := store.CursorRuntime{InstanceID: id, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, RuntimePID: 12345, RuntimeStart: "synthetic-start", Connection: "synthetic-connection", InputPipe: "pipe:[1001]", OutputPipe: "pipe:[1002]", InputFD: 11, OutputFD: 12}
			if ok, err := st.RegisterCursorRuntime(row, b); err != nil || !ok {
				t.Fatalf("register %v %v", ok, err)
			}
			if ok, err := st.SetCursorRuntimeSession(b, "old-session"); err != nil || !ok {
				t.Fatalf("bind %v %v", ok, err)
			}
			b.SessionID = "old-session"
			transcript := writeTranscript(t, b.SessionID)
			gate := store.HookGate{Event: "SessionStart", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
			if _, a, err := st.ApplyHookTransitionResult(id, gate, store.StateWaiting, false, "SessionStart", transcript, true); err != nil || !a.Applied {
				t.Fatalf("start %+v %v", a, err)
			}
			parent := agentParent(t, st, id)
			parent.Agent = store.AgentCursor
			if mode == "wrong parent" {
				parent = foreignParent(t, st, id)
				parent.Agent = store.AgentCursor
			}
			hc := hookConfig(envHook(id, ""), parent)
			hc.VerifyCursor = func(actual *store.Store, got store.CursorRuntime, ref string, ended bool) error {
				if mode == "ordinary evidence lost" {
					return errors.New("independent gateway evidence lost")
				}
				if actual != st || got != b || ref != transcript || ended != b.Terminated {
					t.Fatal("wrong ordinary verifier evidence")
				}
				return nil
			}
			rotationCalls := 0
			if mode != "missing rotation verifier" {
				hc.VerifyCursorRotation = func(actual *store.Store, got store.CursorRuntime, ref string, examined store.Spawn) error {
					rotationCalls++
					if actual != st || got != b || ref != transcript || examined.SessionID != b.SessionID || examined.JSONLPath != transcript {
						t.Fatal("transaction verifier lacked outgoing authority")
					}
					if mode == "rotation evidence lost" {
						return errors.New("kernel changed after transaction acquired")
					}
					return nil
				}
			}
			payload := map[string]any{"hook_event_name": "SessionStart", "origin": "main", "session_id": "fresh-session", "cursor_connection": b.Connection, "cursor_previous_session": b.SessionID, "cursor_rotation_deadline": time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano), "transcript_path": transcript}
			switch mode {
			case "wrong previous":
				payload["cursor_previous_session"] = "foreign-session"
			case "expired":
				payload["cursor_rotation_deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			case "unbounded deadline":
				payload["cursor_rotation_deadline"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
			case "malformed deadline":
				payload["cursor_rotation_deadline"] = "tomorrow"
			case "child":
				payload["agent_id"] = "nested-child"
			case "archive failure":
				storefix.InjectWriteFailure(t, path, storefix.WriteFailReuseArchive, id)
			}
			before := mustGetSpawn(t, st, id)
			raw, _ := json.Marshal(payload)
			ssgHandleWith(t, st, hc, string(raw), nil)
			after := mustGetSpawn(t, st, id)
			bound, err := st.ReadCursorRuntime(id)
			if err != nil {
				t.Fatal(err)
			}
			history, err := st.ListSessionHistory(id, b.Life)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "verified" {
				if after.SessionID != "fresh-session" || bound.SessionID != after.SessionID || len(history) != 1 || history[0].SessionID != b.SessionID || history[0].JSONLPath != transcript || rotationCalls != 1 {
					t.Fatalf("verified publication failed: %+v %+v %+v calls=%d", after, bound, history, rotationCalls)
				}

				b.SessionID = "fresh-session"
				stable := mustGetSpawn(t, st, id)
				for _, event := range []string{"Stop", "SessionEnd"} {
					stale := map[string]any{"hook_event_name": event, "origin": "main", "session_id": "old-session", "cursor_connection": b.Connection, "transcript_path": transcript, "reason": "prompt_input_exit"}
					raw, _ := json.Marshal(stale)
					ssgHandleWith(t, st, hc, string(raw), nil)
					if got := mustGetSpawn(t, st, id); !reflect.DeepEqual(got, stable) {
						t.Fatalf("retired %s changed current launch: %+v", event, got)
					}
				}
				// An independently retired persisted binding, reported with committed identity,
				// still owns termination despite any preceding stale completion.
				if ok, err := st.MarkCursorRuntimeGone(b); err != nil || !ok {
					t.Fatalf("owned death %v %v", ok, err)
				}
				b.Terminated = true
				end := map[string]any{"hook_event_name": "SessionEnd", "origin": "main", "session_id": b.SessionID, "cursor_connection": b.Connection, "transcript_path": transcript, "reason": "prompt_input_exit"}
				raw, _ := json.Marshal(end)
				ssgHandleWith(t, st, hc, string(raw), nil)
				if got := mustGetSpawn(t, st, id); got.State != store.StateEnded || got.SessionID != b.SessionID {
					t.Fatalf("owned death left false-live current identity: %+v", got)
				}

			} else if !reflect.DeepEqual(before, after) || bound != b || len(history) != 0 {
				t.Fatalf("unauthenticated/failed rotation mutated store: %+v %+v %+v", after, bound, history)
			}
		})
	}
}
