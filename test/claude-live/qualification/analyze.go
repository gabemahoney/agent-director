package qualification

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func combine(id string, checks ...runner.Result) runner.Result {
	v := runner.Verdict{Results: checks}
	v.Aggregate()
	return result(id, v.Status, "required component judgments retained", "every component complete and meets its contract", checks)
}
func rename(id string, r runner.Result) runner.Result { r.SignalID = id; return r }
func errorResult(id string, err error) runner.Result {
	return result(id, evidenceStatus(err), err.Error(), "complete compatible evidence", nil)
}
func accepted(id, code, reason string, observed any, refs ...string) runner.Result {
	if !slices.Contains([]string{"lost_wake_delay", "wait_turn_raced", "user_hook_after_sighting", "duplicate_text", "unrecoverable_escaping", "handed_over_not_proven", "esc_gap", "never_sighted_hold"}, code) || len(refs) == 0 {
		return result(id, "fail", "unknown or unevidenced accepted limit", nil, code)
	}
	return result(id, "pass", reason, "documented evidenced limit", map[string]any{"accepted_limit": code, "facts": observed}, refs...)
}

// AnalyzePhase is the finalizer used by both Execute and offline CLI analysis.
// It reads only sealed files under the supplied root and retained imports.
// Future qualification finalizers and offline consumers must reuse this entry
// point and its component judgments; see docs/architecture.md's reuse contract.
func AnalyzePhase(root string) runner.Verdict {
	c, err := LoadCapture(root)
	if err != nil {
		return PhaseResult(c.Manifest, []runner.Result{errorResult("evidence", err)})
	}
	checks := []runner.Result{checkAccounting(c), checkScheduledStarts(c), checkCleanup(c)}
	checks = append(checks, providerRefusalResults(c)...)
	if c.Manifest.Phase != "uncovered" && c.Manifest.Phase != "signals" {
		doc, docErr := c.read("raw/caller-contract.md")
		runtime, runtimeErr := c.read("raw/capabilities.json")
		if err = errors.Join(docErr, runtimeErr); err != nil {
			checks = append(checks, errorResult("contract", err))
		} else {
			checks = append(checks, CheckContractAgreement(doc, runtime))
		}
	}
	switch c.Manifest.Phase {
	case "typed-controls":
		checks = append(checks, analyzeTyped(c)...)
	case "force-states":
		checks = append(checks, analyzeForces(c)...)
	case "dialogs":
		checks = append(checks, analyzeDialogs(c)...)
	case "groups":
		checks = append(checks, analyzeGroups(c)...)
	case "text-limits-backlog":
		checks = append(checks, analyzeLimits(c)...)
	case "lifecycle", "user-hooks":
		checks = append(checks, CheckLifecycleAndHookLimits(c)...)
	case "esc-gap":
		checks = append(checks, CheckEscRecovery(c)...)
	case "uncovered":
		checks = append(checks, analyzeUncovered(c)...)
	case "setup-launches":
		checks = append(checks, analyzeSetup(c)...)
	case "signals":
		checks = append(checks, c.ciResults()...)
	}
	return PhaseResult(c.Manifest, checks)
}

func checkScheduledStarts(c Capture) runner.Result {
	plans := map[string][]string{
		"setup-launches": {"setup-released", "setup-held", "setup-fresh", "setup-fresh-held"},
		"typed-controls": {"typed-ad-start", "typed-control-start", "typed-long-tool-typed-input", "typed-between-tools-typed-input", "typed-idle-ad-input", "typed-idle-typed-input"},
		"force-states":   {"F1", "F2", "F3", "F4", "F5"}, "groups": {"G0", "O1", "O2"},
		"text-limits-backlog": {"N1", "N2", "N3", "N4", "N5", "N7", "N8"},
		"lifecycle":           {"L1", "L2"}, "user-hooks": {"H1", "H2"}, "esc-gap": {"E1", "E2", "E3", "E4"},
	}
	ids := plans[c.Manifest.Phase]
	if c.Manifest.Phase == "dialogs" {
		ids = c.Before.DialogCases
	}
	if len(ids) == 0 {
		return result("scheduled-starts", "pass", "import-only/other-provider phase adds no Claude start", 0, c.Manifest.ObservedTurns)
	}
	var checks []runner.Result
	blocked := refusalBlockedSuffix(c, ids)
	for _, id := range ids {
		if row, ok := blocked[id]; ok {
			checks = append(checks, row)
			continue
		}
		if err := requirePhysicalCase(c, id, c.Manifest.Phase); err != nil {
			spent := false
			for _, charge := range c.After.Charges {
				if charge.Phase == c.Manifest.Phase && charge.Attempt == c.After.Identity.Attempt && charge.Actual > 0 && ((charge.ID == id && (charge.SelectedCase == "" || charge.SelectedCase == id)) || charge.SelectedCase == id) {
					spent = true
				}
			}
			if phaseWorkload(c) && !spent {
				checks = append(checks, workloadMissing(c, "physical/"+id))
				continue
			}
			checks = append(checks, errorResult("physical/"+id, err))
		} else {
			checks = append(checks, result("physical/"+id, "pass", "planned case owns one bound parent start", 1, 1))
		}
	}
	return combine("scheduled-starts", checks...)
}

func checkCleanup(c Capture) runner.Result {
	complete := c.Manifest.RecordingHook.Installed && c.Manifest.RecordingHook.Removed
	for _, o := range c.Manifest.Owned {
		complete = complete && o.Exited && !o.StoppedAt.IsZero() && o.StoppedAt.Before(c.Manifest.Finished)
	}
	for _, row := range workloadRows(c) {
		var w WorkloadOutcome
		b, err := json.Marshal(row.Observed)
		if err == nil && json.Unmarshal(b, &w) == nil && w.TeardownUncertain {
			return result("cleanup", "not_completed", "background fixture descendants were not independently verified gone; teardown uncertainty retained", true, w, row.Evidence...)
		}
	}
	if !complete {
		return result("cleanup", "not_completed", "verified owned exit and private settings removal missing", true, c.Manifest.Owned, "manifest.json")
	}
	if c.Manifest.Finished.Sub(c.Manifest.Started) >= 9*time.Minute {
		return result("cleanup", "fail", "phase exceeded its nine-minute total including 90 s of cleanup", "under nine minutes", c.Manifest.Finished.Sub(c.Manifest.Started), "manifest.json")
	}
	return result("cleanup", "pass", "owned processes exited and exact temporary registrations removed", "verified cleanup", c.Manifest.Owned, "manifest.json")
}
func checkAccounting(c Capture) runner.Result {
	if !samePriorLedger(c.Before.Prior, c.After.Prior) {
		return result("accounting", "fail", "settled prior usage changed during the phase", nil, nil)
	}
	accounting, err := AccountTurns(c.After)
	if err != nil {
		return result("accounting", "fail", err.Error(), "persisted hard ceilings and unique charging owners", accounting, "raw/qualification-ledger-after.json")
	}
	actual := 0
	for _, charge := range c.After.Charges {
		if charge.Attempt != c.Before.Identity.Attempt || charge.Phase != c.Manifest.Phase {
			continue
		}
		if !charge.Reconciled {
			return result("accounting", "not_completed", "phase reservation was not reconciled", nil, charge)
		}
		if charge.Actual > 0 {
			if charge.SelectedCase != "" {
				if er := requirePhysicalCase(c, charge.SelectedCase, charge.Phase); er != nil {
					return errorResult("accounting", er)
				}
			}
			actual += charge.Actual
			index := strings.LastIndex(charge.Prompt, "@")
			if index < 0 || charge.Actual != 1 {
				return result("accounting", "fail", "physical start lacks raw occurrence identity", nil, charge)
			}
			ref := charge.Prompt[index+1:]
			found := false
			for _, h := range c.Hooks {
				if h.Reference == ref && h.Receipt.Event == "UserPromptSubmit" && runner.Text(h.Payload, "agent_id") == "" && runner.Text(h.Payload, "prompt_id") == charge.Prompt[:index] {
					for _, owner := range c.Owners {
						if owner.Owner.LaunchToken == charge.Launch && owner.Owner.SessionID == runner.Text(h.Payload, "session_id") {
							found = true
						}
					}
				}
			}
			if !found {
				return result("accounting", "fail", "charged start has no bound owned parent occurrence", nil, charge)
			}
		}
	}
	if actual != int(c.Manifest.ObservedTurns) {
		return result("accounting", "fail", "raw physical parent starts and observed counter differ", actual, c.Manifest.ObservedTurns, "raw/qualification-ledger-after.json", "manifest.json")
	}
	for _, charge := range c.After.Charges {
		if charge.Attempt == c.Before.Identity.Attempt && charge.Phase == c.Manifest.Phase && strings.HasPrefix(charge.ID, "unexpected-") {
			return result("accounting", "fail", "unplanned parent start was charged; stop for MI", nil, charge)
		}
	}
	return result("accounting", "pass", "parent-only physical starts reconciled once; imported consumption adds no start", "64 E8 / 32 a6 shakedown / 48 full authority", accounting, "raw/qualification-ledger-after.json")
}

func analyzeTyped(c Capture) []runner.Result {
	timings := ExtractTimings(c)
	var out []runner.Result
	for _, state := range []string{"long-tool", "between-tools", "idle"} {
		adID, controlID := "typed-"+state+"-ad-input", "typed-"+state+"-typed-input"
		ad, aok := timingByID(timings, adID)
		control, cok := timingByID(timings, controlID)
		ac, ax := c.caseByID(adID)
		tc, tx := c.caseByID(controlID)
		ao, aoOK := c.observation(adID, "send")
		co, coOK := c.observation(controlID, "typed-control")
		pair := TypedPair{State: state, TextAD: ac.Text, TextControl: tc.Text, ADTrigger: ao.Trigger, ControlTrigger: co.Trigger, ADBoundary: ad.Boundary, ControlBoundary: control.Boundary, ADWhole: ad.Whole, ControlWhole: control.Whole, Evidence: append(append([]string{}, ad.Evidence...), control.Evidence...)}
		valid := aok && cok && ax && tx && aoOK && coOK && ad.State == state && control.State == state
		for _, t := range []Timing{ad, control} {
			if t.BoundaryStatus != "" {
				out = append(out, result("typed-window/"+t.Case, t.BoundaryStatus, t.BoundaryReason, "first eligible owned interval", t, t.Evidence...))
			}
		}
		for i, observation := range []Observation{ao, co} {
			var trigger time.Time
			for _, h := range c.Hooks {
				if h.Reference == observation.Trigger {
					trigger = h.Receipt.Received
				}
			}
			if state == "idle" {
				var prior struct {
					Snapshot runner.Snapshot `json:"snapshot"`
				}
				if c.json(observation.Trigger, &prior) == nil {
					trigger = prior.Snapshot.ReadCompleted
				}
			}
			if trigger.IsZero() {
				valid = false
				continue
			}
			offset := observation.Started.Sub(trigger)
			if i == 0 {
				pair.ADOffset = offset
			} else {
				pair.ControlOffset = offset
			}
			if offset < 0 {
				valid = false
			}
		}
		if !valid {
			pair.ADBoundary = 0
		}
		judgment := CheckTypedPair(pair)
		if ad.Mode == "wait_turn" && ad.Whole && state != "idle" {
			judgment.Status = "not_completed"
			judgment.Reason = "no tool boundary after injection; delivered at Stop"
			for i := range out {
				if out[i].SignalID == "typed-window/"+ad.Case {
					out[i].Status = "not_completed"
					out[i].Reason = judgment.Reason
				}
			}
		}
		if state == "idle" && (phaseWorkload(c) || typedStopFallback(c)) {
			judgment.Status = "not_completed"
			judgment.Reason = "typed branch closed after Stop/workload outcome; idle pair remains a GAP"
		}
		out = append(out, judgment)
	}
	cap := result("steer-cap", "not_completed", "published-cap parent steer not recorded", nil, nil)
	if x, ok := c.caseByID("typed-long-tool-ad-input"); ok {
		limit, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendSteerTurn)
		if int64(len(x.Text)) != limit.MessageBytes {
			cap = result("steer-cap", "fail", "parent steer text differs from published Message cap", limit.MessageBytes, len(x.Text))
		} else if owner, p, h, err := c.prompt(x.Text); err == nil && runner.Text(h, "boundary") == "tool_boundary" {
			body, er := PromptBody(owner.Snapshot, p)
			if er == nil {
				cap = rename("steer-cap", CheckWholeInput(WholeInput{body, "steer_turn", runner.Number(p, "transport_at_ms"), owner.Transcript, []string{owner.SnapshotRef, owner.TranscriptRef}}))
			}
		}
	}
	out = append(out, cap, result("timings", "pass", "raw timing distinctions retained; no Claude delivered_confirmed timing is published", "send/return/issue/complete/sighting/order/intake/first-response", timings))
	return out
}

func analyzeGroups(c Capture) []runner.Result {
	var plans []GroupPlan
	if err := c.json("raw/group-plans.json", &plans); err != nil {
		return []runner.Result{errorResult("steer-group", err)}
	}
	var out []runner.Result
	for _, o := range c.Owners {
		if !o.Owner.Native {
			out = append(out, CheckPackingAndOrder(c.packingInput(o)))
		}
	}
	plansValid := len(plans) == 2
	var gaps []Gap
	timings := ExtractTimings(c)
	for _, plan := range plans {
		expected, err := PlanGroup(plan.Mode)
		if err != nil || !reflect.DeepEqual(plan, expected) {
			plansValid = false
			continue
		}
		name := "steer-group"
		gapName, schedule := "S1-to-S2", "consecutive parent PostToolBatch boundaries in G0"
		if plan.Mode == "wait_turn" {
			name = "ordinary-group"
			gapName, schedule = "O1-to-O2", "ordinary consecutive parent turns"
		}
		var checked []runner.Result
		var pair []Timing
		for _, part := range plan.Parts {
			caseID := plan.Cases[part.First]
			owner, p, h, er := c.prompt(plan.Texts[part.First])
			if er != nil {
				checked = append(checked, errorResult(name, er))
				continue
			}
			body, er := PromptBody(owner.Snapshot, p)
			if er != nil {
				checked = append(checked, errorResult(name, er))
				continue
			}
			var expectedTexts [][]byte
			for _, text := range plan.Texts[part.First : part.First+part.Count] {
				expectedTexts = append(expectedTexts, []byte(text))
			}
			if body != string(store.RenderBusySendPromptBody(expectedTexts)) {
				status, reason := "fail", "issued group differs from its complete known candidate partition"
				if groupPartial(c, plan.Mode) {
					status, reason = "not_completed", "group admission hold lost before complete partition; actual partial input is not O1/O2 or S1/S2"
				}
				checked = append(checked, result(name, status, reason, nil, body, owner.SnapshotRef))
				continue
			}
			mode := "steer_turn"
			after := runner.Number(p, "transport_at_ms")
			if plan.Mode == "wait_turn" {
				mode = "wait_turn"
				after = runner.Number(p, "admitted_at_ms")
				if er = requirePhysicalCase(c, caseID, "groups"); er != nil {
					checked = append(checked, errorResult(name, er))
				}
			}
			if mode == "steer_turn" && runner.Text(h, "boundary") == "turn_end" {
				checked = append(checked, result(name, "not_completed", "early-Stop-fallback: no tool boundary after injection; delivered at Stop", plan.Mode, h, owner.SnapshotRef), CheckWholeInput(WholeInput{body, "wait_turn", runner.Number(p, "admitted_at_ms"), owner.Transcript, []string{owner.SnapshotRef, owner.TranscriptRef}}))
				continue
			}
			if (mode == "steer_turn" && runner.Text(h, "boundary") != "tool_boundary") || (mode == "wait_turn" && runner.Text(h, "boundary") != "turn_end") {
				checked = append(checked, result(name, "fail", "group was relabelled or forced", plan.Mode, h))
			}
			checked = append(checked, CheckWholeInput(WholeInput{body, mode, after, owner.Transcript, []string{owner.SnapshotRef, owner.TranscriptRef}}))
			if timing, ok := timingByID(timings, caseID); ok {
				pair = append(pair, timing)
			}
		}
		out = append(out, combine(name, checked...))
		if len(pair) == 2 {
			gaps = append(gaps, NewGap(gapName, schedule, pair[0], pair[1]))
		}
	}
	if !plansValid {
		out = append(out, result("group-plans", "fail", "group plan differs from candidate-derived minimal corpus", nil, plans))
	}
	if err := requirePhysicalCase(c, "G0", "groups"); err != nil {
		out = append(out, errorResult("group-starts", err))
	}
	if producer, ref, err := c.importPhase("force-states"); err == nil {
		forced := forceGroup(producer)
		forced.Evidence = prefixRefs(ref.Relative, forced.Evidence)
		out = append(out, forced)
		first, a := timingByID(ExtractTimings(producer), "F3")
		second, b := timingByID(ExtractTimings(producer), "F4")
		if a && b {
			gaps = append(gaps, NewGap("F3-to-F4", "one sighting/order-gated force call", first, second))
		}
	} else {
		out = append(out, errorResult("force-group-import", err))
	}
	status := "pass"
	if len(gaps) != 3 {
		status = "not_completed"
	}
	out = append(out, result("group-gaps", status, "three schedules retained separately", "S1/S2, O1/O2, F3/F4", gaps), CheckGroupSchedule(c, plans))
	var ordinary runner.Result
	for _, r := range out {
		if r.SignalID == "ordinary-group" {
			ordinary = r
		}
	}
	out = append(out, rename("small-backlog", ordinary), rename("whole-corpus", combine("whole-corpus", out...)))
	return out
}

func forceGroup(c Capture) runner.Result {
	var checks []runner.Result
	if phaseWorkload(c) {
		checks = append(checks, workloadMissing(c, "force-group"))
	}
	var owners []OwnerEvidence
	var prompts, handovers []runner.Row
	for _, id := range []string{"F3", "F4"} {
		x, ok := c.caseByID(id)
		if !ok {
			return result("force-group", "not_completed", "known forced group case missing", id, nil)
		}
		if err := requirePhysicalCase(c, id, "force-states"); err != nil {
			checks = append(checks, errorResult(id, err))
		}
		o, p, h, err := c.prompt(x.Text)
		if err != nil {
			return errorResult("force-group", err)
		}
		owners = append(owners, o)
		prompts = append(prompts, p)
		handovers = append(handovers, h)
		body, err := PromptBody(o.Snapshot, p)
		if err != nil {
			return errorResult("force-group", err)
		}
		checks = append(checks, CheckWholeInput(WholeInput{body, "wait_turn", runner.Number(p, "admitted_at_ms"), o.Transcript, []string{o.SnapshotRef, o.TranscriptRef}}))
	}
	if runner.Text(handovers[0], "handover_id") != runner.Text(handovers[1], "handover_id") || runner.Text(handovers[0], "boundary") != "force" || runner.Text(handovers[0], "force_id") == "" || runner.Number(prompts[1], "prompt_index") != runner.Number(prompts[0], "prompt_index")+1 {
		return result("force-group", "fail", "F3/F4 are not consecutive prompts of one force", nil, handovers)
	}
	command, ok := c.commandNamed("F3-F4-force")
	if !ok {
		return result("force-group", "fail", "group lacks its public force call", nil, nil)
	}
	var response struct {
		Force *api.ForceResult `json:"force_result"`
	}
	if err := c.commandJSON(command.Stdout, &response); err != nil {
		return errorResult("force-group", err)
	}
	owner, stored, err := c.storedForce(command, response.Force)
	if err != nil {
		return errorResult("force-group", err)
	}
	for i, o := range owners {
		if o.Owner.InstanceID != owner.Owner.InstanceID || o.Owner.LaunchID != owner.Owner.LaunchID || o.Owner.LaunchToken != owner.Owner.LaunchToken || !forceHandover(owner, stored, handovers[i]) {
			return result("force-group", "fail", "group input belongs to a different public force or Launch", nil, handovers, command.Stdout)
		}
	}
	checks = append(checks, CheckPackingAndOrder(c.packingInput(owners[0])))
	return rename("force-group", combine("force-group", checks...))
}

func analyzeLimits(c Capture) []runner.Result {
	var out []runner.Result
	var wakes, steers []NativeSample
	for _, id := range []string{"N1", "N2", "N3", "N4", "N5", "N6"} {
		var s NativeSample
		if c.json("raw/native-sample-"+id+".json", &s) == nil {
			wakes = append(wakes, s)
		}
	}
	if err := c.json("raw/native-steer-samples.json", &steers); err != nil {
		out = append(out, errorResult("native-steer-ladder", err))
	}
	nativeBatchIDs := map[string]bool{}
	verify := func(samples []NativeSample) []NativeSample {
		for i := range samples {
			s := &samples[i]
			data, err := c.read(s.Transcript)
			if err != nil {
				s.Whole = false
				s.NextRequest = false
				s.Error = err.Error()
				out = append(out, errorResult("native-evidence/"+s.Rung.ID, err))
				continue
			}
			o, ok := c.ownerSession(s.Session)
			if !ok || !o.Owner.Native {
				s.Whole = false
				s.NextRequest = false
				s.Error = "native sample lacks its direct owned session"
				continue
			}
			transcript, err := parseOwnedTranscript(data, s.Session)
			if err != nil {
				s.Whole = false
				s.NextRequest = false
				s.Error = err.Error()
				out = append(out, errorResult("native-evidence/"+s.Rung.ID, err))
				continue
			}
			var emission NativeEmission
			if err = c.json(s.Receipt, &emission); err != nil || emission.Case != s.Rung.ID || emission.Session != s.Session || emission.BodySHA != runner.Digest([]byte(s.Rung.Body)) {
				s.Whole = false
				s.NextRequest = false
				s.Error = "native output receipt/corpus mismatch"
				out = append(out, result("native-evidence/"+s.Rung.ID, "fail", s.Error, nil, emission, s.Receipt))
				continue
			}
			output, err := c.read(emission.Output)
			if err != nil {
				s.Whole = false
				s.NextRequest = false
				continue
			}
			stdout, stderr, code, err := NativeOutput(emission.Event, "", s.Rung.Body)
			expected := stdout
			if len(stderr) > 0 {
				expected = stderr
			}
			if err != nil || string(output) != string(expected) || emission.Exit != code {
				s.Whole = false
				s.NextRequest = false
				s.Error = "native stream/exit shape differs"
				out = append(out, result("native-evidence/"+s.Rung.ID, "fail", s.Error, nil, emission, s.Receipt))
				continue
			}
			if s.Rung.Route == "post_tool_batch" {
				var payload runner.Row
				er := c.json(emission.Input, &payload)
				ids := toolIDs(payload)
				matched := false
				if er == nil && len(ids) == 1 && !nativeBatchIDs[ids[0]] && runner.Text(payload, "session_id") == s.Session && runner.Text(payload, "agent_id") == "" {
					for _, h := range c.Hooks {
						if h.Receipt.Event == "PreToolUse" && runner.Text(h.Payload, "session_id") == s.Session && runner.Text(h.Payload, "agent_id") == "" && runner.Text(h.Payload, "tool_use_id") == ids[0] && !h.Receipt.Received.After(emission.Started) {
							input, _ := h.Payload["tool_input"].(map[string]any)
							bg, _ := input["run_in_background"].(bool)
							matched = runner.Text(h.Payload, "tool_name") == "Bash" && !bg && input["command"] == "for i in 1 2; do sleep 1; done"
						}
					}
					nativeBatchIDs[ids[0]] = true
				}
				if !matched {
					s.Whole = false
					s.NextRequest = false
					s.Error = "insufficient-batches: native emission lacks a unique foreground parent batch"
					out = append(out, result("native-evidence/"+s.Rung.ID, "not_completed", s.Error, nil, emission, s.Receipt, emission.Input))
					continue
				}
			}
			s.Emission = emission
			mode := "wait_turn"
			if s.Rung.Route == "post_tool_batch" {
				mode = "steer_turn"
			}
			whole := CheckWholeInput(WholeInput{s.Rung.Body, mode, emission.Started.UnixMilli(), transcript, []string{s.Receipt, s.Transcript}})
			s.Whole = whole.Status == "pass"
			s.NextRequest = false
			for _, q := range transcript.Requests {
				if q.Time.UnixMilli() >= emission.Started.UnixMilli() {
					s.NextRequest = true
					break
				}
			}
			if in, ok := intakeFromResult(whole); ok {
				s.Intake = in
			}
		}
		return samples
	}
	out = append(out, JudgeSizeLadder("wake", verify(wakes)), JudgeSizeLadder("post_tool_batch", verify(steers)))
	var proof BacklogProof
	if err := c.json("raw/backlog-proof.json", &proof); err != nil {
		out = append(out, errorResult("large-backlog", err))
	} else {
		if s, _, ok := c.snapshotCase("backlog-1000-before-refusals"); ok {
			proof.Before = s
		}
		if s, _, ok := c.snapshotCase("backlog-1000-after-refusals"); ok {
			proof.After = s
		}
		if s, _, ok := c.snapshotCase("backlog-reuse-finalized"); ok {
			proof.Final = s
		}
		proof.Exited = false
		for _, owner := range c.Owners {
			if strings.HasSuffix(owner.Owner.InstanceID, "-N8") && owner.Owner.Exited {
				proof.Exited = true
				break
			}
		}
		proof.Lookups = map[string]bool{}
		for _, id := range []string{"backlog-0001", "backlog-0500", "backlog-1000"} {
			if command, ok := c.commandNamed(id + "-final-lookup"); ok {
				var response api.GetDeliveryResult
				if c.commandJSON(command.Stdout, &response) == nil && response.Lookup == "found" && response.Result != nil && response.Result.Final && response.Result.Class == "not_started" {
					proof.Lookups[id] = true
				}
			}
		}
		out = append(out, verifyBacklogRecords(c, proof), combine("large-backlog", CheckBacklog(proof), checkBacklogHold(c, proof)))
	}
	out = append(out, fallbackResult(c, "scheduler/full-backlog-drain", "TestClaudeWakeDrainsBacklog"))
	var capChecks []runner.Result
	if force, ref, err := c.importPhase("force-states"); err != nil {
		capChecks = append(capChecks, errorResult("wake-caps", err))
	} else {
		for _, id := range []string{"F1", "F2"} {
			capChecks = append(capChecks, prefixResult(ref.Relative, checkCap(force, id, "wait_turn")))
		}
	}
	if typed, ref, err := c.importPhase("typed-controls"); err != nil {
		capChecks = append(capChecks, errorResult("steer-cap", err))
	} else {
		capChecks = append(capChecks, prefixResult(ref.Relative, checkCap(typed, "typed-long-tool-ad-input", "steer_turn")))
	}
	out = append(out, combine("ad-caps", capChecks...))
	var refusalChecks []runner.Result
	for _, id := range []string{"backlog-over-text", "backlog-over-count"} {
		command, ok := c.commandNamed(id)
		if !ok {
			refusalChecks = append(refusalChecks, result(id, "not_completed", "refusal command missing", nil, nil))
			continue
		}
		details, err := c.refusal(command)
		if err != nil {
			refusalChecks = append(refusalChecks, errorResult(id, err))
		} else {
			refusalChecks = append(refusalChecks, result(id, "pass", "machine-readable limit refusal retained", details.Reason, details, command.Stdout, command.Stderr))
		}
	}
	if group, ref, err := c.importPhase("groups"); err == nil {
		checks := analyzeGroups(group)
		for _, r := range checks {
			if r.SignalID == "small-backlog" {
				out = append(out, prefixResult(ref.Relative, r))
			}
		}
	} else {
		out = append(out, errorResult("small-backlog", err))
	}
	out = append(out, combine("ad-refusals", refusalChecks...))
	for _, id := range []string{"N1", "N2", "N3", "N4", "N5", "N6", "N7", "N8"} {
		needed := true
		if id == "N6" {
			needed = false
			for _, sample := range wakes {
				if sample.Rung.ID == id {
					needed = true
				}
			}
		}
		if needed {
			if er := requirePhysicalCase(c, id, "text-limits-backlog"); er != nil {
				out = append(out, errorResult("physical/"+id, er))
			}
		}
	}
	return out
}
func prefixResult(prefix string, r runner.Result) runner.Result {
	r.Evidence = prefixRefs(prefix, r.Evidence)
	r.Observed = prefixComponentEvidence(prefix, r.Observed)
	return r
}

// Combined judgments retain their own evidence. Prefix every nested component
// when a capture is exported so b3 gaps never point back at unexported VM paths.
func prefixComponentEvidence(prefix string, value any) any {
	switch v := value.(type) {
	case runner.Result:
		return prefixResult(prefix, v)
	case []runner.Result:
		out := make([]runner.Result, len(v))
		for i, r := range v {
			out[i] = prefixResult(prefix, r)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = prefixComponentEvidence(prefix, child)
		}
		return out
	case map[string]any:
		if id, ok := v["signal_id"].(string); ok && id != "" {
			b, _ := json.Marshal(v)
			var r runner.Result
			if json.Unmarshal(b, &r) == nil && r.Status != "" {
				return prefixResult(prefix, r)
			}
		}
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = prefixComponentEvidence(prefix, child)
		}
		return out
	}
	return value
}
func checkCap(c Capture, id, mode string) runner.Result {
	x, ok := c.caseByID(id)
	if !ok {
		return result(id, "not_completed", "cap case missing", nil, nil)
	}
	limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
	if int64(len(x.Text)) != limits.MessageBytes {
		return result(id, "fail", "Message differs from published cap", limits.MessageBytes, len(x.Text))
	}
	owner, p, _, err := c.prompt(x.Text)
	if err != nil {
		return errorResult(id, err)
	}
	body, err := PromptBody(owner.Snapshot, p)
	if err != nil {
		return errorResult(id, err)
	}
	after := runner.Number(p, "transport_at_ms")
	if mode == "wait_turn" {
		after = runner.Number(p, "admitted_at_ms")
	}
	return rename(id, CheckWholeInput(WholeInput{body, mode, after, owner.Transcript, []string{owner.SnapshotRef, owner.TranscriptRef}}))
}
func (c Capture) ownerSession(sid string) (OwnerEvidence, bool) {
	for _, o := range c.Owners {
		if o.Owner.SessionID == sid {
			return o, true
		}
	}
	return OwnerEvidence{}, false
}
func (c Capture) refusal(command runner.Command) (api.CantSendDetails, error) {
	if command.ExitCode == 0 {
		return api.CantSendDetails{}, corrupt("expected semantic refusal succeeded")
	}
	var envelope struct {
		Details api.CantSendDetails `json:"err_details"`
	}
	for _, ref := range []string{command.Stdout, command.Stderr} {
		b, err := c.read(ref)
		if err != nil {
			return envelope.Details, err
		}
		if json.Unmarshal(b, &envelope) == nil && envelope.Details.Class == "cant_send" {
			return envelope.Details, nil
		}
	}
	return envelope.Details, corrupt("command lacks machine-readable CantSend refusal")
}

func parseOwnedTranscript(data []byte, session string) (signals.Transcript, error) {
	return signals.ParseTranscript(data, session)
}

func verifyBacklogRecords(c Capture, p BacklogProof) runner.Result {
	var checks []runner.Result
	for _, a := range p.Accepted {
		command, ok := c.Commands[a.Output]
		var value api.DeliveryResult
		if !ok || command.ExitCode != 0 || c.commandJSON(a.Output, &value) != nil || !reflect.DeepEqual(a.Result, value) || value.RequestID != a.ID || value.CallerUUID != c.Manifest.RunID {
			return result("backlog-raw-acceptance", "fail", "acceptance differs from its original public Result", nil, a, a.Output)
		}
		text, err := Corpus(a.ID, 1900)
		if err != nil {
			return errorResult("backlog-raw-acceptance", err)
		}
		if !containsArg(command.Argv, "--text", text) || !containsArg(command.Argv, "--delivery-request-id", a.ID) {
			return result("backlog-raw-acceptance", "fail", "acceptance command text/pair differs", nil, a, a.Output)
		}
	}
	for _, r := range p.Refusals {
		command, ok := c.commandNamed(r.Case)
		if !ok {
			checks = append(checks, result(r.Case, "not_completed", "raw refusal command missing", nil, nil))
			continue
		}
		details, err := c.refusal(command)
		if err != nil {
			checks = append(checks, errorResult(r.Case, err))
			continue
		}
		if details.Limit == nil || details.Reason != r.Reason || details.Limit.Actual != r.Actual || details.Limit.Allowed != r.Allowed || details.RetryWith != r.RetryWith || details.ResendSafe != r.ResendSafe {
			checks = append(checks, result(r.Case, "fail", "raw refusal details differ from summary", nil, r))
			continue
		}
		lookup, ok := c.commandNamed(r.Case + "-unclaimed-lookup")
		var value api.GetDeliveryResult
		if !ok || c.commandJSON(lookup.Stdout, &value) != nil {
			checks = append(checks, result(r.Case, "not_completed", "refused-pair Lookup missing", nil, nil))
			continue
		}
		status := "pass"
		if value.Lookup != "unknown" || value.Result != nil {
			status = "fail"
		}
		checks = append(checks, result(r.Case, status, "refusal claimed no confirmed pair", "unknown", value, lookup.Stdout))
	}
	if len(p.Accepted) != 1000 {
		return result("backlog-raw-acceptance", "not_completed", "complete raw acceptance set missing", 1000, len(p.Accepted))
	}
	return combine("backlog-raw-acceptance", checks...)
}
