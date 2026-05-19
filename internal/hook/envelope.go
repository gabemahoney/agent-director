package hook

import "encoding/json"

// EncodeDecision builds the SRD §6.3 / Claude Code 2.x nested decision
// envelope as a single JSON line. Shape:
//
//	{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"|"deny","message":"..."}}}
//
// On a `deny` with an empty reason, the message defaults to
// "Denied by orchestrator" so the TUI always shows the user
// something actionable. On an `allow` with an empty reason, the
// message field is omitted entirely — Claude Code silently drops
// empty messages and we mirror that.
//
// The function returns the JSON serialized form WITHOUT a trailing
// newline; callers append the newline when writing to stdout (the
// hook handler does so via fmt.Fprintln, mirroring how the existing
// state-tracking hook emits its empty output).
//
// Adopted byte-for-byte from claude-status's `hookspec.EncodeHookDecision`
// — that reference is the empirical Claude Code 2.x contract.
func EncodeDecision(behavior, reason string) string {
	decision := map[string]any{
		"behavior": behavior,
	}
	switch {
	case behavior == "deny" && reason == "":
		decision["message"] = "Denied by orchestrator"
	case reason != "":
		decision["message"] = reason
	}
	envelope := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      decision,
		},
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		// Encoding a map[string]any of string scalars cannot fail in
		// practice; a panic here would be a real bug worth surfacing.
		// Returning the canonical deny envelope is the safer
		// alternative — Claude Code at least sees a valid decision.
		return `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Denied by orchestrator"}}}`
	}
	return string(b)
}
