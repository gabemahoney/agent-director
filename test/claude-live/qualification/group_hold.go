package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// groupHold uses the same independent gate driver as N8, but permits the
// earlier completed G0 boundaries. A shell command alone is not a live hold.
func (e *Exercise) groupHold(ctx context.Context, iid string, ordinal int, establish bool) (string, error) {
	var gate string
	var problem error
	_, err := e.awaitProgress(ctx, iid, 75*time.Second, false, func(s runner.Snapshot) bool {
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
		for _, h := range facts {
			if runner.Text(h.Payload, "session_id") == o.SessionID && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(o.Started) && h.Receipt.Event == "Stop" {
				problem = e.workload("precondition-not-established", iid, "G0", "group admission hold lost at parent Stop; no further input", h.Reference)
				return true
			}
		}
		ref, er := e.selectToolWindow(o, facts, ordinal, false)
		if er != nil {
			problem = er
			return true
		}
		for _, call := range facts {
			if ref == "" || call.Reference != ref {
				continue
			}
			input, _ := call.Payload["tool_input"].(map[string]any)
			if runner.Number(input, "timeout") < runner.PhaseLimit.Milliseconds() {
				problem = e.workload("precondition-not-established", iid, "G0", "group fixture timeout does not cover the bounded phase; no further input", ref)
				return true
			}
			entries, er := os.ReadDir(filepath.Join(e.Run.Evidence.Root, "raw/gates"))
			if er != nil && !os.IsNotExist(er) {
				problem = er
				return true
			}
			want, _ := json.Marshal(call.Payload)
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				name := "raw/gates/" + entry.Name()
				if _, er := runner.Read(e.Run.Evidence.Root, name+"/release"); er == nil {
					continue
				} else if !os.IsNotExist(er) {
					problem = er
					return true
				}
				b, er := runner.Read(e.Run.Evidence.Root, name+"/input.json")
				var held runner.Row
				if er != nil || json.Unmarshal(b, &held) != nil {
					continue
				}
				got, _ := json.Marshal(held)
				if string(got) != string(want) || runner.Text(held, "tool_use_id") == "" {
					continue
				}
				raw, er := runner.Read(e.Run.Evidence.Root, name+"/driver.json")
				if os.IsNotExist(er) {
					continue
				}
				var driver gateProcess
				if er != nil {
					problem = er
					return true
				}
				if er = json.Unmarshal(raw, &driver); er != nil {
					problem = er
					return true
				}
				if driver.PID <= 1 || driver.PID == o.PID || driver.PID == os.Getpid() || driver.Start == "" || driver.Run != e.Run.Manifest.RunID || driver.Instance != iid || driver.Session != o.SessionID || driver.CWD != o.CWD || driver.InputSHA != runner.Digest(b) {
					problem = e.workload("precondition-not-established", iid, "G0", "group hold driver identity does not bind the selected invocation", ref, name+"/driver.json")
					return true
				}
				gone, er := runner.ProcessGone(driver.PID, driver.Start)
				if er != nil {
					problem = er
					return true
				}
				if gone {
					problem = e.workload("precondition-not-established", iid, "G0", "group hold driver exited; no further input", ref, name+"/driver.json")
					return true
				}
				if gate != "" {
					problem = errors.New("duplicate live driver for the selected group invocation")
					return true
				}
				gate = name
			}
		}
		if gate == "" && !establish {
			problem = e.workload("precondition-not-established", iid, "G0", "selected group admission hold is no longer established; no further input", ref)
			return true
		}
		return gate != ""
	})
	if problem != nil {
		return "", problem
	}
	return gate, e.workloadTimeout(err, iid, "G0", "precondition-not-established")
}

func (e *Exercise) admitGroup(ctx context.Context, iid string, plan GroupPlan, ordinal int, gate string) (err error) {
	at := len(e.Observations)
	e.Observations = append(e.Observations, Observation{Case: "G0", Instance: iid, Kind: "group-admission", Mode: plan.Mode, State: "partial", Trigger: gate, Started: e.Run.Now().UTC()})
	defer func() { e.Observations[at].Ended = e.Run.Now().UTC() }()
	check := func() error {
		current, er := e.groupHold(ctx, iid, ordinal, false)
		if er == nil && current != gate {
			return e.workload("precondition-not-established", iid, "G0", "group admission driver changed; no further input", gate+"/input.json")
		}
		return er
	}
	for i, text := range plan.Texts {
		if err = check(); err != nil {
			return err
		}
		if err = e.send(ctx, iid, plan.Cases[i], text, plan.Mode, i%2 == 0 || strings.HasPrefix(plan.Cases[i], "O"), false); err != nil {
			return err
		}
		if err = check(); err != nil {
			return err
		}
	}
	label := "group-all-accepted-before-boundary"
	if plan.Mode == "wait_turn" {
		label = "group-ordinary-all-accepted-before-boundary"
	}
	s, err := e.observe(ctx, iid, label)
	if err != nil {
		return err
	}
	for _, text := range plan.Texts {
		held := 0
		for _, m := range s.Tables["messages"] {
			if runner.Text(m, "text") == text && runner.Text(m, "place") == "held" {
				held++
			}
		}
		if held != 1 {
			return e.workload("precondition-not-established", iid, "G0", "complete group partition was not retained Held before release; no further input", gate+"/input.json")
		}
	}
	if err = check(); err != nil {
		return err
	}
	e.Observations[at].State = "complete"
	return nil
}

func groupPartial(c Capture, mode string) bool {
	for _, o := range c.Observations {
		if o.Kind == "group-admission" && o.Mode == mode && o.State == "partial" && phaseWorkload(c) {
			return true
		}
	}
	return false
}

func plannedGroupBody(mode, id string) (string, bool) {
	plan, err := PlanGroup(mode)
	if err != nil {
		return "", false
	}
	for _, part := range plan.Parts {
		if plan.Cases[part.First] == id {
			var texts [][]byte
			for _, text := range plan.Texts[part.First : part.First+part.Count] {
				texts = append(texts, []byte(text))
			}
			return string(store.RenderBusySendPromptBody(texts)), true
		}
	}
	return "", false
}
