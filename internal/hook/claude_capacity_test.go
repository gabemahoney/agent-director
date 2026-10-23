package hook_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func claudeCallbackRow(t *testing.T) (*store.Store, string, string, contextfix.Sample, hook.HandleConfig) {
	t.Helper()
	s := contextfix.Must(t, "claude-parent")
	path := apitest.SeedContextTranscript(t, s)
	identity, err := apitest.ContextWriter(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	const id = "claude-capacity"
	st, db := ssgSeed(t, id, store.StateCheckPermission, s.SessionID, apitest.WithJsonlPath(path), apitest.WithLaunchIdentity(identity), apitest.WithPID(identity.PanePID), apitest.WithProcStarttime(identity.PaneStarttime))
	hc := hookConfig(envHook(id, "on"), agentParent(t, st, id))
	hc.ContextSource = "statusLine"
	hc.Start = s.ObservedAt
	return st, db, id, s, hc
}

func callbackJSON(t *testing.T, raw []byte, change func(map[string]any)) string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	change(p)
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func seedClaudeCallbackChild(t *testing.T, db, id, parentPath string, child contextfix.Sample, visible bool, options ...apitest.SubagentOption) string {
	t.Helper()
	path := store.ClaudeChildPath(parentPath, child.SessionID, child.ChildID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, child.Transcript, 0600); err != nil {
		t.Fatal(err)
	}
	if visible {
		if err := apitest.SeedSubagent(db, id, child.ChildID, append([]apitest.SubagentOption{apitest.WithSubagentSessionID(child.SessionID)}, options...)...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func assertClaudeCallbackContext(t *testing.T, got api.ContextReading, sample contextfix.Sample) {
	t.Helper()
	var want api.ContextReading
	if err := json.Unmarshal(sample.Expected, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("context=%+v; want %s", got, sample.Expected)
	}
}

func TestClaudeCapacityAdmissionAndAuthority(t *testing.T) {
	for _, name := range []string{"accepted", "foreign parent", "wrong process start", "unreadable process start", "wrong session", "wrong version", "wrong model", "zero capacity", "unmatched usage", "wrong agent", "wrong callback", "malformed"} {
		t.Run(name, func(t *testing.T) {
			st, db, id, s, hc := claudeCallbackRow(t)
			request, err := apitest.SeedPermissionRequest(db, id, "Bash")
			if err != nil {
				t.Fatal(err)
			}
			permission, err := st.GetPermissionRequest(id, request.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			prior := mustGetSpawn(t, st, id)
			payload := callbackJSON(t, s.Callback, func(p map[string]any) {
				p["hook_event_name"] = "SessionEnd"
				p["transcript_path"] = "/untrusted/path"
				p["agent_id"] = "new-agent"
				switch name {
				case "wrong session":
					p["session_id"] = "other"
				case "wrong version":
					p["version"] = "2.1.281"
				case "wrong model":
					p["model"].(map[string]any)["id"] = "other"
				case "zero capacity":
					p["context_window"].(map[string]any)["context_window_size"] = 0
				case "unmatched usage":
					p["context_window"].(map[string]any)["current_usage"].(map[string]any)["input_tokens"] = 1
				}
			})
			p := agentParent(t, st, id)
			switch name {
			case "foreign parent":
				p.PID++
			case "wrong process start":
				p.Start = "foreign"
			case "unreadable process start":
				p.Start = ""
			case "wrong agent":
				hc.Agent = "codex"
			case "wrong callback":
				hc.ContextSource = "other"
			case "malformed":
				payload = "{"
			}
			hc.ParentPID = func() int { return p.PID }
			hc.ParentProc = newParentProc(p)
			if out := ssgHandleWith(t, st, hc, payload, nil); out != "" {
				t.Fatalf("capacity callback emitted permission/lifecycle output: %q", out)
			}
			after := mustGetSpawn(t, st, id)
			known := after.ClaudeCapacity != nil && after.ClaudeCapacity.Main != nil && !after.ClaudeCapacity.Main.Invalidated
			if known != (name == "accepted") {
				t.Errorf("capacity=%+v; accepted=%v", after.ClaudeCapacity, name == "accepted")
			}
			after.ClaudeCapacity = nil
			if !reflect.DeepEqual(prior, after) {
				t.Errorf("capacity callback changed lifecycle/session/identity: before=%+v after=%+v", prior, after)
			}
			got, err := st.GetPermissionRequest(id, request.RequestToken)
			if err != nil || !reflect.DeepEqual(got, permission) {
				t.Fatalf("permission changed: %+v %v", got, err)
			}
			_, children, err := st.ListSubagentRecords(id)
			if err != nil || len(children) != 0 {
				t.Fatalf("callback discovered children: %+v %v", children, err)
			}
		})
	}
}

func TestClaudeCapacityInvalidationCannotReplayResponse(t *testing.T) {
	for _, name := range []string{"window changed", "model changed", "version changed", "provider changed", "window override"} {
		t.Run(name, func(t *testing.T) {
			st, _, id, s, hc := claudeCallbackRow(t)
			ssgHandleWith(t, st, hc, string(s.Callback), nil)
			if mustGetSpawn(t, st, id).ContextEvidence().CurrentClaudeCapacity() == nil {
				t.Fatal("baseline capacity missing")
			}
			invalid := callbackJSON(t, s.Callback, func(p map[string]any) {
				switch name {
				case "window changed":
					p["context_window"].(map[string]any)["context_window_size"] = 100000
				case "model changed":
					p["model"].(map[string]any)["id"] = "other"
				case "version changed":
					p["version"] = "2.1.281"
				}
			})
			validEnv := hc.Env
			if name == "provider changed" || name == "window override" {
				hc.Env = func(k string) string {
					if k == "ANTHROPIC_BASE_URL" && name == "provider changed" || k == "CLAUDE_CODE_MAX_CONTEXT_TOKENS" && name == "window override" {
						return "override"
					}
					return validEnv(k)
				}
			}
			ssgHandleWith(t, st, hc, invalid, nil)
			hc.Env = validEnv
			for i := 0; i < 2; i++ {
				ssgHandleWith(t, st, hc, string(s.Callback), nil)
			}
			got, err := api.Status(st, api.RelayView{}, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Context.UsedTokens != nil {
				t.Errorf("replayed same response restored invalidated capacity: %+v", got.Context)
			}
			next := contextfix.ClaudeVariant(t, "new-response")
			if err := os.WriteFile(mustGetSpawn(t, st, id).JSONLPath, next.Transcript, 0600); err != nil {
				t.Fatal(err)
			}
			ssgHandleWith(t, st, hc, string(next.Callback), nil)
			got, err = api.Status(st, api.RelayView{}, id)
			if err != nil {
				t.Fatal(err)
			}
			assertClaudeCallbackContext(t, got.Context, next)
		})
	}
}

func TestClaudeCapacityOnlyAlreadyVisibleChildren(t *testing.T) {
	for _, name := range []string{"current", "prior launch", "wrong session", "unseen"} {
		t.Run(name, func(t *testing.T) {
			st, db, id, parent, hc := claudeCallbackRow(t)
			child := contextfix.Must(t, "claude-child")
			var options []apitest.SubagentOption
			if name == "prior launch" {
				options = append(options, apitest.WithSubagentLaunchToken("b2b2b2b2b2b2b2b2"))
			}
			if name == "wrong session" {
				options = []apitest.SubagentOption{apitest.WithSubagentSessionID("other")}
			}
			path := seedClaudeCallbackChild(t, db, id, mustGetSpawn(t, st, id).JSONLPath, child, name != "unseen", options...)
			ssgHandleWith(t, st, hc, string(parent.Callback), nil)
			hc.ContextSource, hc.Start = "subagentStatusLine", child.ObservedAt
			ssgHandleWith(t, st, hc, string(child.Callback), nil)
			cap := mustGetSpawn(t, st, id).ClaudeCapacity
			_, has := cap.Children[child.ChildID]
			if has != (name == "current") {
				t.Fatalf("child capacity admitted=%v for %s", has, name)
			}
			_, children, err := st.ListSubagentRecords(id)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if name == "unseen" {
				want = 0
			}
			if len(children) != want {
				t.Fatalf("visible children=%d; want %d", len(children), want)
			}
			if name == "current" {
				invalid := callbackJSON(t, child.Callback, func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["model"] = "other" })
				ssgHandleWith(t, st, hc, invalid, nil)
				for i := 0; i < 2; i++ {
					ssgHandleWith(t, st, hc, string(child.Callback), nil)
				}
				result, err := api.ListSubagents(st, id)
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Subagents) != 1 || result.Subagents[0].Context.UsedTokens != nil {
					t.Errorf("replayed child response restored invalidated model: %+v", result)
				}
				// This history is synthetic; the native capture remains unchanged.
				next := bytes.ReplaceAll(child.Transcript, []byte(child.ResponseUUID), []byte("synthetic-child-response"))
				if err := os.WriteFile(path, next, 0600); err != nil {
					t.Fatal(err)
				}
				ssgHandleWith(t, st, hc, string(child.Callback), nil)
				result, err = api.ListSubagents(st, id)
				if err != nil || len(result.Subagents) != 1 {
					t.Fatalf("child recovery: %+v %v", result, err)
				}
				assertClaudeCallbackContext(t, result.Subagents[0].Context, child)
			}
		})
	}
}

func TestClaudeCapacityRefreshExistingChildAtLimit(t *testing.T) {
	st, db, id, parent, hc := claudeCallbackRow(t)
	child := contextfix.Must(t, "claude-child")
	parentPath := mustGetSpawn(t, st, id).JSONLPath
	ssgHandleWith(t, st, hc, string(parent.Callback), nil)
	var captured struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(child.Callback, &captured); err != nil {
		t.Fatal(err)
	}
	var tasks []any
	var paths []string
	for i := 0; i < 129; i++ {
		sample := child
		sample.ChildID = fmt.Sprintf("synthetic-child-%03d", i)
		sample.Transcript = bytes.ReplaceAll(child.Transcript, []byte(child.ChildID), []byte(sample.ChildID))
		paths = append(paths, seedClaudeCallbackChild(t, db, id, parentPath, sample, true))
		task := make(map[string]any, len(captured.Tasks[0]))
		for key, value := range captured.Tasks[0] {
			task[key] = value
		}
		task["id"] = sample.ChildID
		tasks = append(tasks, task)
	}
	hc.ContextSource, hc.Start = "subagentStatusLine", child.ObservedAt
	payload := callbackJSON(t, child.Callback, func(p map[string]any) { p["tasks"] = tasks[:128] })
	ssgHandleWith(t, st, hc, payload, nil)
	capacity := mustGetSpawn(t, st, id).ClaudeCapacity
	if len(capacity.Children) != 128 {
		t.Fatalf("initial children=%d; want 128", len(capacity.Children))
	}
	next := bytes.ReplaceAll(child.Transcript, []byte(child.ChildID), []byte("synthetic-child-000"))
	next = bytes.ReplaceAll(next, []byte(child.ResponseUUID), []byte("synthetic-refreshed-response"))
	if err := os.WriteFile(paths[0], next, 0600); err != nil {
		t.Fatal(err)
	}
	// A new visible child must not displace existing entries or prevent refresh.
	payload = callbackJSON(t, child.Callback, func(p map[string]any) { p["tasks"] = append([]any{tasks[128]}, tasks[:128]...) })
	ssgHandleWith(t, st, hc, payload, nil)
	capacity = mustGetSpawn(t, st, id).ClaudeCapacity
	if len(capacity.Children) != 128 {
		t.Fatalf("refreshed children=%d; want 128", len(capacity.Children))
	}
	if _, admitted := capacity.Children["synthetic-child-128"]; admitted {
		t.Error("new child admitted beyond the capacity limit")
	}
	result, err := api.ListSubagents(st, id)
	if err != nil || len(result.Subagents) != 129 {
		t.Fatalf("existing visibility changed: %+v %v", result, err)
	}
	for _, subagent := range result.Subagents {
		if subagent.SubagentID == "synthetic-child-128" {
			if !reflect.DeepEqual(subagent.Context, api.ContextReading{}) {
				t.Errorf("unadmitted child has context: %+v", subagent.Context)
			}
			continue
		}
		assertClaudeCallbackContext(t, subagent.Context, child)
	}
}
