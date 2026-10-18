package realtmux_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Each adapter crosses its named production boundary; close ends only the caller.
type paneCaller struct {
	t       *testing.T
	surface string
	call    func(string, map[string]any) ([]byte, error)
	close   func()
}

func newPaneCaller(t *testing.T, f *spawnFix, surface, bin string) *paneCaller {
	t.Helper()
	c := &paneCaller{t: t, surface: surface, close: func() {}}
	switch surface {
	case "Go release wrapper":
		source := filepath.Join(t.TempDir(), "main.go")
		program := `package main
import("encoding/json";"fmt";"os";"github.com/gabemahoney/agent-director/pkg/api")
func main(){ c,e:=api.New(api.Options{StorePath:os.Args[1]});if e!=nil{panic(e)};defer c.Close(); var p map[string]any;if e=json.Unmarshal([]byte(os.Args[3]),&p);e!=nil{panic(e)};var extra map[string]string;b,_:=json.Marshal(p["extra_env"]);json.Unmarshal(b,&extra);var r any;switch os.Args[2]{case "resume":r,e=c.Resume(api.ResumeParams{InstanceID:p["instance_id"].(string)});case "spawn":r,e=c.Spawn(api.SpawnParams{InstanceID:p["instance_id"].(string),Agent:p["agent"].(string),CWD:p["cwd"].(string),NoPreTrust:p["no_pre_trust"].(bool),ExtraEnv:extra});default:panic("unsupported wrapper verb")};if e!=nil{fmt.Fprintln(os.Stderr,e);os.Exit(1)};json.NewEncoder(os.Stdout).Encode(r)}
`
		if err := os.WriteFile(source, []byte(program), 0600); err != nil {
			t.Fatal(err)
		}
		wrapper := filepath.Join(t.TempDir(), "embedded-caller")
		if out, err := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", wrapper, source).CombinedOutput(); err != nil {
			t.Fatalf("build standalone Go caller: %v %s", err, out)
		}
		reads := newPaneCaller(t, f, "Go", bin)
		c.call = func(verb string, p map[string]any) ([]byte, error) {
			if verb != "resume" && verb != "spawn" {
				return reads.call(verb, p)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, wrapper, f.DBPath, verb, string(raw)).CombinedOutput()
		}
	case "CLI":
		c.call = func(verb string, p map[string]any) ([]byte, error) {
			args := []string{verb, "--store-path", f.DBPath}
			if extra, ok := p["extra_env"].(map[string]string); ok {
				for key, value := range extra {
					args = append(args, "--extra-env", key+"="+value)
				}
			}
			for _, key := range []string{"instance_id", "text", "key", "n_lines", "request_token", "decision", "expect_pane_sha256", "agent", "cwd", "parent"} {
				if v, ok := p[key]; ok {
					args = append(args, "--"+strings.ReplaceAll(key, "_", "-"), fmt.Sprint(v))
				}
			}
			if allowed, _ := p["allow_pending"].(bool); allowed {
				args = append(args, "--allow-pending")
			}
			for _, key := range []string{"reuse_finished", "no_pre_trust"} {
				if enabled, _ := p[key].(bool); enabled {
					args = append(args, "--"+strings.ReplaceAll(key, "_", "-"))
				}
			}
			if values, ok := p["agent_args"].([]string); ok {
				args = append(args, "--")
				args = append(args, values...)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				return append(out, stderr.Bytes()...), err
			}
			return out, nil
		}
	case "Go":
		client, err := api.New(api.Options{StorePath: f.DBPath})
		if err != nil {
			t.Fatal(err)
		}
		c.close = func() {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		}
		c.call = func(verb string, p map[string]any) ([]byte, error) {
			encoded, _ := json.Marshal(p)
			var result any
			var err error
			switch verb {
			case "spawn":
				reuse, _ := p["reuse_finished"].(bool)
				noTrust, _ := p["no_pre_trust"].(bool)
				args, _ := p["agent_args"].([]string)
				extra, _ := p["extra_env"].(map[string]string)
				result, err = client.Spawn(api.SpawnParams{InstanceID: p["instance_id"].(string), Agent: p["agent"].(string), CWD: p["cwd"].(string), ReuseFinished: reuse, NoPreTrust: noTrust, AgentArgs: args, ExtraEnv: extra})
			case "list":
				parent, _ := p["parent"].(string)
				result, err = client.List(api.ListParams{Parent: parent})
			case "resume":
				result, err = client.Resume(api.ResumeParams{InstanceID: p["instance_id"].(string)})
			case "list-subagents":
				result, err = client.ListSubagents(p["instance_id"].(string))
			case "send-keys":
				var v api.SendKeysParams
				json.Unmarshal(encoded, &v)
				result, err = client.SendKeys(v)
			case "read-pane":
				var v api.ReadPaneParams
				json.Unmarshal(encoded, &v)
				result, err = client.ReadPane(v)
			case "get":
				result, err = client.Get(p["instance_id"].(string))
			case "get-permission":
				var v api.GetPermissionParams
				json.Unmarshal(encoded, &v)
				result, err = client.GetPermission(v)
			case "pause":
				var v api.PauseParams
				json.Unmarshal(encoded, &v)
				result, err = client.Pause(context.Background(), v)
			default:
				t.Fatalf("unsupported public Go verb %s", verb)
			}
			if err != nil {
				return nil, err
			}
			raw, e := json.Marshal(result)
			return raw, e
		}
	case "MCP", "Bun", "Bun installed":
		command, args := bin, []string{"serve", "--stdio", "--store-path", f.DBPath}
		if surface == "Bun" || surface == "Bun installed" {
			// Bundle the package's public export, exactly as its build does, outside the source tree.
			entry, err := filepath.Abs("../../pkg/ts-bun-client/src/index.ts")
			if err != nil {
				t.Fatal(err)
			}
			bundle := filepath.Join(t.TempDir(), "client.js")
			if surface == "Bun installed" {
				bundle = installedCursorClient(t)
			} else if out, err := exec.Command("bun", "build", entry, "--target=bun", "--outfile", bundle).CombinedOutput(); err != nil {
				t.Fatalf("build Bun public export: %v %s", err, out)
			}
			js := fmt.Sprintf(`import {Client} from %q;
import {createInterface} from "node:readline";
const c=await Client.create({storePath:%q,home:%q,_cliPath:%q});
const methods={"spawn":"spawn","resume":"resume","list":"list","list-subagents":"listSubagents","send-keys":"sendKeys","read-pane":"readPane","get":"get","get-permission":"getPermission","pause":"pause"};
try {for await(const line of createInterface({input:process.stdin})) {
 const r=JSON.parse(line); let text="{}";
 if(r.method==="tools/call") {try {text=JSON.stringify(await c[methods[r.params.name]](r.params.arguments));}
 catch(e){text=JSON.stringify({err_name:e.err_name||e.name,message:e.message});}}
 console.log(JSON.stringify({jsonrpc:"2.0",id:r.id,result:{content:[{type:"text",text}]}}));
}} finally {await c.close();}`, bundle, f.DBPath, os.Getenv("HOME"), bin)
			path := filepath.Join(t.TempDir(), "caller.mjs")
			if err := os.WriteFile(path, []byte(js), 0600); err != nil {
				t.Fatal(err)
			}
			command, args = "bun", []string{path}
		}
		m := startStdioMCP(t, command, args, os.Environ(), f.CWD)
		m.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "pane-controls", "version": "1"}})
		c.close = m.stop
		c.call = func(verb string, p map[string]any) ([]byte, error) {
			m.id++
			request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": "tools/call", "params": map[string]any{"name": verb, "arguments": p}})
			if err != nil {
				return nil, err
			}
			if _, err = m.in.Write(append(request, '\n')); err != nil {
				return nil, err
			}
			select {
			case raw, ok := <-m.lines:
				if !ok {
					return nil, fmt.Errorf("%s caller closed stdout", surface)
				}
				var response mcpResult
				if err := json.Unmarshal([]byte(raw), &response); err != nil {
					return []byte(raw), err
				}
				if response.Error != nil {
					return []byte(raw), fmt.Errorf("%s", response.Error.Message)
				}
				if response.Result == nil || len(response.Result.Content) != 1 {
					return []byte(raw), fmt.Errorf("unexpected %s response", surface)
				}
				return []byte(response.Result.Content[0].Text), nil
			case <-time.After(30 * time.Second):
				return nil, fmt.Errorf("%s %s caller deadline", surface, verb)
			}
		}
	default:
		t.Fatalf("unknown caller %s", surface)
	}
	t.Cleanup(c.close)
	return c
}

func (c *paneCaller) must(verb string, p map[string]any) []byte {
	c.t.Helper()
	raw, err := c.call(verb, p)
	var failure struct {
		Name  string          `json:"err_name"`
		Error json.RawMessage `json:"error"`
	}
	if err != nil || json.Unmarshal(raw, &failure) != nil || failure.Name != "" || len(failure.Error) > 0 {
		c.t.Fatalf("%s %s params=%v: err=%v response=%s", c.surface, verb, p, err, raw)
	}
	return raw
}
func (c *paneCaller) send(id, text string) {
	c.t.Helper()
	c.must("send-keys", map[string]any{"instance_id": id, "text": text})
}

type paneRecord struct {
	Sequence   uint64 `json:"sequence"`
	Life       int64  `json:"life_number"`
	Connection string `json:"connection_id"`
	Kind       string `json:"kind"`
	Session    string `json:"session_id"`
	Instance   string `json:"instance_id"`
	Launch     string `json:"launch_token"`
	Data       struct {
		ID          string `json:"submission_id"`
		Text        string `json:"text"`
		Request     string `json:"request_id"`
		RuntimeGone bool   `json:"runtime_gone"`
		Intentional bool   `json:"intentional"`
	} `json:"data"`
}

func paneRecords(t *testing.T, path string) []paneRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result []paneRecord
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines[:len(lines)-1] {
		var r paneRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("protected record: %v %q", err, line)
		}
		result = append(result, r)
	}
	return result
}
func paneRecordCount(t *testing.T, path, kind string) int {
	t.Helper()
	n := 0
	for _, r := range paneRecords(t, path) {
		if r.Kind == kind {
			n++
		}
	}
	return n
}

type panePrompt struct{ Text, Session, Request string }

func panePrompts(t *testing.T, r cursorfix.Runtime) []panePrompt {
	t.Helper()
	var result []panePrompt
	for _, frame := range r.Frames(t) {
		if string(frame["method"]) != `"session/prompt"` {
			continue
		}
		var p struct {
			Session string `json:"sessionId"`
			Prompt  []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		if err := json.Unmarshal(frame["params"], &p); err != nil || len(p.Prompt) != 1 {
			t.Fatalf("invalid actual prompt: %s %v", frame["params"], err)
		}
		result = append(result, panePrompt{p.Prompt[0].Text, p.Session, string(frame["id"])})
	}
	return result
}

// Synthetic ACP, actual tmux, genuine public transports and a protected launch transcript.
func TestCursorPaneControlsPublicMixedInputAndRecreation(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane public composition")
	}
	// Serial: existing real-pane fixtures own HOME/PATH and the tmux environment.
	for _, surface := range []string{"CLI", "MCP", "Go", "Bun"} {
		t.Run(surface, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			id := launch.ID
			bin := buildHookCLI(t, t.TempDir())
			caller := newPaneCaller(t, f, surface, bin)
			before := readRow(t, f.DBPath, id)
			path := before.JSONLPath.(string)
			protected, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			decoyPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db")
			if _, err := apitest.SeedSpawn(decoyPath, id, store.StateWaiting, f.CWD, "off", "decoy", false, apitest.WithAgent(store.AgentCursor)); err != nil {
				t.Fatal(err)
			}
			decoy := readRow(t, decoyPath, id)
			runtimePID := readFileTrim(t, r.PID)
			diagnostic := func() string {
				return fmt.Sprintf("surface=%s id=%s prompts=%+v row=%+v records=%+v", surface, id, panePrompts(t, r), readRow(t, f.DBPath, id), paneRecords(t, path))
			}
			want := []string{"第一 🐝\nline café", "second 🦋\n別の行", "HOLD", "terminal queued café", "public queued 🐝\nlast line"}
			for i, text := range want[:2] {
				caller.send(id, text)
				waitFor(t, "public multiline outcome and committed waiting", func() bool {
					if paneRecordCount(t, path, "turn_outcome") != i+1 || readRow(t, f.DBPath, id).State != store.StateWaiting {
						return false
					}
					pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 200})
					return err == nil && strings.Count(pane.Pane, "Cursor turn complete") == i+1
				}, diagnostic)
			}
			caller.send(id, "HOLD")
			waitFor(t, "held prompt dispatched", func() bool { return len(panePrompts(t, r)) == 3 }, diagnostic)
			launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", want[3])
			launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "Enter")
			waitFor(t, "terminal input admitted before next public input", func() bool { return paneRecordCount(t, path, "submission_accepted") == 4 }, diagnostic)
			caller.send(id, want[4])
			waitFor(t, "public input durably admitted", func() bool { return paneRecordCount(t, path, "submission_accepted") == 5 }, diagnostic)
			caller.close()
			caller = newPaneCaller(t, f, surface, bin)
			caller.must("get", map[string]any{"instance_id": id})
			after := readRow(t, f.DBPath, id)
			if before.SessionID != after.SessionID || before.LaunchToken != after.LaunchToken || before.PanePID != after.PanePID || before.PaneStarttime != after.PaneStarttime || runtimePID != readFileTrim(t, r.PID) || after.State != store.StateWorking {
				t.Fatalf("caller recreation changed active identity: %s", diagnostic())
			}
			if len(panePrompts(t, r)) != 3 {
				t.Fatalf("queued prompt dispatched before release: %s", diagnostic())
			}
			for _, frame := range r.Frames(t) {
				if string(frame["method"]) == `"session/cancel"` {
					t.Fatalf("follow-up canceled active turn: %s", diagnostic())
				}
			}
			if err := os.WriteFile(r.Release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "mixed FIFO outcomes", func() bool { return paneRecordCount(t, path, "turn_outcome") == len(want) }, diagnostic)
			waitFor(t, "mixed turns committed waiting", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, diagnostic)
			prompts := panePrompts(t, r)
			if len(prompts) != len(want) {
				t.Fatalf("duplicate/missing prompts: %s", diagnostic())
			}
			accepted, started, outcomes := map[string]string{}, map[string]string{}, map[string]string{}
			var admitted []string
			for index, rec := range paneRecords(t, path) {
				if rec.Sequence != uint64(index+1) || rec.Instance != id || rec.Launch != before.LaunchToken || rec.Life != before.LifeNumber || rec.Connection == "" {
					t.Fatalf("protected attribution/sequence lost: %+v", rec)
				}
				switch rec.Kind {
				case "submission_accepted":
					if accepted[rec.Data.ID] != "" {
						t.Fatalf("duplicate accepted ID %s", rec.Data.ID)
					}
					accepted[rec.Data.ID] = rec.Data.Text
					admitted = append(admitted, rec.Data.Text)
				case "submission_started":
					if started[rec.Data.ID] != "" {
						t.Fatalf("duplicate dispatch: %+v", rec)
					}
					started[rec.Data.ID] = string(rec.Data.Request)
				case "turn_outcome":
					if outcomes[rec.Data.ID] != "" {
						t.Fatalf("duplicate outcome: %+v", rec)
					}
					outcomes[rec.Data.ID] = string(rec.Data.Request)
				}
			}
			if !reflect.DeepEqual(admitted, want) {
				t.Fatalf("admission FIFO=%q want=%q", admitted, want)
			}
			for i, prompt := range prompts {
				if prompt.Text != want[i] || prompt.Session != before.SessionID {
					t.Fatalf("prompt identity/order: %+v want %q session=%s", prompt, want[i], before.SessionID)
				}
				matches := 0
				for submission, text := range accepted {
					if text == prompt.Text && started[submission] == prompt.Request && outcomes[submission] == prompt.Request {
						matches++
					}
				}
				if matches != 1 {
					t.Fatalf("missing submission/request/outcome correlation for %+v: starts=%v outcomes=%v", prompt, started, outcomes)
				}
			}
			t.Logf("public boundary=%s instance=%s session=%s accepted=%v dispatched=%v completed=%v", surface, id, before.SessionID, accepted, started, outcomes)
			var pane api.ReadPaneResult
			raw := caller.must("read-pane", map[string]any{"instance_id": id, "n_lines": 200})
			if err := json.Unmarshal(raw, &pane); err != nil || !strings.Contains(pane.Pane, "Synthetic reply:") || !strings.Contains(pane.Pane, "last line") {
				t.Fatalf("public pane misses exact reply: %s %v", raw, err)
			}
			for index, control := range []string{"/clear", "/new"} {
				old := readRow(t, f.DBPath, id)
				published := paneRecordCount(t, path, "rotation_published")
				caller.send(id, control)
				waitFor(t, "confirmed public rotation", func() bool {
					row := readRow(t, f.DBPath, id)
					if row.SessionID == old.SessionID || row.State != store.StateWaiting || paneRecordCount(t, path, "rotation_published") != published+1 {
						return false
					}
					pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 200})
					return err == nil && strings.Count(pane.Pane, "Cursor fresh conversation published") == published+1
				}, diagnostic)
				current := readRow(t, f.DBPath, id)
				currentFile, err := os.Stat(path)
				if err != nil || !os.SameFile(protected, currentFile) {
					t.Fatalf("protected launch file replaced across rotation: %v", err)
				}
				inspection := caller.must("get", map[string]any{"instance_id": id})
				if !strings.Contains(string(inspection), current.SessionID.(string)) || !strings.Contains(string(inspection), old.SessionID.(string)) {
					t.Fatalf("public current/history missing: %s", inspection)
				}
				s, err := store.Open(f.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				history, e := s.ListSessionHistory(id, current.LifeNumber.(int64))
				s.Close()
				if e != nil || len(history) != index+1 || current.JSONLPath != before.JSONLPath || current.LifeNumber != before.LifeNumber || current.LaunchToken != before.LaunchToken {
					t.Fatalf("rotation identity/history: %+v %+v %v", current, history, e)
				}
			}
			caller.must("pause", map[string]any{"instance_id": id})
			if got := readRow(t, decoyPath, id); !reflect.DeepEqual(got, decoy) {
				t.Fatal("public caller touched same-ID default-store decoy")
			}
		})
	}
}
