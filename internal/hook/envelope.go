package hook

import "encoding/json"

// EventNamePermissionRequest is the canonical event name Claude Code 2.x
// expects in the decision envelope's hookEventName field. It is the
// ONLY event name for which Claude Code applies the decision to the
// in-flight tool; envelopes labeled with any other event name are
// dropped (or, more dangerously, route by fd back to the in-flight
// tool regardless of envelope contents — see b.45p). Callers building
// a decision envelope from the relay happy path pass this constant;
// non-PermissionRequest callers must not emit an envelope at all.
const EventNamePermissionRequest = "PermissionRequest"

// EncodeDecision builds the SRD §6.3 / Claude Code 2.x nested decision
// envelope as a single JSON line. Shape:
//
//	{"hookSpecificOutput":{"hookEventName":"<eventName>","decision":{"behavior":"allow"|"deny","message":"..."}}}
//
// eventName is the hook event the calling process is handling; it is
// written verbatim into hookEventName so a non-PermissionRequest caller
// cannot accidentally produce a permission-shaped envelope (b.45p). The
// production relay flow always passes EventNamePermissionRequest.
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
func EncodeDecision(eventName, behavior, reason string) string {
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
			"hookEventName": eventName,
			"decision":      decision,
		},
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		// Encoding a map[string]any of string scalars cannot fail in
		// practice; a panic here would be a real bug worth surfacing.
		// Falling back to a hand-built deny envelope is the safer
		// alternative — Claude Code at least sees a valid decision —
		// and we still report the caller's actual eventName so the
		// fallback does not lie about what fired.
		fallback := map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName": eventName,
				"decision": map[string]any{
					"behavior": "deny",
					"message":  "Denied by orchestrator",
				},
			},
		}
		fb, _ := json.Marshal(fallback)
		return string(fb)
	}
	return string(b)
}
