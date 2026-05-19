// Package manifest is the single source of truth for the claude-director
// CLI/MCP verb surface.
//
// Each VerbDef entry records a verb's name, description, parameters, result
// fields, and the set of error names it may emit. The CLI dispatch table, the
// MCP tool schema (Epic 11), and the generated reference docs
// (docs/cli-reference.md, docs/mcp-reference.md — Task 6 of Epic 1) all
// derive from Verbs. Adding a verb in any other way drifts from the manifest
// and is caught by the CI doc-drift gate.
//
// This package is deliberately minimal: types, a Verbs slice, and a Lookup
// helper. It does not import internal/store, internal/config, or cmd/ —
// dependencies flow downward toward internal/store, never sideways or up.
package manifest

//go:generate go run github.com/gabemahoney/claude-director/tools/gen-docs

// VerbDef describes one CLI/MCP verb exposed by claude-director.
type VerbDef struct {
	Name         string
	Description  string
	Params       []ParamDef
	ResultFields []FieldDef
	ErrorNames   []string
}

// ParamDef describes one input parameter of a verb.
type ParamDef struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// FieldDef describes one field of a verb's result object.
type FieldDef struct {
	Name        string
	Type        string
	Description string
}

// Verbs is the canonical, ordered list of verbs implemented by this binary.
// Epic 2+ workers append entries here as they implement new verbs.
var Verbs = []VerbDef{
	{
		Name:        "help",
		Description: "Print the manifest-derived list of verbs as JSON; intended for SessionStart / SessionEnd reason=compact hooks.",
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{
				Name:        "verbs",
				Type:        "[]VerbSummary",
				Description: "Array of {name, description} for every verb in the manifest.",
			},
		},
		// Empty (not nil) so JSON marshalling renders [] consistently and
		// SRD §12.4 "help has no error conditions" is reflected in shape.
		ErrorNames: []string{},
	},
	{
		Name:        "spawn",
		Description: "Launch a tracked Claude Code instance inside a new tmux session. Fire-and-forget: returns the claude_instance_id; state moves from pending to waiting on the first SessionStart hook.",
		Params: []ParamDef{
			{
				Name:        "cwd",
				Type:        "string",
				Description: "Absolute (or ~/-prefixed) path the Spawn's Claude starts in. Required.",
				Required:    true,
			},
			{
				Name:        "template",
				Type:        "string",
				Description: "Optional named template under ~/.claude-director/templates/. Per-call params layer on top per SRD §7.1 (scalars replace; maps merge; permissions arrays concat; claude_args replaces wholesale).",
			},
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Optional explicit id (UUID4 minted when absent). Collision against a live row returns ErrInstanceIdCollision.",
			},
			{
				Name:        "label",
				Type:        "[]string (k=v)",
				Description: "Repeated KEY=VALUE pairs. Each becomes CLAUDE_DIRECTOR_LABEL_<UPPER_KEY> on the session env and persists in labels.",
			},
			{
				Name:        "allow",
				Type:        "[]string",
				Description: "Repeated permissions.allow entries concatenated with the user / project tiers.",
			},
			{
				Name:        "deny",
				Type:        "[]string",
				Description: "Repeated permissions.deny entries concatenated with the user / project tiers.",
			},
			{
				Name:        "ask",
				Type:        "[]string",
				Description: "Repeated permissions.ask entries concatenated with the user / project tiers.",
			},
			{
				Name:        "relay-mode",
				Type:        "string",
				Description: "on / off. Empty falls back to config defaults.relay_mode (default off).",
			},
			{
				Name:        "extra-env",
				Type:        "[]string (K=V)",
				Description: "Repeated KEY=VALUE pairs injected on the tmux session env. Reserved keys (CLAUDE_DIRECTOR_*) rejected; auth env vars (ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN) allowed.",
			},
			{
				Name:        "claude_args",
				Type:        "[]string (after --)",
				Description: "Pass-through argv to `claude` after the supervisor's own flags. Denied: --settings, --resume, --continue, --print, --output-format.",
			},
		},
		ResultFields: []FieldDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "The id (caller-supplied or freshly-minted UUID4) the row is tracked under.",
			},
		},
		ErrorNames: []string{
			"ErrCwdMissing",
			"ErrCwdNotAPath",
			"ErrCwdNotFound",
			"ErrCwdNotADirectory",
			"ErrRelayModeInvalid",
			"ErrSpawnDeniedFlag",
			"ErrReservedEnvKey",
			"ErrInstanceIdCollision",
			"ErrTmuxNotAvailable",
			"ErrTmuxSessionCreate",
			"ErrTemplateNotFound",
			"ErrTemplateMalformed",
			"ErrTemplateNameUnsafe",
		},
	},
	{
		Name:        "status",
		Description: "Return the current state of a tracked Spawn (pending/waiting/working/ask_user/check_permission/ended/missing).",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the Spawn to inspect.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{
			{
				Name:        "state",
				Type:        "string",
				Description: "Current state column value.",
			},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "get",
		Description: "Return the full DB row for a tracked Spawn (id, parent, state, cwd, session name, args, relay mode, session_id, labels, timestamps).",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the Spawn to fetch.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{
			{Name: "claude_instance_id", Type: "string", Description: "Stable id of the Spawn."},
			{Name: "parent_id", Type: "string", Description: "Parent Spawn id (CLAUDE_DIRECTOR_INSTANCE_ID env at spawn time), empty when launched by a human shell."},
			{Name: "state", Type: "string", Description: "Current state column value."},
			{Name: "cwd", Type: "string", Description: "Canonicalized cwd."},
			{Name: "tmux_session_name", Type: "string", Description: "tmux session under which the Spawn is running."},
			{Name: "claude_args", Type: "[]string", Description: "Verbatim argv passed through to claude after --settings."},
			{Name: "relay_mode", Type: "string", Description: "on / off."},
			{Name: "jsonl_path", Type: "string", Description: "Last known transcript path. Empty until a future Epic persists it; resume composes the path on demand from cwd + claude_session_id."},
			{Name: "claude_session_id", Type: "string", Description: "Claude Code session UUID, extracted from SessionStart hook's transcript_path."},
			{Name: "labels", Type: "map[string]string", Description: "Caller-supplied labels."},
			{Name: "started_at", Type: "timestamp", Description: "Row insert time."},
			{Name: "last_seen_at", Type: "timestamp", Description: "Last hook UPSERT time."},
			{Name: "ended_at", Type: "timestamp?", Description: "Set when state moves to ended (omitted while live)."},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "send-keys",
		Description: "Send text into a tracked Spawn's tmux pane. `\\r` bytes are stripped (prevent premature submission); `\\n` bytes are preserved (composed-but-unsubmitted newlines in Claude's input box); a single Enter is always appended to submit the composed buffer.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the live Spawn to drive.",
				Required:    true,
			},
			{
				Name:        "text",
				Type:        "string",
				Description: "Text to type into the Spawn's input. `\\r` stripped pre-send; `\\n` preserved as newline-in-input.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrSpawnNotInteractive",
			"ErrSendKeysWhileRelayed",
			"ErrTmuxNotAvailable",
			"ErrTmuxSendKeys",
		},
	},
	{
		Name:        "read-pane",
		Description: "Capture the last N lines of a tracked Spawn's tmux pane. Default 25 lines, no upper cap. Default ANSI handling strips escape codes but preserves unicode TUI glyphs (❯, ⎿, 🐝). `ansi=true` returns raw bytes.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the Spawn to read.",
				Required:    true,
			},
			{
				Name:        "n_lines",
				Type:        "int",
				Description: "Number of trailing pane lines to return. Defaults to 25 when 0/omitted. No upper cap.",
			},
			{
				Name:        "ansi",
				Type:        "bool",
				Description: "When true, return raw bytes from tmux (escape codes preserved). When false (default), strip ANSI sequences while preserving unicode glyphs.",
			},
		},
		ResultFields: []FieldDef{
			{
				Name:        "pane",
				Type:        "string",
				Description: "Captured pane text. ANSI handling depends on the `ansi` parameter.",
			},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrTmuxNotAvailable",
			"ErrTmuxCaptureFailed",
		},
	},
	{
		Name:        "kill",
		Description: "Terminate the Spawn's tmux session. Idempotent on terminal states (ended/missing). Does NOT promise state cleanup — the row stays in its prior state until find-missing reconciles it.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the Spawn to kill.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "decide",
		Description: "Orchestrator's allow/deny verdict on an open PermissionRequest. Race-free first-call-wins via a single-statement UPDATE guarded by `decision IS NULL`. Only callable on Spawns with relay_mode=on.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the Spawn sitting on the PermissionRequest.",
				Required:    true,
			},
			{
				Name:        "decision",
				Type:        "string",
				Description: "Either `allow` or `deny`.",
				Required:    true,
			},
			{
				Name:        "reason",
				Type:        "string",
				Description: "Free-text message surfaced to Claude. On `deny` with empty reason the envelope defaults to \"Denied by orchestrator\".",
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrRelayModeOff",
			"ErrNoOpenPermissionRequest",
			"ErrAlreadyDecided",
			"ErrInvalidDecision",
		},
	},
	{
		Name:        "resume",
		Description: "Bring a terminated (ended/missing) Spawn back to life via `claude --resume`. Same claude_instance_id, fresh tmux session, same JSONL transcript. parent_id is re-derived from the caller's CLAUDE_DIRECTOR_INSTANCE_ID env var on every resume.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the terminated Spawn to resurrect.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{
			{Name: "claude_instance_id", Type: "string", Description: "The same id passed in (resume preserves the instance id across resurrection)."},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrSpawnNotResumable",
			"ErrNoSessionId",
			"ErrJsonlMissing",
			"ErrTmuxNotAvailable",
			"ErrTmuxSessionCreate",
		},
	},
	{
		Name:        "find-missing",
		Description: "Reconcile DB state against live processes. Scans live-state rows (including pending), diffs against the OS probe (Linux /proc / macOS sysctl), transitions unprobeable rows to `missing`. Degraded-mode guard: 0 readable processes + ≥1 live rows → log warning + refuse to write.",
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows transitioned to missing on this sweep."},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of rows transitioned to missing."},
		},
		ErrorNames: []string{
			"ErrProbeUnsupported",
		},
	},
	{
		Name:        "expire",
		Description: "Remove terminal-state rows (ended/missing) whose ended_at is older than the retention window. Default window is config defaults.expire_retention_days; --older-than overrides. Does NOT touch tmux or JSONL transcripts.",
		Params: []ParamDef{
			{
				Name:        "older_than",
				Type:        "duration",
				Description: "Duration override (e.g. `7d`, `2h`, `0d`). When omitted, defaults.expire_retention_days from config applies.",
			},
		},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows removed."},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of rows removed."},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "delete",
		Description: "Admin batch removal by claude_instance_id. Bypasses all guards. Does NOT touch tmux sessions or JSONL transcripts. Per-row result map records ok/error per id; the batch never aborts on a partial failure.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "[]string",
				Description: "Id(s) to delete. Repeatable on CLI; JSON array via MCP.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{
			{Name: "results", Type: "map[string]string", Description: "Per-id result: \"ok\" on success, an err_name string on failure."},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "make-template",
		Description: "Save a reusable spawn preset. The TOML file lands under ~/.claude-director/templates/<name>.toml; spawn --template <name> applies it. Reserved per-invocation params (template, claude_instance_id, tmux_session_name) are NOT accepted.",
		Params: []ParamDef{
			{
				Name:        "name",
				Type:        "string",
				Description: "Template name. Must be filename-safe (no path separators, no leading dot, no `..`).",
				Required:    true,
			},
			{
				Name:        "cwd",
				Type:        "string",
				Description: "Bake a default cwd into the template. Per-call --cwd overrides.",
			},
			{
				Name:        "relay_mode",
				Type:        "string",
				Description: "Bake a default relay_mode (on/off). Per-call --relay-mode overrides.",
			},
			{
				Name:        "claude_args",
				Type:        "[]string",
				Description: "Bake default Claude argv. Per-call --claude-args REPLACES the template's array wholesale (not concat).",
			},
			{
				Name:        "extra_env",
				Type:        "map[string]string",
				Description: "Bake env-var entries. Per-call --extra-env merges by key; per-call wins on collision.",
			},
			{
				Name:        "label",
				Type:        "[]string",
				Description: "Bake label k=v entries. Per-call --label merges by key; per-call wins on collision.",
			},
			{
				Name:        "allow",
				Type:        "[]string",
				Description: "Bake permissions.allow entries. Per-call --allow CONCATENATES (does not replace).",
			},
			{
				Name:        "deny",
				Type:        "[]string",
				Description: "Bake permissions.deny entries. Per-call --deny CONCATENATES.",
			},
			{
				Name:        "ask",
				Type:        "[]string",
				Description: "Bake permissions.ask entries. Per-call --ask CONCATENATES.",
			},
		},
		ResultFields: []FieldDef{
			{Name: "path", Type: "string", Description: "Absolute path of the written template file."},
		},
		ErrorNames: []string{
			"ErrTemplateNameUnsafe",
			"ErrTemplateExists",
			"ErrTemplateMalformed",
		},
	},
	{
		Name:        "list",
		Description: "Enumerate Spawn rows. All filters AND together. Returned order is unspecified — callers sort with jq etc.",
		Params: []ParamDef{
			{
				Name:        "state",
				Type:        "[]string",
				Description: "Filter by state. Multiple values OR together. Comma-separated on CLI; JSON array via MCP.",
			},
			{
				Name:        "label",
				Type:        "[]string",
				Description: "Filter by label k=v. Repeatable on CLI; each entry must contain a literal `=`. Multiple entries AND together.",
			},
			{
				Name:        "parent",
				Type:        "string",
				Description: "Filter by parent_id exact match.",
			},
			{
				Name:        "cwd",
				Type:        "string",
				Description: "Filter by canonicalized cwd exact match.",
			},
			{
				Name:        "limit",
				Type:        "int",
				Description: "Cap result count. 0 / omitted means no cap.",
			},
		},
		ResultFields: []FieldDef{
			{Name: "spawns", Type: "[]Spawn", Description: "Matching rows. Empty array when none match (never null)."},
		},
		ErrorNames: []string{
			"ErrListInvalidLabel",
		},
	},
	{
		Name:        "pause",
		Description: "Politely shut down a waiting Spawn by sending `/exit` and waiting up to pause.timeout_seconds for the row to reach `ended`. One-shot — no caller-side polling. Terminal states (ended/missing) are no-op success.",
		Params: []ParamDef{
			{
				Name:        "claude_instance_id",
				Type:        "string",
				Description: "Id of the Spawn to pause.",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrSpawnNotPausable",
			"ErrPauseTimeout",
			"ErrTmuxNotAvailable",
			"ErrTmuxSendKeys",
		},
	},
	{
		Name:        "serve",
		Description: "Start the stdio MCP server. Long-lived process that exposes every other verb as an MCP tool over JSON-RPC on stdin/stdout. Typically registered with `claude mcp add claude-director <binary-path> serve --stdio`.",
		Params: []ParamDef{
			{
				Name:        "stdio",
				Type:        "bool",
				Description: "Enter the stdio MCP loop (required for v1; other transports may land in future Epics).",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames:   []string{},
	},
	{
		Name:        "version",
		Description: "Print the binary's build-time version stamp as JSON ({version, commit}). Used by install.sh to verify a local binary matches the current source tree before installing it.",
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{
				Name:        "version",
				Type:        "string",
				Description: "Human-readable version stamp from `git describe --tags --always --dirty` at build time. \"dev\" for unstamped builds.",
			},
			{
				Name:        "commit",
				Type:        "string",
				Description: "Full git SHA the binary was built from. \"unknown\" for unstamped builds.",
			},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "hook",
		Description: "Internal: invoked by Claude Code on lifecycle events via the per-Spawn --settings hooks. Reads payload JSON from stdin, writes a row UPSERT, exits 0 (state-tracking fail-open).",
		Params: []ParamDef{
			{
				Name:        "stdin",
				Type:        "json",
				Description: "Claude Code hook payload (hook_event_name, transcript_path, tool_name, reason, ...).",
				Required:    true,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames:   []string{},
	},
}

// Lookup returns the VerbDef registered under name. The second return is
// false when no verb with that name exists.
func Lookup(name string) (VerbDef, bool) {
	for _, v := range Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return VerbDef{}, false
}
