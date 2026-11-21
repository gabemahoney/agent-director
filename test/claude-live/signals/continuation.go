package signals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type continuationCall struct {
	receipt runner.ContinuationReceipt
	payload runner.Row
}

func (c capture) continuationWakeText() string {
	if c.m.ContinuationHook != nil && c.m.ContinuationHook.Variant == runner.ContinuationHeldSteer {
		return ContinuationWakeOrder
	}
	return ContinuationWake
}

func (c capture) continuationInstruction() (string, error) {
	h := c.m.ContinuationHook
	if h == nil {
		return "", errors.New("missing continuation hook")
	}
	if h.Variant != "" && (c.m.ProbePlan == nil || c.m.ProbePlan.Version != PlanVersion) {
		return "", errors.New("continuation variant is not bound to the current plan")
	}
	return runner.ContinuationInstruction(h.Variant)
}

func analyzeContinuation(root string, primary capture, v *runner.Verdict) {
	r := runner.Result{SignalID: Continuation, Status: "not_completed", Reason: "capture has no isolated sibling-Stop continuation exercise", Expected: "sibling Stop continuation makes a tool call while later STEER stays Held; written+sighting then releases it once", Evidence: primary.refs}
	defer func() { v.Results = append(v.Results, r) }()
	if !primary.hasCase(Continuation) {
		return
	}
	r.Status, r.Reason = "fail", "missing or contrary sibling-Stop continuation evidence"
	if len(primary.m.Owned) != 2 {
		r.Observed = "variant requires two isolated launches"
		return
	}
	c, err := loadOwner(root, 1)
	if err != nil {
		r.Observed = err.Error()
		return
	}
	r.Evidence = c.refs
	observed, err := c.continuationEvidence()
	r.Observed = observed
	if errors.Is(err, errContinuationWindow) || errors.Is(err, errHeldContinuation) {
		r.Status, r.Reason = "not_completed", err.Error()
		return
	}
	if err != nil {
		r.Reason = err.Error()
		return
	}
	r.Status, r.Reason = "pass", "sibling tool leaves the unsighted wake unordered and STEER Held; sighting plus written releases one later steer"
}

var errHeldContinuation = errors.New("capture lacks the held-steer sibling-tool interval")

var errContinuationWindow = errors.New("text-only continuation window missed; no retry authorized")

func (c capture) continuationEvidence() (map[string]any, error) {
	obs := map[string]any{"model_requests": len(c.t.Requests), "injected_inputs": len(c.s.Tables["messages"]), "observed_turns": c.m.Owned[0].ObservedTurns}
	x, ok := c.caseRecord(Continuation)
	count := 0
	for _, candidate := range c.m.Cases {
		if candidate.ID == Continuation {
			count++
		}
	}
	if !ok || count != 1 || !x.Ready || x.Text != c.continuationWakeText() || x.Mode != "wait_turn" || x.RequestID != Continuation {
		return obs, errors.New("unbound continuation case")
	}
	if c.m.TurnBudget != 7 || c.m.TurnsReserved > 7 || c.m.ObservedTurns > 7 {
		return obs, errors.New("continuation plan exceeds seven-turn authorization")
	}
	calls, err := c.continuationCalls()
	if err != nil {
		return obs, err
	}
	wake, h, err := c.prompt(c.continuationWakeText())
	obs["wake"] = facts(wake, err)
	if err != nil {
		return obs, err
	}
	complete := runner.Number(wake, "transport_at_ms")
	release := c.nextStop(complete).UnixMilli()
	obs["release_stop_ms"] = release
	// The scheduling observation identifies the intended request even when
	// transport loses the race with its finish. Never substitute a later turn.
	b, err := c.indexed("raw/text-finish-window-1.json")
	var window struct {
		RequestID   string    `json:"request_id"`
		RequestLine int       `json:"request_line"`
		RequestTime time.Time `json:"request_time"`
	}
	if err != nil || json.Unmarshal(b, &window) != nil || window.RequestID == "" {
		return obs, errors.New("missing intended continuation request")
	}
	if c.transcriptErr != nil {
		return obs, c.transcriptErr
	}
	for _, row := range c.t.Rows {
		if row.Type == "assistant" && row.RequestRef == window.RequestID && row.Message.StopReason == "end_turn" && row.Timestamp.UnixMilli() < complete {
			return obs, errContinuationWindow
		}
	}
	if runner.Text(h, "boundary") != "force" || runner.Number(h, "intent_running_turn") != 1 {
		return obs, errors.New("wake was not forced during a running turn")
	}
	req, terminal, err := c.textWindow(complete, release)
	obs["active_request_line"], obs["terminal_line"] = req.Line, terminal
	if err != nil {
		return obs, err
	}
	if req.ID != window.RequestID || req.Line != window.RequestLine || !req.Time.Equal(window.RequestTime) {
		return obs, errors.New("wake missed the intended request")
	}
	var finish time.Time
	for _, row := range c.t.Rows {
		if row.Line == terminal {
			finish = row.Timestamp
		}
	}
	next, continuation, err := c.continuationRequest(req.ID, terminal, release, x.ObservedUntil)
	obs["continuation_request"] = continuation
	if err != nil {
		return obs, err
	}
	var parent runner.Row
	var parentAt time.Time
	for _, p := range c.hooks["UserPromptSubmit"] {
		at, e := time.Parse(time.RFC3339Nano, runner.Text(p, "_received"))
		if e == nil && c.parent(p, "UserPromptSubmit") && at.Before(req.Time) && at.After(parentAt) {
			parent, parentAt = p, at
		}
	}
	parentID := runner.Text(parent, "prompt_id")
	if parentID == "" {
		return obs, errors.New("missing originating parent prompt")
	}
	// Each sibling may run before or after AD. Bound all three observations
	// by the same terminal response and next request; never treat receipt time
	// as AD's entry timestamp. More than one candidate is ambiguous.
	stops, adStops, blocks := 0, 0, 0
	for _, p := range c.hooks["Stop"] {
		at, e := time.Parse(time.RFC3339Nano, runner.Text(p, "_received"))
		if e == nil && c.parent(p, "Stop") && at.After(finish) && at.Before(next.Time) {
			stops++
			active, valid := p["stop_hook_active"].(bool)
			if !valid || active || runner.Text(p, "prompt_id") != parentID {
				return obs, errors.New("releasing Stop does not match the original parent")
			}
		}
	}
	for _, p := range c.trail {
		at, e := time.Parse(time.RFC3339Nano, runner.Text(p, "ts"))
		if e == nil && runner.Text(p, "event") == "ad.hook.fired" && runner.Text(p, "event_name") == "Stop" && runner.Text(p, "session_id") == c.m.Owned[0].SessionID && runner.Text(p, "agent_id") == "" && at.After(finish) && at.Before(next.Time) {
			adStops++
			if at.UnixMilli() < release || (runner.Text(p, "upsert_outcome") != "updated" && runner.Text(p, "upsert_outcome") != "no_change") {
				return obs, errors.New("AD Stop cannot corroborate release")
			}
		}
	}
	for _, call := range calls {
		if call.receipt.Decision != "block" {
			continue
		}
		blocks++
		if !call.receipt.Received.After(finish) || !call.receipt.Finished.Before(next.Time) || runner.Text(call.payload, "prompt_id") != parentID {
			return obs, errors.New("continuation decision cannot link to releasing Stop")
		}
	}
	obs["releasing_stop_payloads"], obs["releasing_ad_stops"], obs["continuation_blocks"] = stops, adStops, blocks
	if stops != 1 || adStops != 1 || blocks != 1 {
		return obs, errors.New("ambiguous releasing Stop linkage")
	}
	if !c.hasCase(ContinuationSteerCase) {
		return obs, errHeldContinuation
	}
	return c.continuationHold(obs, next, parentID, finish)
}

type toolEvidence struct {
	ID          string    `json:"tool_use_id"`
	Started     time.Time `json:"started"`
	Completed   time.Time `json:"completed"`
	Batch       time.Time `json:"batch"`
	ADBatch     time.Time `json:"ad_batch"`
	NextRequest time.Time `json:"next_request"`
	NextID      string    `json:"next_request_id"`
}

var errNoContinuationTool = errors.New("continuation did not make its requested foreground tool call")

// AD facts/trail and provider transcript times have millisecond precision.
// Separate buckets prove order; a sub-millisecond difference or equality
// across these sources does not. Recorder times may be finer, never coarser.
func beforeMillis(a, b time.Time) bool {
	return !a.IsZero() && !b.IsZero() && a.UnixMilli() < b.UnixMilli()
}

// Bind actual tool use, its result in a real subsequent model request, the
// passive fields and AD's completed event. An instruction is never evidence.
func (c capture) requestTool(req Request, program, parentID string) (toolEvidence, error) {
	var tool toolEvidence
	uses := 0
	for _, row := range c.t.Rows {
		if row.Type != "assistant" || row.RequestRef != req.ID {
			continue
		}
		var blocks []runner.Row
		if json.Unmarshal(row.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			input, _ := b["input"].(map[string]any)
			if runner.Text(b, "type") == "tool_use" && runner.Text(b, "name") == "Bash" && runner.Text(input, "command") == program {
				tool.ID, tool.Started = runner.Text(b, "id"), row.Timestamp
				uses++
			}
		}
	}
	if uses == 0 {
		return tool, errNoContinuationTool
	}
	if uses != 1 || tool.ID == "" {
		return tool, errors.New("ambiguous continuation tool")
	}
	results := 0
	for _, row := range c.t.Rows {
		if row.Type != "user" {
			continue
		}
		var blocks []runner.Row
		if json.Unmarshal(row.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if runner.Text(b, "type") == "tool_result" && runner.Text(b, "tool_use_id") == tool.ID {
				tool.Completed = row.Timestamp
				results++
			}
		}
	}
	if results != 1 || !tool.Completed.After(tool.Started) {
		return tool, errors.New("missing unique completed continuation tool")
	}
	for _, next := range c.t.Requests {
		if next.Time.Before(tool.Completed) {
			continue
		}
		for _, message := range next.Messages {
			var blocks []runner.Row
			if message.Role != "user" || json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if runner.Text(b, "type") == "tool_result" && runner.Text(b, "tool_use_id") == tool.ID && (tool.NextRequest.IsZero() || next.Time.Before(tool.NextRequest)) {
					tool.NextRequest = next.Time
					tool.NextID = next.ID
				}
			}
		}
	}
	pre, batch, ad := 0, 0, 0
	for _, p := range c.hooks["PreToolUse"] {
		input, _ := p["tool_input"].(map[string]any)
		if c.parent(p, "PreToolUse") && runner.Text(p, "prompt_id") == parentID && runner.Text(p, "tool_use_id") == tool.ID && runner.Text(p, "tool_name") == "Bash" && runner.Text(input, "command") == program {
			pre++
		}
	}
	for _, p := range c.hooks["PostToolBatch"] {
		if !c.parent(p, "PostToolBatch") || runner.Text(p, "prompt_id") != parentID {
			continue
		}
		calls, _ := p["tool_calls"].([]any)
		for _, raw := range calls {
			call, _ := raw.(map[string]any)
			input, _ := call["tool_input"].(map[string]any)
			if runner.Text(call, "tool_use_id") == tool.ID && runner.Text(call, "tool_name") == "Bash" && runner.Text(input, "command") == program {
				tool.Batch, _ = time.Parse(time.RFC3339Nano, runner.Text(p, "_received"))
				batch++
			}
		}
	}
	for _, p := range c.trail {
		at, e := time.Parse(time.RFC3339Nano, runner.Text(p, "ts"))
		if e == nil && runner.Text(p, "event") == "ad.hook.fired" && runner.Text(p, "event_name") == "PostToolBatch" && runner.Text(p, "session_id") == c.m.Owned[0].SessionID && runner.Text(p, "agent_id") == "" && (runner.Text(p, "upsert_outcome") == "updated" || runner.Text(p, "upsert_outcome") == "no_change") && at.After(tool.Started) && at.Before(tool.NextRequest) {
			tool.ADBatch = at
			ad++
		}
	}
	if pre != 1 || batch != 1 || ad != 1 || tool.NextRequest.IsZero() || !tool.Batch.After(tool.Started) || !tool.Batch.Before(tool.NextRequest) {
		return tool, errors.New("tool lacks unique parent PreToolUse/PostToolBatch/AD event and next request")
	}
	if !beforeMillis(tool.Completed, tool.Batch) || !beforeMillis(tool.Completed, tool.ADBatch) || !beforeMillis(tool.Batch, tool.NextRequest) || !beforeMillis(tool.ADBatch, tool.NextRequest) {
		return tool, errors.New("PostToolBatch is not strictly after the completed tool and before its next request")
	}
	return tool, nil
}

func (c capture) continuationHold(obs map[string]any, next Request, parentID string, finish time.Time) (map[string]any, error) {
	x, valid := c.caseRecord(ContinuationSteerCase)
	if !valid || x.Text != ContinuationSteer || x.Mode != "steer_turn" || !x.Ready {
		return obs, errors.New("unbound continuation steer case")
	}
	tool, err := c.requestTool(next, runner.ContinuationToolProgram, parentID)
	obs["sibling_tool"] = tool
	if errors.Is(err, errNoContinuationTool) {
		return obs, errHeldContinuation
	}
	if err != nil {
		return obs, err
	}
	wake, _, we := c.prompt(ContinuationWakeOrder)
	steer, sh, se := c.prompt(ContinuationSteer)
	obs["steer"] = facts(steer, se)
	held, he := c.heldOverlap(ContinuationWakeOrder, ContinuationSteer)
	obs["initial_held_interval"] = held
	if he != nil {
		return obs, he
	}
	if !held {
		return obs, errHeldContinuation
	}
	sm, sme := c.acceptedCase(ContinuationSteerCase, ContinuationSteer)
	cmd := c.commands[x.Command]
	if sme != nil || !beforeMillis(cmd.Ended, finish) {
		return obs, errHeldContinuation
	}
	order, oe := c.wakeOrder(ContinuationWakeOrder)
	obs["wake_order_ms"], obs["wake_order_error"] = order, errorText(oe)
	history, err := c.history()
	if err != nil {
		return obs, err
	}
	beforeStop := false
	for _, s := range history {
		p := snapshotPrompt(s, wake)
		if runner.Text(p, "transport") != "written" || runner.Text(p, "admission_kind") != "" {
			continue
		}
		copy := c
		copy.s = s
		m, e := copy.message(ContinuationSteer)
		// The successful send and a completed read must both precede the
		// original terminal response. Stop's trail is post-processing only.
		if !s.Observed.Before(cmd.Ended) && beforeMillis(s.ReadCompleted, finish) && runner.Text(p, "order_kind") == "" && e == nil && sameMessage(m, sm) && runner.Text(m, "place") == "held" {
			beforeStop = true
			obs["held_before_terminal_read_completed"] = s.ReadCompleted
		}
	}
	obs["held_before_original_terminal"] = beforeStop
	if !beforeStop {
		return obs, errHeldContinuation
	}
	if we != nil || oe != nil {
		return obs, errors.New("wake order or unique later steer issue is invalid")
	}
	if err := c.continuationToolHold(obs, history, wake, sm, tool); err != nil {
		return obs, err
	}
	wi, wakeForm, wie := c.continuationWakeIntake(next)
	obs["wake_input_form"] = wakeForm
	si, sie := c.intake(ContinuationSteer, "steer_turn")
	obs["wake_input"], obs["steer_input"], obs["wake_input_error"], obs["steer_input_error"] = wi, si, errorText(wie), errorText(sie)
	if wie != nil || sie != nil || !earlier(wi, si) {
		return obs, errors.New("whole wake input must precede continuation STEER input")
	}
	wakeParent := ""
	for _, p := range c.hooks["UserPromptSubmit"] {
		if c.parent(p, "UserPromptSubmit") && runner.Text(p, "prompt") == Render(ContinuationWakeOrder, "wait_turn") {
			wakeParent = runner.Text(p, "prompt_id")
		}
	}
	wakeTool, err := c.requestTool(c.t.Requests[wi.Request], BusyProgram, wakeParent)
	obs["post_sighting_tool"] = wakeTool
	if err != nil {
		return obs, err
	}
	issue := runner.Number(steer, "issued_at_ms")
	written := runner.Number(steer, "transport_at_ms")
	envelopes := c.envelopeTimes(ContinuationSteer)
	if wakeTool.ID == tool.ID || issue <= max(order, runner.Number(wake, "admitted_at_ms"), runner.Number(wake, "transport_at_ms"), wakeTool.Completed.UnixMilli()) || written >= wakeTool.NextRequest.UnixMilli() || runner.Text(sh, "boundary") != "tool_boundary" || runner.Text(steer, "order_kind") != "emission_complete" || runner.Number(steer, "order_at_ms") != written || len(envelopes) != 1 || !beforeMillis(time.UnixMilli(written), envelopes[0]) || !beforeMillis(envelopes[0], wakeTool.NextRequest) || c.t.Requests[si.Request].ID != wakeTool.NextID {
		return obs, errors.New("STEER did not issue once at the distinct post-sighting tool boundary")
	}
	if !c.caseDone(ContinuationSteerCase) {
		return obs, errors.New("later steer exercise lacks a completed model request")
	}
	return obs, nil
}

// A 200ms sampler need not land in the few milliseconds between PostToolBatch
// and sighting. Bound that gap by both retained Held states AND the complete
// issued-prompt log; sampled absence alone cannot prove no issue in the gap.
func (c capture) continuationToolHold(obs map[string]any, history []runner.Snapshot, wake, sm runner.Row, tool toolEvidence) error {
	var lastBeforeResult, firstSighted runner.Snapshot
	for _, s := range history {
		if s.Observed.Before(tool.Completed) {
			lastBeforeResult = s
		}
		if firstSighted.Observed.IsZero() && runner.Text(snapshotPrompt(s, wake), "admission_kind") == "prompt_sighting" {
			firstSighted = s
		}
	}
	// Use the last pre-result observation, not an earlier convenient Held
	// sample. Its entire read must precede the independent tool result.
	pre := snapshotPrompt(lastBeforeResult, wake)
	copy := c
	copy.s = lastBeforeResult
	m, err := copy.message(ContinuationSteer)
	if !beforeMillis(lastBeforeResult.ReadCompleted, tool.Completed) || runner.Text(pre, "transport") != "written" || runner.Text(pre, "admission_kind") != "" || runner.Text(pre, "order_kind") != "" || err != nil || !sameMessage(m, sm) || runner.Text(m, "place") != "held" {
		return errHeldContinuation
	}
	obs["last_unsighted_held_read_completed"] = lastBeforeResult.ReadCompleted
	sighting := runner.Number(wake, "admitted_at_ms")
	if sighting <= tool.ADBatch.UnixMilli() {
		return errors.New("wake sighting did not follow the completed sibling PostToolBatch")
	}
	copy.s = firstSighted
	m, err = copy.message(ContinuationSteer)
	if firstSighted.ReadCompleted.IsZero() {
		return errHeldContinuation
	}
	if err != nil || !sameMessage(m, sm) || runner.Text(m, "place") != "held" {
		return errors.New("STEER was not Held in the first sighted snapshot")
	}
	obs["first_sighted_held_at"] = firstSighted.Observed
	// prompt traverses the final retained Message/member/handover/prompt
	// records, requiring exactly one issued prompt on the owned Launch. It
	// does not infer issuance or its absence from any sampled store state.
	steer, _, err := c.prompt(ContinuationSteer)
	obs["logged_steer_issue_exactly_once"] = err == nil
	if err != nil {
		return fmt.Errorf("continuation requires exactly one logged STEER issue: %w", err)
	}
	issue := runner.Number(steer, "issued_at_ms")
	obs["logged_steer_issue_ms"] = issue
	if issue <= sighting {
		return errors.New("logged STEER issue is not strictly after the unsighted gap")
	}
	obs["logged_steer_issue_free_gap"] = true
	return nil
}

func (c capture) continuationCalls() ([]continuationCall, error) {
	h, o := c.m.ContinuationHook, c.m.Owned[0]
	if h == nil || !h.Installed || !h.Removed || h.InstanceID != o.InstanceID || h.CWD != o.CWD || h.Command == "" || h.Command != c.m.RecordingHook.Command || h.SHA256 != c.m.RecordingHook.SHA256 {
		return nil, errors.New("unbound continuation installation/removal")
	}
	b, err := c.indexed("raw/continuation-owner.json")
	var owner runner.Owned
	if err != nil || json.Unmarshal(b, &owner) != nil || owner.InstanceID != o.InstanceID || owner.SessionID != o.SessionID || owner.LaunchID != o.LaunchID || owner.LaunchToken != o.LaunchToken || owner.CWD != o.CWD || owner.Transcript != o.Transcript {
		return nil, errors.New("continuation owner differs from captured Launch")
	}
	b, err = c.indexed("raw/continuation-settings.json")
	var settings any
	if err != nil || json.Unmarshal(b, &settings) != nil {
		return nil, errors.New("missing continuation settings")
	}
	// The evidence may have been copied; infer the original root from owned
	// cwd, never substitute the analyzer's current directory into hook args.
	reason, err := c.continuationInstruction()
	if err != nil {
		return nil, err
	}
	args := []any{"continue-stop", "--evidence", filepath.Dir(o.CWD), "--run-id", c.m.RunID}
	if h.Variant != "" {
		args = append(args, "--variant", h.Variant)
	}
	want := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": h.Command, "args": args, "timeout": float64(3)}}}}}}
	if filepath.Base(o.CWD) != "work-continuation" || !reflect.DeepEqual(settings, want) {
		return nil, errors.New("unexpected continuation registration")
	}
	if _, err = os.Lstat(filepath.Join(c.root, "work-continuation/.claude/settings.local.json")); !os.IsNotExist(err) {
		return nil, errors.New("continuation registration was not removed")
	}
	if b, err = c.indexed("raw/continuation/used"); err != nil || len(b) != 0 {
		return nil, errors.New("missing one-shot continuation token")
	}
	dir, err := runner.Confined(c.root, "raw/continuation/events")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var calls []continuationCall
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "invocation-") {
			return nil, errors.New("unexpected continuation entry")
		}
		ref := "raw/continuation/events/" + entry.Name()
		files, e := os.ReadDir(filepath.Join(dir, entry.Name()))
		if e != nil || len(files) != 3 {
			return nil, errors.New("incomplete continuation invocation")
		}
		b, e := c.indexed(ref + "/receipt.json")
		var rec runner.ContinuationReceipt
		if e != nil || json.Unmarshal(b, &rec) != nil || rec.Schema != runner.Schema || rec.RunID != c.m.RunID || rec.Event != "Stop" || rec.Input != "input.json" || !rec.Complete || rec.Received.IsZero() || rec.Finished.Before(rec.Received) {
			return nil, errors.New("invalid continuation receipt")
		}
		b, e = c.indexed(ref + "/input.json")
		var p runner.Row
		if e != nil || json.Unmarshal(b, &p) != nil || runner.Digest(b) != rec.SHA256 || len(b) != rec.Bytes || !c.parent(p, "Stop") || runner.Text(p, "cwd") != o.CWD {
			return nil, errors.New("invalid continuation input")
		}
		b, e = c.indexed(ref + "/output.json")
		if e != nil {
			return nil, e
		}
		active, valid := p["stop_hook_active"].(bool)
		if !valid {
			return nil, errors.New("continuation lacks Stop state")
		}
		if rec.Decision == "block" {
			var output map[string]string
			if active || json.Unmarshal(b, &output) != nil || len(output) != 2 || output["decision"] != "block" || output["reason"] != reason {
				return nil, errors.New("invalid continuation decision")
			}
		} else if rec.Decision != "" || len(b) != 0 {
			return nil, fmt.Errorf("unexpected continuation output: %s", ref)
		}
		calls = append(calls, continuationCall{rec, p})
	}
	return calls, nil
}
