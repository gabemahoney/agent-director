package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// budgetVariant is the SessionEnd budget a sampled case's agents run under.
type budgetVariant int

const (
	// budgetDefault: Claude Code's own default SessionEnd budget; the
	// harness generates no layer and sets no budget variable.
	budgetDefault budgetVariant = iota
	// budgetRaisedHook: a generated layer whose slow SessionEnd hook
	// carries a raised per-hook `timeout`.
	budgetRaisedHook
	// budgetRaisedEnv: the same slow hook with no `timeout`, and
	// CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS raised in the agent's
	// environment (spawn --extra-env).
	budgetRaisedEnv
)

// maxSetupFailures is how many samples in a row may fail before their
// measured action (spawn, report-in, identification) before the run is
// aborted: the setup is then broken for every sample, and each failed
// sample can leave a real agent running, so the run stops instead of
// starting the rest (the 2x-estimate stop rule).
const maxSetupFailures = 3

// launch is how one sampled agent starts.
type launch struct {
	variant budgetVariant
	// midTurn agents get the mid-turn prompt as their initial prompt and
	// are measured while working.
	midTurn bool
	// mcp agents get the MCP configuration as --mcp-config.
	mcp bool
}

// measureFunc performs one sample's measured action on a ready agent and
// fills s with its outcome.
type measureFunc func(h *harness, caseID string, a agentRef, s *sample)

// registerSampled registers one RN-6 or RN-2 case: cfg.samples agents,
// each launched as l and measured by measure. The case is sampled (the
// 20-sample floor applies in real mode) and runs in real and dry mode.
func registerSampled(id string, fam family, title, trigger string, l launch, measure measureFunc) {
	spec := caseSpec{id: id, family: fam, title: title, sampled: true,
		modes: []mode{modeReal, modeDry}, generated: l.variant != budgetDefault}
	spec.run = func(h *harness) (caseResult, error) {
		return h.runSampled(spec, l, trigger, measure)
	}
	registerCase(spec)
}

// runSampled runs one sampled case: for each sample a fresh working
// directory and agent, the wait for it to be ready, its budgets (from the
// first agent that got that far) and the measured action. A missing MCP
// configuration in real mode makes the case a "not run" row. Setup
// failures are counted in the result; maxSetupFailures in a row abort the
// run, as does an isolation refusal.
func (h *harness) runSampled(spec caseSpec, l launch, trigger string, measure measureFunc) (caseResult, error) {
	res := newCaseResult(spec)
	res.Trigger = trigger
	mcpPath := ""
	if l.mcp {
		path, notRun, err := h.mcpForCases()
		if err != nil {
			return res, err
		}
		if notRun != "" {
			res.NotRun = notRun
			return res, nil
		}
		mcpPath = path
		if res.MCPServers, err = mcpServerNames(path); err != nil {
			return res, err
		}
		notes, err := mcpMissingCommands(path, h.env.getenv("PATH"))
		if err != nil {
			return res, err
		}
		res.Notes = append(res.Notes, notes...)
	}
	haveBudgets, setupFailures := false, 0
	for i := 0; i < h.cfg.samples; i++ {
		a, br, notes, failed, err := h.startSample(spec.id, i, l, mcpPath)
		if err != nil {
			return res, err
		}
		if !haveBudgets && br != nil {
			res.Budgets, haveBudgets = *br, true
			res.Notes = append(res.Notes, notes...)
		}
		if failed != nil {
			res.Samples = append(res.Samples, *failed)
			if setupFailures++; setupFailures >= maxSetupFailures {
				return res, fmt.Errorf("%d samples in a row of case %s failed before their measured action (last: %s); the run stops rather than start more agents",
					setupFailures, spec.id, failed.Reason)
			}
			continue
		}
		setupFailures = 0
		s := sample{Case: spec.id, Index: i, InstanceID: a.InstanceID}
		measure(h, spec.id, a, &s)
		res.Samples = append(res.Samples, s)
	}
	return res, nil
}

// startSample launches sample i's agent and waits until it is ready for
// its measured action: reported in (waiting), or working for a mid-turn
// agent. It returns the identified agent, its budgets and the hook-program
// notes; or, when the sample cannot go on, failed holds it (spawn failed,
// did_not_reach_prompt, not identified). A returned error aborts the run:
// an isolation refusal, or a working directory that cannot be made.
func (h *harness) startSample(caseID string, i int, l launch, mcpPath string) (a agentRef, br *budgetReport, notes []string, failed *sample, err error) {
	s := sample{Case: caseID, Index: i}
	gen := h.raisedLayer(l.variant)
	dir, err := h.prepareAgentDir(caseID, i, gen)
	if err != nil {
		return a, nil, nil, nil, err
	}
	env := h.raisedEnv(l.variant)
	var args []string
	if l.midTurn {
		args = append(args, h.cfg.midTurnPrompt)
	}
	if mcpPath != "" {
		// After the prompt: --mcp-config takes one or more values, so
		// nothing positional may follow it.
		args = append(args, "--mcp-config", mcpPath)
	}
	a, err = h.spawnAgent(caseID, spawnSpec{CWD: dir, ClaudeArgs: args, ExtraEnv: env})
	s.InstanceID = a.InstanceID
	if err != nil {
		var r *refusal
		if errors.As(err, &r) {
			return a, nil, nil, nil, err
		}
		s.Outcome, s.Reason = outcomeFailed, "spawn: "+err.Error()
		return a, nil, nil, &s, nil
	}
	want := stateWaiting
	if l.midTurn {
		want = stateWorking
	}
	if _, err := h.waitState(caseID, a.InstanceID, want); err != nil {
		s.Outcome, s.Reason = outcomeFailed, err.Error()
		if errors.Is(err, errNotReady) {
			s.Outcome = outcomeNotReady
		}
		return a, nil, nil, &s, nil
	}
	if err := h.identify(caseID, &a); err != nil {
		s.Outcome, s.Reason = outcomeFailed, "identify: "+err.Error()
		return a, nil, nil, &s, nil
	}
	layers := agentLayers(h.iso.Home, a.CWD, gen != nil, agentDirectorLayer(procRoot, a.PID))
	report := captureBudgets(layers, h.childEnvMap(), env, h.iso.ClaudeCode)
	return a, &report, hookProgramsMissing(layers, h.env.getenv("PATH")), nil, nil
}

// raisedLayer is the generated layer of a raised-budget variant: one slow
// SessionEnd hook (sleep, in exec form) that runs longer than Claude Code's
// default budget and shorter than the raised one, with the raised per-hook
// `timeout` in the hook variant. nil for the default budget.
func (h *harness) raisedLayer(v budgetVariant) map[string]any {
	if v == budgetDefault {
		return nil
	}
	sleep, err := lookPathIn("sleep", h.env.getenv("PATH"))
	if err != nil {
		sleep = "sleep"
	}
	hook := map[string]any{
		"type": "command", "command": sleep,
		"args": []string{strconv.FormatFloat(float64(h.cfg.slowHookMS)/1000, 'f', 3, 64)},
	}
	if v == budgetRaisedHook {
		hook["timeout"] = h.cfg.raisedHookTimeoutSeconds
	}
	return map[string]any{"hooks": map[string]any{
		"SessionEnd": []any{map[string]any{"hooks": []any{hook}}},
	}}
}

// raisedEnv is the spawn's extra environment for a variant: the raised
// CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS for the env variant, else none.
func (h *harness) raisedEnv(v budgetVariant) map[string]string {
	if v != budgetRaisedEnv {
		return nil
	}
	return map[string]string{sessionEndBudgetEnv: strconv.Itoa(h.cfg.raisedEnvTimeoutMS)}
}

// variantTitle names a variant in a case title.
func variantTitle(v budgetVariant) string {
	switch v {
	case budgetRaisedHook:
		return "SessionEnd budget raised by a per-hook timeout"
	case budgetRaisedEnv:
		return "SessionEnd budget raised by " + sessionEndBudgetEnv
	default:
		return "default SessionEnd budget"
	}
}

// applyPoll records a poller's result on s; on completion the time is d.
func applyPoll(s *sample, pr pollResult, d time.Duration) {
	s.Polls, s.ProbeErrors = pr.Polls, pr.ProbeErrors
	if pr.Outcome == outcomeCompleted {
		s.setTime(d)
		return
	}
	s.Outcome = pr.Outcome
	if pr.LastError != "" {
		s.Reason = "last poll: " + pr.LastError
	}
}

// measureKill is RN-6's measured action: kill the agent's pane on the
// private socket and poll the start-time reader every 100 ms until the
// agent process (with its start time) is gone. The time runs from the
// instant just before kill-pane.
func measureKill(h *harness, caseID string, a agentRef, s *sample) {
	t0, err := h.killPane(caseID, a)
	if err != nil {
		s.Outcome, s.Reason = outcomeFailed, "kill-pane: "+err.Error()
		return
	}
	pr := pollUntilGone(h.clock, t0, h.cfg.sampleCeiling, processGone(h.procs, a.PID, a.StartTime))
	applyPoll(s, pr, pr.Elapsed)
}

// measureNaturalExit is RN-2's natural exit: the Ctrl-D keystrokes at the
// idle prompt, then the session-listing poll.
func measureNaturalExit(h *harness, caseID string, a agentRef, s *sample) {
	if err := h.naturalExit(caseID, a); err != nil {
		s.Outcome, s.Reason = outcomeFailed, "natural-exit keystroke: "+err.Error()
		return
	}
	h.measureEndedToGone(caseID, a, s, nil)
}

// measurePause is RN-2's pause exit: agent-director pause (it sends /exit
// and waits for the row to end), then the session-listing poll. A pause
// error does not stop the sample: the stored ended_at decides it.
func measurePause(h *harness, caseID string, a agentRef, s *sample) {
	h.measureEndedToGone(caseID, a, s, h.pause(caseID, actionMeasured, a))
}

// measureEndedToGone polls the private server's session listing every
// 100 ms until the agent's session no longer lists, then reads the row's
// stored ended_at (read-only get) and records the time from ended_at to the
// poll that saw the session gone, on the wall clock (wn). A row with no
// ended_at is no_ended_at, never a time, with the trail's ignored-hook
// reasons and the trigger's error, if any.
func (h *harness) measureEndedToGone(caseID string, a agentRef, s *sample, triggerErr error) {
	pr := pollUntilGone(h.clock, h.clock.Now(), h.cfg.sampleCeiling, h.sessionGone(caseID, a))
	r, err := h.getRow(caseID, a.InstanceID)
	var why []string
	if triggerErr != nil {
		why = append(why, "trigger: "+triggerErr.Error())
	}
	switch {
	case err != nil:
		s.Outcome = outcomeFailed
		why = append(why, "get after the exit: "+err.Error())
	case r.EndedAt == nil:
		s.Outcome = outcomeNoEndedAt
		s.Polls, s.ProbeErrors = pr.Polls, pr.ProbeErrors
		why = append(why, "the row has no ended_at (no SessionEnd applied); state "+r.State)
		if reasons, terr := ignoredHookReasons(h.trailPath(), a.InstanceID); terr == nil && len(reasons) > 0 {
			why = append(why, "ignored hooks: "+strings.Join(reasons, ", "))
		}
	default:
		applyPoll(s, pr, pr.ObservedAt.Sub(*r.EndedAt))
	}
	if len(why) > 0 {
		s.Reason = strings.Join(append(why, s.Reason), "; ")
		s.Reason = strings.TrimSuffix(s.Reason, "; ")
	}
}
