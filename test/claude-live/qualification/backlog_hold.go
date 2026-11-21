package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// backlogHold validates the independent PreToolUse scheduling fixture against
// the exact passive parent invocation. A model's promise to wait is not a hold.
func (e *Exercise) backlogHold(ctx context.Context, iid, command string) error {
	var problem error
	_, err := e.Run.AwaitActivityChecked(ctx, iid, 75*time.Second, false, func(ctx context.Context, _ runner.Snapshot) error {
		return e.checkProviderProgress(ctx, iid)
	}, func(s runner.Snapshot) bool {
		if len(s.Tables["prompts"]) != 0 {
			problem = e.workload("backlog-not-exercised", iid, "N8", "backlog unexpectedly issued during harness hold; zero further traffic")
			return true
		}
		o, er := e.owner(iid)
		if er != nil {
			problem = er
			return true
		}
		facts, er := LiveHookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events)
		if er != nil {
			problem = er
			return true
		}
		var call HookFact
		for _, h := range facts {
			if runner.Text(h.Payload, "session_id") != o.SessionID || runner.Text(h.Payload, "agent_id") != "" {
				continue
			}
			if h.Receipt.Event == "Stop" {
				problem = e.workload("backlog-not-exercised", iid, "N8", "owned parent stopped before/during harness hold; no further backlog traffic", h.Reference)
				return true
			}
			if h.Receipt.Event == "PreToolUse" {
				var v struct {
					Input struct {
						Command    string `json:"command"`
						Background bool   `json:"run_in_background"`
					} `json:"tool_input"`
				}
				b, _ := json.Marshal(h.Payload)
				_ = json.Unmarshal(b, &v)
				if call.Reference != "" || v.Input.Command != command || v.Input.Background || runner.Text(h.Payload, "tool_name") != "Bash" {
					problem = e.workload("backlog-not-exercised", iid, "N8", "missing/wrong/background foreground hold; zero further sends", h.Reference)
					return true
				}
				call = h
			}
			if h.Receipt.Event == "PostToolBatch" {
				problem = e.workload("backlog-not-exercised", iid, "N8", "held invocation returned before backlog checks completed", h.Reference)
				return true
			}
		}
		if call.Reference == "" {
			return false
		}
		entries, er := os.ReadDir(filepath.Join(e.Run.Evidence.Root, "raw/gates"))
		if er != nil {
			return false
		}
		for _, entry := range entries {
			ref := "raw/gates/" + entry.Name()
			if !entry.IsDir() {
				continue
			}
			if _, er = runner.Read(e.Run.Evidence.Root, ref+"/release"); er == nil {
				continue
			}
			b, er := runner.Read(e.Run.Evidence.Root, ref+"/input.json")
			if er != nil {
				continue
			}
			var held runner.Row
			if json.Unmarshal(b, &held) != nil {
				continue
			}
			want, _ := json.Marshal(call.Payload)
			got, _ := json.Marshal(held)
			if string(want) == string(got) && runner.Text(held, "tool_use_id") != "" {
				raw, er := runner.Read(e.Run.Evidence.Root, ref+"/driver.json")
				if os.IsNotExist(er) {
					continue
				}
				if er != nil {
					problem = er
					return true
				}
				var driver gateProcess
				if er = json.Unmarshal(raw, &driver); er != nil {
					problem = er
					return true
				}
				if driver.PID <= 1 || driver.PID == o.PID || driver.PID == os.Getpid() || driver.Start == "" || driver.Run != e.Run.Manifest.RunID || driver.Instance != iid || driver.Session != o.SessionID || driver.CWD != o.CWD || driver.InputSHA != runner.Digest(b) {
					problem = e.workload("backlog-not-exercised", iid, "N8", "harness hold driver identity does not bind this invocation", ref+"/driver.json")
					return true
				}
				gone, er := runner.ProcessGone(driver.PID, driver.Start)
				if er != nil {
					problem = er
					return true
				}
				if gone {
					problem = e.workload("backlog-not-exercised", iid, "N8", "harness hold driver exited; zero further traffic", ref+"/driver.json")
					return true
				}
				now := e.Run.Now().UTC()
				obs := Observation{Case: "N8-hold-first", Instance: iid, Kind: "backlog-hold", Trigger: ref, State: fmt.Sprintf("%d@%s", driver.PID, driver.Start), Started: now, Ended: now}
				last := -1
				for i, x := range e.Observations {
					if x.Case == "N8-hold-last" {
						last = i
					}
				}
				if last < 0 {
					e.Observations = append(e.Observations, obs)
					obs.Case = "N8-hold-last"
					e.Observations = append(e.Observations, obs)
				} else {
					obs.Case = "N8-hold-last"
					e.Observations[last] = obs
				}
				return true
			}
		}
		return false
	})
	if problem != nil {
		return problem
	}
	if err != nil {
		if strings.Contains(err.Error(), "unexpected dialog") {
			return e.workload("precondition-not-established", iid, "", "parent entered an unrelated question/Dialog; no answer or further workload input sent")
		}
		if ctx.Err() != nil {
			return err
		}
		return e.workload("backlog-not-exercised", iid, "N8", "hold not established or lost; zero further sends; "+err.Error())
	}
	return nil
}

// Gate processes identify themselves before publishing their request. Cleanup
// stops only these exact driver identities after every provider owner exited.
type gateProcess struct {
	PID      int    `json:"pid"`
	Start    string `json:"process_start"`
	Run      string `json:"run"`
	Instance string `json:"instance"`
	Session  string `json:"session"`
	CWD      string `json:"cwd"`
	InputSHA string `json:"input_sha256"`
}

func (e *Exercise) finishGateProcesses(ctx context.Context) error {
	root := filepath.Join(e.Run.Evidence.Root, "raw/gates")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ref := "raw/gates/" + entry.Name()
		b, er := runner.Read(e.Run.Evidence.Root, ref+"/driver.json")
		if os.IsNotExist(er) {
			continue
		}
		if er != nil {
			return er
		}
		var driver gateProcess
		if er = json.Unmarshal(b, &driver); er != nil {
			return er
		}
		input, er := runner.Read(e.Run.Evidence.Root, ref+"/input.json")
		if er != nil {
			return er
		}
		owned := false
		for _, o := range e.Run.Manifest.Owned {
			if driver.Run == e.Run.Manifest.RunID && driver.Instance == o.InstanceID && driver.Session == o.SessionID && driver.CWD == o.CWD && driver.InputSHA == runner.Digest(input) && driver.PID != o.PID && driver.PID != os.Getpid() && o.Exited {
				owned = true
			}
		}
		if !owned {
			return errors.New("gate process has no exact exited owner; retained for inspection")
		}
		if driver.PID <= 1 || driver.Start == "" {
			return errors.New("invalid exact gate process identity")
		}
		p, er := os.FindProcess(driver.PID)
		if er != nil {
			return er
		}
		gone, er := runner.ProcessGone(driver.PID, driver.Start)
		if er == nil && !gone {
			er = p.Kill()
		}
		_ = p.Release()
		if er != nil {
			return er
		}
		if er = e.Run.AwaitEvidence(ctx, 5*time.Second, func(context.Context) (bool, error) { return runner.ProcessGone(driver.PID, driver.Start) }); er != nil {
			return er
		}
	}
	return nil
}

// The sealed first/last observations attest the same live driver across every
// admission and refusal. A receipt left by a dead callback is insufficient.
func checkBacklogHold(c Capture, proof BacklogProof) runner.Result {
	first, a := c.observation("N8-hold-first", "backlog-hold")
	last, b := c.observation("N8-hold-last", "backlog-hold")
	if !a || !b || len(proof.Accepted) == 0 {
		return result("backlog-hold", "not_completed", "harness-owned live hold evidence missing", nil, nil)
	}
	if first.Instance != last.Instance || first.Trigger != last.Trigger || first.State != last.State {
		return result("backlog-hold", "fail", "hold identity changed during admission", first, last)
	}
	var driver gateProcess
	if err := c.json(first.Trigger+"/driver.json", &driver); err != nil {
		return errorResult("backlog-hold", err)
	}
	input, err := c.read(first.Trigger + "/input.json")
	if err != nil {
		return errorResult("backlog-hold", err)
	}
	owner, ok := c.ownerSession(driver.Session)
	if !ok || driver.PID <= 1 || driver.Start == "" || driver.Run != c.Manifest.RunID || driver.Instance != first.Instance || driver.Instance != owner.Owner.InstanceID || driver.CWD != owner.Owner.CWD || driver.InputSHA != runner.Digest(input) || first.State != fmt.Sprintf("%d@%s", driver.PID, driver.Start) || !owner.Owner.Exited || owner.Owner.StoppedAt.Before(last.Ended) {
		return result("backlog-hold", "fail", "hold driver is not bound to the exited owned parent", nil, driver)
	}
	end := proof.Accepted[len(proof.Accepted)-1].Ended
	for _, id := range []string{"backlog-over-text", "backlog-over-count"} {
		cmd, ok := c.commandNamed(id)
		if !ok {
			return result("backlog-hold", "not_completed", "refusal hold interval is incomplete", id, nil)
		}
		if cmd.Ended.After(end) {
			end = cmd.Ended
		}
	}
	if first.Ended.After(proof.Accepted[0].Started) || last.Started.Before(end) {
		return result("backlog-hold", "fail", "live hold does not span admission and refusal checks", nil, []Observation{first, last})
	}
	return result("backlog-hold", "pass", "same exact live harness driver spans all admission/refusal checks and parent exited before release", nil, driver, first.Trigger+"/driver.json", first.Trigger+"/input.json")
}
