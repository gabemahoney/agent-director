package spawn

import (
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// composeEnv builds the env-var map handed to tmux's -e flags (SRD §7.4
// step 1). The result is deterministic given identical inputs — order is
// not relevant to tmux, but a deterministic map makes test assertions
// straightforward.
//
// Composition rules:
//
//   - AGENT_DIRECTOR_INSTANCE_ID — always set; identifies the row to the
//     hook handler (SRD §3.2) and propagates parent_id (SRD §7.5).
//   - AGENT_DIRECTOR_RELAY_MODE  — always set; mirrors the per-Spawn
//     value so the hook can fail-closed without a DB read (SRD §6.5).
//   - AGENT_DIRECTOR_LABEL_<UPPER_KEY> — one per caller label, with the
//     key normalized to env-var form (uppercase, non-alphanumeric → '_').
//   - AGENT_DIRECTOR_AGENT=codex and BASH_ENV= (empty) — a codex agent only
//     (SR-10.1, Q5(c)); a BASH_ENV in ExtraEnv wins.
//   - everything in ExtraEnv — including auth env vars (ANTHROPIC_API_KEY,
//     CLAUDE_CODE_OAUTH_TOKEN). The validation step has already rejected
//     every key whose env-var name (the part before the first '=', the name
//     tmux sets) starts with AGENT_DIRECTOR_, so no shadowing is possible
//     here, or is HOME (ReservedHomeKey, bug b.nas), which would move the
//     agent's hook to another agent-director store.
//
// Label key collisions across normalization (e.g. "my-key" and "my_key"
// both → "MY_KEY") use the iteration order of the input map; the SRD does
// not specify a tiebreak. Callers should not rely on order.
func composeEnv(r Resolved) map[string]string {
	out := make(map[string]string, 2+len(r.AgentDirectorLabels)+len(r.ExtraEnv))
	out["AGENT_DIRECTOR_INSTANCE_ID"] = r.InstanceID
	out["AGENT_DIRECTOR_RELAY_MODE"] = r.RelayMode
	for k, v := range r.AgentDirectorLabels {
		out["AGENT_DIRECTOR_LABEL_"+normalizeLabelKey(k)] = v
	}
	if r.Agent == config.AgentCursor {
		out[EnvAgent] = config.AgentCursor
	}
	if r.Agent == config.AgentCodex {
		// A Codex launch only (a Claude launch's environment is unchanged).
		// The variable tells `agent-director help` the caller is a Codex
		// agent (EnvAgent). BASH_ENV is set empty so a startup file the
		// caller's shell would source never prints into the stdout a hook's
		// shell command writes (Q5(c)); a BASH_ENV in extra_env overwrites it
		// in the loop below. CODEX_HOME is never set here: it comes only from
		// extra_env, never from agent-director's own environment.
		out[EnvAgent] = EnvAgentCodex
		out["BASH_ENV"] = ""
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
