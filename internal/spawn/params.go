package spawn

import "github.com/gabemahoney/claude-director/internal/config"

// SpawnParams is the caller-supplied input to a `spawn` call. Field names
// map onto SRD §12 verb parameters (PascalCase here per Go convention; the
// CLI / MCP layers flatten to snake_case in their respective schemas).
type SpawnParams struct {
	// CWD is the directory the Spawn's Claude will start in. Required.
	// Absolute or "~/"-prefixed; validation canonicalizes via EvalSymlinks.
	CWD string

	// Template names a stored template under ~/.claude-director/templates/.
	// Epic 3 leaves the template merge a no-op (the field is preserved so
	// Epic 7 can fold real template loading in without API churn).
	Template string

	// TmuxSessionName is an explicit override. Almost always empty so the
	// defaults pass synthesizes <sanitize(basename(cwd))>-<id[:8]>.
	TmuxSessionName string

	// ClaudeInstanceID is an explicit override. Almost always empty so the
	// defaults pass mints a UUID4. When supplied, validation checks it
	// against the store for live-state collisions before INSERT.
	ClaudeInstanceID string

	// ExtraEnv injects KEY=VAL env vars on the tmux session. Reserved
	// keys (CLAUDE_DIRECTOR_*) are rejected; auth env vars
	// (ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN) are explicitly allowed.
	ExtraEnv map[string]string

	// ClaudeDirectorLabels are caller-owned tags. Each key is normalized to
	// env-var form (uppercase, non-alphanumeric → '_') and exposed as
	// CLAUDE_DIRECTOR_LABEL_<UPPER_KEY>=<VAL> on the session env. The
	// labels also persist into the spawns.labels JSON column.
	ClaudeDirectorLabels map[string]string

	// ClaudeArgs is the verbatim argv passed through to `claude` after
	// --settings. The denied-flag check rejects supervisor-owned flags.
	ClaudeArgs []string

	// Permissions is the per-Spawn permission overlay synthesized into
	// --settings. allow / deny / ask arrays concatenate with the user's
	// settings tier per Claude Code's merge semantics.
	Permissions *Permissions

	// RelayMode is "on" / "off" / "" (use config default). Mirrored into
	// CLAUDE_DIRECTOR_RELAY_MODE on the session env for the hook.
	RelayMode string
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

// Resolve folds template values into per-call params. TODO(epic-7):
// template merge per SRD §7.1 — load template via internal/config, apply
// per-tier merge rules (scalars replace, maps merge by key, arrays inside
// permissions concatenate). For Epic 3 we deliberately punt: the caller's
// fields are the resolved fields.
func Resolve(p SpawnParams, _ config.Config) (Resolved, error) {
	return Resolved{SpawnParams: p}, nil
}
