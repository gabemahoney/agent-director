package signals

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Analyze reads only the supplied evidence directory. Expectations come from
// the contract/corpus and capability table, never from claimed observations in
// a manifest or from a positive fixture recorded against another provider.
func Analyze(root string) runner.Verdict {
	c, err := load(root)
	v := runner.Verdict{Schema: runner.Schema, RunID: c.m.RunID, Phase: c.m.Phase, Candidate: c.m.Candidate, ProviderVersion: c.m.ProviderVersion, Results: append([]runner.Result(nil), c.m.Problems...)}
	v.NamedPremises = []runner.NamedPremise{{ID: RA, Reference: "SRD t1.kii.kn O4 R-a / SR-8.3", Reason: "Unmeasured: AD's awaiting-take guard prevents issuing a later wake while earlier steer context is pending; this probe does not bypass that guard."}}
	if err != nil {
		status := "fail"
		attempted := false
		for _, x := range c.m.Cases {
			attempted = attempted || (x.Ready && x.ID != "startup")
		}
		if c.m.Schema == runner.Schema && len(c.m.Problems) > 0 && !attempted {
			status = "not_completed"
		}
		v.Results = append(v.Results, runner.Result{SignalID: "evidence-integrity", Status: status, Reason: err.Error(), Expected: "valid, scoped complete artifacts", Observed: err.Error(), Evidence: []string{"manifest.json"}})
		for _, id := range CheckIDs {
			v.Results = append(v.Results, runner.Result{SignalID: id, Status: "not_completed", Reason: "evidence could not be loaded", Expected: "complete signal evidence", Observed: err.Error(), Evidence: []string{"manifest.json"}})
		}
		v.Aggregate()
		return v
	}
	if len(c.unavailable) != 0 {
		v.Results = append(v.Results, runner.Result{SignalID: "capture-incomplete", Status: "not_completed", Reason: "capture ended before signal exercises; collection did not export every raw file", Expected: "collected transcript and trail", Observed: c.unavailable, Evidence: []string{"manifest.json"}})
	}
	add := func(id, caseID string, ok bool, expected, observed any) {
		status, reason := "pass", "expected signal observed"
		if !ok {
			status, reason = "fail", "signal differs or is missing after the completed exercise"
		}
		if !c.caseDone(caseID) {
			status, reason = "not_completed", "independent request/tool/end-turn evidence does not establish a completed exercise"
		}
		v.Results = append(v.Results, runner.Result{SignalID: id, Status: status, Reason: reason, Expected: expected, Observed: observed, Evidence: c.refs})
	}
	// The run must have installed and removed its one silent recorder; a kit
	// build or an otherwise healthy agent cannot excuse missing observations.
	add("recording-hook", "idle-wake", c.m.RecordingHook.Installed && c.m.RecordingHook.Removed && len(c.hooks) > 0, "installed, observed and removed passive recorder", c.m.RecordingHook)
	budgetOK := c.m.TurnsReserved <= c.m.TurnBudget && c.m.ObservedTurns <= int64(c.m.TurnBudget) && c.m.TurnsReserved > 0 && c.m.TurnBudget <= 7
	if c.m.ProbePlan != nil {
		p := c.m.ProbePlan
		budgetOK = budgetOK && p.Version == PlanVersion && p.PrimaryTurns == 4 && p.ContinuationTurns == 2 && p.ScheduledTurns == 6 && p.AggregateCap == 7 && c.m.TurnsReserved <= 6
	}
	add("turn-budget", "idle-wake", budgetOK, "six scheduled turns (4+2); aggregate observed cap seven; no retry", map[string]any{"plan": c.m.ProbePlan, "reserved": c.m.TurnsReserved, "observed": c.m.ObservedTurns, "model_requests": len(c.t.Requests), "injected_inputs": len(c.s.Tables["messages"])})
	exited := true
	for _, o := range c.m.Owned {
		exited = exited && o.Exited
	}
	add("owned-cleanup", "idle-wake", exited, "all recorded owned processes exited", exited)
	var wakePath string
	for _, l := range c.s.Tables["launches"] {
		if runner.Text(l, "launch_id") == c.m.Owned[0].LaunchID {
			wakePath = runner.Text(l, "wake_path")
		}
	}
	sessionPayload := c.payload("SessionStart", func(p runner.Row) bool { return runner.Text(p, "source") == "startup" })
	watch := false
	for _, r := range c.t.Rows {
		if r.Attachment.Event != "SessionStart" {
			continue
		}
		var out struct {
			Specific struct {
				Event string   `json:"hookEventName"`
				Paths []string `json:"watchPaths"`
			} `json:"hookSpecificOutput"`
		}
		if json.Unmarshal([]byte(r.Attachment.Stdout), &out) == nil && out.Specific.Event == "SessionStart" {
			for _, p := range out.Specific.Paths {
				if p == wakePath && p != "" {
					watch = true
				}
			}
		}
	}
	add("session-watch", "idle-wake", sessionPayload && watch && c.fired("SessionStart"), "parent SessionStart, AD watchPaths for this Launch", map[string]any{"payload": sessionPayload, "watch_paths": watch, "wake_path": wakePath, "ad_callback": c.fired("SessionStart")})
	fileChanged := c.payload("FileChanged", func(p runner.Row) bool {
		return c.during(p, "idle-wake") && runner.Text(p, "file_path") == wakePath && runner.Text(p, "event") == "change"
	})
	add("file-changed", "idle-wake", fileChanged && c.fired("FileChanged"), "parent FileChanged with file_path equal to watched wake_path, event=change, AD callback", map[string]bool{"payload": fileChanged, "ad_callback": c.fired("FileChanged")})
	plain, _, plainErr := c.prompt(Plain)
	plainPrompt := c.payload("UserPromptSubmit", func(p runner.Row) bool {
		return runner.Text(p, "prompt") == Render(Plain, "wait_turn") && runner.Text(p, "prompt_id") != ""
	})
	add("user-prompt-identity", "plain-sighting", plainPrompt && c.fired("UserPromptSubmit"), "parent UserPromptSubmit with session, transcript, prompt_id and exact wrapped known prompt", map[string]bool{"payload": plainPrompt, "ad_callback": c.fired("UserPromptSubmit")})
	add("wake-wrapping", "idle-wake", plainPrompt, Render(Plain, "wait_turn"), c.prompts())
	add("plain-sighting", "plain-sighting", plainErr == nil && runner.Text(plain, "admission_kind") == "prompt_sighting" && runner.Number(plain, "admitted_at_ms") >= runner.Number(plain, "issued_at_ms"), "one exact plain Message bound to one AD prompt_sighting", facts(plain, plainErr))
	plainIn, plainInErr := c.intake(Plain, "wait_turn")
	add("async-rewake", "idle-wake", fileChanged && plainPrompt && plainErr == nil && plainInErr == nil, "FileChanged wake reaches a model request without typed input", map[string]any{"file_changed": fileChanged, "prompt": facts(plain, plainErr), "intake": plainIn, "intake_error": errorText(plainInErr)})
	tag, _, tagErr := c.prompt(Reserved)
	tagPrompt := c.payload("UserPromptSubmit", func(p runner.Row) bool {
		return runner.Text(p, "prompt") == Render(Reserved, "wait_turn") && runner.Text(p, "prompt_id") != ""
	})
	add("reserved-escaping", "reserved-tags", tagPrompt, Render(Reserved, "wait_turn"), c.prompts())
	add("reserved-no-sighting", "reserved-tags", tagErr == nil && tagPrompt && runner.Text(tag, "admission_kind") == "" && tag["admitted_at_ms"] == nil && runner.Text(tag, "order_kind") == "" && tag["order_at_ms"] == nil, "reserved-tag wake emitted whole; no sighting or order before Launch End", facts(tag, tagErr))
	busyPromptID := ""
	for _, p := range c.hooks["UserPromptSubmit"] {
		if c.parent(p, "UserPromptSubmit") && runner.Text(p, "prompt") == Render(c.toolTurnText(), "wait_turn") {
			busyPromptID = runner.Text(p, "prompt_id")
		}
	}
	batch := c.payload("PostToolBatch", func(p runner.Row) bool {
		calls, ok := p["tool_calls"].([]any)
		return ok && len(calls) > 0 && busyPromptID != "" && runner.Text(p, "prompt_id") == busyPromptID
	})
	steer, steerH, steerErr := c.prompt(Steer)
	envelope := c.envelope(Steer)
	add("post-tool-batch", "steer-turn-end", batch && c.fired("PostToolBatch") && envelope && steerErr == nil && runner.Text(steerH, "boundary") == "tool_boundary", "parent tool_calls and exact whole additionalContext, issued by AD at tool_boundary", map[string]any{"payload": batch, "ad_callback": c.fired("PostToolBatch"), "envelope": envelope, "prompt": facts(steer, steerErr)})
	whole := "pass"
	intakes := map[string]any{}
	for _, input := range []struct{ text, mode, caseID string }{{Plain, "wait_turn", "idle-wake"}, {Reserved, "wait_turn", "reserved-tags"}, {Steer, "steer_turn", "steer-turn-end"}, {c.wakeText(), "wait_turn", "overlap-wake"}} {
		in, e := c.intake(input.text, input.mode)
		complete := c.caseDone(input.caseID)
		intakes[input.text] = map[string]any{"intake": in, "error": errorText(e), "exercise_complete": complete}
		if e != nil && complete {
			whole = "fail"
		} else if !complete && whole != "fail" {
			whole = "not_completed"
		}
	}
	v.Results = append(v.Results, runner.Result{SignalID: "whole-next-input", Status: whole, Reason: "each exercise is judged independently; a later interruption cannot erase an earlier mismatch", Expected: "each complete output exactly once in the NEXT eligible model input, with separator and provider transformation", Observed: intakes, Evidence: c.refs})
	steerIn, steerInErr := c.intake(Steer, "steer_turn")
	stop := c.nextStop(runner.Number(steer, "order_at_ms"))
	take := runner.Number(steer, "admitted_at_ms")
	stopPayload := c.payload("Stop", func(p runner.Row) bool {
		_, ok := p["stop_hook_active"].(bool)
		return ok && busyPromptID != "" && runner.Text(p, "prompt_id") == busyPromptID
	})
	turnOK := runner.Text(steer, "order_kind") == "emission_complete" && runner.Number(steer, "order_at_ms") == runner.Number(steer, "transport_at_ms") && steerErr == nil && steerInErr == nil && runner.Number(steerH, "intent_running_turn") == 1 && runner.Text(steer, "admission_kind") == "native_signal" && take > steerIn.Time.UnixMilli() && !stop.IsZero() && stop.UnixMilli() >= take && stopPayload
	add("turn-end-order", "steer-turn-end", turnOK, "whole steer intake before the next parent Stop and AD native_signal take", map[string]any{"prompt": facts(steer, steerErr), "intake": steerIn, "intake_error": errorText(steerInErr), "next_ad_stop": stop, "stop_payload": stopPayload})
	c.overlap(&v)
	analyzeContinuation(root, c, &v)
	v.Aggregate()
	return v
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
func facts(row runner.Row, err error) any {
	return map[string]any{"record": row, "error": errorText(err)}
}

func (c capture) payload(event string, check func(runner.Row) bool) bool {
	for _, p := range c.hooks[event] {
		if c.parent(p, event) && check(p) {
			return true
		}
	}
	return false
}

func (c capture) prompts() []string {
	var p []string
	for _, h := range c.hooks["UserPromptSubmit"] {
		p = append(p, runner.Text(h, "prompt"))
	}
	sort.Strings(p)
	return p
}

func (c capture) envelope(text string) bool {
	return len(c.envelopeTimes(text)) == 1
}

func (c capture) envelopeTimes(text string) []time.Time {
	var times []time.Time
	for _, r := range c.t.Rows {
		if r.Attachment.Event != "PostToolBatch" || r.Attachment.Stdout == "" {
			continue
		}
		var out map[string]json.RawMessage
		if json.Unmarshal([]byte(r.Attachment.Stdout), &out) != nil {
			continue
		}
		var specific map[string]string
		if json.Unmarshal(out["hookSpecificOutput"], &specific) != nil {
			continue
		}
		if specific["additionalContext"] == Prompt(text) && specific["hookEventName"] == "PostToolBatch" && len(out) == 1 && len(specific) == 2 {
			times = append(times, r.Timestamp)
		}
	}
	return times
}

func (c capture) intake(text, mode string) (Intake, error) {
	if c.transcriptErr != nil {
		return Intake{}, c.transcriptErr
	}
	p, _, err := c.prompt(text)
	if err != nil {
		return Intake{}, err
	}
	want, role := NativeWake(Render(text, mode)), "user"
	if mode == "steer_turn" {
		want, role = NativeContext(text), "system"
	}
	after := runner.Number(p, "transport_at_ms")
	// A queued wake need not be taken at the parent's Stop or by a sibling's
	// continuation request. Its exact UPS sighting identifies its eligible
	// next input; physical output alone does not identify that request.
	if mode == "wait_turn" && runner.Text(p, "admission_kind") == "prompt_sighting" {
		after = runner.Number(p, "admitted_at_ms")
	}
	var additional []inputForm
	if text == Steer || text == OverlapWake {
		// Both inputs must be uniquely bound AD emissions before using their
		// measured shared system block as an alternative whole-input form.
		_, _, steerErr := c.prompt(Steer)
		_, _, wakeErr := c.prompt(OverlapWake)
		if steerErr == nil && wakeErr == nil {
			if form, ok := c.t.mergedForm(after, text); ok {
				additional = append(additional, form)
			}
		}
	}
	return c.t.nextInput(after, want, role, additional)
}

func (c capture) nextStop(after int64) time.Time {
	var found time.Time
	for _, r := range c.trail {
		if runner.Text(r, "event") != "ad.hook.fired" || runner.Text(r, "event_name") != "Stop" || runner.Text(r, "session_id") != c.m.Owned[0].SessionID || runner.Text(r, "agent_id") != "" || (runner.Text(r, "upsert_outcome") != "updated" && runner.Text(r, "upsert_outcome") != "no_change") {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, runner.Text(r, "ts"))
		if err == nil && t.UnixMilli() > after && (found.IsZero() || t.Before(found)) {
			found = t
		}
	}
	return found
}

func Format(v runner.Verdict) string {
	var b strings.Builder
	for _, p := range v.NamedPremises {
		fmt.Fprintf(&b, "%s: named, unmeasured — %s (%s)\n", p.ID, p.Reason, p.Reference)
	}
	for _, r := range v.Results {
		expected, _ := json.Marshal(r.Expected)
		observed, _ := json.Marshal(r.Observed)
		fmt.Fprintf(&b, "%s: %s — %s\nexpected: %s\nobserved: %s\n", r.SignalID, r.Status, r.Reason, expected, observed)
	}
	fmt.Fprintf(&b, "VERDICT: %s\n", v.Status)
	return b.String()
}
