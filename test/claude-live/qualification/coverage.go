package qualification

import "fmt"

type CoverageRow struct {
	ID     string   `json:"id"`
	Source string   `json:"source"`
	Phases []string `json:"phases"`
	Checks []string `json:"checks"`
	Kind   string   `json:"kind"`
}

// Coverage contains references, not copies of normative requirements.
// Reuse this registry for qualification coverage; follow docs/architecture.md,
// "Claude qualification (reusable analysis and accounting)".
// Future coverage consumers must reuse this registry rather than copy its rows.
func Coverage() []CoverageRow {
	rows := []CoverageRow{}
	add := func(id, source string, phases, checks []string) {
		rows = append(rows, CoverageRow{id, source, phases, checks, "required"})
	}
	for _, r := range []struct{ id, phase, check string }{
		{"fresh-resumed", "setup-launches", "launch-setup"}, {"dialogs", "dialogs", "dialog-matrix"}, {"busy-idle-compaction-startup", "force-states", "force-table"}, {"parent-subagent-background", "dialogs", "dialog-matrix"}, {"forced-states", "force-states", "force-table"}, {"mixed-options", "groups", "whole-corpus"}, {"whole-unicode-tags", "groups", "whole-corpus"}, {"packing-boundaries", "text-limits-backlog", "ad-caps"}, {"multi-prompt-groups", "groups", "group-gaps"}, {"backlog", "text-limits-backlog", "large-backlog"}, {"crash-lost-reply", "lifecycle", "lost-reply"}, {"concurrent-callbacks", "lifecycle", "concurrent-delivery"}, {"replacement", "lifecycle", "fresh-reuse"}, {"typed-controls", "typed-controls", "typed-long-tool"}, {"hooks-stale-unknown", "user-hooks", "stale-unknown"}, {"uncovered-agents", "uncovered", "uncovered-agents"},
	} {
		add("sr8.3/"+r.id, "SR-8.3", []string{r.phase}, []string{r.check})
	}
	for _, r := range []struct{ id, phase, check string }{
		{"Agents busy send does not cover", "uncovered", "uncovered-agents"}, {"Dialog safety", "dialogs", "dialog-matrix"}, {"Send Modes", "groups", "whole-corpus"}, {"Accepting", "text-limits-backlog", "ad-refusals"}, {"Order and integrity", "groups", "ordinary-group"}, {"Waiting and resume", "setup-launches", "install-resume"}, {"Forced delivery", "force-states", "force-table"}, {"Confirm From Transcripts", "setup-launches", "defaults"}, {"Event-only delivery", "lifecycle", "read-only"}, {"Agent Defaults", "setup-launches", "defaults"}, {"Results, IDs, Lookup and recovery", "lifecycle", "lost-reply"}, {"No raw keys", "groups", "whole-corpus"}, {"Limits and CSCB traffic", "text-limits-backlog", "large-backlog"}, {"Release", "setup-launches", "install-resume"},
	} {
		add("prd/"+r.id, "PRD acceptance: "+r.id, []string{r.phase}, []string{r.check})
	}
	for _, id := range []string{"p-b", "p-c", "turn-end-premise", "wake-order-point"} {
		add("o4/"+id, "O4; SR-2.5", []string{"signals"}, []string{id})
	}
	add("sr2.5/never-sighted-hold", "SR-2.5", []string{"user-hooks", "signals"}, []string{"never-sighted-hold"})
	rows = append(rows, CoverageRow{"o4/r-a", "O4 R-a", nil, nil, "named_premise"}, CoverageRow{"prd/Seamless upgrades", "E7", nil, nil, "scheduler"})
	rows = append(rows, CoverageRow{ID: "claude/prompt-replacement", Source: "https://code.claude.com/docs/en/hooks#decision-control — UserPromptSubmit can block or add additionalContext, and cannot replace the prompt; Main Agent Q-H1 decision", Kind: "claude_inapplicable"})
	add("sr8.3/hook-block", "SR-8.3; Q-H1", []string{"user-hooks"}, []string{"hook-block"})
	add("sr8.3/hook-context", "SR-8.3; Q-H1", []string{"user-hooks"}, []string{"hook-context"})
	add("sr4.4/dialog-force-states", "SR-4.4; candidate force table", []string{"dialogs"}, []string{"permission-force", "plan-force"})
	rows = append(rows,
		CoverageRow{ID: "scheduler/full-backlog-drain", Source: "SR-8.3 backlog; decision 11", Kind: "scheduler"},
		CoverageRow{ID: "scheduler/wake-order-guards", Source: "SR-2.5; ok order-point tests", Kind: "scheduler"},
		CoverageRow{ID: "scheduler/interface-parity", Source: "SR-8.3; CLI/MCP/Go/TypeScript", Kind: "scheduler"})
	return rows
}

type DialogKey struct {
	Mode      string `json:"mode"`
	Confirmed bool   `json:"confirmed"`
	Relay     bool   `json:"relay"`
	Origin    string `json:"origin"`
	State     string `json:"state"`
}

func (d DialogKey) ID() string {
	return fmt.Sprintf("dialog/%s/confirmed=%t/relay=%t/%s/%s", d.Mode, d.Confirmed, d.Relay, d.Origin, d.State)
}
func DialogDimensions() []DialogKey {
	var out []DialogKey
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		for _, confirm := range []bool{false, true} {
			for _, relay := range []bool{false, true} {
				for _, origin := range []string{"main", "subagent", "background"} {
					for _, state := range []string{"open", "about_to_open"} {
						out = append(out, DialogKey{mode, confirm, relay, origin, state})
					}
				}
			}
		}
	}
	return out
}
