package qualification

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func analyzeDialogs(c Capture) []runner.Result {
	var observations []DialogObservation
	if err := c.json("raw/dialogs.json", &observations); err != nil {
		return []runner.Result{errorResult("dialog-matrix", err)}
	}
	rows := CheckDialogMatrix(c, observations)
	out := append([]runner.Result{}, rows...)
	out = append(out, combine("dialog-matrix", rows...), checkPauseRefusals(c, "dialog-pause-"))
	if containsSelection(c.Before.DialogCases, "D2") || containsSelection(c.Before.DialogCases, "D3") {
		doc, err := c.read("raw/caller-contract.md")
		if err != nil {
			out = append(out, errorResult("permission-force", err))
			return out
		}
		contract, err := ParseContract(doc)
		if err != nil {
			out = append(out, errorResult("permission-force", err))
			return out
		}
		if containsSelection(c.Before.DialogCases, "D2") {
			out = append(out, rename("permission-force", forceState(c, contract, "permission_dialog", "D2", "D2")))
		}
		if containsSelection(c.Before.DialogCases, "D3") {
			out = append(out, rename("plan-force", forceState(c, contract, "plan_dialog", "D3", "D3")))
		}
	}
	for _, id := range c.Before.DialogCases {
		if err := requirePhysicalCase(c, id, "dialogs"); err != nil {
			if phaseWorkload(c) && workloadCaseUnspent(c, id) {
				out = append(out, workloadMissing(c, "dialog-start/"+id))
			} else {
				out = append(out, errorResult("dialog-start/"+id, err))
			}
		}
	}
	return out
}
func containsSelection(selection []string, id string) bool {
	for _, v := range selection {
		if v == id {
			return true
		}
	}
	return false
}

// CheckDialogMatrix emits a separately addressable result for every required
// combination. The complete matrix cannot hide missing relay/origin rows.
func CheckDialogMatrix(c Capture, observations []DialogObservation) []runner.Result {
	selected := c.Before.DialogCases
	required := func(k DialogKey) bool {
		if k.Relay {
			return containsSelection(selected, "D1")
		}
		id := "D4"
		if k.Origin == "subagent" {
			id = "D5"
		}
		if k.Origin == "background" {
			id = "D6"
		}
		return containsSelection(selected, id)
	}
	var out []runner.Result
	for _, key := range DialogDimensions() {
		if !required(key) {
			continue
		}
		var matches []DialogObservation
		for _, o := range observations {
			if o.Key == key {
				matches = append(matches, o)
			}
		}
		if len(matches) == 0 {
			fallback, err := c.scheduler(key.ID())
			if err != nil {
				out = append(out, result(key.ID(), evidenceStatus(err), "required Dialog combination absent or invalid: "+err.Error(), key, nil))
				continue
			}
			out = append(out, result(key.ID(), "pass", "scheduler-covered native-unavailable combination; no live claim", key, fallback, fallback.Evidence))
			continue
		}
		if len(matches) != 1 {
			out = append(out, result(key.ID(), "fail", "duplicate/contradictory Dialog dimension evidence", key, matches))
			continue
		}
		out = append(out, checkDialog(c, matches[0]))
	}
	for _, o := range observations {
		known := false
		for _, key := range DialogDimensions() {
			if key == o.Key {
				known = true
				break
			}
		}
		if !known {
			out = append(out, result("dialog/unknown", "fail", "unknown Dialog dimension key", nil, o))
		}
	}
	return out
}

func checkDialog(c Capture, o DialogObservation) runner.Result {
	fail := func(reason string, observed any) runner.Result {
		return result(o.Key.ID(), "fail", reason, o.Key, observed, o.Before, o.After, o.Send, o.Permission)
	}
	missing := func(reason string) runner.Result {
		return result(o.Key.ID(), "not_completed", reason, o.Key, o, o.Before, o.After, o.Send)
	}
	before, bok := c.Snapshots[o.Before]
	after, aok := c.Snapshots[o.After]
	if !bok || !aok {
		return missing("scoped before/after Dialog snapshots missing")
	}
	if before.InstanceID != o.Instance || after.InstanceID != o.Instance || len(before.Tables["spawns"]) != 1 || len(after.Tables["spawns"]) != 1 {
		return fail("Dialog target mismatch", o)
	}
	sid := runner.Text(before.Tables["spawns"][0], "session_id")
	if sid == "" || sid != runner.Text(after.Tables["spawns"][0], "session_id") || runner.Text(before.Tables["spawns"][0], "launch_token") != runner.Text(after.Tables["spawns"][0], "launch_token") {
		return fail("Dialog parent session changed", nil)
	}
	owner, ok := c.owner(o.Instance, runner.Text(before.Tables["spawns"][0], "launch_token"))
	if !ok || owner.Owner.SessionID != sid {
		return fail("Dialog lacks owned Launch", nil)
	}
	spawn, ok := c.commandNamed("spawn-" + strings.TrimPrefix(o.Instance, "live-"+c.Manifest.RunID+"-"))
	relay := "off"
	if o.Key.Relay {
		relay = "on"
	}
	if !ok || !containsArg(spawn.Argv, "--relay-mode", relay) {
		return fail("relay dimension differs from the owned spawn command", spawn)
	}
	var gate runner.Row
	if err := c.json(o.Gate+"/input.json", &gate); err != nil {
		return missing("native about-to-open tool request missing")
	}
	if runner.Text(gate, "session_id") != sid || runner.Text(gate, "tool_name") != "Bash" {
		return fail("gate belongs to another parent/tool", gate)
	}
	input, _ := gate["tool_input"].(map[string]any)
	command, _ := input["command"].(string)
	target := dialogTarget(owner.Owner.CWD, o.Key.Origin)
	if !strings.Contains(command, target) {
		return fail("native request does not target this origin's owned harmless file", gate)
	}
	agent := runner.Text(gate, "agent_id")
	if o.Key.Origin == "main" && agent != "" {
		return fail("main-origin key names a child tool", gate)
	}
	if o.Key.Origin != "main" {
		if agent == "" {
			return fail("child/background key has no native agent identity", gate)
		}
		if !dialogChildOrigin(owner, o.Key.Origin, target) {
			return missing("parent Task/Agent invocation does not independently establish the requested child/background origin")
		}
	}
	if o.Key.State == "open" {
		var permission runner.Row
		if err := c.json(o.Permission, &permission); err != nil {
			return missing("native PermissionRequest evidence absent")
		}
		data, _ := json.Marshal(permission)
		if runner.Text(permission, "session_id") != sid || runner.Text(permission, "hook_event_name") != "PermissionRequest" || !strings.Contains(string(data), target) {
			return fail("open request belongs to another Dialog", permission)
		}
	}
	send, ok := c.Commands[o.Send]
	if !ok {
		return missing("queued-send command missing")
	}
	if !containsArg(send.Argv, "--instance-id", o.Instance) || !containsArg(send.Argv, "--send-mode", o.Key.Mode) || !containsArg(send.Argv, "--confirmed-delivery", fmt.Sprint(o.Key.Confirmed)) {
		return fail("command options differ from Dialog dimension key", send)
	}
	var resultValue api.DeliveryResult
	if c.commandJSON(o.Send, &resultValue) != nil {
		return fail("invalid Dialog send Result", nil)
	}
	if send.ExitCode != 0 || resultValue.Class != "held" || resultValue.Final {
		return fail("Dialog send did not remain held and unconfirmed", resultValue)
	}
	// Four variants share these exact snapshots. They must add four Held
	// Messages, with no physical issue or parent input while the Dialog holds.
	if len(after.Tables["messages"])-len(before.Tables["messages"]) != 4 {
		return fail("Dialog variant custody count changed unexpectedly", nil)
	}
	if !sameCustody(before, after, true) {
		return fail("Dialog queue substituted pre-existing custody", nil)
	}
	if !reflect.DeepEqual(before.Tables["prompts"], after.Tables["prompts"]) || !reflect.DeepEqual(before.Tables["handovers"], after.Tables["handovers"]) {
		return fail("queueing behind Dialog issued or claimed parent input", nil)
	}
	for _, h := range c.Hooks {
		if h.Receipt.Event == "UserPromptSubmit" && runner.Text(h.Payload, "agent_id") == "" && runner.Text(h.Payload, "session_id") == sid && !h.Receipt.Received.Before(before.Observed) && !h.Receipt.Received.After(after.ReadCompleted) {
			return fail("Dialog variants created an unplanned parent start", h.Reference)
		}
	}
	for _, cmd := range c.Commands {
		if cmd.Started.Before(before.Observed) || cmd.Ended.After(after.ReadCompleted) {
			continue
		}
		if containsArg(cmd.Argv, "--instance-id", o.Instance) && hasVerb(cmd.Argv, "decide") {
			return fail("queue operation answered the Dialog", cmd)
		}
	}
	// All pane bytes remain retained for intelligence; no animation or screen
	// string is interpreted as an answer. Structural native requests and the
	// absence of an input/decision path are the automated safety evidence.
	if !o.Key.Relay {
		ended := false
		endName := "relay-off-end-" + strings.TrimPrefix(o.Instance, "live-"+c.Manifest.RunID+"-")
		for _, cmd := range c.Commands {
			inWindow := !cmd.Ended.Before(before.Observed) && (owner.Owner.StoppedAt.IsZero() || !cmd.Started.After(owner.Owner.StoppedAt))
			if inWindow && directDialogPaneInput(cmd.Argv, owner.Owner) {
				return fail("relay-off Dialog received direct tmux pane input", cmd)
			}
			if !containsArg(cmd.Argv, "--instance-id", o.Instance) {
				continue
			}
			if hasVerb(cmd.Argv, "decide") {
				return fail("relay-off Dialog was answered", cmd)
			}
			if cmd.Started.Before(before.Observed) || (!owner.Owner.StoppedAt.IsZero() && cmd.Started.After(owner.Owner.StoppedAt)) {
				continue
			}
			if hasVerb(cmd.Argv, "send-keys") {
				var queued api.DeliveryResult
				busy := containsArg(cmd.Argv, "--send-mode", "wait_turn") || containsArg(cmd.Argv, "--send-mode", "steer_turn")
				if hasFlag(cmd.Argv, "--key") || !busy || cmd.ExitCode != 0 || c.commandJSON(cmd.Stdout, &queued) != nil || queued.Class != "held" || queued.Final {
					return fail("relay-off Dialog received automated pane input", cmd)
				}
			}
			keyless := hasVerb(cmd.Argv, "kill") || hasVerb(cmd.Argv, "pause")
			if cmd.Name == endName && keyless && cmd.ExitCode == 0 && !cmd.Started.Before(after.ReadCompleted) && !cmd.Ended.Before(cmd.Started) && owner.Owner.Exited && !owner.Owner.StoppedAt.Before(cmd.Ended) {
				ended = true
			}
		}
		if !ended {
			return missing("relay-off Dialog lacks an owned keyless ending after its queue window")
		}
	}
	return result(o.Key.ID(), "pass", "known native Dialog stayed structurally held; queue changed no input or decision", o.Key, o, o.Before, o.After, o.Send, o.Gate+"/input.json", o.Permission)
}

// Direct controls use tmux rather than AD, so they have no --instance-id.
// The phase owns its private socket; input there, or to its recorded target
// through the inherited socket, cannot be ignored by the relay-off judgment.
func directDialogPaneInput(args []string, owner runner.Owned) bool {
	if len(args) == 0 || filepath.Base(args[0]) != "tmux" {
		return false
	}
	input := false
	socket, target := "", ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "send-keys":
			input = true
		case "-S", "-t":
			flag := args[i]
			if i+1 < len(args) {
				i++
				if flag == "-S" {
					socket = args[i]
				} else {
					target = strings.TrimPrefix(args[i], "=")
				}
			}
		}
	}
	if !input || (socket != "" && owner.Socket != "" && socket != owner.Socket) {
		return false
	}
	if owner.Socket != "" && socket == owner.Socket {
		return true
	}
	for _, id := range []string{owner.InstanceID, owner.TmuxID} {
		if id != "" && (target == id || strings.HasPrefix(target, id+":")) {
			return true
		}
	}
	return false
}

func hasVerb(args []string, verb string) bool { return len(args) > 1 && args[1] == verb }
func dialogChildOrigin(owner OwnerEvidence, origin, target string) bool {
	matches := 0
	for _, row := range owner.Transcript.Rows {
		if row.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type, Name string
			Input      map[string]any
		}
		if json.Unmarshal(row.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || (b.Name != "Agent" && b.Name != "Task") {
				continue
			}
			data, _ := json.Marshal(b.Input)
			if !strings.Contains(string(data), target) {
				continue
			}
			background, _ := b.Input["run_in_background"].(bool)
			if (origin == "background") == background {
				matches++
			}
		}
	}
	return matches == 1
}

func checkPauseRefusals(c Capture, prefix string) runner.Result {
	var checks []runner.Result
	for _, command := range c.allCommands(prefix) {
		var record struct {
			Command       runner.Command
			Before, After runner.Snapshot
		}
		if err := c.json("raw/"+command.Name+".json", &record); err != nil {
			checks = append(checks, errorResult("pause-refusal", err))
			continue
		}
		name, details := publicError(c, command)
		if command.ExitCode == 0 || name != "ErrSpawnNotPausable" || (details["cause"] != "not_waiting" && details["cause"] != "turn_pending") {
			checks = append(checks, result("pause-refusal", "fail", "conditional pause did not give a documented no-signal refusal", nil, map[string]any{"error": name, "details": details}, command.Stdout, command.Stderr))
			continue
		}
		unchanged := true
		for _, table := range []string{"messages", "handovers", "prompts", "members", "forces", "launches"} {
			unchanged = unchanged && reflect.DeepEqual(record.Before.Tables[table], record.After.Tables[table])
		}
		if !unchanged {
			checks = append(checks, result("pause-refusal", "fail", "refused pause changed protected work", nil, record))
		} else {
			checks = append(checks, result("pause-refusal", "pass", "conditional refusal changed no protected work", nil, details, command.Stdout, command.Stderr))
		}
	}
	if len(checks) == 0 {
		return result("pause-refusal", "not_completed", "no live working/pending conditional-pause refusal", "live evidence or exact E6-case fallback", nil)
	}
	return combine("pause-refusal", checks...)
}
