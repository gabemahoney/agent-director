//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/cursormcp"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
)

func toolEvents(t *testing.T, e *requestEnv, kind string) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, event := range e.runtime.MCPEvents(t) {
		if string(event["event"]) == fmt.Sprintf("%q", kind) {
			out = append(out, event)
		}
	}
	return out
}

func waitToolEvent(t *testing.T, e *requestEnv, kind string, count int, budget time.Duration) []map[string]json.RawMessage {
	t.Helper()
	until := time.Now().Add(budget)
	for {
		events := toolEvents(t, e, kind)
		if len(events) >= count {
			return events
		}
		if time.Now().After(until) {
			t.Fatalf("missing %s #%d: %s", kind, count, e.runtime.MCPEvents(t))
		}
		time.Sleep(time.Millisecond)
	}
}

func runToolMCP(t *testing.T, e *requestEnv, registration cursormcp.Stdio, calls []map[string]any) {
	t.Helper()
	e.runtime.Configure(t, cursorfix.Scenario{MCPCalls: calls})
	rpc := newProtocol(e.process.input, e.process.output)
	t.Cleanup(rpc.close)
	t.Cleanup(func() {
		for _, event := range toolEvents(t, e, "started") {
			var pid int
			json.Unmarshal(event["pid"], &pid)
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	if _, err := rpc.request("session/new", map[string]any{"cwd": os.Getenv("HOME"), "mcpServers": []cursormcp.Stdio{registration}}); err != nil {
		t.Fatal(err)
	}
	if frame := nextFrame(t, rpc); frame.err != nil {
		t.Fatal(frame.err)
	}
	waitToolEvent(t, e, "notification", 1, 2*time.Second)
}

func TestCursorGuidanceToolCaptureCutRetriesCompleteRead(t *testing.T) {
	e := newRequestEnv(t, "off", "published-session")
	registration, observation, _ := restartRegistration(t, e, true)
	const pane = "complete pane beyond the first sixteen bytes\nfinal sentinel"
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	faketmuxfix.Tables{}.Update(t, row.Identity.Socket, func(table *faketmuxfix.Table) { table.Sessions[0].Panes[0].Capture = pane })
	heldGuidanceQuery(t, "capture-pane", true, true)
	// startProcess already captured PATH; scoped MCP inherits this registration env.
	registration.Env = append(registration.Env, cursormcp.Env{Name: "PATH", Value: os.Getenv("PATH")})
	calls := []map[string]any{{"name": "get", "arguments": map[string]any{"instance_id": e.binding.InstanceID}}, {"name": "read-pane", "arguments": map[string]any{"instance_id": e.binding.InstanceID}}}
	runToolMCP(t, e, registration, calls)
	if err := observation.Wait(); err != nil {
		t.Fatal(err)
	}
	events := waitToolEvent(t, e, "response_received", 3, 2*time.Second)
	if string(events[2]["code"]) != "-32000" {
		t.Fatalf("held capture did not fail: %s", events)
	}
	logical := waitToolEvent(t, e, "tool_response", 2, 2*time.Second)
	if len(logical) != 2 {
		t.Fatalf("logical results=%d want 2", len(logical))
	}
	failedCapture := false
	for _, event := range e.runtime.MCPEvents(t) {
		if string(event["event"]) == `"response_received"` && string(event["code"]) == "-32000" {
			failedCapture = true
			continue
		}
		if !failedCapture {
			continue
		}
		if string(event["event"]) == `"request_written"` && string(event["method"]) == `"tools/call"` {
			failedCapture = false
			break
		}
		if string(event["event"]) == `"tool_response"` {
			t.Fatalf("partial capture counted as logical result before retry: %s", event)
		}
	}
	if failedCapture {
		t.Fatal("failed capture had no subsequent physical retry")
	}
	if !bytes.Contains(logical[1]["response"], []byte("final sentinel")) {
		t.Fatalf("complete capture not returned: %s", logical[1])
	}
	if len(toolEvents(t, e, "failure")) != 0 {
		t.Fatal("transient read aborted caller")
	}
	plain := filepath.Join(t.TempDir(), "help")
	scenario := guidanceScenario(t)
	if err := os.WriteFile(plain, []byte(scenario.MCPExpectedInstructions), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "../../test/driver/cursor-callers-help-records.cjs", e.runtime.MCPTrace, plain, e.binding.InstanceID, filepath.Join(t.TempDir(), "inventory"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unchanged exchange validator: %v %s", err, output)
	}
	before := e.binding
	after, err := e.s.ReadCursorRuntime(before.InstanceID)
	if err != nil || after != before {
		t.Fatalf("read changed current ownership: %+v %v", after, err)
	}
	assertToolCorrelations(t, e, 3)
}

// The only synthetic boundary is the MCP peer; the runtime uses its normal ACP/control pipes.
func syntheticToolMCP(t *testing.T, mode string) (*requestEnv, string) {
	t.Helper()
	e := newRequestEnv(t, "off", "published-session")
	dir := t.TempDir()
	endpoint := filepath.Join(dir, "endpoint.js")
	source := `const fs=require('fs'),rl=require('readline').createInterface({input:process.stdin});
const mode=process.argv[2],dir=process.argv[3];let n=0;
const send=r=>process.stdout.write(JSON.stringify(r)+'\n');
rl.on('line',line=>{const q=JSON.parse(line);if(!q.id)return;
 if(q.method==='initialize'){send({jsonrpc:'2.0',id:q.id,result:{protocolVersion:'2024-11-05',instructions:'synthetic'}});return;}
 n++;fs.appendFileSync(dir+'/requests',JSON.stringify(q)+'\n');
 const success={jsonrpc:'2.0',id:q.id,result:{content:[{type:'text',text:'complete'}]}};
 const error={jsonrpc:'2.0',id:q.id,error:{code:-32000,message:'arbitrary description',data:{err_name:'ErrTmuxUnresponsive'}}};
 if(mode==='once'&&n>1){send(success);return;}
 if(mode==='wrong-name')error.error.data.err_name='ErrOwnershipChanged';
 if(mode==='text-only'){delete error.error.data;error.error.message='ErrTmuxUnresponsive';}
 if(mode==='wrong-code')error.error.code=-32602;
 if(mode==='missing-message')delete error.error.message;
 if(mode==='array-data')error.error.data=['ErrTmuxUnresponsive'];
 if(mode==='both')error.result=success.result;
 if(mode==='wrong-id')error.id='wrong';
 if(mode==='wrong-version')error.jsonrpc='1.0';
 if(mode==='malformed'){process.stdout.write('{broken\n');return;}
 if(mode==='isError'){success.result.isError=true;send(success);return;}
 if(mode==='missing-result'){delete success.result;send(success);return;}
 if(mode==='false-error'){success.error=false;send(success);return;}
 if(mode==='eof'){process.stdout.end();return;}
 if(mode==='exit'){process.exit(0);}
 if(mode==='deadline-success'||mode==='deadline-error'){
  fs.writeFileSync(dir+'/clock-jump','');send(mode==='deadline-success'?success:error);return;
 }
 if(mode==='persistent'&&n===1)setTimeout(()=>send(success),10200);
 if(mode==='persistent'&&n>=21)return;
 send(error);
});`
	if err := os.WriteFile(endpoint, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(mode, "deadline-") {
		raw, err := os.ReadFile(e.runtime.Program)
		if err != nil {
			t.Fatal(err)
		}
		preamble := fmt.Sprintf("const realClock=performance;global.performance={now:()=>realClock.now()+(require('fs').existsSync(%q)?10001:0)};\n", filepath.Join(dir, "clock-jump"))
		raw = bytes.Replace(raw, []byte("// Synthetic ACP"), []byte(preamble+"// Synthetic ACP"), 1)
		if err := os.WriteFile(e.runtime.Program, raw, 0700); err != nil {
			t.Fatal(err)
		}
		// newRequestEnv started the original fixture; replace only this test's owned peer.
		e.process.terminate()
		e.process, err = startProcess(e.runtime.Program, []string{"acp"}, t.TempDir(), os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.process.terminate(); e.process.input.Close(); e.process.output.Close() })
	}
	registration := cursormcp.Stdio{Command: "/usr/local/bin/node", Args: []string{endpoint, mode, dir}, Env: []cursormcp.Env{}}
	runToolMCP(t, e, registration, nil)
	return e, dir
}

func assertToolCorrelations(t *testing.T, e *requestEnv, want int) {
	t.Helper()
	ids := map[string]bool{}
	for _, event := range toolEvents(t, e, "request_written") {
		if string(event["method"]) != `"tools/call"` {
			continue
		}
		id := string(event["id"])
		if ids[id] {
			t.Fatalf("reused correlation: %s", id)
		}
		ids[id] = true
	}
	if len(ids) != want {
		t.Fatalf("physical tool requests=%d want %d", len(ids), want)
	}
}

func TestCursorGuidanceToolRetryOnlyTypedRead(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		success    bool
	}{
		{"once", "read-pane", true}, {"once", "send-keys", false}, {"once", "spawn", false}, {"once", "decide", false}, {"once", "get", false},
		{"wrong-name", "read-pane", false}, {"text-only", "read-pane", false}, {"wrong-code", "read-pane", false}, {"missing-message", "read-pane", false},
		{"array-data", "read-pane", false}, {"both", "read-pane", false}, {"wrong-id", "read-pane", false}, {"wrong-version", "read-pane", false},
		{"malformed", "read-pane", false}, {"isError", "read-pane", false}, {"eof", "read-pane", false}, {"exit", "read-pane", false},
		{"missing-result", "read-pane", false}, {"false-error", "read-pane", false},
		{"deadline-success", "read-pane", false}, {"deadline-error", "read-pane", false},
	} {
		t.Run(tc.name+"/"+tc.tool, func(t *testing.T) {
			e, dir := syntheticToolMCP(t, tc.name)
			e.runtime.MCPCommand(t, "read", "calls", map[string]any{"name": tc.tool, "arguments": map[string]any{"instance_id": "same captured arguments"}})
			result := waitToolEvent(t, e, "control_result", 1, 2*time.Second)[0]
			if (len(result["error"]) == 0) != tc.success {
				t.Fatalf("control=%s", result)
			}
			want := 1
			if tc.success {
				want = 2
			}
			assertToolCorrelations(t, e, want)
			successes := len(toolEvents(t, e, "tool_response"))
			if (successes == 1) != tc.success {
				t.Fatalf("logical successes=%d", successes)
			}
			replies := toolEvents(t, e, "response_received")
			if tc.name == "once" && tc.success && (len(replies) != 3 || string(replies[1]["code"]) != "-32000") {
				t.Fatalf("failed attempt hidden: %s", replies)
			}
			if tc.success {
				raw, err := os.ReadFile(filepath.Join(dir, "requests"))
				if err != nil {
					t.Fatal(err)
				}
				var original, retry struct {
					Params json.RawMessage `json:"params"`
				}
				requests := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
				if len(requests) != 2 {
					t.Fatalf("captured requests=%s", raw)
				}
				if err := json.Unmarshal(requests[0], &original); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(requests[1], &retry); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(original.Params, retry.Params) {
					t.Fatal("retry changed captured call arguments")
				}
			}
		})
	}
}

func TestCursorGuidanceToolRetryDeadlineAndRetirement(t *testing.T) {
	for _, mode := range []string{"persistent", "retired"} {
		t.Run(mode, func(t *testing.T) {
			e, dir := syntheticToolMCP(t, "persistent")
			started := time.Now()
			e.runtime.MCPCommand(t, "read", "calls", map[string]any{"name": "read-pane", "arguments": map[string]any{"instance_id": "same captured arguments"}})
			waitToolEvent(t, e, "response_received", 2, time.Second)
			if mode == "retired" {
				var pid int
				json.Unmarshal(toolEvents(t, e, "started")[0]["pid"], &pid)
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			}
			result := waitToolEvent(t, e, "control_result", 1, 11*time.Second)[0]
			if len(result["error"]) == 0 {
				t.Fatalf("unavailable peer became logical success: %s", result)
			}
			if mode == "persistent" {
				elapsed := time.Since(started)
				if elapsed < 9800*time.Millisecond || elapsed > 11*time.Second {
					t.Fatalf("logical deadline reset or shortened: %v", elapsed)
				}
				if !bytes.Contains(result["error"], []byte("MCP response deadline")) {
					t.Fatalf("wrong failure: %s", result)
				}
				waitToolEvent(t, e, "response_rejected", 1, time.Second)
			} else if time.Since(started) > time.Second {
				t.Fatal("retirement did not cancel backoff")
			}
			before, err := os.ReadFile(filepath.Join(dir, "requests"))
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
			after, err := os.ReadFile(filepath.Join(dir, "requests"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || len(toolEvents(t, e, "tool_response")) != 0 {
				t.Fatal("late request or logical result after deadline/retirement")
			}
			physical := bytes.Count(before, []byte("\n"))
			if physical < 1 || physical > 24 {
				t.Fatalf("unbounded attempts: %d", physical)
			}
			assertToolCorrelations(t, e, physical)
			if mode == "persistent" {
				failed := 0
				for _, event := range toolEvents(t, e, "response_received") {
					if string(event["code"]) == "-32000" {
						failed++
					}
				}
				if physical < 3 || failed != physical-1 {
					t.Fatalf("failed attempts missing: requests=%d typed replies=%d", physical, failed)
				}
				t.Logf("one original10s budget; elapsed=%s physical=%d typed failures=%d; final silent request timed out; late success rejected", time.Since(started), physical, failed)
			}
			if mode == "retired" {
				e.runtime.MCPCommand(t, "replace", "restart")
				replacement := waitToolEvent(t, e, "control_result", 2, 2*time.Second)[1]
				if len(replacement["error"]) != 0 {
					t.Fatalf("replacement failed: %s", replacement)
				}
				time.Sleep(150 * time.Millisecond)
				if len(toolEvents(t, e, "tool_response")) != 0 {
					t.Fatal("retired call resumed on replacement")
				}
				replaced, err := os.ReadFile(filepath.Join(dir, "requests"))
				if err != nil || !bytes.Equal(before, replaced) {
					t.Fatal("retired call wrote after replacement")
				}
			}
		})
	}
}
