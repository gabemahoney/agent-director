//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/adminapi"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Persistence failure at admission, outcome or intentional publication stops the owned runtime truthfully.
func TestCursorPersistenceFailureStopsAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane persistence failures")
	}
	for _, stage := range []string{"input replacement", "input permission", "queued admission", "queued cancellation", "turn outcome", "end publication", "rotation confirmation", "rotation publication"} {
		t.Run(stage, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			// Keep this owned server stable while the fault destroys its gateway pane.
			sentinel := launch.rt.startSession(t, uniqueName())
			waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			row := readRow(t, f.DBPath, launch.ID)
			path := row.JSONLPath.(string)
			runtimePID := 0
			fmt.Sscan(readFileTrim(t, r.PID), &runtimePID)
			gatewayPID := int(row.PanePID.(int64))
			if stage == "turn outcome" || strings.HasPrefix(stage, "queued") {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "HOLD"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "held turn", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWorking }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
				waitFor(t, "held prompt receipt", func() bool {
					for _, frame := range r.Frames(t) {
						if string(frame["method"]) == `"session/prompt"` {
							return true
						}
					}
					return false
				}, nil)
			}
			if strings.HasPrefix(stage, "rotation ") {
				r.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "/new"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "real pane rotation request", func() bool {
					n := 0
					for _, frame := range r.Frames(t) {
						if string(frame["method"]) == `"session/new"` {
							n++
						}
					}
					return n == 2
				}, nil)
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "real-rotation-held"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "real pane held record", func() bool {
					raw, err := os.ReadFile(path)
					return err == nil && strings.Contains(string(raw), `"kind":"input_held"`)
				}, nil)
			}
			if stage == "queued cancellation" {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "queued-never-dispatch"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "durable queued acceptance", func() bool {
					raw, err := os.ReadFile(path)
					return err == nil && strings.Contains(string(raw), `"kind":"submission_accepted"`) && strings.Contains(string(raw), "queued-never-dispatch")
				}, nil)
			}
			switch stage {
			case "input replacement", "turn outcome", "queued admission", "queued cancellation", "rotation confirmation":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement must survive\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "rotation publication":
				storefix.InjectWriteFailure(t, f.DBPath, storefix.WriteFailReuseArchive, launch.ID)
			case "input permission":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "end publication":
				if err := apitest.SeedSubagent(f.DBPath, launch.ID, "synthetic-cleanup-fault"); err != nil {
					t.Fatal(err)
				}
				storefix.InjectWriteFailure(t, f.DBPath, storefix.WriteFailSubagentRecord, launch.ID)
			}
			if strings.HasPrefix(stage, "rotation ") {
				if err := os.WriteFile(r.NewRelease, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if stage == "turn outcome" {
				if err := os.WriteFile(r.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if stage == "queued cancellation" {
				launch.rt.run(t, "send-keys", "-t", launch.Row.PaneID.(string), "C-c")
			} else if stage == "end publication" {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, err := f.API.Pause(ctx, api.PauseParams{InstanceID: launch.ID}); err == nil {
					t.Fatal("failed end publication reported successful pause")
				}
			} else {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "must not be admitted"}); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "gateway failed closed", func() bool { return pidGone(gatewayPID) }, func() string { return procState(gatewayPID) })
			waitFor(t, "owned runtime gone", func() bool { return pidGone(runtimePID) }, func() string { return procState(runtimePID) })
			if after := readRow(t, f.DBPath, launch.ID); after.State == store.StateEnded {
				t.Fatal("persistence failure fabricated ended")
			}
			if strings.HasPrefix(stage, "rotation ") {
				actual, err := store.Open(f.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				after, readErr := actual.GetSpawn(launch.ID)
				history, historyErr := actual.ListSessionHistory(launch.ID, after.LifeNumber)
				bound, bindErr := actual.ReadCursorRuntime(launch.ID)
				actual.Close()
				if readErr != nil || historyErr != nil || bindErr != nil || after.SessionID != row.SessionID || bound.SessionID != after.SessionID || len(history) != 0 {
					t.Fatalf("failed actual rotation fabricated identity/history: %+v %+v %+v errors=%v/%v/%v", after, bound, history, readErr, bindErr, historyErr)
				}
				raw, err := os.ReadFile(r.Raw)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "real-rotation-held") {
					t.Fatal("failed actual rotation sent provisional bytes")
				}
			}
			failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID)
			if len(failures) != 1 {
				t.Fatalf("observable failure missing: %v", failures)
			}
			if stage == "rotation publication" {
				if !strings.Contains(fmt.Sprint(failures[0]["reason"]), "Cursor rotation report failed; publication may already be committed; held input refused") {
					t.Fatalf("actual failed rotation lost exact notice: %v", failures)
				}
				inspected, err := f.API.Get(launch.ID)
				if err != nil || inspected.SessionID != row.SessionID || len(inspected.PriorSessions) != 0 {
					t.Fatalf("public failed-row inspection: %+v %v", inspected, err)
				}
				available, err := os.ReadFile(inspected.JSONLPath)
				if err != nil || !strings.Contains(string(available), `"kind":"input_held"`) || strings.Contains(string(available), `"kind":"session_end"`) {
					t.Fatalf("available records invented successful end: %q %v", available, err)
				}
				// Follow the documented live-row cleanup, then inspect public status.
				if inspected.State != store.StateEnded && inspected.State != store.StateMissing {
					if _, err := f.API.Kill(api.KillParams{InstanceID: launch.ID}); err != nil {
						t.Fatal(err)
					}
					if _, err := f.API.FindMissing(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				status, err := f.API.Status(launch.ID)
				if err != nil || status.State != store.StateEnded && status.State != store.StateMissing {
					t.Fatalf("cleanup did not reconcile failed row: %+v %v", status, err)
				}
				freshID := newInstanceID("cursor-recovery")
				cmd := exec.Command(buildHookCLI(t, t.TempDir()), "spawn", "--store-path", f.DBPath, "--agent", "cursor", "--instance-id", freshID, "--cwd", f.CWD, "--no-pre-trust")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fresh unique instance after reconciliation: %v %s", err, out)
				}
				fresh := f.launched(t, freshID)
				t.Cleanup(func() { fresh.rt.run(t, "kill-server") })
				waitFor(t, "fresh recovery instance ready", func() bool {
					pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: freshID, NLines: 100})
					return err == nil && strings.Contains(pane.Pane, "Cursor ready")
				}, nil)
				if raw, err := os.ReadFile(r.Raw); err != nil || strings.Contains(string(raw), "real-rotation-held") {
					t.Fatalf("fresh instance replayed refused input: %v", err)
				}
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: freshID}); err != nil {
					t.Fatal(err)
				}
			}
			if pidGone(sentinel.PanePID) {
				t.Fatal("failure cleanup killed the independent owned sentinel")
			}
			for _, frame := range r.Frames(t) {
				if strings.Contains(string(frame["params"]), "must not be admitted") || strings.Contains(string(frame["params"]), "queued-never-dispatch") {
					t.Fatal("failed persistence admitted prompt")
				}
			}
			if stage == "input replacement" || stage == "turn outcome" || strings.HasPrefix(stage, "queued") || stage == "rotation confirmation" {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "replacement must survive\n" {
					t.Fatalf("replacement mutated %q %v", raw, err)
				}
				// Inspect the original protected records that remain available. The
				// replacement is not history, and an acceptance is not execution.
				available, err := os.ReadFile(path + ".original")
				if err != nil {
					t.Fatal(err)
				}
				var queuedID string
				for _, line := range strings.Split(strings.TrimSpace(string(available)), "\n") {
					var rec struct {
						Kind string `json:"kind"`
						Data struct {
							ID   string `json:"submission_id"`
							Text string `json:"text"`
						} `json:"data"`
					}
					if err := json.Unmarshal([]byte(line), &rec); err != nil {
						t.Fatal(err)
					}
					if rec.Kind == "submission_accepted" && rec.Data.Text == "queued-never-dispatch" {
						queuedID = rec.Data.ID
					}
					if rec.Kind == "session_end" || rec.Kind == "turn_outcome" || (queuedID != "" && rec.Data.ID == queuedID && rec.Kind != "submission_accepted") {
						t.Fatalf("partial protected history falsely confirms completion or queued execution: %s", rec.Kind)
					}
				}
				if stage == "queued cancellation" && queuedID == "" {
					t.Fatal("available records lost the previously accepted queued identity")
				}
				before := len(r.Frames(t))
				if err := os.WriteFile(r.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if len(r.Frames(t)) != before || !pidGone(runtimePID) {
					t.Fatal("protected inspection recovered transient work")
				}
			}
		})
	}
}

func TestCursorHumanAdminDeleteLeavesRunningPaneUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane operator deletion")
	}
	f, r, launch := newCursorPane(t, false)
	waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
	runtimePID := 0
	fmt.Sscan(readFileTrim(t, r.PID), &runtimePID)
	result, err := adminapi.Delete(f.API, []string{launch.ID})
	if err != nil || result.Results[launch.ID] != "ok" {
		t.Fatalf("operator delete %+v %v", result, err)
	}
	st, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.GetSpawn(launch.ID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("operator delete did not remove row: %v", err)
	}
	if pidGone(runtimePID) || pidGone(int(launch.Row.PanePID.(int64))) {
		t.Fatal("operator row deletion unexpectedly terminated conversation")
	}
	if got := launch.rt.formatInt(t, launch.Row.PaneID.(string), "#{pane_pid}"); got != int(launch.Row.PanePID.(int64)) {
		t.Fatal("operator delete changed actual pane")
	}
}

// b.ivj Reproduction, D1-D3: a failed lead turn survives once, exposes its
// sanitized reason, and never ends its independently launched child.
func TestCursorLeadPromptInternalErrorPreservesChild(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane lead and child failures")
	}
	f, lead, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Relay: "on"})
	_, _, child := newCursorPaneScenario(t, false, "", cursorPaneOptions{Shared: f, Script: &cursorfix.Scenario{SessionID: "ivj-child"}})
	for _, id := range []string{launch.ID, child.ID} {
		waitFor(t, "waiting "+id, func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, nil)
	}
	if err := apitest.SeedParentChild(f.DBPath, launch.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	childBefore := readRow(t, f.DBPath, child.ID)
	childBinding, err := st.ReadCursorRuntime(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertChildUntouched := func() {
		t.Helper()
		got := readRow(t, f.DBPath, child.ID)
		binding, err := st.ReadCursorRuntime(child.ID)
		if got.ParentID != launch.ID || got.State != store.StateWaiting || got != childBefore || err != nil || binding != childBinding {
			t.Fatalf("lead failure changed child: row=%+v binding=%+v err=%v", got, binding, err)
		}
		for _, identity := range []struct {
			pid   int
			start string
		}{{binding.GatewayPID, binding.GatewayStart}, {binding.RuntimePID, binding.RuntimeStart}} {
			observed, ok := readStat(identity.pid)
			if !ok || observed.gone() || observed.start != identity.start {
				t.Fatalf("lead failure ended or replaced child process %d", identity.pid)
			}
		}
		if !child.rt.hasSession(t, child.SessionID) {
			t.Fatal("lead failure removed child's tmux session")
		}
	}

	const reason = "Cursor ACP session/prompt failed: runtime internal error; cause unknown"
	lead.Configure(t, cursorfix.Scenario{PromptReply: json.RawMessage(`{"error":{"code":-32603,"message":"SYNTHETIC internal"}}`)})
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "ivj first failure"}); err != nil {
		t.Fatal(err)
	}
	assertCursorTurnFailure(t, f, launch, reason)
	waitFor(t, "sanitized turn failure rendered", func() bool {
		pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: launch.ID, NLines: 100})
		if err == nil && strings.Contains(pane.Pane, "SYNTHETIC") {
			t.Fatalf("peer diagnostic leaked into pane: %s", pane.Pane)
		}
		// capture-pane preserves wrapping at the real terminal's width.
		return err == nil && strings.Contains(strings.ReplaceAll(pane.Pane, "\n", ""), "Cursor turn failed: "+reason)
	}, nil)
	assertChildUntouched()

	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "ivj second failure"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second failure ends lead gateway and session", func() bool {
		return pidGone(int(launch.Row.PanePID.(int64))) && readRow(t, f.DBPath, launch.ID).State == store.StateMissing && !launch.rt.hasSession(t, launch.SessionID)
	}, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
	assertCursorFailureViews(t, f, launch.ID, store.StateMissing, reason)
	failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID)
	if len(failures) != 1 || failures[0]["reason"] != reason || strings.Contains(fmt.Sprint(failures), "SYNTHETIC") {
		t.Fatalf("session-ending failure trail: %v", failures)
	}
	if turns := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.turn_failed", launch.ID); len(turns) != 1 {
		t.Fatalf("recoverable failure trail count = %d, want 1", len(turns))
	}
	assertChildUntouched()
	if prompts := panePrompts(t, lead); len(prompts) != 2 || prompts[0].Text != "ivj first failure" || prompts[1].Text != "ivj second failure" {
		t.Fatalf("failed prompts replayed or lost: %+v", prompts)
	}
}

func assertCursorTurnFailure(t *testing.T, f *spawnFix, launch spawned, reason string) {
	t.Helper()
	gatewayPID := int(launch.Row.PanePID.(int64))
	waitFor(t, "first failure returns to waiting", func() bool {
		return pidGone(gatewayPID) || (readRow(t, f.DBPath, launch.ID).State == store.StateWaiting && len(hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.turn_failed", launch.ID)) == 1)
	}, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
	if pidGone(gatewayPID) || !launch.rt.hasSession(t, launch.SessionID) || readRow(t, f.DBPath, launch.ID).State != store.StateWaiting {
		t.Fatalf("first prompt failure ended session: %+v", readRow(t, f.DBPath, launch.ID))
	}
	turns := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.turn_failed", launch.ID)
	if len(turns) != 1 || turns[0]["reason"] != reason {
		t.Fatalf("turn failure trail: %v; want reason %q", turns, reason)
	}
	if failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID); len(failures) != 0 {
		t.Fatalf("first prompt failure emitted fatal event: %v", failures)
	}
	assertCursorFailureViews(t, f, launch.ID, store.StateWaiting, reason)
}

func assertCursorFailureViews(t *testing.T, f *spawnFix, id, state, reason string) {
	t.Helper()
	got, err := f.API.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	many, err := f.API.GetMany([]string{id})
	if err != nil || len(many.Spawns) != 1 || len(many.NotFound) != 0 {
		t.Fatalf("get-many: %+v %v", many, err)
	}
	status, err := f.API.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]any{"get": got, "get-many": many.Spawns[0], "status": status} {
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view["state"] != state || view["failure_reason"] != reason || strings.Contains(string(raw), "SYNTHETIC") {
			t.Fatalf("%s = %s; want state %q and sanitized failure_reason %q", name, raw, state, reason)
		}
	}
}

func TestCursorStoppingOneLaunchPreservesOtherLaunchAndWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent real launches")
	}
	for _, outcome := range []string{"stop", "correlated rejection"} {
		t.Run(outcome, func(t *testing.T) { cursorConcurrentSurvivors(t, outcome) })
	}
}

type cursorOperationArtifact struct {
	Operation string             `json:"operation"`
	Input     string             `json:"input"`
	Result    string             `json:"result"`
	PID       int                `json:"pid"`
	CWD       string             `json:"cwd"`
	Selection map[string]*string `json:"selection"`
	Hook      *struct {
		Status int `json:"status"`
	} `json:"hook"`
}

func cursorConcurrentSurvivors(t *testing.T, outcome string) {
	selected := map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "data")}
	f, a, first := newCursorPaneScenario(t, false, "independent-worker", cursorPaneOptions{ExtraEnv: selected, Script: &cursorfix.Scenario{RecordArtifacts: true}})
	_, b, second := newCursorPaneScenario(t, false, "", cursorPaneOptions{Shared: f, ExtraEnv: selected, Relay: "on", Script: &cursorfix.Scenario{SessionID: "peer-b", RecordArtifacts: true}})
	distinct := map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "data")}
	_, c, third := newCursorPaneScenario(t, false, "", cursorPaneOptions{Shared: f, ExtraEnv: distinct, Script: &cursorfix.Scenario{SessionID: "peer-c", RecordArtifacts: true}})
	st, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runtimes := []cursorfix.Runtime{a, b, c}
	launches := []spawned{first, second, third}
	bindings := make([]store.CursorRuntime, 3)
	starts := make([]procStat, 3)
	for i, r := range runtimes {
		bindings[i], err = st.ReadCursorRuntime(launches[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(readFileTrim(t, r.PID))
		var ok bool
		starts[i], ok = readStat(pid)
		if !ok || starts[i].gone() || bindings[i].RuntimePID != pid || bindings[i].RuntimeStart != starts[i].start {
			t.Fatal("overlapping runtime absent")
		}
		for j := 0; j < i; j++ {
			if bindings[j].RuntimePID == pid || bindings[j].Connection == bindings[i].Connection || bindings[j].GatewayPID == bindings[i].GatewayPID || bindings[j].Token == bindings[i].Token {
				t.Fatal("launch identities not independent")
			}
		}
		want := selected
		if i == 2 {
			want = distinct
		}
		got := r.Selection(t)
		for key, value := range want {
			if got[key] == nil || *got[key] != value {
				t.Fatalf("received selection %v", got)
			}
		}
		if got["CURSOR_API_KEY"] != nil || got["AGENT_CLI_CREDENTIAL_STORE"] != nil {
			t.Fatal("credential selector inherited")
		}
		cursorUsefulTurn(t, f, r, launches[i].ID, fmt.Sprintf("before-%d", i), want)
	}
	worker, _ := strconv.Atoi(readFileTrim(t, a.WorkerPID))
	workerStart, ok := readStat(worker)
	if !ok || workerStart.gone() {
		t.Fatal("worker absent")
	}
	first.rt.trackPane(worker)
	cursorWorkerOperation(t, a, "worker-before", "independent before", nil, f.CWD, selected)
	// This real worker invokes the shipped hook with B's exact current claim.
	b.Configure(t, cursorfix.Scenario{RecordArtifacts: true, Requests: []json.RawMessage{relayFrame("foreign-held", "session/request_permission", cursorfix.Permission(bindings[1].SessionID, "foreign-held", "execute"))}, AfterAnswers: []json.RawMessage{json.RawMessage(`{"method":"session/update","params":{"sessionId":"peer-b","update":{"sessionUpdate":"tool_call_update","toolCallId":"foreign-held","status":"completed","rawOutput":{"exitCode":0,"stdout":"synthetic current result","stderr":""}}}}`)}})
	if _, err = f.API.SendKeys(api.SendKeysParams{InstanceID: second.ID, Text: "held-target"}); err != nil {
		t.Fatal(err)
	}
	requests := relayRequests(t, f, second.ID, 1)
	before := readRow(t, f.DBPath, second.ID)
	claim := map[string]any{"program": buildHookCLI(t, t.TempDir()), "args": []string{"hook", "--agent", "cursor"}, "env": map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": second.ID, "AGENT_DIRECTOR_AGENT": "cursor", "AGENT_DIRECTOR_CURSOR_STORE_PATH": f.DBPath}, "payload": map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": bindings[1].SessionID, "cursor_connection": bindings[1].Connection, "origin": "main", "transcript_path": before.JSONLPath}}
	cursorWorkerOperation(t, a, "foreign-report", "foreign claim", claim, f.CWD, selected)
	bound, err := st.ReadCursorRuntime(second.ID)
	afterRequests, requestErr := st.PermissionRequestsForSpawn(second.ID)
	if err != nil || requestErr != nil || !reflect.DeepEqual(requests, afterRequests) || bound != bindings[1] || readRow(t, f.DBPath, second.ID) != before {
		t.Fatal("foreign worker changed target authority")
	}
	if _, err = f.API.Decide(api.DecideParams{InstanceID: second.ID, RequestToken: requests[0].RequestToken, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "legitimate target continues", func() bool { return readRow(t, f.DBPath, second.ID).State == store.StateWaiting }, nil)
	b.Configure(t, cursorfix.Scenario{RecordArtifacts: true})
	if outcome == "stop" {
		if _, err = f.API.Pause(context.Background(), api.PauseParams{InstanceID: first.ID}); err != nil {
			t.Fatal(err)
		}
	} else {
		// b.ivj D1: one correlated rejection ends only its turn; the next is fatal.
		a.Configure(t, cursorfix.Scenario{PromptReply: json.RawMessage(`{"error":{"code":-32000,"message":"Authentication required"}}`)})
		if _, err = f.API.SendKeys(api.SendKeysParams{InstanceID: first.ID, Text: "affected-rejection"}); err != nil {
			t.Fatal(err)
		}
		assertCursorTurnFailure(t, f, first, "Cursor ACP session/prompt failed: authentication required; check the selected runtime's existing login")
		if _, err = f.API.SendKeys(api.SendKeysParams{InstanceID: first.ID, Text: "affected-second-rejection"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "affected gateway gone", func() bool { return pidGone(int(first.Row.PanePID.(int64))) }, nil)
	wantState := store.StateEnded
	if outcome != "stop" {
		wantState = store.StateMissing
	}
	if got := readRow(t, f.DBPath, first.ID); got.State != wantState {
		t.Fatalf("affected outcome %s; want %s", got.State, wantState)
	}
	for i := 1; i < 3; i++ {
		bound, err := st.ReadCursorRuntime(launches[i].ID)
		pid, _ := strconv.Atoi(readFileTrim(t, runtimes[i].PID))
		observed, ok := readStat(pid)
		if err != nil || bound != bindings[i] || !ok || observed.gone() || observed.start != starts[i].start {
			t.Fatal("peer authority or runtime changed")
		}
		want := selected
		if i == 2 {
			want = distinct
		}
		cursorUsefulTurn(t, f, runtimes[i], launches[i].ID, fmt.Sprintf("after-%d", i), want)
	}
	cursorWorkerOperation(t, a, "worker-after", "independent after", nil, f.CWD, selected)
	observed, ok := readStat(worker)
	if !ok || observed.gone() || observed.start != workerStart.start {
		t.Fatal("worker replaced")
	}
	for i, r := range runtimes {
		if i > 0 {
			if _, err = f.API.Pause(context.Background(), api.PauseParams{InstanceID: launches[i].ID}); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, "runtime ended", func() bool { return pidGone(bindings[i].RuntimePID) }, nil)
		methods := map[string]int{}
		for _, frame := range r.Frames(t) {
			var method string
			json.Unmarshal(frame["method"], &method)
			if method != "" {
				methods[method]++
			}
		}
		prompts := 2
		if i == 1 {
			prompts = 3
		}
		if i == 0 && outcome == "stop" {
			prompts = 1
		} else if i == 0 {
			prompts = 3
		}
		if !reflect.DeepEqual(methods, map[string]int{"initialize": 1, "session/new": 1, "session/prompt": prompts}) {
			t.Fatalf("unexpected retry/fallback frames: %v", methods)
		}
		// Shared-directory artifacts must coexist; none may appear in the other selection.
		intended, other := selected, distinct
		if i == 2 {
			intended, other = distinct, selected
		}
		for _, p := range panePrompts(t, r) {
			if p.Text == "affected-rejection" || p.Text == "affected-second-rejection" {
				continue
			}
			var operation any
			if err = json.Unmarshal([]byte(p.Request), &operation); err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("%d-%v.json", bindings[i].RuntimePID, operation)
			for _, dir := range intended {
				if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			for _, dir := range other {
				if _, err = os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatal("artifact crossed directory selection")
				}
			}
		}
	}
}

func cursorUsefulTurn(t *testing.T, f *spawnFix, r cursorfix.Runtime, id, text string, selected map[string]string) {
	t.Helper()
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: text}); err != nil {
		t.Fatal(err)
	}
	var prompt panePrompt
	waitFor(t, "correlated completed useful work", func() bool {
		for _, p := range panePrompts(t, r) {
			if p.Text == text {
				prompt = p
			}
		}
		if prompt.Request == "" || readRow(t, f.DBPath, id).State != store.StateWaiting {
			return false
		}
		var request any
		if json.Unmarshal([]byte(prompt.Request), &request) != nil {
			return false
		}
		completed := false
		for _, record := range paneRecords(t, readRow(t, f.DBPath, id).JSONLPath.(string)) {
			if record.Kind == "turn_outcome" && record.Data.Request == fmt.Sprint(request) && record.Session == prompt.Session && record.Instance == id {
				completed = true
			}
		}
		if !completed {
			return false
		}
		pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
		if err != nil || !strings.Contains(pane.Pane, "Synthetic reply: "+text) {
			return false
		}
		for _, frame := range r.SentFrames(t) {
			if string(frame["id"]) == prompt.Request && string(frame["result"]) == `{"stopReason":"end_turn"}` {
				return true
			}
		}
		return false
	}, nil)
	var operation any
	if err := json.Unmarshal([]byte(prompt.Request), &operation); err != nil {
		t.Fatal(err)
	}
	op := fmt.Sprint(operation)
	pid, _ := strconv.Atoi(readFileTrim(t, r.PID))
	for _, dir := range selected {
		raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d-%s.json", pid, op)))
		if err != nil {
			t.Fatal(err)
		}
		var got cursorOperationArtifact
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Input != text || got.Result != "Synthetic reply: "+text || got.PID != pid || got.CWD != f.CWD || !reflect.DeepEqual(got.Selection, r.Selection(t)) {
			t.Fatalf("misattributed useful result %+v", got)
		}
	}
}

func cursorWorkerOperation(t *testing.T, r cursorfix.Runtime, op, input string, hook map[string]any, cwd string, selected map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"operation": op, "input": input, "hook": hook})
	request := r.WorkerPID + ".request.json"
	if err := os.WriteFile(request+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(request+".tmp", request); err != nil {
		t.Fatal(err)
	}
	var got cursorOperationArtifact
	waitFor(t, "worker operation result", func() bool {
		raw, err := os.ReadFile(r.WorkerPID + ".result.json")
		return err == nil && json.Unmarshal(raw, &got) == nil && got.Operation == op
	}, nil)
	pid, _ := strconv.Atoi(readFileTrim(t, r.WorkerPID))
	if got.Input != input || got.Result != "Synthetic reply: "+input || got.PID != pid || got.CWD != cwd || got.Selection["CURSOR_API_KEY"] != nil || got.Selection["AGENT_CLI_CREDENTIAL_STORE"] != nil {
		t.Fatalf("worker result %+v", got)
	}
	if hook != nil && (got.Hook == nil || got.Hook.Status != 0) {
		t.Fatalf("real hook failed %+v", got)
	}
	for key, dir := range selected {
		if got.Selection[key] == nil || *got.Selection[key] != dir {
			t.Fatal("worker selection changed")
		}
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%d-%s.json", pid, op))); err != nil {
			t.Fatal(err)
		}
	}
}

// b.x9o: real ACP permission admission, actual gateway death, then a sweep
// without an earlier active read. Observational closeout must send no answer.
func TestFindMissingCursorPermissionWithoutDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("real gateway recovery")
	}
	const session = "sweep-permission"
	script := cursorfix.Scenario{SessionID: session, Requests: []json.RawMessage{relayFrame("sweep", "session/request_permission", cursorfix.Permission(session, "sweep-tool", "execute"))}}
	f, peer, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 30})
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "request permission"}); err != nil {
		t.Fatal(err)
	}
	request := relayRequests(t, f, launch.ID, 1)[0]
	st, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	binding, err := st.ReadCursorRuntime(launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), rawInput(t, peer)...)
	process, err := os.FindProcess(binding.GatewayPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "gateway process gone", func() bool { return pidGone(binding.GatewayPID) }, func() string { return procState(binding.GatewayPID) })
	if row := readRow(t, f.DBPath, launch.ID); row.State == store.StateMissing || row.State == store.StateEnded {
		t.Fatalf("precondition already recovered: %+v", row)
	}
	got, err := f.API.FindMissing(context.Background())
	if err != nil || got.Count != 1 || len(got.IDs) != 1 || got.IDs[0] != launch.ID {
		t.Fatalf("sweep=%+v %v", got, err)
	}
	after, err := st.GetPermissionRequest(launch.ID, request.RequestToken)
	if err != nil || !after.Closed() || !after.DeliveredAt.IsZero() || after.ProvenGone() {
		t.Fatalf("sweep fabricated answer/proof: %+v %v", after, err)
	}
	if row := readRow(t, f.DBPath, launch.ID); row.State != store.StateMissing {
		t.Fatalf("not durably missing: %+v", row)
	}
	// b.x9o: the exact gateway loss confirms missing (process) but is no
	// evidence about the runtime, so no process record is stored and
	// process_liveness stays unknown; the public read agrees with the store.
	got2, err := f.API.Get(launch.ID)
	if err != nil || got2.ProcessLiveness != store.ProcessUnknown || got2.Confirmations.ProcessLiveness.Status != store.ConfirmationUnknown ||
		got2.Confirmations.State.Source == nil || *got2.Confirmations.State.Source != store.EvidenceProcess {
		t.Fatalf("get after the sweep: %s %+v, state record %+v, %v; want unknown runtime, missing confirmed by process",
			got2.ProcessLiveness, got2.Confirmations.ProcessLiveness, got2.Confirmations.State, err)
	}
	if row, err := st.GetSpawn(launch.ID); err != nil || row.ProcessEvidence != "" || row.StateConfirmation(store.SettleRead{}).Source != store.EvidenceProcess {
		t.Fatalf("stored records %q, %+v, %v; want no process record and the missing value's process record", row.ProcessEvidence, row.StateConfirmation(store.SettleRead{}), err)
	}
	if !reflect.DeepEqual(before, rawInput(t, peer)) || len(relayReplies(t, peer)) != 0 {
		t.Fatal("observational sweep dispatched ACP bytes")
	}
}
