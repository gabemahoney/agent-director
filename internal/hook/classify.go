package hook

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/claude-director/internal/store"
)

// Tool name carve-outs.
//
// AskUserQuestion is the only PreToolUse matcher whose tool_name routes
// to a distinct state (`ask_user`). All other tool names collapse to
// `working` per SRD §5.2.
const ToolAskUserQuestion = "AskUserQuestion"

// softRefreshReasons enumerates the SessionEnd reason values that
// translate to a soft-refresh (last_seen_at update only). The set is
// closed per SRD §5.2 footnote; new reasons fall through to `ended`.
var softRefreshReasons = map[string]bool{
	"clear":   true,
	"compact": true,
}

// payload is the leniently-typed shape ClassifyEvent reads. Fields are
// pointers / strings so missing values surface as zero values rather than
// JSON errors — the soft-refresh-on-unknown-event behavior depends on us
// being able to read what's there without throwing on what isn't.
type payload struct {
	// Claude Code's standard field name. The SRD's test examples use
	// `event_name`; we accept both so the surface is forgiving.
	HookEventName  string `json:"hook_event_name"`
	EventName      string `json:"event_name"`
	ToolName       string `json:"tool_name"`
	Reason         string `json:"reason"`
	TranscriptPath string `json:"transcript_path"`
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
	// changing state — SessionEnd reason=clear|compact and unknown events.
	SoftRefresh bool

	// SessionID is the basename-without-extension of transcript_path when
	// the event is SessionStart and the path is present. Used to update
	// spawns.claude_session_id (SRD §8.3).
	SessionID string

	// UnknownEvent is true when the payload's event name does not match
	// any documented value. Production callers log this at info level so
	// upstream Claude Code additions surface in operator logs.
	UnknownEvent bool
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

	res := ClassifyResult{EventName: p.eventName()}

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
		res.NewState = store.StateWaiting
	case "PermissionRequest":
		res.NewState = store.StateCheckPermission
	case "SessionEnd":
		if softRefreshReasons[p.Reason] {
			res.SoftRefresh = true
		} else {
			res.NewState = store.StateEnded
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
