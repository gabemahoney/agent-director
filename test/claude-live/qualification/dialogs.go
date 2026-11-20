package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type DialogObservation struct {
	ID         string    `json:"id"`
	Key        DialogKey `json:"key"`
	Instance   string    `json:"instance"`
	Gate       string    `json:"gate"`
	Permission string    `json:"permission"`
	Before     string    `json:"before"`
	After      string    `json:"after"`
	Send       string    `json:"send"`
	Decision   string    `json:"decision,omitempty"`
}

func DialogSelection(cases []string) error {
	seen := map[string]bool{}
	for _, id := range cases {
		if !slices.Contains([]string{"D1", "D2", "D3", "D4", "D5", "D6"}, id) || seen[id] {
			return errors.New("invalid or duplicate Dialog case selection")
		}
		seen[id] = true
	}
	if seen["D2"] && !seen["D1"] || seen["D3"] && (!seen["D1"] || !seen["D2"]) {
		return errors.New("D2 requires D1; D3 requires D1 and D2 in the same invocation")
	}
	return nil
}

func gateFixture(r *runner.Run) map[string]any {
	limit := 60
	if r.Manifest.Phase == "text-limits-backlog" || r.Manifest.Phase == "groups" {
		limit = 600
	}
	return map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": filepath.Join(r.Kit, "bin/claude-live"), "args": []string{"qualification-gate", "--evidence", r.Evidence.Root, "--run-id", r.Manifest.RunID}, "timeout": limit}}}}}}
}

// GateHook is a separate, deliberate scheduling fixture, never a replacement
// for the silent recorder or AD's hooks. It pauses before a harmless owned
// Bash request, allowing both sides of the about-to-open boundary to be kept.
func GateHook(root, run string, input io.Reader) error {
	if run == "" || !strings.HasPrefix(os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"), "live-"+run+"-") {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return err
	}
	var p runner.Row
	if json.Unmarshal(data, &p) != nil || runner.Text(p, "tool_name") != "Bash" {
		return nil
	}
	path, err := runner.Confined(root, "raw/gates")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(path, "invocation-")
	if err != nil {
		return err
	}
	start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
	if !alive || !known {
		return errors.New("cannot establish scheduling driver process identity")
	}
	driver := gateProcess{PID: os.Getpid(), Start: start, Run: run, Instance: os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"), Session: runner.Text(p, "session_id"), CWD: runner.Text(p, "cwd"), InputSHA: runner.Digest(data)}
	b, err := json.Marshal(driver)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "driver.json"), b, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "input.json"), data, 0600); err != nil {
		return err
	}
	limit := 50 * time.Second
	if strings.HasSuffix(os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"), "-N8") || strings.HasSuffix(os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"), "-G0") {
		limit = 600 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	for {
		if _, err = os.Stat(filepath.Join(dir, "release")); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = runner.Sleep(ctx, 20*time.Millisecond); err != nil {
			return err
		}
	}
}

func dialogTarget(work, origin string) string { return filepath.Join(work, "qd-"+origin+".txt") }
func dialogAction(origin, target string) string {
	command := "printf qualification > " + ShellQuote(target)
	switch origin {
	case "main":
		return "Run foreground Bash exactly: " + command + "."
	case "subagent":
		return "Use the Task/Agent tool for a foreground subagent. Its only job is Bash exactly: " + command + ". Wait for it."
	default:
		return "Use the Task/Agent tool with run_in_background=true for a background subagent. Its only job is Bash exactly: " + command + ". Wait for its completion without sending another user prompt."
	}
}

func (e *Exercise) awaitGate(ctx context.Context, iid, target string) (string, error) {
	var ref string
	var originErr error
	_, err := e.awaitProgress(ctx, iid, 75*time.Second, true, func(s runner.Snapshot) bool {
		base := filepath.Join(e.Run.Evidence.Root, "raw/gates")
		entries, er := os.ReadDir(base)
		if er != nil {
			return false
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := "raw/gates/" + entry.Name()
			if _, er := runner.Read(e.Run.Evidence.Root, name+"/release"); er == nil {
				continue
			}
			b, er := runner.Read(e.Run.Evidence.Root, name+"/input.json")
			if er != nil {
				continue
			}
			var p runner.Row
			if json.Unmarshal(b, &p) != nil {
				continue
			}
			input, _ := p["tool_input"].(map[string]any)
			command, _ := input["command"].(string)
			if strings.Contains(command, target) {
				owner, er := e.owner(iid)
				if er != nil {
					originErr = er
					return true
				}
				if runner.Text(p, "session_id") != owner.SessionID || runner.Text(p, "tool_name") != "Bash" {
					continue
				}
				origin := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(target), "qd-"), ".txt")
				agent := runner.Text(p, "agent_id")
				if (origin == "main") != (agent == "") {
					originErr = e.workload("different-origin", iid, "", "requested Dialog origin differs from actual gated tool; zero probes sent", name+"/input.json")
					return true
				}
				if origin != "main" {
					data, er := e.Run.ReadTranscript(owner)
					if er != nil {
						return false
					}
					tr, er := signals.ParseTranscript(data, owner.SessionID)
					if er != nil {
						originErr = er
						return true
					}
					if !dialogChildOrigin(OwnerEvidence{Transcript: tr}, origin, target) {
						return false
					}
				}
				ref = name
				return true
			}
		}
		return false
	})
	if originErr != nil {
		return ref, originErr
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return ref, e.workload("different-origin", iid, "", "requested Dialog origin was not established; no probe input sent")
	}
	return ref, err
}

func (e *Exercise) permission(ctx context.Context, iid, target string, relay bool) (api.PermissionRequestInfo, string, error) {
	var request api.PermissionRequestInfo
	var ref string
	_, err := e.awaitProgress(ctx, iid, 60*time.Second, true, func(s runner.Snapshot) bool {
		facts, er := HookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events)
		if er != nil {
			return false
		}
		owner, ownerErr := e.owner(iid)
		if ownerErr != nil {
			return false
		}
		for _, h := range facts {
			if h.Receipt.Event != "PermissionRequest" || runner.Text(h.Payload, "tool_name") != "Bash" || runner.Text(h.Payload, "session_id") != owner.SessionID {
				continue
			}
			b, er := json.Marshal(h.Payload)
			if er == nil && strings.Contains(string(b), target) {
				ref = h.Reference
			}
		}
		if ref == "" {
			return false
		}
		if !relay {
			return true
		}
		_, b, er := e.Run.AD(ctx, "dialog-get", "get", "--instance-id", iid)
		if er != nil {
			return false
		}
		var row api.SpawnRow
		if json.Unmarshal(b, &row) != nil {
			return false
		}
		for _, pr := range row.PermissionRequests {
			if pr.Decision == nil && pr.ToolName == "Bash" && strings.Contains(pr.ToolInput, target) {
				request = pr
				return true
			}
		}
		return false
	})
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return request, ref, e.workload("different-origin", iid, "", "requested permission Dialog was not presented; later probes remain incomplete")
	}
	return request, ref, err
}

const dialogProbeText = "Qualification Dialog probe. Keep following the original harmless actions; do not start any new tool or permission operation for this text."

func (e *Exercise) dialogVariants(ctx context.Context, iid, origin, state, gate, permission string, relay bool) ([]DialogObservation, error) {
	var out []DialogObservation
	if err := e.capability(ctx, iid); err != nil {
		return out, err
	}
	before, err := e.observe(ctx, iid, "dialog-"+origin+"-"+state+"-before")
	if err != nil {
		return out, err
	}
	beforeRef := e.Observations[len(e.Observations)-1].Snapshot
	_, _, err = e.Run.AD(ctx, "dialog-pane-before", "read-pane", "--instance-id", iid, "--n-lines", "100")
	if err != nil {
		return out, err
	}
	_ = before
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		for _, confirmed := range []bool{false, true} {
			key := DialogKey{mode, confirmed, relay, origin, state}
			id := fmt.Sprintf("dialog-%04d", len(e.Run.Manifest.Cases))
			if err = e.send(ctx, iid, id, dialogProbeText, mode, confirmed, false); err != nil {
				return out, err
			}
			if err = e.checkFallback(ctx, iid); err != nil {
				return out, err
			}
			out = append(out, DialogObservation{ID: id, Key: key, Instance: iid, Gate: gate, Permission: permission, Before: beforeRef, Send: e.Observations[len(e.Observations)-1].Command})
		}
	}
	if _, err = e.observe(ctx, iid, "dialog-"+origin+"-"+state+"-after"); err != nil {
		return out, err
	}
	afterRef := e.Observations[len(e.Observations)-1].Snapshot
	if state == "open" {
		id := fmt.Sprintf("dialog-pause-%s-%s", iid, origin)
		if err = e.conditionalPauseProbe(ctx, iid, id); err != nil {
			return out, err
		}
	}

	_, _, err = e.Run.AD(ctx, "dialog-pane-after", "read-pane", "--instance-id", iid, "--n-lines", "100")
	if err != nil {
		return out, err
	}
	for i := range out {
		out[i].After = afterRef
	}
	return out, nil
}

func Dialogs(ctx context.Context, e *Exercise) (err error) {
	l, err := LoadLedger(e.Ledger)
	if err != nil {
		return err
	}
	selection := l.DialogCases
	if len(selection) == 0 {
		selection = []string{"D1", "D2", "D3", "D4", "D5", "D6"}
	}
	if err = DialogSelection(selection); err != nil {
		return err
	}
	var observations []DialogObservation
	defer func() { err = errors.Join(err, e.Run.Evidence.JSON("raw/dialogs.json", observations)) }()
	if slices.Contains(selection, "D1") {
		work := filepath.Join(e.Run.Evidence.Root, "work-D1")
		prompt := "Perform these three harmless permission-needing requests in order, waiting for each permission decision and result before the next: "
		for _, origin := range []string{"main", "subagent", "background"} {
			prompt += dialogAction(origin, dialogTarget(work, origin)) + " "
		}
		prompt += "After all three, finish in five words."
		iid, er := e.spawnConfigured(ctx, "D1", prompt, LaunchOptions{Allowed: "Task,Agent,EnterPlanMode", Permission: "default", Fixture: gateFixture(e.Run)})
		if er != nil {
			return er
		}
		if !slices.Contains(selection, "D2") {
			return e.workload("precondition-not-established", iid, "D1", "Dialog probe fallback needs its mutually exclusive D2 slot; D1-only selection adds no unreserved input")
		}
		var probeBodies [][]byte
		for i := 0; i < 24; i++ {
			probeBodies = append(probeBodies, []byte(dialogProbeText))
		}
		limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendWaitTurn)
		parts, er := store.PartitionBusySendPrompts(probeBodies, limits, nil)
		if er != nil {
			return er
		}
		if len(parts) != 1 {
			return e.workload("precondition-not-established", iid, "D1", "Dialog probes exceed the one fallback slot; zero probes sent")
		}
		if er = e.reserveSlot("D2", dialogAlternatives()...); er != nil {
			return er
		}
		for _, origin := range []string{"main", "subagent", "background"} {
			target := dialogTarget(work, origin)
			gate, er := e.awaitGate(ctx, iid, target)
			if er != nil {
				return fmt.Errorf("%s Dialog origin unavailable: %w", origin, er)
			}
			items, er := e.dialogVariants(ctx, iid, origin, "about_to_open", gate, "", true)
			if er != nil {
				return er
			}
			observations = append(observations, items...)
			if er = e.Run.Evidence.Put(gate+"/release", []byte("release\n")); er != nil {
				return er
			}
			pr, ref, er := e.permission(ctx, iid, target, true)
			if er != nil {
				return er
			}
			items, er = e.dialogVariants(ctx, iid, origin, "open", gate, ref, true)
			if er != nil {
				return er
			}
			observations = append(observations, items...)
			if origin == "background" && slices.Contains(selection, "D2") {
				if er = e.send(ctx, iid, "D2", "D2: After the three original permission actions finish, use EnterPlanMode if needed, write one sentence planning a harmless file in your own working directory, then call ExitPlanMode to present it. Do not execute that plan.", "wait_turn", true, true); er != nil {
					return er
				}
			}
			if _, _, er = e.Run.AD(ctx, "dialog-decide", "decide", "--instance-id", iid, "--request-token", pr.RequestToken, "--decision", "allow"); er != nil {
				return er
			}
		}
		if slices.Contains(selection, "D2") {
			if er = e.waitSighting(ctx, iid, "D2: After the three original permission actions finish, use EnterPlanMode if needed, write one sentence planning a harmless file in your own working directory, then call ExitPlanMode to present it. Do not execute that plan."); er != nil {
				return er
			}
			var plan api.PermissionRequestInfo
			_, er = e.awaitProgress(ctx, iid, 75*time.Second, true, func(s runner.Snapshot) bool {
				_, b, x := e.Run.AD(ctx, "plan-get", "get", "--instance-id", iid)
				if x != nil {
					return false
				}
				var row api.SpawnRow
				if json.Unmarshal(b, &row) != nil {
					return false
				}
				for _, pr := range row.PermissionRequests {
					if pr.Decision != nil {
						continue
					}
					if pr.ToolName == "EnterPlanMode" {
						_, _, x = e.Run.AD(ctx, "enter-plan-decide", "decide", "--instance-id", iid, "--request-token", pr.RequestToken, "--decision", "allow")
						return false
					}
					if pr.ToolName == "ExitPlanMode" {
						plan = pr
						return true
					}
				}
				return false
			})
			if errors.Is(er, context.DeadlineExceeded) && ctx.Err() == nil {
				return e.workload("plan-not-present", iid, "D2", "requested plan was not presented; no D3 input sent")
			}
			if er != nil {
				return er
			}
			if slices.Contains(selection, "D3") {
				if er = e.reserve("D3"); er != nil {
					return er
				}
				if er = e.send(ctx, iid, "D3", "D3: Acknowledge this plan-Dialog probe in five words and finish. No more tools.", "wait_turn", true, true); er != nil {
					return er
				}
			}
			if _, _, er = e.Run.AD(ctx, "plan-decide", "decide", "--instance-id", iid, "--request-token", plan.RequestToken, "--decision", "deny"); er != nil {
				return er
			}
			if slices.Contains(selection, "D3") {
				if er = e.waitSighting(ctx, iid, "D3: Acknowledge this plan-Dialog probe in five words and finish. No more tools."); er != nil {
					return er
				}
			}
		}
		if er = e.stop(ctx, iid, "relay-on-end", false); er != nil {
			return er
		}
	}
	for i, origin := range []string{"main", "subagent", "background"} {
		id := fmt.Sprintf("D%d", i+4)
		if !slices.Contains(selection, id) {
			continue
		}
		work := filepath.Join(e.Run.Evidence.Root, "work-"+id)
		target := dialogTarget(work, origin)
		iid, er := e.spawnConfigured(ctx, id, dialogAction(origin, target)+" Then finish in five words.", LaunchOptions{Relay: "off", Allowed: "Task,Agent", Permission: "default", Fixture: gateFixture(e.Run)})
		if er != nil {
			return er
		}
		gate, er := e.awaitGate(ctx, iid, target)
		if er != nil {
			return fmt.Errorf("relay-off %s origin unavailable: %w", origin, er)
		}
		items, er := e.dialogVariants(ctx, iid, origin, "about_to_open", gate, "", false)
		if er != nil {
			return er
		}
		observations = append(observations, items...)
		if er = e.Run.Evidence.Put(gate+"/release", []byte("release\n")); er != nil {
			return er
		}
		_, ref, er := e.permission(ctx, iid, target, false)
		if er != nil {
			return er
		}
		items, er = e.dialogVariants(ctx, iid, origin, "open", gate, ref, false)
		if er != nil {
			return er
		}
		observations = append(observations, items...)
		if er = e.stop(ctx, iid, "relay-off-end-"+id, false); er != nil {
			return er
		}
	}
	return nil
}
