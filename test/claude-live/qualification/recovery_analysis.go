package qualification

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// These judgments use the same sealed raw files as offline analysis. A
// collector summary is an index into those facts, never proof by itself.
func CheckLifecycleAndHookLimits(c Capture) []runner.Result {
	if c.Manifest.Phase == "user-hooks" {
		return checkUserHooks(c)
	}
	var out []runner.Result
	var receipt LostClientReceipt
	var interrupted struct {
		Receipt      LostClientReceipt
		Command      runner.Command
		ResponseLost bool `json:"response_lost"`
	}
	err := c.json("raw/lost-client-accepted.json", &receipt)
	if err == nil {
		err = c.json("raw/lost-client-interrupted.json", &interrupted)
	}
	if err != nil {
		out = append(out, errorResult("lost-reply", err))
	} else {
		status := "pass"
		reason := "real owned caller lost its reply; repeated pair retained one owner"
		if receipt.PID <= 0 || receipt.ProcessStart == "" || receipt.Pair != "L2" || receipt.Accepted.IsZero() || !reflect.DeepEqual(receipt, interrupted.Receipt) || !interrupted.ResponseLost || interrupted.Command.ExitCode == 0 {
			status = "fail"
			reason = "lost-reply process/receipt identity is contradictory"
		}
		before, ref, ok := c.snapshotCase("lost-client-durable-acceptance")
		x, xok := c.caseByID("L2")
		matches := 0
		for _, m := range before.Tables["messages"] {
			if runner.Text(m, "text") == x.Text {
				matches++
			}
		}
		if !ok || !xok {
			status = "not_completed"
		} else if matches != 1 {
			status = "fail"
		}
		for _, id := range []string{"L2-repeat-before-crash", "L2-repeat-during-resume"} {
			r, er := deliveryCommand(c, id)
			if er != nil {
				out = append(out, errorResult("lost-reply/"+id, er))
				continue
			}
			if r.RequestID != "L2" || r.CallerUUID != c.Manifest.RunID || r.Class == "delivered_confirmed" {
				status = "fail"
			}
		}
		out = append(out, result("lost-reply", status, reason, "one stored owner and original pair", receipt, ref, "raw/lost-client-interrupted.json"))
	}
	out = append(out, checkResume(c, "crash-resume", "L1-crash-owned-agent", "L2-resume", "L2"))
	lookup, err := lookupCommand(c, "lifecycle-fresh-final-lookup")
	if err != nil {
		out = append(out, errorResult("fresh-reuse", err))
	} else {
		status := "pass"
		if !lookup.Final || lookup.Class != "not_started" || !lookup.ResendSafe || lookup.RetryWith != "new_id" {
			status = "fail"
		}
		reuse, ok := c.commandNamed("lifecycle-fresh-reuse")
		old, newSID := "", ""
		for _, o := range c.Owners {
			if strings.HasSuffix(o.Owner.InstanceID, "-L1") {
				if old == "" {
					old = o.Owner.SessionID
				}
				newSID = o.Owner.SessionID
			}
		}
		if !ok || old == "" || newSID == "" {
			status = "not_completed"
		} else if old == newSID || !hasFlag(reuse.Argv, "--reuse-finished") {
			status = "fail"
		}
		out = append(out, result("fresh-reuse", status, "fresh conversation finalizes never-issued custody without replay", "new session; final not_started", lookup, reuse.Stdout))
	}
	repeat, a := c.commandNamed("L2-repeat-during-resume")
	force, b := c.commandNamed("L2-concurrent-force")
	if !a || !b {
		out = append(out, result("concurrent-delivery", "not_completed", "concurrent public callers missing", nil, nil))
	} else if repeat.Started.Before(force.Ended) && force.Started.Before(repeat.Ended) {
		checks := []runner.Result{fallbackResult(c, "scheduler/concurrent-callbacks", "TestBusySendWakeOrderRaces")}
		for _, o := range c.Owners {
			issued := false
			for _, h := range o.Snapshot.Tables["handovers"] {
				if runner.Text(h, "launch_token") != o.Owner.LaunchToken {
					continue
				}
				for _, p := range o.Snapshot.Tables["prompts"] {
					if runner.Text(p, "handover_id") == runner.Text(h, "handover_id") && runner.Text(p, "emission") == "issued" {
						issued = true
					}
				}
			}
			if issued {
				checks = append(checks, CheckPackingAndOrder(c.packingInput(o)))
			}
		}
		out = append(out, combine("concurrent-delivery", checks...))
	} else {
		out = append(out, result("concurrent-delivery", "not_completed", "call windows did not overlap; deterministic case required", nil, []runner.Command{repeat, force}), fallbackResult(c, "scheduler/concurrent-callbacks", "TestBusySendWakeOrderRaces"))
	}
	var reads []runner.Result
	for _, id := range []string{"lifecycle-busy-held", "lifecycle-final-held"} {
		before, br, a := c.snapshotCase(id + "-reads-before")
		after, ar, b := c.snapshotCase(id + "-reads-after")
		if !a || !b {
			reads = append(reads, result(id, "not_completed", "bounded read window missing", nil, nil))
			continue
		}
		same := true
		for _, table := range []string{"messages", "handovers", "prompts", "members", "forces"} {
			same = same && reflect.DeepEqual(before.Tables[table], after.Tables[table])
		}
		status := "pass"
		if !same {
			status = "fail"
		}
		reads = append(reads, result(id, status, "get/status/list/Lookup/sweep did not issue work", true, same, br, ar))
	}
	out = append(out, combine("read-only", reads...))
	for _, id := range []string{"L1", "L2"} {
		if er := requirePhysicalCase(c, id, "lifecycle"); er != nil {
			out = append(out, errorResult("physical/"+id, er))
		}
	}
	return out
}

func deliveryCommand(c Capture, name string) (api.DeliveryResult, error) {
	command, ok := c.commandNamed(name)
	if !ok {
		return api.DeliveryResult{}, absent("missing public command %s", name)
	}
	var r api.DeliveryResult
	if err := c.commandJSON(command.Stdout, &r); err != nil {
		return r, err
	}
	if command.ExitCode != 0 || r.Class == "" {
		return r, corrupt("public delivery command %s did not succeed", name)
	}
	return r, nil
}
func lookupCommand(c Capture, name string) (api.DeliveryResult, error) {
	command, ok := c.commandNamed(name)
	if !ok {
		return api.DeliveryResult{}, absent("missing Lookup %s", name)
	}
	var r api.GetDeliveryResult
	if err := c.commandJSON(command.Stdout, &r); err != nil {
		return api.DeliveryResult{}, err
	}
	if command.ExitCode != 0 || r.Lookup != "found" || r.Result == nil {
		return api.DeliveryResult{}, corrupt("Lookup %s lacks a retained Result", name)
	}
	return *r.Result, nil
}
func checkResume(c Capture, id, stopName, resumeName, caseID string) runner.Result {
	stop, a := c.commandNamed(stopName)
	resume, b := c.commandNamed(resumeName)
	x, has := c.caseByID(caseID)
	if !a || !b || !has {
		return result(id, "not_completed", "owned stop/resume/corpus missing", nil, nil)
	}
	iid := x.InstanceID
	var old, newOwner *OwnerEvidence
	for i := range c.Owners {
		o := &c.Owners[i]
		if o.Owner.InstanceID != iid {
			continue
		}
		if !o.Owner.Started.After(stop.Started) {
			old = o
		}
		if !o.Owner.Started.Before(stop.Ended) && !o.Owner.Started.After(resume.Ended) {
			newOwner = o
			break
		}
	}
	if old == nil || newOwner == nil {
		return result(id, "not_completed", "distinct owned Launch records missing", nil, nil, stop.Stdout, resume.Stdout)
	}
	if !old.Owner.Exited || old.Owner.StoppedAt.After(resume.Started) || old.Owner.SessionID != newOwner.Owner.SessionID || old.Owner.LaunchToken == newOwner.Owner.LaunchToken || stop.ExitCode != 0 || resume.ExitCode != 0 {
		return result(id, "fail", "resume did not preserve conversation after actual old exit", nil, []runner.Owned{old.Owner, newOwner.Owner}, stop.Stdout, resume.Stdout)
	}
	if hasFlag(resume.Argv, "--text") || hasFlag(resume.Argv, "--prompt") {
		return result(id, "fail", "resume replayed input", nil, resume)
	}
	owner, p, _, err := c.prompt(x.Text)
	if err != nil {
		return errorResult(id, err)
	}
	if owner.Owner.LaunchToken != newOwner.Owner.LaunchToken {
		return result(id, "fail", "carried input bound to the wrong Launch", nil, p)
	}
	body, err := PromptBody(owner.Snapshot, p)
	if err != nil {
		return errorResult(id, err)
	}
	return rename(id, CheckWholeInput(WholeInput{body, "wait_turn", runner.Number(p, "admitted_at_ms"), owner.Transcript, []string{stop.Stdout, resume.Stdout, owner.SnapshotRef, owner.TranscriptRef}}))
}

func checkUserHooks(c Capture) []runner.Result {
	var out []runner.Result
	receipts := map[string]UserHookReceipt{}
	for ref := range c.Manifest.Artifacts {
		if !strings.HasPrefix(ref, "raw/user-hook-fixtures/") || !strings.HasSuffix(ref, "/receipt.json") {
			continue
		}
		var r UserHookReceipt
		if err := c.json(ref, &r); err != nil {
			out = append(out, errorResult("user-hook-receipt", err))
			continue
		}
		if _, exists := receipts[r.Action]; exists {
			out = append(out, result("user-hook-receipt", "fail", "duplicate behavior receipt", nil, r))
		}
		receipts[r.Action] = r
		var payload runner.Row
		var output map[string]any
		if err := c.json(r.Input, &payload); err != nil {
			out = append(out, errorResult("user-hook-receipt", err))
			continue
		}
		if err := c.json(r.Output, &output); err != nil {
			out = append(out, errorResult("user-hook-receipt", err))
			continue
		}
		if runner.Text(payload, "session_id") != r.Session || runner.Text(payload, "prompt_id") != r.Prompt || runner.Text(payload, "hook_event_name") != "UserPromptSubmit" || r.Written.Before(r.Started) {
			out = append(out, result("user-hook-receipt", "fail", "behavior receipt does not bind its native input", nil, r))
		}
	}
	for _, v := range []struct{ id, action, caseID string }{{"hook-block", "block", "H1"}, {"hook-context", "additionalContext", "H2"}} {
		r, ok := receipts[v.action]
		if !ok {
			out = append(out, result(v.id, "not_completed", "separately named behavior fixture receipt missing", v.action, nil))
			continue
		}
		owner, owned := c.owner(r.Instance, "")
		x, has := c.caseByID(v.caseID)
		if !owned || !has || owner.Owner.SessionID != r.Session {
			out = append(out, result(v.id, "fail", "fixture lacks owned parent/case", nil, r))
			continue
		}
		var native runner.Row
		if err := c.json(r.Input, &native); err != nil {
			out = append(out, errorResult(v.id, err))
			continue
		}
		matches := 0
		for _, h := range c.Hooks {
			if h.Receipt.Event == "UserPromptSubmit" && runner.Text(h.Payload, "agent_id") == "" && runner.Text(h.Payload, "session_id") == r.Session && runner.Text(h.Payload, "prompt_id") == r.Prompt && runner.Text(h.Payload, "prompt") == runner.Text(native, "prompt") {
				matches++
			}
		}
		if matches != 1 || runner.Text(native, "cwd") != owner.Owner.CWD {
			out = append(out, result(v.id, "fail", "behavior fixture is not bound to one passive owned parent occurrence", 1, matches, r.Input))
			continue
		}
		var output map[string]any
		err := c.json(r.Output, &output)
		valid := err == nil
		if v.action == "block" {
			valid = valid && output["decision"] == "block"
		} else {
			specific, _ := output["hookSpecificOutput"].(map[string]any)
			valid = valid && specific["hookEventName"] == "UserPromptSubmit" && specific["additionalContext"] == HookContext
		}
		data, _ := json.Marshal(output)
		if strings.Contains(string(data), "updatedPrompt") || !valid {
			out = append(out, result(v.id, "fail", "fixture emitted an invented field or wrong native output", nil, output, r.Output))
			continue
		}
		sent, er := deliveryCommand(c, v.caseID)
		if er != nil {
			out = append(out, errorResult(v.id, er))
			continue
		}
		if sent.Class == "delivered_confirmed" {
			out = append(out, result(v.id, "fail", "Claude fixture was falsely confirmed", nil, sent))
			continue
		}
		if v.action == "additionalContext" {
			contextSeen := false
			for _, q := range owner.Transcript.Requests {
				b, _ := json.Marshal(q)
				if strings.Contains(string(b), HookContext) {
					contextSeen = true
				}
			}
			if !contextSeen {
				out = append(out, result(v.id, "not_completed", "additionalContext model request evidence missing", HookContext, nil, r.Output, owner.TranscriptRef))
			} else {
				out = append(out, result(v.id, "pass", "native additionalContext judged independently from block and replacement", HookContext, sent, r.Output, owner.TranscriptRef))
			}
		} else {
			leaked := false
			for _, q := range owner.Transcript.Requests {
				if q.Time.Before(r.Started) {
					continue
				}
				b, _ := json.Marshal(q)
				leaked = leaked || strings.Contains(string(b), x.Text)
			}
			status := "pass"
			if leaked {
				status = "fail"
			}
			if !owner.Owner.Exited || owner.Owner.StoppedAt.Before(r.Written) {
				status = "not_completed"
			}
			out = append(out, result(v.id, status, "genuine block output and bounded terminal model-input observation", false, leaked, r.Input, r.Output, owner.TranscriptRef))
			_, p, _, pe := c.prompt(x.Text)
			if pe == nil && runner.Text(p, "admission_kind") == "prompt_sighting" && runner.Number(p, "order_at_ms") > 0 && runner.Number(p, "admitted_at_ms") <= r.Written.UnixMilli() {
				out = append(out, accepted("post-sighting-hook", "user_hook_after_sighting", "AD sighting/order preceded a genuine user block; input remains unconfirmed", p, r.Output, owner.SnapshotRef))
			} else {
				out = append(out, result("post-sighting-hook", "not_completed", "genuine block was not independently ordered after AD sighting; no fabricated hook order", "actual post-sighting hook decision", p, r.Input, r.Output))
			}
		}
	}
	out = append(out, checkNeverSighted(c), fallbackResult(c, "stale-unknown", "TestBusySendSightingRefusals"))
	for _, id := range []string{"H1", "H2"} {
		if err := requirePhysicalCase(c, id, "user-hooks"); err != nil {
			out = append(out, errorResult("physical/"+id, err))
		}
	}
	return out
}
func checkNeverSighted(c Capture) runner.Result {
	x, ok := c.caseByID("H2")
	if !ok {
		return result("never-sighted-hold", "not_completed", "terminal ambiguous wake missing", nil, nil)
	}
	owner, p, _, err := c.prompt(x.Text)
	if err != nil {
		return errorResult("never-sighted-hold", err)
	}
	if runner.Number(p, "order_at_ms") != 0 || runner.Text(p, "admission_kind") == "prompt_sighting" {
		return result("never-sighted-hold", "not_completed", "fixture was sighted; it cannot establish the never-sighted limit", nil, p)
	}
	// Only the named hold followers may follow this terminal fixture on its Launch.
	for _, later := range c.Manifest.Cases {
		if later.InstanceID != x.InstanceID || later.ID == x.ID || later.ID == "H2-hold-wait_turn" || later.ID == "H2-hold-steer_turn" {
			continue
		}
		if !later.Started.Before(x.Started) && (owner.Owner.StoppedAt.IsZero() || later.Started.Before(owner.Owner.StoppedAt)) {
			return result("never-sighted-hold", "fail", "unrelated case follows the terminal fixture before Launch End", nil, later)
		}
	}
	var refs []string
	for _, id := range []string{"H2-hold-before-boundary", "H2-hold-after-boundary", "H2-hold-after-Stop"} {
		s, ref, has := c.snapshotCase(id)
		if !has {
			return result("never-sighted-hold", "not_completed", "required boundary/Stop hold observation missing", id, nil)
		}
		refs = append(refs, ref)
		for _, mode := range []string{"wait_turn", "steer_turn"} {
			f, found := c.caseByID("H2-hold-" + mode)
			if !found {
				return result("never-sighted-hold", "not_completed", "named follower missing", mode, nil)
			}
			count := 0
			for _, m := range s.Tables["messages"] {
				if runner.Text(m, "text") == f.Text {
					count++
					if runner.Text(m, "place") != "held" {
						return result("never-sighted-hold", "fail", "follower escaped the unsighted wake", nil, m, ref)
					}
				}
			}
			if count != 1 {
				return result("never-sighted-hold", "fail", "follower custody is not unique", 1, count, ref)
			}
		}
	}
	stop := false
	for _, h := range c.Hooks {
		stop = stop || (h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == owner.Owner.SessionID && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(x.Started) && !h.Receipt.Received.After(owner.Owner.StoppedAt))
	}
	if !stop || !owner.Owner.Exited {
		return result("never-sighted-hold", "not_completed", "terminal Stop/actual End missing", nil, nil)
	}
	guard := fallbackResult(c, "scheduler/never-sighted-hold", "TestBusySendWakeOrderAcrossEnd")
	return combine("never-sighted-hold", guard, accepted("never-sighted-hold-observed", "never_sighted_hold", "both modes remained Held across tool boundary and Stop until actual Launch End", p, refs...))
}

func CheckEscRecovery(c Capture) []runner.Result {
	var out []runner.Result
	var notifications, placements []runner.Result
	for round := 1; round <= 2; round++ {
		id := fmt.Sprintf("esc-%d", round)
		name := "esc-typed-recovery"
		held := "E3"
		if round == 2 {
			name = "esc-resume-recovery"
			held = "E4"
		}
		var rec EscObservation
		if err := c.json("raw/"+id+"-gap.json", &rec); err != nil {
			out = append(out, errorResult(name, err))
			continue
		}
		if rec.ID != id || rec.Held != held || rec.Escape.IsZero() || rec.Delivery.AwaitingTurnStart <= 0 || rec.Result.Class != "held" || rec.Result.Wait == nil || rec.Result.Wait.Reason != "readiness_unknown" {
			out = append(out, result(name, "fail", "Esc gap identity/readiness/Held facts conflict", nil, rec))
			continue
		}
		var force ForceObservation
		data, err := c.read(rec.Force)
		if err == nil {
			force, err = ParseForce(data)
		}
		if err != nil || force.Outcome != "refused" || force.Reason != "input_unresolved" {
			out = append(out, result(name, "fail", "force with nonempty Held did not refuse input_unresolved", nil, force, rec.Force))
			continue
		}
		owner, ok := c.owner(rec.Instance, "")
		if !ok {
			out = append(out, result(name, "not_completed", "owned Esc parent missing", nil, nil))
			continue
		}
		window := rec.Notification
		status := "pass"
		reason := "mandatory bounded idle-prompt Notification observation retained"
		if !window.Observed || window.Session != owner.Owner.SessionID || window.Started.Before(rec.Escape) || window.Ended.Before(window.Started) {
			status = "not_completed"
		}
		actual := []HookFact{}
		for _, h := range c.Hooks {
			if h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == window.Session && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(rec.Escape) && !h.Receipt.Received.After(window.Ended) {
				status = "fail"
				reason = "Stop occurred inside the claimed unresolved Esc gap"
			}
			if runner.Text(h.Payload, "session_id") != window.Session || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(window.Started) || h.Receipt.Received.After(window.Ended) {
				continue
			}
			if h.Receipt.Event == "Notification" {
				actual = append(actual, h)
			}
			if h.Receipt.Event == "Stop" {
				status = "fail"
				reason = "Stop occurred inside the claimed unresolved Esc gap"
			}
		}
		if !reflect.DeepEqual(actual, window.Notifications) && !(len(actual) == 0 && len(window.Notifications) == 0) {
			status = "fail"
		}
		idlePrompt := false
		for _, h := range actual {
			idlePrompt = idlePrompt || runner.Text(h.Payload, "notification_type") == "idle_prompt"
		}
		if !idlePrompt && window.Ended.Sub(window.Started) < 19*time.Second {
			status = "not_completed"
		}
		notifications = append(notifications, result(id+"-notification", status, reason, "idle_prompt payload/time or >=19s bounded absence", window, "raw/"+id+"-gap.json"))
		var whole struct {
			Body   string
			Prompt runner.Row
		}
		ref := "raw/" + rec.Steer + "-whole-steer.json"
		if err = c.json(ref, &whole); err != nil {
			placements = append(placements, errorResult(id, err))
		} else {
			data, er := c.read("raw/" + rec.Steer + "-before-Escape.jsonl")
			if er != nil {
				placements = append(placements, errorResult(id, er))
			} else {
				tr, er := parseOwnedTranscript(data, owner.Owner.SessionID)
				if er != nil {
					placements = append(placements, errorResult(id, er))
				} else {
					placement := CheckWholeInput(WholeInput{whole.Body, "steer_turn", runner.Number(whole.Prompt, "transport_at_ms"), tr, []string{ref, "raw/" + rec.Steer + "-before-Escape.jsonl"}})
					if intake, ok := intakeFromResult(placement); placement.Status == "pass" && (!ok || !intake.Time.Before(rec.Escape)) {
						placement.Status = "fail"
						placement.Reason = "whole steer intake did not precede actual Escape"
					}
					placements = append(placements, rename(id, placement))
				}
			}
		}
		if round == 1 {
			x, has := c.caseByID("E2")
			if !has {
				out = append(out, result(name, "not_completed", "fresh typed recovery missing", nil, nil))
				continue
			}
			stopFound := false
			timings := ExtractTimings(c)
			typed, tok := timingByID(timings, "E2")
			carried, cok := timingByID(timings, "E3")
			for _, h := range c.Hooks {
				if h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == window.Session && runner.Text(h.Payload, "agent_id") == "" && h.Receipt.Received.After(x.Started) && !carried.Issue.IsZero() && h.Receipt.Received.UnixMilli() <= carried.Issue.UnixMilli() {
					stopFound = true
				}
			}
			if !stopFound || !tok || !cok || !typed.Whole || !carried.Whole || carried.Sighting.Before(typed.Intake) {
				out = append(out, result(name, "not_completed", "fresh typed whole turn, Stop and subsequent carried intake are required", nil, []Timing{typed, carried}))
			} else {
				out = append(out, accepted(name, "esc_gap", "fresh typed turn and its Stop released E3", []Timing{typed, carried}, typed.Evidence...))
			}
		} else {
			out = append(out, checkResume(c, name, "esc-keyless-pause", "esc-keyless-resume", "E4"))
		}
	}
	out = append(out, combine("esc-notification", notifications...), combine("esc-input-placement", placements...))
	for _, id := range []string{"E1", "E2", "E3", "E4"} {
		if err := requirePhysicalCase(c, id, "esc-gap"); err != nil {
			out = append(out, errorResult("physical/"+id, err))
		}
	}
	return out
}

func hasFlag(args []string, key string) bool {
	for _, arg := range args {
		if arg == key || strings.HasPrefix(arg, key+"=") {
			return true
		}
	}
	return false
}
