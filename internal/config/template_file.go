package config

// TemplateFile is the on-disk TOML shape of a spawn template
// (SRD §10.2). Fields mirror the per-call spawn params with three
// deliberate omissions:
//
//   - template            — recursion would be ill-defined.
//   - claude_instance_id  — must be per-invocation (uniqueness).
//   - tmux_session_name   — must be per-invocation (derived from id).
//
// All fields are optional in the file; absent fields fall back to the
// per-call value (which itself may fall back to config defaults). The
// TOML library represents an absent map as nil and an empty `[...]`
// as a zero-length slice — Merge distinguishes them by nil-ness, so
// the file's emptiness is faithfully preserved through the round-trip.
type TemplateFile struct {
	CWD                  string               `toml:"cwd,omitempty"`
	RelayMode            string               `toml:"relay_mode,omitempty"`
	ClaudeArgs           []string             `toml:"claude_args,omitempty"`
	ExtraEnv             map[string]string    `toml:"extra_env,omitempty"`
	AgentDirectorLabels map[string]string    `toml:"labels,omitempty"`
	Permissions          *TemplatePermissions `toml:"permissions,omitempty"`
}

// TemplatePermissions mirrors the SRD §6.1 three-arrays surface. Each
// slice survives the TOML round-trip and feeds Merge's leaf-array
// concat path.
type TemplatePermissions struct {
	Allow []string `toml:"allow,omitempty"`
	Deny  []string `toml:"deny,omitempty"`
	Ask   []string `toml:"ask,omitempty"`
}
