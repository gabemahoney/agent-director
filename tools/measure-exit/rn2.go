package main

// RN-2 (SRD Open Questions): the time from the row's stored ended_at,
// written by agent-director's SessionEnd hook, to the moment the agent's
// session no longer lists on the private server (100 ms polls). It informs
// the stopping window (90 s) and its minimum (30 s). All three cases run
// under the default SessionEnd budget (the minimum's rule reads them); the
// budgets in force are still recorded.
//
// Case ids (stable): rn2.natural, rn2.pause, rn2.mcp.

// rn2PauseTrigger is the pause exit as the results state it.
const rn2PauseTrigger = "agent-director pause (sends /exit and waits for the row to end); the listing poll starts when pause returns"

func init() {
	registerSampled("rn2.natural", familyRN2, "RN-2 natural exit (Ctrl-D at the idle prompt)",
		naturalExitTrigger, launch{}, measureNaturalExit)
	registerSampled("rn2.pause", familyRN2, "RN-2 pause's /exit",
		rn2PauseTrigger, launch{}, measurePause)
	registerSampled("rn2.mcp", familyRN2, "RN-2 pause's /exit with the deployment's MCP servers attached",
		rn2PauseTrigger, launch{mcp: true}, measurePause)
}
