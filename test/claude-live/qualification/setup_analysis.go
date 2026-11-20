package qualification

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func analyzeSetup(c Capture) []runner.Result {
	var out []runner.Result
	latest, la := c.commandNamed("latest-release")
	released, ra := c.commandNamed("released-version")
	var release struct {
		Tag               string `json:"tag_name"`
		Draft, Prerelease bool
	}
	var version struct{ Version string }
	if !la || !ra {
		out = append(out, result("latest-release", "not_completed", "latest-release prerequisite missing", nil, nil))
	} else if c.commandJSON(latest.Stdout, &release) != nil || c.commandJSON(released.Stdout, &version) != nil || latest.ExitCode != 0 || released.ExitCode != 0 || release.Tag == "" || release.Draft || release.Prerelease || strings.TrimPrefix(release.Tag, "v") != strings.TrimPrefix(version.Version, "v") {
		out = append(out, result("latest-release", "fail", "starting version was not the actual latest released build", release.Tag, version.Version, latest.Stdout, released.Stdout))
	} else {
		out = append(out, result("latest-release", "pass", "actual latest stable release started before candidate install", release.Tag, version.Version, latest.Stdout, released.Stdout))
	}
	held, err := deliveryCommand(c, "setup-held")
	if err != nil {
		out = append(out, errorResult("route-unavailable", err))
	} else {
		status := "pass"
		if held.Class != "held" || held.Wait == nil || held.Wait.Reason != "route_unavailable" {
			status = "fail"
		}
		out = append(out, result("route-unavailable", status, "old owned Launch retained route-unavailable custody", "held/route_unavailable", held))
	}
	out = append(out, combine("install-resume", checkResume(c, "old-launch-resume", "setup-old-end", "setup-old-resume", "setup-held"), checkResume(c, "fresh-launch-resume", "setup-fresh-pause", "setup-fresh-resume", "setup-fresh-held")))
	var setup []runner.Result
	calls := 0
	for ref := range c.Manifest.Artifacts {
		if strings.HasPrefix(ref, "raw/mcp/request-") {
			var req struct {
				Method string
				Params map[string]any
			}
			if c.json(ref, &req) == nil && req.Method == "tools/call" && req.Params["name"] == "qualification_echo" {
				calls++
			}
		}
	}
	status := "pass"
	if calls == 0 {
		status = "not_completed"
	}
	if calls > 1 {
		status = "fail"
	}
	setup = append(setup, result("mcp-slash-once", status, "startup slash invokes owned MCP exactly once and is not replayed", 1, calls))
	hooks := map[string]int{}
	for ref := range c.Manifest.Artifacts {
		if strings.HasPrefix(ref, "raw/setup-user-hooks/") && strings.HasSuffix(ref, "/input.json") {
			var h runner.Row
			if c.json(ref, &h) == nil {
				hooks[runner.Text(h, "hook_event_name")]++
			}
		}
	}
	status = "pass"
	if hooks["SessionStart"] < 4 || hooks["UserPromptSubmit"] < 4 {
		status = "not_completed"
	}
	setup = append(setup, result("preserved-user-hooks", status, "independent harmless hooks participated before and after install/resume", "SessionStart and UserPromptSubmit receipts", hooks))
	command, ok := c.commandNamed("spawn-setup-fresh")
	if !ok || !containsArg(command.Argv, "--mcp-config", c.Root+"/raw/setup-mcp.json") || !hasFlag(command.Argv, "--append-system-prompt") {
		setup = append(setup, result("saved-native-arguments", "not_completed", "fresh launch setup arguments missing", nil, nil))
	} else {
		setup = append(setup, result("saved-native-arguments", "pass", "MCP, custom system prompt and slash startup configured", "native args retained", command.Argv, command.Stdout))
	}
	out = append(out, combine("launch-setup", setup...))
	var defaults []runner.Result
	var freshArgs []string
	for _, name := range []string{"setup-fresh-defaults", "setup-resumed-defaults"} {
		command, ok := c.commandNamed(name)
		if !ok {
			defaults = append(defaults, result(name, "not_completed", "defaults read missing", nil, nil))
			continue
		}
		var get struct {
			AgentArgs     []string                  `json:"agent_args"`
			AgentDefaults *api.AgentDefaults        `json:"agent_defaults"`
			Capabilities  *api.DeliveryCapabilities `json:"delivery_capabilities"`
		}
		err := c.commandJSON(command.Stdout, &get)
		status := "pass"
		if err != nil || get.AgentDefaults == nil || get.Capabilities == nil {
			status = "not_completed"
		} else if get.AgentDefaults.SendMode != "wait_turn" || !get.AgentDefaults.ConfirmedDelivery || get.AgentDefaults.ConfirmFromTranscripts || get.Capabilities.ConfirmFromTranscriptsCapability != "not_needed" {
			status = "fail"
		}
		if name == "setup-fresh-defaults" {
			freshArgs = get.AgentArgs
		} else if len(freshArgs) == 0 || !reflect.DeepEqual(freshArgs, get.AgentArgs) {
			status = "fail"
		}
		defaults = append(defaults, result(name, status, "saved defaults and explicit transcript-confirmation override retained; Claude reader is not_needed", "wait_turn/confirmed/false/not_needed", get, command.Stdout))
	}
	out = append(out, combine("defaults", defaults...))
	return out
}

func analyzeUncovered(c Capture) []runner.Result {
	launch, err := c.read("raw/qualification/launch.go")
	if err != nil {
		return []runner.Result{errorResult("uncovered-agents", err)}
	}
	constants, err := c.read("raw/qualification/spawns.go")
	if err != nil {
		return []runner.Result{errorResult("uncovered-agents", err)}
	}
	kinds, err := CandidateKinds(launch, constants)
	if err != nil {
		return []runner.Result{errorResult("uncovered-agents", err)}
	}
	var agents []UncoveredAgent
	if err = c.json("raw/uncovered-agents.json", &agents); err != nil {
		return []runner.Result{errorResult("uncovered-agents", err)}
	}
	var checks []runner.Result
	for _, kind := range kinds {
		if kind == "claude" {
			continue
		}
		var matches []UncoveredAgent
		for _, a := range agents {
			if a.Kind == kind {
				matches = append(matches, a)
			}
		}
		if len(matches) != 1 {
			checks = append(checks, result("uncovered/"+kind, "not_completed", "candidate kind lacks exactly one explicit availability assessment", kind, matches))
			continue
		}
		agent := matches[0]
		var rows []runner.Result
		if !agent.Exited {
			rows = append(rows, result(kind+"/cleanup", "fail", "owned other-provider process not verified exited", true, false))
		}
		if agent.Usability != "native_input_confirmed" {
			for _, call := range agent.Calls {
				if row, judged := CheckUncoveredOperation(c, agent, call); judged {
					rows = append(rows, row)
				}
			}
			fallback := fallbackResult(c, "uncovered/"+kind, "busy-send-uncovered")
			rows = append(rows, fallback)
			rows = append(rows, result(kind+"/availability", "pass", "installation, credential status and native usability remain distinct; deterministic coverage grants no live PASS", nil, agent, "raw/uncovered-agents.json"))
		} else {
			seen := map[string]bool{}
			for _, call := range agent.Calls {
				seen[call.Case] = true
				if row, judged := CheckUncoveredOperation(c, agent, call); judged {
					rows = append(rows, row)
				}

				if call.Case == "get-coverage" {
					var get struct {
						BusySend string `json:"busy_send"`
					}
					status := "pass"
					if c.commandJSON(call.Command.Stdout, &get) != nil || get.BusySend != "not_covered" {
						status = "fail"
					}
					rows = append(rows, result(kind+"/coverage", status, "candidate kind reports uncovered", "not_covered", get, call.Command.Stdout))
				}
			}
			for _, required := range []string{"busy-choice-0", "busy-choice-1", "busy-choice-2", "busy-choice-3", "busy-choice-4", "busy-choice-5", "force", "conditional-pause", "get-coverage", "native-ordinary", "native-id-repeat", "raw-key"} {
				if !seen[required] {
					rows = append(rows, result(kind+"/"+required, "not_completed", "required actual uncovered path absent", required, nil))
				}
			}
			for _, verb := range []string{"resume", "reuse", "template", "spawn"} {
				for _, n := range []string{"0", "1", "2"} {
					if !seen[verb+"-default-"+n] {
						rows = append(rows, result(kind+"/"+verb+n, "not_completed", "explicit false/default refusal absent", nil, nil))
					}
				}
			}
			before, br, a := c.snapshotCase("uncovered-" + kind + "-before-refusals")
			after, ar, b := c.snapshotCase("uncovered-" + kind + "-after-refusals")
			if !a || !b {
				rows = append(rows, result(kind+"/no-input", "not_completed", "refusal custody snapshots absent", nil, nil))
			} else if stringJSON(before.Tables["prompts"]) != stringJSON(after.Tables["prompts"]) {
				rows = append(rows, result(kind+"/no-input", "fail", "refusal issued parent input", nil, nil, br, ar))
			}
			rows = append(rows, fallbackResult(c, "uncovered/"+kind+"/request-token"))
		}
		checks = append(checks, combine("uncovered/"+kind, rows...))
	}
	checks = append(checks, fallbackResult(c, "scheduler/interface-parity"))
	return append(checks, combine("uncovered-agents", checks...))
}
func stringJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func sameNativeReceipt(a, b api.SendKeysResult, id string) bool {
	return a.RequestID == id && b.RequestID == id && a.Outcome == "submitted" && b.Outcome == "submitted" && a.SessionID != "" && a.SessionID == b.SessionID && a.RecordID != "" && a.RecordID == b.RecordID && digestText(a.RecordSHA256, 32) && a.RecordSHA256 == b.RecordSHA256 && a.Class == "" && b.Class == ""
}

func CheckUncoveredOperation(c Capture, agent UncoveredAgent, call UncoveredCall) (runner.Result, bool) {
	id := "uncovered/" + agent.Kind + "/" + call.Case
	bad := func(reason string, observed any) (runner.Result, bool) {
		return result(id, "fail", reason, call.Expected, observed, call.Command.Stdout, call.Command.Stderr), true
	}
	good := func(observed any) (runner.Result, bool) {
		return result(id, "pass", "actual public operation/proof agrees with its own contract", call.Expected, observed, call.Command.Stdout, call.Command.Stderr), true
	}
	if !containsArg(call.Command.Argv, "--instance-id", agent.Instance) && !strings.Contains(call.Case, "-default-") {
		if call.Case != "auth-status" {
			return bad("operation targets another instance", call.Command)
		}
	}
	switch call.Case {
	case "force":
		var value struct {
			Force *api.ForceResult `json:"force_result"`
		}
		if c.commandJSON(call.Command.Stdout, &value) != nil || call.Command.ExitCode != 0 || value.Force == nil {
			return bad("force lacks its successful structured result", value)
		}
		f := value.Force
		if f.Outcome != "refused" || f.Reason != "agent_not_covered" || f.ForceID == "" || len(f.CarriedPairs) != 0 || len(f.CarriedResults) != 0 {
			return bad("force did not refuse without carried input", f)
		}
		bound := 0
		for _, o := range c.Owners {
			if o.Owner.InstanceID != agent.Instance {
				continue
			}
			for _, row := range o.Snapshot.Tables["forces"] {
				if runner.Text(row, "force_id") == f.ForceID && runner.Text(row, "instance_id") == agent.Instance && runner.Text(row, "launch_id") == o.Owner.LaunchID && runner.Text(row, "outcome") == "refused" && runner.Text(row, "reason") == "agent_not_covered" {
					bound++
				}
			}
		}
		if bound != 1 {
			return bad("force refusal lacks its unique owned stored binding", f)
		}
		return good(f)
	case "conditional-pause":
		name, details := publicError(c, call.Command)
		if call.Command.ExitCode == 0 || name != "ErrSpawnNotPausable" || details["cause"] != "agent_not_covered" {
			return bad("conditional pause uses a different refusal contract", details)
		}
		return good(details)
	case "native-ordinary", "native-id-repeat":
		if call.Case == "native-ordinary" && call.Command.ExitCode != 0 {
			name, details := publicError(c, call.Command)
			if name == "ErrAgentCapabilityUnsupported" && hasFlag(call.Command.Argv, "--delivery-request-id") {
				return result(id, "pass", "documented pane-only native request-ID unsupported outcome; no native input or usability proof claimed", "ErrAgentCapabilityUnsupported", map[string]any{"err_name": name, "details": details, "unsupported": "native_delivery_request_id"}, call.Command.Stdout, call.Command.Stderr), true
			}
		}
		var value api.SendKeysResult
		if call.Command.ExitCode != 0 || c.commandJSON(call.Command.Stdout, &value) != nil {
			return bad("native request/repeat failed", call.Command)
		}
		expected := "uncovered-" + agent.Kind + "-ordinary"
		if !sameNativeReceipt(value, value, expected) || !containsArg(call.Command.Argv, "--delivery-request-id", expected) {
			return bad("native request lacks exact request/session/record proof", value)
		}
		owner, owned := c.owner(agent.Instance, "")
		if !owned || owner.Owner.SessionID != value.SessionID {
			return bad("native proof belongs to another owned session", value)
		}
		if call.Case == "native-id-repeat" {
			found := false
			for _, original := range agent.Calls {
				if original.Case != "native-ordinary" {
					continue
				}
				var first api.SendKeysResult
				if c.commandJSON(original.Command.Stdout, &first) != nil || !sameNativeReceipt(first, value, expected) || !reflect.DeepEqual(original.Command.Argv, call.Command.Argv) {
					return bad("repeat changed original receipt/input instead of reading it", value)
				}
				found = true
			}
			if !found {
				return bad("native repeat has no original owned operation", value)
			}
		}
		return good(value)
	case "raw-key":
		var value map[string]any
		if call.Command.ExitCode != 0 || c.commandJSON(call.Command.Stdout, &value) != nil || value == nil || len(value) != 0 || !containsArg(call.Command.Argv, "--key", "Escape") {
			return bad("raw-key operation did not preserve the legacy empty result", value)
		}
		return good(value)
	}
	if strings.HasPrefix(call.Case, "busy-choice-") || strings.Contains(call.Case, "-default-") {
		name, details := publicError(c, call.Command)
		if call.Command.ExitCode == 0 || name != "ErrCantSend" || details["reason"] != "agent_not_covered" {
			return bad("send/default operation violated ErrCantSend refusal", details)
		}
		return good(details)
	}
	return runner.Result{}, false
}
