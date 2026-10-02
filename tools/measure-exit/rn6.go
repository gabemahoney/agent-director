package main

// RN-6 (SRD Open Questions): Claude Code's exit after its pane is killed,
// which sets kill_exit_wait_ms. Each sample kills the agent's pane with the
// harness's own tmux kill-pane on the private socket and polls the
// start-time reader every 100 ms until the agent process is gone. Three
// kinds of agent (idle, mid-turn, with the deployment's MCP servers), each
// under the default SessionEnd budget and under the two raised budgets.
//
// Case ids (stable: decide and the SRD tables key on them):
//
//	rn6.idle, rn6.midturn, rn6.mcp                      default budget
//	rn6.<kind>.raised-hook                              per-hook timeout raised
//	rn6.<kind>.raised-env                               CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS raised

// rn6Trigger is RN-6's measured action as the results state it.
const rn6Trigger = "tmux kill-pane of the agent's pane on the private socket; timed until the start-time reader no longer finds the agent pid with its start time (100 ms polls)"

// rn6Kinds are RN-6's three kinds of agent.
var rn6Kinds = []struct {
	id, title string
	l         launch
}{
	{"idle", "idle agent", launch{}},
	{"midturn", "agent mid-turn", launch{midTurn: true}},
	{"mcp", "agent with the deployment's MCP servers", launch{mcp: true}},
}

// rn6Variants are the SessionEnd budgets every kind runs under, the
// default first (its cases are the ones decide's RN-6 rule reads).
var rn6Variants = []struct {
	suffix string
	v      budgetVariant
}{
	{"", budgetDefault},
	{".raised-hook", budgetRaisedHook},
	{".raised-env", budgetRaisedEnv},
}

// rn6CaseID is the id of one RN-6 case.
func rn6CaseID(kind, suffix string) string { return "rn6." + kind + suffix }

func init() {
	for _, v := range rn6Variants {
		for _, k := range rn6Kinds {
			l := k.l
			l.variant = v.v
			registerSampled(rn6CaseID(k.id, v.suffix), familyRN6,
				"RN-6 pane kill, "+k.title+", "+variantTitle(v.v), rn6Trigger, l, measureKill)
		}
	}
}
