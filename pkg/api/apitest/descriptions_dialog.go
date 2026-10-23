package apitest

// descriptions_dialog.go holds the shared description helper's cases of the
// proof texts of b.146 step 2c as they read on a Codex row (b.ssz Epic 4):
// ErrDialogMaybeOpen's description names what would prove the request gone,
// and that is the agent's own signal. A Claude row's text names Claude Code's
// proofs (DescDialogMaybeOpenClaude); a Codex row's request carries no
// tool_use_id and no PostToolUse proves it, so its text names the main
// agent's Stop or Interrupt instead (DescDialogMaybeOpenCodex). The
// manifest's proven_gone_how text says the same of the field
// (DescProvenGoneHow).

// DescDialogMaybeOpenClaude is ErrDialogMaybeOpen's description on a Claude
// row: the tool_use_id PostToolUse, the end of the main agent's turn and the
// end of the agent are the proofs, the other requests it counts are those
// that still hold the dialog, and it says nothing was sent. It never names an
// Interrupt. Check it with AssertDescription.
func DescDialogMaybeOpenClaude() DescCase {
	return DescCase{
		Name: "ErrDialogMaybeOpen on a Claude row",
		Require: []string{
			"is not proven gone (no PostToolUse with its tool_use_id, no end of the main agent's turn after it for a request with no agent_id, and no end of its agent is recorded)",
			"so its permission dialog may still be on the pane",
			"other request(s) of the spawn still hold the dialog (err_details)",
			"nothing was sent",
		},
		MustNot: []string{"Interrupt", "are not proven gone"},
	}
}

// DescDialogMaybeOpenCodex is ErrDialogMaybeOpen's description on a Codex row:
// the proofs are the main agent's Stop or Interrupt after the request (for a
// request with no agent_id) and the end of the agent, the other requests it
// counts are those that still hold the dialog, and it never names a
// PostToolUse or a tool_use_id, which prove nothing on Codex. Check it with
// AssertDescription.
func DescDialogMaybeOpenCodex() DescCase {
	return DescCase{
		Name: "ErrDialogMaybeOpen on a Codex row",
		Require: []string{
			"is not proven gone (no Stop or Interrupt of the main agent after it for a request with no agent_id, and no end of its agent is recorded)",
			"so its permission dialog may still be on the pane",
			"other request(s) of the spawn still hold the dialog (err_details)",
			"nothing was sent",
		},
		MustNot: []string{"PostToolUse", "tool_use_id", "are not proven gone"},
	}
}

// DescProvenGoneHow is the manifest's text of the proven_gone_how field on a
// permission request's delivery facts: tool_ran is Claude hook or validated
// Cursor execution proof, never Codex. turn_end is the main Stop or idle-prompt
// Notification, Codex Interrupt or verified Cursor main Stop/Interrupt only
// for durable eligible origin. Check it with AssertAgentTextCase.
func DescProvenGoneHow() DescCase {
	return DescCase{
		Name: "proven_gone_how field",
		Require: []string{
			"tool_ran: a Claude PostToolUse or PostToolUseFailure carried its tool_use_id",
			"validated correlated Cursor execution output established that exact tool ran (never on a Codex row)",
			"turn_end: the turn of the agent that asked ended after it was written",
			"the main agent's Stop or idle-prompt Notification, a Codex main agent's Interrupt (Esc), or a verified Cursor main Stop/Interrupt",
			"proves only a durably main-turn-eligible request recorded before the hook began",
			"Legacy non-Cursor main requests retain eligibility",
			"Cursor unknown-origin and child requests are ineligible even without a child id",
			"Decisions, delivery acknowledgements and pane-answer claims do not grant eligibility",
			"agent_gone: its non-Cursor spawn was marked missing, ended or resumed",
		},
	}
}
