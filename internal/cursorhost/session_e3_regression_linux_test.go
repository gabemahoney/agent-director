//go:build linux

package cursorhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func e3Update(t *testing.T, session, kind string, content json.RawMessage) json.RawMessage {
	t.Helper()
	update := map[string]any{"sessionUpdate": kind, "toolCallId": "createPlan", "status": "in_progress"}
	if len(content) > 0 {
		update["content"] = content
	}
	raw, err := json.Marshal(frame{JSONRPC: "2.0", Method: "session/update", Params: mustE3JSON(t, map[string]any{"sessionId": session, "update": update})})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func mustE3JSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func e3Plan(t *testing.T) cursorfix.Authorization {
	t.Helper()
	for _, a := range cursorfix.Authorizations(t) {
		if a.Name == "plan" {
			return a
		}
	}
	t.Fatal("captured plan fixture absent")
	return cursorfix.Authorization{}
}

// The array update is synthetic causal evidence, not a recovered live envelope.
func TestCursorE3ToolUpdateBeforePlanAdmission(t *testing.T) {
	// Serial: real Run/report subprocess fixture sets HOME/PATH and launch identity.
	for _, tc := range []struct {
		name, kind, content, session string
		refused                      bool
	}{
		{"tool array", "tool_call_update", `[{"type":"content","content":{"type":"text","text":"draft"}}]`, "synthetic-session-1", false},
		{"tool empty array", "tool_call_update", `[]`, "synthetic-session-1", false},
		{"tool null", "tool_call_update", `null`, "synthetic-session-1", false},
		{"tool omitted", "tool_call_update", ``, "synthetic-session-1", false},
		{"ordinary text", "agent_message_chunk", `{"type":"text","text":"main-text-control"}`, "synthetic-session-1", false},
		{"foreign update", "tool_call_update", `[]`, "foreign-session", true},
		{"malformed text", "agent_message_chunk", `[{"type":"text","text":"bad"}]`, "synthetic-session-1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := startCursorSession(t, "")
			plan := e3Plan(t)
			update := e3Update(t, tc.session, tc.kind, json.RawMessage(tc.content))
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: c.e.binding.SessionID, BeforeRequests: []json.RawMessage{update}, Requests: []json.RawMessage{mustE3JSON(t, frame{JSONRPC: "2.0", ID: plan.ID, Method: plan.Method, Params: plan.Params})}})
			c.keys(t, "scripted\r")
			if tc.refused {
				err := c.awaitFailure(t, 3*time.Second)
				if tc.session == "foreign-session" && !strings.Contains(err.Error(), "event session mismatch") {
					t.Errorf("foreign attribution gate changed: %v", err)
				}
				rows, e := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
				if e != nil || len(rows) != 0 {
					t.Fatalf("refused update admitted plan: %+v %v", rows, e)
				}
				return
			}
			c.wait(t, "real plan token after same-session update", func() bool {
				rows, e := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
				return e == nil && len(rows) == 1
			})
			rows, err := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].RequestToken == "" || !e3EqualJSON(t, []byte(rows[0].ToolInput), plan.Params) || rows[0].Decision != "" || !rows[0].DeliveredAt.IsZero() || rows[0].ProvenGone() {
				t.Fatalf("plan admission fabricated decision/execution: %+v", rows[0])
			}
			bound, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(row.JSONLPath)
			if err != nil {
				t.Fatal(err)
			}
			var want frame
			if err = json.Unmarshal(update, &want); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
				var record struct {
					Kind       string `json:"kind"`
					Session    string `json:"session_id"`
					Connection string `json:"connection_id"`
					Instance   string `json:"instance_id"`
					Token      string `json:"launch_token"`
					Life       int64  `json:"life_number"`
					Data       struct {
						Update     json.RawMessage `json:"update"`
						Submission string          `json:"submission_id"`
						Request    string          `json:"request_id"`
					} `json:"data"`
				}
				if err = json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record.Kind == "turn_outcome" || record.Kind == "session_end" {
					t.Fatalf("pending plan invented completion: %s", line)
				}
				if record.Kind != "update" {
					continue
				}
				var gotValue, wantValue any
				if err = json.Unmarshal(record.Data.Update, &gotValue); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(want.Params, &wantValue); err != nil {
					t.Fatal(err)
				}
				if reflect.DeepEqual(gotValue, wantValue) {
					found = true
					if record.Session != bound.SessionID || record.Connection != bound.Connection || record.Instance != bound.InstanceID || record.Token != bound.Token || record.Life != bound.Life || record.Data.Submission == "" || record.Data.Request == "" {
						t.Fatalf("update lost exact launch/turn binding: %s", line)
					}
				}
			}
			if !found {
				t.Fatal("raw synthetic update not durably retained")
			}
			if bytes.Contains(c.pane.bytes(), []byte("draft")) {
				t.Fatal("tool content rendered as main text")
			}
			if tc.kind == "agent_message_chunk" && !bytes.Contains(c.pane.bytes(), []byte("main-text-control")) {
				t.Fatal("ordinary main text not rendered")
			}
			if got := c.prompts(t); len(got) != 1 || got[0] != "scripted" {
				t.Fatalf("invented turn: %q", got)
			}
			c.stop(t)
		})
	}
}

func TestCursorE3FatalRunClosesMissing(t *testing.T) {
	// Serial: real Run/report subprocess and PTY own process-wide environment.
	for _, mode := range []string{"DIE", "malformed update"} {
		t.Run(mode, func(t *testing.T) {
			c := startCursorSession(t, "")
			if mode == "malformed update" {
				permission := mustE3JSON(t, frame{JSONRPC: "2.0", ID: json.RawMessage(`"fatal-pending"`), Method: "session/request_permission", Params: cursorfix.Permission(c.e.binding.SessionID, "genuine", "execute")})
				c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: c.e.binding.SessionID, Requests: []json.RawMessage{permission, e3Update(t, c.e.binding.SessionID, "agent_message_chunk", json.RawMessage(`[]`))}})
			}
			c.keys(t, mode+"\r")
			err := c.awaitFailure(t, 3*time.Second)
			t.Logf("fatal Run returned: %v", err)
			b, e := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
			if e != nil {
				t.Fatal(e)
			}
			awaitProcessGone(t, b.RuntimePID)
			row, e := c.e.s.GetSpawn(b.InstanceID)
			if e != nil {
				t.Fatal(e)
			}
			if row.State != store.StateMissing {
				t.Errorf("fatal Run completed and owned runtime gone but row=%s; want missing", row.State)
			}
			if mode == "malformed update" {
				requests, readErr := c.e.s.PermissionRequestsForSpawn(b.InstanceID)
				if readErr != nil || len(requests) != 1 || !requests[0].Closed() {
					t.Errorf("fatal Run did not close admitted request: %+v %v", requests, readErr)
				}
				if len(requests) == 1 && (requests[0].ProvenGone() || requests[0].ProvenGoneHow != "" || !requests[0].DeliveredAt.IsZero() || requests[0].PaneAnswered()) {
					t.Errorf("fatal Run fabricated request proof or delivery: %+v", requests[0])
				}
			}
			for _, r := range c.records(t) {
				if r.Kind == "session_end" || r.Kind == "turn_outcome" {
					t.Errorf("fatal Run invented durable success: %+v", r)
				}
			}
		})
	}
}

type e3CrashReceipt struct {
	DB, Home, Instance string
}

// The child emits a receipt only after the real Run admits a durable request.
func TestCursorE3GatewayReadCrashHelper(t *testing.T) {
	if os.Getenv("AD_E3_GATEWAY_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	c := startCursorSession(t, "")
	permission := mustE3JSON(t, frame{JSONRPC: "2.0", ID: json.RawMessage(`"pending"`), Method: "session/request_permission", Params: cursorfix.Permission(c.e.binding.SessionID, "genuine", "execute")})
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: c.e.binding.SessionID, Requests: []json.RawMessage{permission}})
	c.keys(t, "scripted\r")
	c.wait(t, "committed request", func() bool {
		rows, e := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
		return e == nil && len(rows) == 1
	})
	b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(e3CrashReceipt{c.e.path, os.Getenv("HOME"), b.InstanceID}); err != nil {
		t.Fatal(err)
	}
	<-make(chan struct{})
}

func TestCursorE3PublicReadsReconcileKilledGateway(t *testing.T) {
	// Serial: subprocess has an isolated verified pane/store and this test sets TMPDIR/HOME.
	for _, first := range []string{"Get", "Status"} {
		t.Run(first, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			sentinel := exec.Command("sleep", "30")
			if err := sentinel.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { sentinel.Process.Kill(); sentinel.Wait() }()
			other := newRequestEnv(t, "off", "synthetic-session-1")
			otherBefore, err := other.s.GetSpawn(other.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCursorE3GatewayReadCrashHelper$")
			cmd.Env = append(os.Environ(), "AD_E3_GATEWAY_HELPER=1")
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			ready := make(chan string, 1)
			go func() {
				s := bufio.NewScanner(out)
				if s.Scan() {
					ready <- s.Text()
				}
			}()
			var receipt e3CrashReceipt
			select {
			case line := <-ready:
				if err = json.Unmarshal([]byte(line), &receipt); err != nil {
					t.Fatalf("invalid gateway receipt %q: %v", line, err)
				}
			case <-time.After(8 * time.Second):
				t.Fatalf("gateway readiness budget exceeded: %s", stderr.String())
			}
			st, err := store.Open(receipt.DB)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			b, err := st.ReadCursorRuntime(receipt.Instance)
			if err != nil {
				t.Fatal(err)
			}
			if start, alive, known := probe.NewProcChecker().StartTime(b.GatewayPID); !known || !alive || start != b.GatewayStart {
				t.Fatal("gateway identity not independently alive before kill")
			}
			if start, alive, known := probe.NewProcChecker().StartTime(b.RuntimePID); !known || !alive || start != b.RuntimeStart {
				t.Fatal("owned runtime identity not independently alive before kill")
			}
			before, err := st.GetSpawn(b.InstanceID)
			if err != nil || before.State != store.StateCheckPermission {
				t.Fatalf("not committed request hold before gateway kill: %+v %v", before, err)
			}
			requests, err := st.PermissionRequestsForSpawn(b.InstanceID)
			if err != nil || len(requests) != 1 || requests[0].Closed() {
				t.Fatalf("no independently committed open request: %+v %v", requests, err)
			}
			client, err := api.New(api.Options{StorePath: receipt.DB, ConfigPath: filepath.Join(receipt.Home, "absent-config.toml")})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			sibling := e3SiblingLaunch(t, st, otherBefore, other.binding)
			aliveGet, getErr := client.Get(b.InstanceID)
			aliveStatus, statusErr := client.Status(b.InstanceID)
			if getErr != nil || statusErr != nil || aliveGet.State != before.State || aliveStatus.State != before.State {
				t.Fatalf("alive gateway falsely retired: Get=%s/%v Status=%s/%v", aliveGet.State, getErr, aliveStatus.State, statusErr)
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("gateway kill unexpectedly exited successfully")
			}
			awaitProcessGone(t, b.GatewayPID)
			awaitProcessGone(t, b.RuntimePID)
			t.Logf("exact gateway %d/%s and runtime %d/%s gone after gateway-only SIGKILL", b.GatewayPID, b.GatewayStart, b.RuntimePID, b.RuntimeStart)
			read := func(which string) {
				t.Helper()
				if which == "Get" {
					got, e := client.Get(b.InstanceID)
					if e != nil || got.State != store.StateMissing {
						t.Errorf("Get after exact owned death=%s err=%v; want missing without FindMissing", got.State, e)
					}
				} else {
					got, e := client.Status(b.InstanceID)
					if e != nil || got.State != store.StateMissing {
						t.Errorf("Status after exact owned death=%s err=%v; want missing without FindMissing", got.State, e)
					}
				}
			}
			read(first)
			if first == "Get" {
				read("Status")
			} else {
				read("Get")
			}
			requests, err = st.PermissionRequestsForSpawn(b.InstanceID)
			if err != nil || len(requests) != 1 || !requests[0].Closed() {
				t.Errorf("dead launch request not transactionally closed: %+v %v", requests, err)
			}
			if len(requests) == 1 && (requests[0].ProvenGone() || requests[0].ProvenGoneHow != "" || !requests[0].DeliveredAt.IsZero() || requests[0].PaneAnswered()) {
				t.Errorf("gateway SIGKILL fabricated request proof or delivery: %+v", requests[0])
			}
			after, err := st.GetSpawn(b.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			read(first)
			repeated, err := st.GetSpawn(b.InstanceID)
			if err != nil || repeated.RowVersion != after.RowVersion {
				t.Errorf("repeat public read changed terminal row: %+v %v", repeated, err)
			}
			raw, err := os.ReadFile(before.JSONLPath)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(`"kind":"session_end"`)) {
				t.Fatal("SIGKILL invented durable intentional end")
			}
			if err = sentinel.Process.Signal(syscall.Signal(0)); err != nil {
				t.Errorf("unrelated sentinel lost: %v", err)
			}
			siblingAfter, siblingErr := st.GetSpawn(sibling.InstanceID)
			if siblingErr != nil || !reflect.DeepEqual(sibling, siblingAfter) {
				t.Errorf("same-store live launch changed: %+v %v", siblingAfter, siblingErr)
			}
			otherAfter, err := other.s.GetSpawn(other.binding.InstanceID)
			if err != nil || !reflect.DeepEqual(otherBefore, otherAfter) {
				t.Errorf("other launch changed: %+v %v", otherAfter, err)
			}
			if start, alive, known := probe.NewProcChecker().StartTime(other.process.pid); !known || !alive || start != other.process.start {
				t.Error("other launch runtime lost")
			}
		})
	}
}

func e3EqualJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(av, bv)
}

// e3SiblingLaunch binds the existing unrelated live runtime in the crash store.
func e3SiblingLaunch(t *testing.T, st *store.Store, original store.Spawn, binding store.CursorRuntime) store.Spawn {
	t.Helper()
	original.InstanceID = "other-live-launch"
	if err := st.InsertPending(original); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetSpawn(original.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := st.RecordLaunchIdentity(row.InstanceID, row.RowVersion, original.Identity.Token, original.Identity); err != nil || applied != store.CondApplied {
		t.Fatalf("sibling launch identity: %v %v", applied, err)
	}
	row, err = st.GetSpawn(original.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	binding.InstanceID = row.InstanceID
	binding.Life = row.LifeNumber
	session := binding.SessionID
	binding.SessionID = ""
	if ok, err := st.RegisterCursorRuntime(row, binding); err != nil || !ok {
		t.Fatalf("sibling runtime: %v %v", ok, err)
	}
	if ok, err := st.SetCursorRuntimeSession(binding, session); err != nil || !ok {
		t.Fatalf("sibling session: %v %v", ok, err)
	}
	binding.SessionID = session
	gate := store.HookGate{Event: "SessionStart", ParentPID: binding.GatewayPID, ParentStart: binding.GatewayStart, SessionID: session, Cursor: &binding}
	if applied, err := st.ApplyHookTransition(row.InstanceID, gate, store.StateWaiting, false, "SessionStart", "", true); err != nil || !applied.Applied {
		t.Fatalf("sibling waiting: %+v %v", applied, err)
	}
	row, err = st.GetSpawn(row.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
