package qualification

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func analyzeForces(c Capture) []runner.Result {
	doc, err := c.read("raw/caller-contract.md")
	if err != nil {
		return []runner.Result{errorResult("force-table", err)}
	}
	contract, err := ParseContract(doc)
	if err != nil {
		return []runner.Result{errorResult("force-table", err)}
	}
	var rows []runner.Result
	for _, pair := range []struct{ state, command, caseID string }{{"not_started", "not-started-force", "F1"}, {"idle", "F1-idle-force", "F1"}, {"tool_running", "F2-force", "F2"}, {"between_tools", "F3-F4-force", "F3"}, {"compacting", "F5", "F5"}, {"ended", "ended-force", "ended-held"}} {
		rows = append(rows, forceState(c, contract, pair.state, pair.command, pair.caseID))
	}
	// Dialogs owns permission/plan starts and verdict rows; the summary
	// assembles the complete measured table after that phase has run.
	pending := pendingForce(c)
	group := forceGroup(c)
	var caps []runner.Result
	for _, id := range []string{"F1", "F2"} {
		caps = append(caps, checkCap(c, id, "wait_turn"))
	}
	table := combine("force-table", rows...)
	return append(rows, table, rename("force-pending", pending), group, combine("wake-caps", caps...))
}

func forceState(c Capture, contract Contract, state, commandName, caseID string) runner.Result {
	command, ok := c.commandNamed(commandName)
	if !ok {
		return result("force/"+state, "not_completed", "force call absent", state, nil)
	}
	var response struct {
		Force *api.ForceResult `json:"force_result"`
	}
	if err := c.commandJSON(command.Stdout, &response); err != nil {
		return errorResult("force/"+state, err)
	}
	if response.Force == nil {
		return result("force/"+state, "fail", "public force_result missing", nil, nil, command.Stdout)
	}
	f := response.Force
	forceOwner, stored, err := c.storedForce(command, f)
	if err != nil {
		return resultForceError(state, err.Error(), f, command.Stdout)
	}
	obs := ForceObservation{State: state, Outcome: f.Outcome, Reason: f.Reason, ForceID: f.ForceID, Started: command.Started, Returned: command.Ended, Evidence: []string{command.Stdout}}
	x, hasCase := c.caseByID(caseID)
	if hasCase {
		owner, p, h, err := c.prompt(x.Text)
		if err == nil && f.Outcome != "refused" {
			if owner.Owner.InstanceID != forceOwner.Owner.InstanceID || owner.Owner.LaunchID != forceOwner.Owner.LaunchID || owner.Owner.LaunchToken != forceOwner.Owner.LaunchToken || !forceHandover(forceOwner, stored, h) {
				return resultForceError(state, "public force does not own the admitted handover", f, command.Stdout)
			}
			obs.Written = timeFromMillis(runner.Number(p, "transport_at_ms"))
			obs.Sighted = timeFromMillis(runner.Number(p, "admitted_at_ms"))
			obs.Ordered = timeFromMillis(runner.Number(p, "order_at_ms"))
			input := c.packingInput(owner)
			body, bodyErr := PromptBody(owner.Snapshot, p)
			if bodyErr == nil {
				_, orderErr := validateOrder(input, p, h, body)
				obs.OrderProven = orderErr == nil
			}
			obs.LaterOutcome = runner.Text(stored, "outcome")
			obs.Evidence = append(obs.Evidence, owner.SnapshotRef, owner.TranscriptRef)
		}
	}
	before, after, bok, aok := c.forceSnapshots(command)
	obs.HeldBefore = heldCount(before)
	obs.HeldAfter = heldCount(after)
	obs.Exercised = c.forceStateEstablished(state, command, before, bok)
	if state == "not_started" || state == "idle" {
		var missed struct{ State string }
		if c.json("raw/F1-missed-window.json", &missed) == nil {
			obs.Exercised = false
		}
	}

	if state == "not_started" || state == "ended" {
		obs.Exercised = obs.Exercised && bok && aok && obs.HeldBefore > 0
	}
	for _, result := range f.CarriedResults {
		if result.Class == "delivered_confirmed" || result.InstanceID == "" {
			return resultForceError(state, "carried Claude Result is false or unbound", f, command.Stdout)
		}
	}
	check := CheckForceState(contract, obs)
	if obs.Exercised && f.Outcome == "refused" && bok && aok && !sameCustody(before, after, false) {
		return resultForceError(state, "refusal substituted immutable custody", f, command.Stdout)
	}
	if check.Status == "pass" && state != "not_started" && state != "ended" && !obs.OrderProven {
		return resultForceError(state, "shared ci sighting/Launch/order proof did not validate", obs, command.Stdout)
	}
	// Raw force output is not model intake. The whole input has its own row.
	if check.Status == "pass" && state != "not_started" && state != "ended" && hasCase {
		whole := checkCapOrWhole(c, x)
		if whole.Status != "pass" {
			return combine("force/"+state, check, whole)
		}
	}
	return check
}

// storedForce joins the public result to one force on its command's owned
// Launch. Refused forces have no handover; their stored refusal is still
// required, including calls deliberately made after the Launch ended.
func (c Capture) storedForce(command runner.Command, f *api.ForceResult) (OwnerEvidence, runner.Row, error) {
	var owner OwnerEvidence
	var stored runner.Row
	matches := 0
	if command.ExitCode != 0 || f == nil || f.ForceID == "" {
		return owner, stored, corrupt("force lacks a successful public force identity")
	}
	for _, o := range c.Owners {
		if o.Owner.Native || o.Owner.InstanceID == "" || o.Owner.LaunchID == "" || !containsArg(command.Argv, "--instance-id", o.Owner.InstanceID) {
			continue
		}
		for _, row := range o.Snapshot.Tables["forces"] {
			if runner.Text(row, "force_id") == f.ForceID && runner.Text(row, "instance_id") == o.Owner.InstanceID && runner.Text(row, "launch_id") == o.Owner.LaunchID {
				owner, stored = o, row
				matches++
			}
		}
	}
	if matches != 1 {
		return owner, stored, corrupt("public force lacks its unique owned stored force")
	}
	if f.Outcome == "refused" && (runner.Text(stored, "outcome") != f.Outcome || runner.Text(stored, "reason") != f.Reason || runner.Text(stored, "handover_id") != "") {
		return owner, stored, corrupt("public refusal differs from its owned stored force")
	}
	return owner, stored, nil
}

func forceHandover(owner OwnerEvidence, force, h runner.Row) bool {
	if runner.Text(force, "handover_id") == "" || runner.Text(force, "handover_id") != runner.Text(h, "handover_id") || runner.Text(h, "force_id") != runner.Text(force, "force_id") || runner.Text(h, "boundary") != "force" || runner.Text(h, "instance_id") != owner.Owner.InstanceID || runner.Text(h, "launch_token") != owner.Owner.LaunchToken || runner.Text(h, "session_id") != owner.Owner.SessionID {
		return false
	}
	matches := 0
	for _, row := range owner.Snapshot.Tables["handovers"] {
		if runner.Text(row, "handover_id") == runner.Text(h, "handover_id") {
			matches++
		}
	}
	return matches == 1
}

func resultForceError(state, reason string, observed any, ref string) runner.Result {
	return result("force/"+state, "fail", reason, "bound current force Results without Claude confirmation", observed, ref)
}
func heldCount(s runner.Snapshot) int {
	n := 0
	for _, m := range s.Tables["messages"] {
		if runner.Text(m, "place") == "held" {
			n++
		}
	}
	return n
}
func (c Capture) forceSnapshots(command runner.Command) (runner.Snapshot, runner.Snapshot, bool, bool) {
	var before, after runner.Snapshot
	bok, aok := false, false
	all := make([]runner.Snapshot, 0, len(c.Snapshots)+1)
	for _, s := range c.Snapshots {
		all = append(all, s)
	}
	var idleBefore runner.Snapshot
	if c.json("raw/F1-idle-before.json", &idleBefore) == nil {
		all = append(all, idleBefore)
	}
	iid := ""
	for i, arg := range command.Argv {
		if arg == "--instance-id" && i+1 < len(command.Argv) {
			iid = command.Argv[i+1]
		}
	}
	for _, s := range all {
		if s.InstanceID != iid {
			continue
		}
		if !s.ReadCompleted.After(command.Started) && (!bok || s.ReadCompleted.After(before.ReadCompleted)) {
			before = s
			bok = true
		}
		if !s.Observed.Before(command.Ended) && (!aok || s.Observed.Before(after.Observed)) {
			after = s
			aok = true
		}
	}
	return before, after, bok, aok
}
func (c Capture) forceStateEstablished(state string, command runner.Command, before runner.Snapshot, bound bool) bool {
	at := command.Started
	iid := ""
	for i, arg := range command.Argv {
		if arg == "--instance-id" && i+1 < len(command.Argv) {
			iid = command.Argv[i+1]
		}
	}
	owner, ok := c.owner(iid, "")
	if !ok {
		return false
	}
	sid := owner.Owner.SessionID
	switch state {
	case "not_started":
		for _, fact := range c.Hooks {
			if fact.Receipt.Event == "SessionStart" && runner.Text(fact.Payload, "session_id") == sid && !fact.Receipt.Received.After(at) {
				return false
			}
		}
		return bound && len(before.Tables["spawns"]) == 1 && runner.Text(before.Tables["spawns"][0], "state") == "pending" && !hasIssued(before)
	case "idle":
		for _, p := range owner.Snapshot.Tables["prompts"] {
			if runner.Text(p, "emission") == "issued" && runner.Number(p, "issued_at_ms") < at.UnixMilli() {
				return false
			}
		}
		return bound && idle(before) && heldCount(before) > 0 && !hasIssued(before)
	case "ended":
		for _, l := range owner.Snapshot.Tables["launches"] {
			if runner.Text(l, "launch_token") == owner.Owner.LaunchToken && runner.Number(l, "ended_at_ms") > 0 && runner.Number(l, "ended_at_ms") <= at.UnixMilli() {
				return true
			}
		}
		return false
	case "compacting":
		pre, post := false, false
		for _, h := range c.Hooks {
			if runner.Text(h.Payload, "session_id") != sid || h.Receipt.Received.After(at) {
				continue
			}
			pre = pre || h.Receipt.Event == "PreCompact"
			post = post || h.Receipt.Event == "PostCompact"
		}
		return pre && !post
	case "permission_dialog", "plan_dialog":
		var latest runner.Command
		for _, cmd := range c.Commands {
			if cmd.Started.After(at) || cmd.Started.Before(latest.Started) {
				continue
			}
			if !slices.Contains(cmd.Argv, "get") || !slices.Contains(cmd.Argv, iid) {
				continue
			}
			latest = cmd
		}
		if latest.Stdout == "" {
			return false
		}
		var row api.SpawnRow
		if c.commandJSON(latest.Stdout, &row) != nil {
			return false
		}
		for _, pr := range row.PermissionRequests {
			if pr.Decision == nil && ((state == "plan_dialog" && pr.ToolName == "ExitPlanMode") || (state == "permission_dialog" && pr.ToolName != "ExitPlanMode" && pr.ToolName != "EnterPlanMode")) {
				return true
			}
		}
		return false
	}
	var lastPre, lastPost time.Time
	var nextPre time.Time
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") != sid || runner.Text(h.Payload, "agent_id") != "" {
			continue
		}
		t := h.Receipt.Received
		if h.Receipt.Event == "PreToolUse" && runner.Text(h.Payload, "tool_name") == "Bash" {
			if !t.After(at) && t.After(lastPre) {
				lastPre = t
			}
			if t.After(at) && (nextPre.IsZero() || t.Before(nextPre)) {
				nextPre = t
			}
		}
		if h.Receipt.Event == "PostToolBatch" && !t.After(at) && t.After(lastPost) {
			lastPost = t
		}
	}
	if state == "tool_running" {
		return !lastPre.IsZero() && (lastPost.IsZero() || lastPre.After(lastPost))
	}
	if state == "between_tools" {
		return !lastPost.IsZero() && lastPost.After(lastPre) && !nextPre.IsZero()
	}
	return false
}

func pendingForce(c Capture) runner.Result {
	command, ok := c.commandNamed("F2-force")
	if !ok {
		return result("force-pending", "not_completed", "F2 force missing", nil, nil)
	}
	var response struct {
		Force *api.ForceResult `json:"force_result"`
	}
	if err := c.commandJSON(command.Stdout, &response); err != nil {
		return errorResult("force-pending", err)
	}
	if response.Force == nil {
		return result("force-pending", "fail", "busy F2 did not return pending awaiting admission", nil, response, command.Stdout)
	}
	owner, stored, err := c.storedForce(command, response.Force)
	if err != nil {
		return errorResult("force-pending", err)
	}
	if response.Force.Outcome == "handed_over" && forceToolEnded(c, command) {
		return result("force-pending", "not_completed", "precondition-not-established: foreground tool completed before force returned", "tool outstanding through force return", command, command.Stdout)
	}
	if response.Force.Outcome != "pending" || response.Force.Reason != "awaiting_admission" {
		return result("force-pending", "fail", "busy F2 did not return pending awaiting admission", nil, response, command.Stdout)
	}
	bound := false
	for _, h := range owner.Snapshot.Tables["handovers"] {
		bound = bound || forceHandover(owner, stored, h)
	}
	if !bound {
		return result("force-pending", "fail", "pending force lacks its owned forced handover", nil, response, command.Stdout)
	}
	second, ok := c.commandNamed("second-force-refusal")
	if !ok {
		return result("force-pending", "not_completed", "concurrent second force not recorded", nil, nil)
	}
	var refusal struct {
		Force *api.ForceResult `json:"force_result"`
	}
	if err := c.commandJSON(second.Stdout, &refusal); err != nil {
		return errorResult("force-pending", err)
	}
	if refusal.Force == nil || refusal.Force.Outcome != "refused" || refusal.Force.Reason != "handover_in_progress" {
		return result("force-pending", "fail", "second force did not refuse the active handover", nil, refusal, second.Stdout)
	}
	secondOwner, _, err := c.storedForce(second, refusal.Force)
	if err != nil {
		return errorResult("force-pending", err)
	}
	if secondOwner.Owner.InstanceID != owner.Owner.InstanceID || secondOwner.Owner.LaunchID != owner.Owner.LaunchID || refusal.Force.ForceID == response.Force.ForceID {
		return result("force-pending", "fail", "concurrent refusal is not a distinct force on the pending Launch", nil, refusal, second.Stdout)
	}
	tail, _, ok := c.snapshotCase("F2-tail-return")
	if !ok {
		return result("force-pending", "not_completed", "returned-tail snapshot missing", nil, nil)
	}
	f3, ok := c.caseByID("F3")
	if !ok {
		return result("force-pending", "not_completed", "known tail case missing", nil, nil)
	}
	returned := false
	for _, m := range tail.Tables["messages"] {
		if runner.Text(m, "text") == f3.Text && runner.Text(m, "place") == "held" {
			returned = true
		}
	}
	for _, p := range tail.Tables["prompts"] {
		if runner.Text(p, "emission") == "released" && runner.Text(p, "transport") == "written" {
			return result("force-pending", "fail", "released tail claims a physical output", nil, p)
		}
	}
	if !returned {
		return result("force-pending", "not_completed", "F2 tail return to ordinary Held custody not established", nil, tail)
	}
	return result("force-pending", "pass", "pending F2, rejected concurrent force and never-issued tail return retained", "one pending issue; tail Held until later F3", response, command.Stdout, second.Stdout)
}
func checkCapOrWhole(c Capture, x runner.Case) runner.Result {
	owner, p, h, err := c.prompt(x.Text)
	if err != nil {
		return errorResult("whole/"+x.ID, err)
	}
	body, err := PromptBody(owner.Snapshot, p)
	if err != nil {
		return errorResult("whole/"+x.ID, err)
	}
	mode, after := "wait_turn", runner.Number(p, "admitted_at_ms")
	if runner.Text(h, "boundary") == "tool_boundary" {
		mode, after = "steer_turn", runner.Number(p, "transport_at_ms")
	}
	return rename("whole/"+x.ID, CheckWholeInput(WholeInput{body, mode, after, owner.Transcript, []string{owner.SnapshotRef, owner.TranscriptRef}}))
}
func validateOrder(in PackingEvidence, p, h runner.Row, body string) (int64, error) {
	return signals.WakeOrderBody(in.Owner, in.Snapshot, p, h, body, in.Hooks, in.Trail, in.History)
}
func publicError(c Capture, command runner.Command) (string, map[string]any) {
	for _, ref := range []string{command.Stdout, command.Stderr} {
		b, err := c.read(ref)
		if err != nil {
			continue
		}
		var envelope struct {
			Name    string         `json:"err_name"`
			Details map[string]any `json:"err_details"`
		}
		if json.Unmarshal(b, &envelope) == nil && envelope.Name != "" {
			return envelope.Name, envelope.Details
		}
	}
	return "", nil
}
func containsArg(args []string, key, value string) bool {
	for i, arg := range args {
		if arg == key && i+1 < len(args) && args[i+1] == value {
			return true
		}
		if strings.HasPrefix(arg, key+"=") && strings.TrimPrefix(arg, key+"=") == value {
			return true
		}
	}
	return false
}
