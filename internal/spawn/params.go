package spawn

import "github.com/gabemahoney/agent-director/internal/config"

// SpawnParams is the caller-supplied input to a `spawn` call. Field names
// map onto SRD §12 verb parameters (PascalCase here per Go convention; the
// CLI / MCP layers flatten to snake_case in their respective schemas).
type SpawnParams struct {
	// CWD is the directory the Spawn's Claude will start in. Required.
	// Absolute or "~/"-prefixed; validation canonicalizes via EvalSymlinks.
	CWD string

	// Template names a stored template under ~/.agent-director/templates/.
	// Epic 3 leaves the template merge a no-op (the field is preserved so
	// Epic 7 can fold real template loading in without API churn).
	Template string

	// TmuxSessionName is an explicit override. Almost always empty so the
	// defaults pass synthesizes <sanitize(basename(cwd))>-<id[:8]>.
	TmuxSessionName string

	// TmuxSessionNameSupplied distinguishes "caller explicitly passed
	// --tmux-session-name (possibly empty)" from "caller omitted the
	// flag". The CLI parser sets this via flag.FlagSet.Visit; the MCP
	// dispatcher sets it when tmux_session_name is present and not null
	// (b.7or). An explicit empty supplied value trips ErrTmuxSessionNameEmpty,
	// while a bare omission falls through to composeSessionName.
	// Templates never set this — templates cannot supply
	// tmux_session_name (SR-5.2).
	TmuxSessionNameSupplied bool

	// ClaudeInstanceID is an explicit override. Almost always empty so the
	// defaults pass mints a UUID4. When supplied, it collides with any
	// existing row (ErrInstanceIdCollision) unless ReuseFinished is set, in
	// which case a finished row is reused and a live row, pending included,
	// still collides.
	ClaudeInstanceID string

	// ExtraEnv injects KEY=VAL env vars on the tmux session. Keys whose
	// env-var name (the part before the first '=', the name tmux sets) is
	// reserved (AGENT_DIRECTOR_*, and HOME with any value) are rejected with
	// ErrReservedEnvKey; auth env vars (ANTHROPIC_API_KEY,
	// CLAUDE_CODE_OAUTH_TOKEN) are explicitly allowed. To give the agent its
	// own Claude Code config, set an absolute CLAUDE_CONFIG_DIR. Keys that are
	// not valid env-var names (empty, or holding '=' or a NUL byte) are
	// rejected with ErrReservedEnvKey too, never rewritten.
	ExtraEnv map[string]string

	// AgentDirectorLabels are caller-owned tags. Each key is normalized to
	// env-var form (uppercase, non-alphanumeric → '_') and exposed as
	// AGENT_DIRECTOR_LABEL_<UPPER_KEY>=<VAL> on the session env. The
	// labels also persist into the spawns.labels JSON column.
	AgentDirectorLabels map[string]string

	// ClaudeArgs is the verbatim argv passed through to `claude` after
	// --settings. The denied-flag check rejects supervisor-owned flags.
	ClaudeArgs []string

	// Permissions is the per-Spawn permission overlay synthesized into
	// --settings. allow / deny / ask arrays concatenate with the user's
	// settings tier per Claude Code's merge semantics.
	Permissions *Permissions

	// RelayMode is "on" / "off" / "" (use config default). Mirrored into
	// AGENT_DIRECTOR_RELAY_MODE on the session env for the hook.
	RelayMode string

	// NoPreTrust opts out of the folder-trust pre-write (PreTrust) into
	// <CLAUDE_CONFIG_DIR>/.claude.json when the spawn's extra env sets
	// CLAUDE_CONFIG_DIR, and ~/.claude.json otherwise; a CLAUDE_CONFIG_DIR
	// that is set but not an absolute path (ConfigDirUsable) gets no
	// pre-write at all, and PreTrust reports it failed. Default false
	// (= pre-trust IS performed) marks the cwd trusted so the agent skips
	// Claude Code's folder-trust prompt. True attempts no pre-trust, so the
	// prompt must be answered for an unseen cwd. The choice is recorded on
	// the row (no_pre_trust) for its life, and every resume of that life
	// follows it: no resume pre-trusts over an opt-out (SR-22.6).
	NoPreTrust bool

	// ReuseFinished opts in to reusing an explicit ClaudeInstanceID whose
	// row is finished (ended or missing); a live row, pending included,
	// still gives ErrInstanceIdCollision. It has no effect without an
	// explicit ClaudeInstanceID (a minted id cannot collide) and applies to
	// this call only; templates and config never carry it. Default false
	// keeps the collision behaviour: any existing row gives
	// ErrInstanceIdCollision. A successful spawn does not say whether it
	// created a fresh row or reset a finished one. A reused id starts a new
	// life with no memory of its earlier lives: Resume and Get never use or
	// show an earlier life's history, so the earlier conversation cannot be
	// resumed through agent-director afterwards. Go callers link the
	// library, so the field exists at compile time; CLI, MCP and TypeScript
	// callers detect it by the binary's version (the manifest's
	// reuse_finished text gives each surface's source).
	ReuseFinished bool
}

// Permissions captures the three SRD §6.1 arrays. Nil entries are omitted
// from the synthesized --settings JSON; empty (non-nil) entries serialize
// as [].
type Permissions struct {
	Allow []string
	Deny  []string
	Ask   []string
}

// Resolved is the post-template input to validation. Epic 3 keeps the
// template merge a no-op; Resolve(p, cfg) wraps p verbatim. Epic 7 will
// replace this with the real merge.
//
// The split between SpawnParams and Resolved keeps the validation /
// defaults functions decoupled from the template loader so unit tests can
// exercise them against synthesized inputs without touching disk.
type Resolved struct {
	SpawnParams
}

// Resolve folds template values into per-call params per SRD §7.1.
//
// Merge rules (template is the base; per-call layered on top):
//
//   - Scalars (CWD, RelayMode): per-call non-empty REPLACES the
//     template value. Per-call empty falls back to template.
//   - Top-level maps (ExtraEnv, AgentDirectorLabels): merge by key.
//     Template keys survive; per-call keys win on collision. Either
//     side may be nil — the result is nil only when both are nil.
//   - Permissions arrays (Allow, Deny, Ask): CONCAT. Template entries
//     come first, per-call entries appended. A nil per-call
//     Permissions field falls back to the template's value wholesale
//     (no concat happens because there's nothing to append).
//   - ClaudeArgs: per-call non-nil REPLACES the template's slice
//     wholesale. A nil per-call slice falls back to the template.
//     Explicit empty (len=0, non-nil) replaces (with an empty slice).
//
// When p.Template is empty (the common path for ad-hoc spawns) Resolve
// is a near no-op: it returns the params verbatim wrapped in Resolved.
// The function never mutates p.
func Resolve(p SpawnParams, _ config.Config) (Resolved, error) {
	if p.Template == "" {
		return Resolved{SpawnParams: p}, nil
	}

	tmpl, err := config.LoadTemplate(p.Template)
	if err != nil {
		return Resolved{}, err
	}

	merged := p

	// Scalars: per-call empty falls back to template.
	if merged.CWD == "" {
		merged.CWD = tmpl.CWD
	}
	if merged.RelayMode == "" {
		merged.RelayMode = tmpl.RelayMode
	}

	// Maps: merge with per-call winning on collision.
	merged.ExtraEnv = mergeStringMap(tmpl.ExtraEnv, merged.ExtraEnv)
	merged.AgentDirectorLabels = mergeStringMap(tmpl.AgentDirectorLabels, merged.AgentDirectorLabels)

	// ClaudeArgs: per-call replace wholesale. nil per-call → use
	// template. Explicit empty (`[]`) replaces with empty.
	if merged.ClaudeArgs == nil {
		// Defensive copy of the template slice so the caller can't
		// mutate the cached template state via the resolved struct.
		if tmpl.ClaudeArgs != nil {
			merged.ClaudeArgs = append([]string(nil), tmpl.ClaudeArgs...)
		}
	}

	// Permissions: concat each leaf array. Per-call nil → use template.
	switch {
	case merged.Permissions == nil && tmpl.Permissions == nil:
		// no permissions on either side; merged stays nil
	case merged.Permissions == nil:
		merged.Permissions = &Permissions{
			Allow: append([]string(nil), tmpl.Permissions.Allow...),
			Deny:  append([]string(nil), tmpl.Permissions.Deny...),
			Ask:   append([]string(nil), tmpl.Permissions.Ask...),
		}
	case tmpl.Permissions == nil:
		// per-call only; keep merged.Permissions as-is
	default:
		merged.Permissions = &Permissions{
			Allow: concatStrings(tmpl.Permissions.Allow, merged.Permissions.Allow),
			Deny:  concatStrings(tmpl.Permissions.Deny, merged.Permissions.Deny),
			Ask:   concatStrings(tmpl.Permissions.Ask, merged.Permissions.Ask),
		}
	}

	return Resolved{SpawnParams: merged}, nil
}

// mergeStringMap layers `over` on top of `base`. Per-call wins on
// collision. Returns nil iff both inputs are nil; otherwise allocates
// a fresh map so neither input is observably mutated.
func mergeStringMap(base, over map[string]string) map[string]string {
	if base == nil && over == nil {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// concatStrings returns a fresh slice with `base` first, then `over`.
// Either side may be nil. The result is nil only when both are nil.
func concatStrings(base, over []string) []string {
	if base == nil && over == nil {
		return nil
	}
	out := make([]string, 0, len(base)+len(over))
	out = append(out, base...)
	out = append(out, over...)
	return out
}
