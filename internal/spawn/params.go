package spawn

import "github.com/gabemahoney/agent-director/internal/config"

// SpawnParams is the caller-supplied input to a `spawn` call. Field names
// map onto SRD §12 verb parameters (PascalCase here per Go convention; the
// CLI / MCP layers flatten to snake_case in their respective schemas).
type SpawnParams struct {
	// CWD is the directory the Spawn's agent will start in. Required.
	// Absolute or "~/"-prefixed; validation canonicalizes via EvalSymlinks.
	CWD string

	// Template names a stored template under ~/.config/agent-director/templates/.
	// Epic 3 leaves the template merge a no-op (the field is preserved so
	// Epic 7 can fold real template loading in without API churn).
	Template string

	// TmuxSessionName is an explicit override. Almost always empty so the
	// defaults pass synthesizes <sanitize(basename(cwd))>-<id[:8]>.
	TmuxSessionName string

	// TmuxSessionNameSupplied distinguishes "caller explicitly passed
	// tmux_session_name (possibly empty)" from "caller omitted it". The
	// CLI parser sets this via flag.FlagSet.Visit; the MCP
	// dispatcher sets it when tmux_session_name is present and not null
	// (b.7or). An explicit empty supplied value trips ErrTmuxSessionNameEmpty,
	// while a bare omission falls through to composeSessionName.
	// Templates never set this — templates cannot supply
	// tmux_session_name (SR-5.2).
	TmuxSessionNameSupplied bool

	// InstanceID is an explicit override. Almost always empty so the
	// defaults pass mints a UUID4. When supplied, it collides with any
	// existing row (ErrInstanceIdCollision) unless ReuseFinished is set, in
	// which case a finished row is reused and a live row, pending included,
	// still collides.
	InstanceID string

	// ExtraEnv injects KEY=VAL env vars on the tmux session. Keys whose
	// env-var name (the part before the first '=', the name tmux sets) is
	// reserved (AGENT_DIRECTOR_*, and HOME with any value) are rejected with
	// ErrReservedEnvKey; auth env vars (ANTHROPIC_API_KEY,
	// CLAUDE_CODE_OAUTH_TOKEN) are explicitly allowed. To give the agent its
	// own config, set an absolute CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME
	// (Codex). Keys that are not valid env-var names (empty, or holding '=' or
	// a NUL byte) are rejected with ErrReservedEnvKey too, never rewritten.
	ExtraEnv map[string]string

	// AgentDirectorLabels are caller-owned tags. Each key is normalized to
	// env-var form (uppercase, non-alphanumeric → '_') and exposed as
	// AGENT_DIRECTOR_LABEL_<UPPER_KEY>=<VAL> on the session env. The
	// labels also persist into the spawns.labels JSON column.
	AgentDirectorLabels map[string]string

	// Agent is the agent the spawn is for: "claude" or "codex", compared
	// case-sensitively; empty means not given. As the caller sets it, it is
	// the per-call agent; Resolve fills an empty one from the template's
	// agent, so after Resolve it is the resolved agent (SR-1.2) and empty
	// only when neither names one (Validate then gives ErrAgentMissing). No
	// config key or environment variable supplies it (SR-1.1).
	Agent string

	// AgentArgs is the verbatim argv passed through to the agent program.
	// For claude they follow the supervisor's own flags and the denied-flag
	// check rejects supervisor-owned flags. For codex they precede
	// agent-director's per-launch settings block, which sits before the
	// first "--"; CheckCodexArgs refuses what would fight those settings.
	// Nil means not supplied (a template's
	// are inherited); a supplied empty list inherits nothing.
	AgentArgs []string

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
	// pre-write at all, and PreTrust reports it failed. A codex agent has no
	// file: its pre-trust puts the cwd's trust and the hook-trust bypass on
	// the launch's command line (CodexPreTrust), and a CODEX_HOME in the
	// extra env that is set but not absolute reports it failed. Default false
	// (= pre-trust IS performed) marks the cwd trusted so the agent skips
	// its folder-trust prompt. True attempts no pre-trust, so the
	// prompt must be answered for an unseen cwd. The choice is recorded on
	// the row (no_pre_trust) until the next spawn or reuse, and every resume
	// follows it: no resume pre-trusts over an opt-out (SR-22.6).
	NoPreTrust bool

	// ReuseFinished opts in to reusing an explicit InstanceID whose
	// row is finished (ended or missing); a live row, pending included,
	// still gives ErrInstanceIdCollision. It has no effect without an
	// explicit InstanceID (a minted id cannot collide) and applies to
	// this call only; templates and config never carry it. Default false
	// keeps the collision behaviour: any existing row gives
	// ErrInstanceIdCollision. A successful spawn does not say whether it
	// created a fresh row or reset a finished one. Same-agent reuse starts a
	// fresh conversation and retains session history and subagent records.
	// Only an agent switch starts a new life; switching back also starts with
	// no memory of its earlier lives: resume and get never use or show an
	// earlier life's history. Resume continues only the latest session; older
	// sessions are listed for inspection only. Go callers link the
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

	// InheritedArgsAgent is the agent of the template whose non-empty
	// agent_args this spawn inherited, and "" when it inherited none (no
	// template, a template with no agent_args, or a call that supplied its
	// own list, an empty one included). Validate refuses a value that differs
	// from the resolved agent (ErrAgentArgsMismatch, SR-2.1).
	InheritedArgsAgent string

	// DisableAskUser is cfg.Defaults.DisableAskUser as Resolve saw it: when
	// true, Codex's question tool is kept off, so Validate refuses a Codex
	// argument that would turn it back on (CheckCodexArgs). The Codex launch
	// composition adds the deny group and the tool-off setting from
	// cfg.Defaults.DisableAskUser; both values come from the same loaded
	// config. It has no effect on a Claude spawn.
	DisableAskUser bool
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
//   - Agent: per-call non-empty REPLACES the template's; per-call empty
//     takes the template's (which may be empty). Resolve does not check the
//     value; Validate does (SR-1.3).
//   - AgentArgs: per-call non-nil REPLACES the template's slice
//     wholesale. A nil per-call slice falls back to the template.
//     Explicit empty (len=0, non-nil) replaces (with an empty slice).
//     Inherited non-empty template arguments are recorded in
//     Resolved.InheritedArgsAgent for the ownership check (SR-2.1).
//
// When p.Template is empty (the common path for ad-hoc spawns) Resolve
// is a near no-op: it returns the params verbatim wrapped in Resolved.
// The function never mutates p.
func Resolve(p SpawnParams, cfg config.Config) (Resolved, error) {
	if p.Template == "" {
		return Resolved{SpawnParams: p, DisableAskUser: cfg.Defaults.DisableAskUser}, nil
	}

	tmpl, err := config.LoadTemplate(p.Template)
	if err != nil {
		return Resolved{}, err
	}

	merged := p
	var inheritedArgsAgent string

	// Scalars: per-call empty falls back to template.
	if merged.CWD == "" {
		merged.CWD = tmpl.CWD
	}
	if merged.RelayMode == "" {
		merged.RelayMode = tmpl.RelayMode
	}
	if merged.Agent == "" {
		merged.Agent = tmpl.Agent
	}

	// Maps: merge with per-call winning on collision.
	merged.ExtraEnv = mergeStringMap(tmpl.ExtraEnv, merged.ExtraEnv)
	merged.AgentDirectorLabels = mergeStringMap(tmpl.AgentDirectorLabels, merged.AgentDirectorLabels)

	// AgentArgs: per-call replace wholesale. nil per-call → use
	// template. Explicit empty (`[]`) replaces with empty.
	if merged.AgentArgs == nil {
		// Defensive copy of the template slice so the caller can't
		// mutate the cached template state via the resolved struct.
		if tmpl.AgentArgs != nil {
			merged.AgentArgs = append([]string(nil), tmpl.AgentArgs...)
		}
		if len(tmpl.AgentArgs) > 0 {
			inheritedArgsAgent = tmpl.Agent
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

	return Resolved{SpawnParams: merged, InheritedArgsAgent: inheritedArgsAgent, DisableAskUser: cfg.Defaults.DisableAskUser}, nil
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
