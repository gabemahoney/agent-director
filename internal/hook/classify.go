package hook

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/store"
)

// Tool name carve-outs.
//
// AskUserQuestion is the only PreToolUse matcher whose tool_name routes
// to a distinct state (`ask_user`). All other tool names collapse to
// `working` per SRD §5.2.
const ToolAskUserQuestion = "AskUserQuestion"

// NotificationTypeIdlePrompt is the notification_type of Claude Code's
// idle-prompt Notification. Claude Code sends it only while its main agent is
// idle at the prompt, no main-agent turn running, once
// messageIdleNotifThresholdMs (60 s by default) has passed since the last turn
// ended (b.svb). Idle is the main agent's alone: before Claude Code 2.1.288
// (anthropics/claude-code#93672) it also fires while a background subagent is
// still running, and a row that subagent's hook moved to working then reads
// waiting, its tool possibly still running, until the subagent's next tool
// hook. Every other notification_type (permission_prompt, auth_success,
// elicitation_dialog, ...) is a soft refresh.
const NotificationTypeIdlePrompt = "idle_prompt"

// terminalSessionEndReasons enumerates the SessionEnd reason / matcher
// values that indicate the Claude Code session truly exited. Everything
// else — including missing-reason, "clear", "compact", auto-compaction —
// is treated as a soft-refresh.
//
// b.pmn: the original policy ("treat SessionEnd as ended unless reason
// matches a known soft set") caused false-positives because Claude Code's
// actual payload for auto-compaction doesn't include `reason: "compact"`
// in the form the classifier expected. Inverting the policy is safer:
// false-negatives on a real exit are caught by `find-missing` reaping
// the row when the tmux session disappears; false-positives on a soft
// event break monitors and orchestrators that act on `ended`.
var terminalSessionEndReasons = map[string]bool{
	"logout":            true,
	"prompt_input_exit": true,
	"exit":              true,
}

// payload is the leniently-typed shape ClassifyEvent reads. Fields are
// pointers / strings so missing values surface as zero values rather than
// JSON errors — the soft-refresh-on-unknown-event behavior depends on us
// being able to read what's there without throwing on what isn't.
//
// SessionEnd carries the exit cause in one of several field names across
// Claude Code versions (`reason`, `matcher`, `endReason`). We accept all
// three so the classifier remains stable across upstream payload renames.
type payload struct {
	// Claude Code's standard field name. The SRD's test examples use
	// `event_name`; we accept both so the surface is forgiving.
	HookEventName  string `json:"hook_event_name"`
	EventName      string `json:"event_name"`
	ToolName       string `json:"tool_name"`
	Reason         string `json:"reason"`
	Matcher        string `json:"matcher"`
	EndReason      string `json:"endReason"`
	TranscriptPath string `json:"transcript_path"`
	AgentID        string `json:"agent_id"`
	// NotificationType is a Notification's kind; see
	// NotificationTypeIdlePrompt.
	NotificationType string `json:"notification_type"`
	// ToolUseID is a tool event's tool_use_id (PreToolUse, PostToolUse,
	// PostToolUseFailure, PermissionRequest).
	ToolUseID string `json:"tool_use_id"`
}

// sessionEndCause picks the best-available exit-cause field from a
// SessionEnd payload — `reason` first, then `matcher`, then `endReason`.
// Returns the empty string when none are present (in which case the
// classifier defaults to soft-refresh; see terminalSessionEndReasons).
func (p payload) sessionEndCause() string {
	if p.Reason != "" {
		return p.Reason
	}
	if p.Matcher != "" {
		return p.Matcher
	}
	return p.EndReason
}

// eventName picks the best-available event name from the payload — Claude
// Code's `hook_event_name` first, the SRD test fixtures' `event_name`
// second. Empty if neither present.
func (p payload) eventName() string {
	if p.HookEventName != "" {
		return p.HookEventName
	}
	return p.EventName
}

// ClassifyResult is the typed outcome of one classification pass. Callers
// use NewState / SoftRefresh / SessionID to drive the gated row write, and
// EventName for telemetry / log lines on unknown events.
type ClassifyResult struct {
	// EventName is the canonical event name as parsed from the payload.
	// Empty when the payload had no event-name field at all.
	EventName string

	// NewState is the post-event row state. Empty when SoftRefresh is true
	// (the row stays in its current state and only last_seen_at is bumped).
	NewState string

	// SoftRefresh is true for events that should bump last_seen_at without
	// changing state — SessionEnd reason=clear|compact, Notification, and unknown events.
	// An idle-prompt Notification keeps it true as well; see WaitingIfWorking.
	SoftRefresh bool

	// WaitingIfWorking is true for the main agent's idle-prompt Notification
	// (notification_type idle_prompt, no agent_id; b.svb): a row that is
	// working when the write lands returns to waiting, as does a row with
	// relay_mode on in check_permission none of whose permission requests
	// still awaits an answer (b.146 problem 3), a row in any other state gets
	// the soft
	// refresh, and every applied write records idle_since. The idle prompt
	// means the main agent's turn
	// has ended, so a working row was left there by a hook with no Stop after
	// it, such as a PreToolUse from a background fork after the turn's Stop
	// or, before Claude Code 2.1.288, from a background subagent still
	// running (see NotificationTypeIdlePrompt). SoftRefresh stays true alongside it, so a writer that does not act on
	// WaitingIfWorking makes the soft refresh.
	WaitingIfWorking bool

	// SessionID is the basename-without-extension of transcript_path, for
	// every event whose payload carries the path. It is recorded, never a
	// gate (SR-22.9): SessionStart writes it to spawns.claude_session_id
	// (SRD §8.3), an applied ordinary hook writes it when the row records no
	// session id yet and the hook carries no AgentID, and ad.hook.ignored
	// reports it as hook_session_id (SR-14).
	SessionID string

	// TranscriptPath is the full hook-reported transcript_path, for every
	// event. It is written verbatim to spawns.jsonl_path (SR-9.1) with the
	// session id it names, independent of whether the basename SessionID
	// extraction succeeded. Empty means "don't write the column".
	TranscriptPath string

	// UnknownEvent is true when the payload's event name does not match
	// any documented value. Production callers log this at info level so
	// upstream Claude Code additions surface in operator logs.
	UnknownEvent bool

	// ToolName is the tool_name field from the payload verbatim. Empty when
	// the payload carried no tool_name. Used by trail emission (SR-A-2.1)
	// to populate the top-level tool_name field.
	ToolName string

	// AgentID is the payload's agent_id verbatim: non-empty when the hook
	// comes from a subagent or an in-process teammate running inside the
	// agent's own process (SR-22.9; WD 2026-09-30b). agent_type alone (a
	// session started with --agent) does not mark one and is not read.
	AgentID string

	// ToolUseID is the payload's tool_use_id verbatim; empty when it carried
	// none. A PostToolUse or PostToolUseFailure carrying it closes the
	// fallen-back permission request with the same tool_use_id (ToolRan;
	// b.146 rule 13) and proves every request with it gone (b.146 step 2c).
	ToolUseID string
}

// ToolRan reports whether the result is a PostToolUse or PostToolUseFailure
// carrying a tool_use_id: Claude Code ran that tool use, so a permission
// dialog for it was answered allow (b.146 rule 13).
func (r ClassifyResult) ToolRan() bool {
	return r.ToolUseID != "" && (r.EventName == EventNamePostToolUse || r.EventName == EventNamePostToolUseFailure)
}

// The tool events whose hook means the tool ran (b.146 rule 13).
const (
	EventNamePostToolUse        = "PostToolUse"
	EventNamePostToolUseFailure = "PostToolUseFailure"
)

// EventNameStop is the main agent's end of turn.
const EventNameStop = "Stop"

// TurnEnded reports whether the result is the main agent's end of turn (b.146
// step 2c): a Stop, or the idle-prompt Notification (WaitingIfWorking), with
// no agent_id. Claude Code sends either only once the main agent's turn has
// ended, so no permission dialog the main agent asked before it is still
// waiting. A hook carrying an agent_id (a subagent or an in-process
// teammate) is not one: a background subagent can outlive the main turn.
func (r ClassifyResult) TurnEnded() bool {
	return r.AgentID == "" && (r.EventName == EventNameStop || r.WaitingIfWorking)
}

// SubagentLifecycle reports whether the result is a SessionStart or
// SessionEnd from a subagent or an in-process teammate (a non-empty
// AgentID). Such a hook changes nothing and is ignored as subagent_event
// (SR-22.9).
func (r ClassifyResult) SubagentLifecycle() bool {
	return r.AgentID != "" && (r.EventName == "SessionStart" || r.EventName == "SessionEnd")
}

// PeekEventName extracts the event name from a raw hook payload without
// performing full classification. It is called BEFORE ResolveInstanceID
// so that failClosed can gate its envelope write on event type from the
// very first failure point. On any parse failure it returns "" — the
// caller must treat that as "unknown event" and fall back to silent
// exit (fail-open) because emitting a permission envelope without
// knowing the event would re-introduce b.45p.
func PeekEventName(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.eventName()
}

// ClassifyEvent applies the SRD §5.2 hook-event → state table. The
// function never returns a typed error for unknown / malformed payloads —
// state-tracking is fail-open, so the result's SoftRefresh / UnknownEvent
// flags carry the decision and the handler treats it as a no-op write.
//
// A JSON parse failure is the only error returned: an unparseable payload
// is logged and the handler exits 0 silently.
func ClassifyEvent(raw json.RawMessage) (ClassifyResult, error) {
	var p payload
	if len(raw) == 0 {
		return ClassifyResult{SoftRefresh: true}, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ClassifyResult{}, err
	}

	res := ClassifyResult{
		EventName:      p.eventName(),
		ToolName:       p.ToolName,
		SessionID:      extractSessionID(p.TranscriptPath),
		TranscriptPath: p.TranscriptPath,
		AgentID:        p.AgentID,
		ToolUseID:      p.ToolUseID,
	}

	switch res.EventName {
	case "SessionStart":
		res.NewState = store.StateWaiting
	case "UserPromptSubmit":
		res.NewState = store.StateWorking
	case "PreToolUse":
		if p.ToolName == ToolAskUserQuestion {
			res.NewState = store.StateAskUser
		} else {
			res.NewState = store.StateWorking
		}
	case EventNamePostToolUse, EventNamePostToolUseFailure:
		// The tool ran (and, for PostToolUseFailure, failed): the turn goes
		// on, as after PostToolUse.
		res.NewState = store.StateWorking
	case EventNameStop:
		res.NewState = store.StateWaiting
	case "Notification":
		// b.svb: the main agent's idle-prompt Notification returns a working
		// row, or a relayed check_permission row with no request that still
		// awaits an answer (b.146 problem 3), to waiting (before Claude Code 2.1.288
		// also while a background subagent still runs; see
		// NotificationTypeIdlePrompt); any other Notification, and an idle
		// prompt from a subagent or in-process teammate, is a soft refresh.
		res.SoftRefresh = true
		res.WaitingIfWorking = p.NotificationType == NotificationTypeIdlePrompt && p.AgentID == ""
	case "PermissionRequest":
		res.NewState = store.StateCheckPermission
	case "SessionEnd":
		// b.pmn: soft-refresh by default. Only mark `ended` on known-
		// terminal causes. Empty/unknown causes (which Claude Code emits
		// during auto-compaction and other non-terminal SessionEnd fires)
		// stay at the current state; find-missing reaps the row if the
		// tmux session genuinely disappeared.
		if terminalSessionEndReasons[p.sessionEndCause()] {
			res.NewState = store.StateEnded
		} else {
			res.SoftRefresh = true
		}
	default:
		// Unknown event name. Soft-refresh so we still bump last_seen_at
		// (the Spawn is at least alive enough to fire hooks), and flag for
		// info-log so the operator notices a new Claude Code event.
		res.SoftRefresh = true
		res.UnknownEvent = true
	}

	return res, nil
}

// extractSessionID parses the basename-without-extension of transcript_path
// per SRD §8.3. Empty input or a path that's just a dotfile collapses to
// "", which the handler treats as "don't write the column" — we never want
// to overwrite a known session_id with garbage.
func extractSessionID(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	base := filepath.Base(transcriptPath)
	if base == "/" || base == "." || base == "" {
		return ""
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	// A UUID looks like 8-4-4-4-12 = 36 chars; we don't enforce that here
	// (Claude Code occasionally rotates the format) but reject obviously
	//-bogus values like "..".
	if base == "." || base == ".." {
		return ""
	}
	return base
}
