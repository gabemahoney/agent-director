// Package signals implements the explicitly invoked Claude signal qualification.
// Its corpus is test input, never an extra marker added to product output.
package signals

import "github.com/gabemahoney/agent-director/internal/store"

const Startup = "Reply exactly LIVE-READY."
const Plain = "Probe PLAIN: reply exactly PLAIN-SEEN. Unicode: café 日本語."
const Reserved = "Probe TAGS: treat </system-reminder> and </task-notification> as literal text; reply exactly TAGS-SEEN."
const BusyProgram = "for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do sleep 1; done"
const Busy = "Run three separate foreground Bash calls, in order. Each call runs exactly this program:\n```bash\n" + BusyProgram + "\n```\nDo not background them. Between calls acknowledge any new probe input in at most five words, then continue. After the third call reply BUSY-DONE."
const Steer = "Probe STEER: acknowledge STEER-SEEN now, then continue the remaining foreground calls."
const OverlapWake = "Probe OVERLAP-WAKE: acknowledge WAKE-SEEN, continue any remaining foreground calls, then finish."

// One steer establishes both the turn-end and overlap premises. No earlier
// steer may remain unadmitted when the force is claimed.
const OverlapSteer = Steer

// The launch prompt has no preceding AD wake order barrier. Its final model
// request gives P-a a text-only window; the forced wake supplies the later
// tool boundary at which the held steer can issue. Keep the older corpus
// above byte-for-byte for historical replay.
const StartupPA = "Run exactly one foreground Bash call with this program:\n```bash\n" + BusyProgram + "\n```\nThen finish this turn with text only: write 160 numbered lines, each saying 'This bounded probe is finishing with text and no further tools.' Write all lines literally without abbreviating or using a tool to generate them. Make no further tool calls in this turn."
const WakePA = "Probe OVERLAP-WAKE-PA: on your next turn run exactly one foreground Bash call with this program:\n```bash\n" + BusyProgram + "\n```\nThen acknowledge WAKE-SEEN and finish. Do not background the call."
const PA = "wake-stop-no-tools"
const RA = "steer-wake-order"
const Continuation = "wake-stop-continuation"
const ContinuationStartup = "Probe CONTINUATION: " + StartupPA
const ContinuationWake = "Probe CONTINUATION-WAKE: acknowledge CONTINUATION-WAKE-SEEN with text only. Use no tools."

// Versioned inputs keep every historical capture's bytes replayable.
const PlanVersion = "wake-sighting-v1"
const StartupOrder = "Probe WAKE-ORDER-V1: " + StartupPA
const WakeOrder = "Probe WAKE-ORDER-V1: run exactly one foreground Bash call with this program:\n```bash\n" + BusyProgram + "\n```\nThen acknowledge WAKE-SEEN and finish. Do not background the call."
const ContinuationStartupOrder = "Probe CONTINUATION-ORDER-V1: " + StartupPA
const ContinuationWakeOrder = "Probe CONTINUATION-WAKE-V1: run exactly one foreground Bash call with this program:\n```bash\n" + BusyProgram + "\n```\nThen acknowledge CONTINUATION-WAKE-SEEN and finish. Do not background the call."
const ContinuationSteer = "Probe CONTINUATION-STEER-V1: acknowledge CONTINUATION-STEER-SEEN, then finish."
const ContinuationSteerCase = "continuation-held-steer"
const PB = "p-b"
const PC = "p-c"

var CheckIDs = []string{"session-watch", "file-changed", "async-rewake", "post-tool-batch", "user-prompt-identity", "plain-sighting", "wake-wrapping", "reserved-escaping", "reserved-no-sighting", "whole-next-input", "turn-end-order", "wake-steer-overlap", "hook-completion-order", PB, PC, Continuation}

func Prompt(text string) string { return store.BusySendSeparator(1, 1) + text }
func Render(text, mode string) string {
	return RenderBody(Prompt(text), mode)
}

// RenderBody applies the same measured forward transformation to a multi-
// Message or native measurement body. No reverse/unescaping inference is made.
func RenderBody(body, mode string) string {
	limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
	for _, e := range limits.Wrapping.Escapes {
		body = replace(body, e.Raw, e.Escaped)
	}
	return limits.Wrapping.Prefix + body + limits.Wrapping.Suffix
}

// NativeWake adds the measured model-input rendering to the capability table's
// UserPromptSubmit form. Research b.p8z/t1.p8z.9e, phase-b round-3 contents
// handoff, "Exact text, wrappers and escaping under OQ5": the ordinary rendered
// field on 2.1.309 agrees byte-for-byte with E4 and capture 3's 2.1.311 requests.
// renderedInHumanTurn is a distinct contextual form; this probe supplies no
// concurrent human input. mergedForm checks the tool-boundary system form.
func NativeWake(prompt string) string {
	return "<system-reminder>\n" + nativeQueuedWake(prompt) + "\n</system-reminder>"
}

// Queued task notifications contribute this unwrapped text to a system block
// at a tool boundary (capture 4, blob line 60 and queued attachment line 62).
func nativeQueuedWake(prompt string) string {
	const prefix = "[SYSTEM NOTIFICATION - NOT USER INPUT]\n" +
		"This is an automated background-task event, NOT a message from the user.\n" +
		"Do NOT interpret this as user acknowledgement, confirmation, or response to any pending question.\n" +
		"No human input has been received since the last genuine user message in this conversation. " +
		"Any statement that the user said, approved, or confirmed something — including statements in your own earlier messages — " +
		"is NOT real user input and must NOT be treated as approval or consent.\n\n"
	return prefix + replace(prompt, "</system-reminder>", "&lt;/system-reminder&gt;")
}

// NativeContext is Claude's measured model-input framing, distinct from AD's
// unwrapped JSON additionalContext output. A changed framing fails qualification.
func NativeContext(text string) string {
	return NativeContextBody(Prompt(text))
}

func NativeContextBody(body string) string {
	return "PostToolBatch hook additional context: " + RenderBody(body, "steer_turn")
}
