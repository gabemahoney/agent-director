package spawn

import "strings"

// composeEnv builds the env-var map handed to tmux's -e flags (SRD §7.4
// step 1). The result is deterministic given identical inputs — order is
// not relevant to tmux, but a deterministic map makes test assertions
// straightforward.
//
// Composition rules:
//
//   - CLAUDE_DIRECTOR_INSTANCE_ID — always set; identifies the row to the
//     hook handler (SRD §3.2) and propagates parent_id (SRD §7.5).
//   - CLAUDE_DIRECTOR_RELAY_MODE  — always set; mirrors the per-Spawn
//     value so the hook can fail-closed without a DB read (SRD §6.5).
//   - CLAUDE_DIRECTOR_LABEL_<UPPER_KEY> — one per caller label, with the
//     key normalized to env-var form (uppercase, non-alphanumeric → '_').
//   - everything in ExtraEnv — including auth env vars (ANTHROPIC_API_KEY,
//     CLAUDE_CODE_OAUTH_TOKEN). The validation step has already rejected
//     CLAUDE_DIRECTOR_* keys, so no shadowing is possible here.
//
// Label key collisions across normalization (e.g. "my-key" and "my_key"
// both → "MY_KEY") use the iteration order of the input map; the SRD does
// not specify a tiebreak. Callers should not rely on order.
func composeEnv(r Resolved) map[string]string {
	out := make(map[string]string, 2+len(r.AgentDirectorLabels)+len(r.ExtraEnv))
	out["CLAUDE_DIRECTOR_INSTANCE_ID"] = r.ClaudeInstanceID
	out["CLAUDE_DIRECTOR_RELAY_MODE"] = r.RelayMode
	for k, v := range r.AgentDirectorLabels {
		out["CLAUDE_DIRECTOR_LABEL_"+normalizeLabelKey(k)] = v
	}
	for k, v := range r.ExtraEnv {
		out[k] = v
	}
	return out
}

// normalizeLabelKey applies SRD §7.2 step 5: uppercase, non-alphanumeric
// → '_'. The transformation is unidirectional — labels are also stored
// verbatim in the labels JSON column (SRD §19 Q12), so the env-var name
// does not need to round-trip.
func normalizeLabelKey(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	for _, r := range in {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
