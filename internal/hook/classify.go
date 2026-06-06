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
// use NewState / SoftRefresh / SessionID to drive the row UPSERT, and
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
	SoftRefresh bool

	// SessionID is the basename-without-extension of transcript_path when
	// the event is SessionStart and the path is present. Used to update
	// spawns.claude_session_id (SRD §8.3).
	SessionID string

	// UnknownEvent is true when the payload's event name does not match
	// any documented value. Production callers log this at info level so
	// upstream Claude Code additions surface in operator logs.
	UnknownEvent bool

	// ToolName is the tool_name field from the payload verbatim. Empty when
	// the payload carried no tool_name. Used by trail emission (SR-A-2.1)
	// to populate the top-level tool_name field.
	ToolName string
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

	res := ClassifyResult{EventName: p.eventName(), ToolName: p.ToolName}

	switch res.EventName {
	case "SessionStart":
		res.NewState = store.StateWaiting
		res.SessionID = extractSessionID(p.TranscriptPath)
	case "UserPromptSubmit":
		res.NewState = store.StateWorking
	case "PreToolUse":
		if p.ToolName == ToolAskUserQuestion {
			res.NewState = store.StateAskUser
		} else {
			res.NewState = store.StateWorking
		}
	case "PostToolUse":
		res.NewState = store.StateWorking
	case "Stop":
		res.NewState = store.StateWaiting
	case "Notification":
		res.SoftRefresh = true
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
