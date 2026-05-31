// Package manifest is the single source of truth for the agent-director
// CLI/MCP verb surface.
//
// Each VerbDef entry records a verb's name, description, parameters, result
// fields, and the set of error names it may emit. The CLI dispatch table, the
// MCP tool schema (Epic 11), and the generated reference docs
// (docs/cli-reference.md, docs/mcp-reference.md — Task 6 of Epic 1) all
// derive from Verbs. Adding a verb in any other way drifts from the manifest
// and is caught by the CI doc-drift gate.
//
// This package is deliberately minimal: types, a Verbs slice, and lookup /
// filter helpers. It does not import internal/store, internal/config, or
// cmd/ — dependencies flow downward toward internal/store, never sideways
// or up.
package manifest

//go:generate go run github.com/gabemahoney/agent-director/tools/gen-docs

// VerbDef describes one CLI/MCP verb exposed by agent-director.
type VerbDef struct {
	Name        string
	Description string

	// Callable is true when the verb is exposed as a synchronous method on
	// pkg/api.Client. help (informational/CLI-side), serve (long-running MCP
	// server), and hook (SRD §3.2 fail-open) are Callable: false.
	// 15 verbs are Callable: true.
	Callable bool

	// HandleFree is true when the verb can be invoked without a Client handle
	// (no *store.Store / *tmux.Client / config needed). Only version qualifies
	// today — it returns compiled-in build metadata.
	HandleFree bool

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

	// Nullable, AllowEmpty, AllowedValues are field-level markers for use by
	// envelope-diff harnesses (Epic 3), smoke tests (Epic 4), and cross-language
	// bindings (Epic 5). Every ParamDef must declare Nullable and AllowEmpty
	// explicitly; AllowedValues is nil when the param has no enum constraint.
	Nullable      bool
	AllowEmpty    bool
	AllowedValues []string // nil if not an enum
}

// FieldDef describes one field of a verb's result object.
type FieldDef struct {
	Name        string
	Type        string
	Description string

	// Nullable, AllowEmpty, AllowedValues follow the same semantics as in
	// ParamDef. Every FieldDef must declare Nullable and AllowEmpty explicitly.
	Nullable      bool
	AllowEmpty    bool
	AllowedValues []string // nil if not an enum
}

// stateEnum is the exhaustive set of valid spawn state strings (SRD §6).
// Inlined into AllowedValues where state fields appear.
var stateEnum = []string{
	"pending", "waiting", "working", "ask_user", "check_permission",
	"ended", "missing",
}

// Verbs is the canonical, ordered list of verbs implemented by this binary.
// Epic 2+ workers append entries here as they implement new verbs.
var Verbs = []VerbDef{
	{
		Name:        "help",
		Description: "Print the manifest-derived list of verbs as JSON; intended for SessionStart / SessionEnd reason=compact hooks.",
		Callable:    false,
		HandleFree:  false,
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{
				Name:          "verbs",
				Type:          "[]VerbSummary",
				Description:   "Array of {name, description} for every verb in the manifest.",
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
		},
		// Empty (not nil) so JSON marshalling renders [] consistently and
		// SRD §12.4 "help has no error conditions" is reflected in shape.
		ErrorNames: []string{},
	},
	{
		Name:        "spawn",
		Description: "Launch a tracked Claude Code instance inside a new tmux session. Fire-and-forget: returns the claude_instance_id; state moves from pending to waiting on the first SessionStart hook.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "cwd",
				Type:          "string",
				Description:   "Absolute (or ~/-prefixed) path the Spawn's Claude starts in. Required.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "template",
				Type:          "string",
				Description:   "Optional named template under ~/.agent-director/templates/. Per-call params layer on top per SRD §7.1 (scalars replace; maps merge; permissions arrays concat; claude_args replaces wholesale).",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Optional explicit id (UUID4 minted when absent). Collision against a live row returns ErrInstanceIdCollision.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "label",
				Type:          "[]string (k=v)",
				Description:   "Repeated KEY=VALUE pairs. Each becomes AGENT_DIRECTOR_LABEL_<UPPER_KEY> on the session env and persists in labels.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "allow",
				Type:          "[]string",
				Description:   "Repeated permissions.allow entries concatenated with the user / project tiers.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "deny",
				Type:          "[]string",
				Description:   "Repeated permissions.deny entries concatenated with the user / project tiers.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "ask",
				Type:          "[]string",
				Description:   "Repeated permissions.ask entries concatenated with the user / project tiers.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "relay-mode",
				Type:          "string",
				Description:   "on / off. Empty falls back to config defaults.relay_mode (default off).",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: []string{"on", "off", ""},
			},
			{
				Name:          "extra-env",
				Type:          "[]string (K=V)",
				Description:   "Repeated KEY=VALUE pairs injected on the tmux session env. Reserved keys (AGENT_DIRECTOR_*) rejected; auth env vars (ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN) allowed.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "claude_args",
				Type:          "[]string (after --)",
				Description:   "Pass-through argv to `claude` after the supervisor's own flags. Denied: --settings, --resume, --continue, --print, --output-format.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "no-pre-trust",
				Type:          "bool",
				Description:   "Skip pre-writing projects.<cwd>.hasTrustDialogAccepted=true into the spawn's .claude.json (resolves to <CLAUDE_CONFIG_DIR>/.claude.json if CLAUDE_CONFIG_DIR is set in extra-env, otherwise ~/.claude.json). Default off (pre-trust IS performed so Claude Code skips its workspace-trust dialog and the Spawn becomes interactive immediately).",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "tmux-session-name",
				Type:          "string",
				Description:   "Optional explicit tmux session name. Empty/omitted falls back to <basename(cwd)>-<id[:8]>. Validated app-side: rejects empty (when supplied), '#' ':' '.', ASCII control chars, non-UTF-8, and >64 bytes. NO DB uniqueness check; live-collision surfaces as the wrapped tmux new-session error. Name reuse across ended spawns is supported.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "The id (caller-supplied or freshly-minted UUID4) the row is tracked under.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
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
			"ErrTmuxSessionNameEmpty",
			"ErrTmuxSessionNameInvalid",
			"ErrTmuxSessionNameTooLong",
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
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the Spawn to inspect.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{
				Name:          "state",
				Type:          "string",
				Description:   "Current state column value.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: stateEnum,
			},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "get",
		Description: "Return the full DB row for a tracked Spawn (id, parent, state, cwd, session name, args, relay mode, session_id, labels, timestamps).",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the Spawn to fetch.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "claude_instance_id", Type: "string", Description: "Stable id of the Spawn.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "parent_id", Type: "string", Description: "Parent Spawn id (AGENT_DIRECTOR_INSTANCE_ID env at spawn time), empty when launched by a human shell.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "state", Type: "string", Description: "Current state column value.", Nullable: false, AllowEmpty: false, AllowedValues: stateEnum},
			{Name: "cwd", Type: "string", Description: "Canonicalized cwd.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "tmux_session_name", Type: "string", Description: "tmux session under which the Spawn is running.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "claude_args", Type: "[]string", Description: "Verbatim argv passed through to claude after --settings.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "relay_mode", Type: "string", Description: "on / off.", Nullable: false, AllowEmpty: false, AllowedValues: []string{"on", "off"}},
			{Name: "jsonl_path", Type: "string", Description: "Last known transcript path. Empty until a future Epic persists it; resume composes the path on demand from cwd + claude_session_id.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "claude_session_id", Type: "string", Description: "Claude Code session UUID, extracted from SessionStart hook's transcript_path.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "labels", Type: "map[string]string", Description: "Caller-supplied labels.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "started_at", Type: "timestamp", Description: "Row insert time.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "last_seen_at", Type: "timestamp", Description: "Last hook UPSERT time.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "ended_at", Type: "timestamp?", Description: "Set when state moves to ended (omitted while live).", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "permission_requests", Type: "[]object", Description: "All open (undecided) permission requests awaiting orchestrator decision. Always a non-null array ([] when empty). Populated only when state == check_permission; empty array for all other states. Each element: request_id (int) — autoincrement row id; request_token (string) — UUIDv4 token minted by runRelay, pass to decide verb to target this row; tool_name (string) — Claude Code tool that triggered the request; tool_input (string) — raw JSON string of the tool's input, NOT a nested object (consumers parse it themselves); requested_at (RFC3339 timestamp) — created_at of the row.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "send-keys",
		Description: "Send text into a tracked Spawn's tmux pane. `\\r` bytes are stripped (prevent premature submission); `\\n` bytes are preserved (composed-but-unsubmitted newlines in Claude's input box); a single Enter is always appended to submit the composed buffer.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the live Spawn to drive.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "text",
				Type:          "string",
				Description:   "Text to type into the Spawn's input. `\\r` stripped pre-send; `\\n` preserved as newline-in-input.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "allow_pending",
				Type:          "bool",
				Description:   "When true, also permit send-keys on a pending Spawn (pre-SessionStart use case). ended/missing Spawns are still rejected even with this flag set.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
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
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the Spawn to read.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "n_lines",
				Type:          "int",
				Description:   "Number of trailing pane lines to return. Defaults to 25 when 0/omitted. No upper cap.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "ansi",
				Type:          "bool",
				Description:   "When true, return raw bytes from tmux (escape codes preserved). When false (default), strip ANSI sequences while preserving unicode glyphs.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "allow_pending",
				Type:          "bool",
				Description:   "Accepted for surface symmetry with send-keys. ReadPane has no state guard (pending/ended/missing are all readable), so this flag has no behavioral effect.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{
				Name:          "pane",
				Type:          "string",
				Description:   "Captured pane text. ANSI handling depends on the `ansi` parameter.",
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
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
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the Spawn to kill.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
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
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the Spawn sitting on the PermissionRequest.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "request_token",
				Type:          "string",
				Description:   "UUIDv4 token identifying the specific permission request to decide. Minted by runRelay per-request; required to enforce per-row isolation when multiple concurrent requests exist for the same Spawn.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "decision",
				Type:          "string",
				Description:   "Either `allow` or `deny`.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: []string{"allow", "deny"},
			},
			{
				Name:          "reason",
				Type:          "string",
				Description:   "Free-text message surfaced to Claude. On `deny` with empty reason the envelope defaults to \"Denied by orchestrator\".",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrRelayModeOff",
			"ErrNoOpenPermissionRequest",
			"ErrAlreadyDecided",
			"ErrAmbiguousRequest",
			"ErrInvalidDecision",
		},
	},
	{
		Name:        "resume",
		Description: "Bring a terminated (ended/missing) Spawn back to life via `claude --resume`. Same claude_instance_id, fresh tmux session, same JSONL transcript. parent_id is re-derived from the caller's AGENT_DIRECTOR_INSTANCE_ID env var on every resume.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the terminated Spawn to resurrect.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "claude_instance_id", Type: "string", Description: "The same id passed in (resume preserves the instance id across resurrection).", Nullable: false, AllowEmpty: false, AllowedValues: nil},
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
		Callable:    true,
		HandleFree:  false,
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows transitioned to missing on this sweep. Zero is a legitimate happy-path result when nothing needed reaping (or when the degraded-mode guard refused to write).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of rows transitioned to missing.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrProbeUnsupported",
		},
	},
	{
		Name:        "expire",
		Description: "Remove terminal-state rows (ended/missing) whose ended_at is older than the retention window. Default window is config defaults.expire_retention_days; --older-than overrides. Does NOT touch tmux or JSONL transcripts.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "older_than",
				Type:          "duration",
				Description:   "Duration override (e.g. `7d`, `2h`, `0d`). When omitted, defaults.expire_retention_days from config applies.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows removed. Zero is a legitimate happy-path result when no terminal rows matched the retention window.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of rows removed.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "delete",
		Description: "Admin batch removal by claude_instance_id. Bypasses all guards. Does NOT touch tmux sessions or JSONL transcripts. Per-row result map records ok/error per id; the batch never aborts on a partial failure.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "[]string",
				Description:   "Id(s) to delete. Repeatable on CLI; JSON array via MCP.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "results", Type: "map[string]string", Description: "Per-id result: \"ok\" on success, an err_name string on failure.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "make-template",
		Description: "Save a reusable spawn preset. The TOML file lands under ~/.agent-director/templates/<name>.toml; spawn --template <name> applies it. Reserved per-invocation params (template, claude_instance_id, tmux_session_name) are NOT accepted.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "name",
				Type:          "string",
				Description:   "Template name. Must be filename-safe (no path separators, no leading dot, no `..`).",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "cwd",
				Type:          "string",
				Description:   "Bake a default cwd into the template. Per-call --cwd overrides.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "relay_mode",
				Type:          "string",
				Description:   "Bake a default relay_mode (on/off). Per-call --relay-mode overrides.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: []string{"on", "off", ""},
			},
			{
				Name:          "claude_args",
				Type:          "[]string",
				Description:   "Bake default Claude argv. Per-call --claude-args REPLACES the template's array wholesale (not concat).",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "extra_env",
				Type:          "map[string]string",
				Description:   "Bake env-var entries. Per-call --extra-env merges by key; per-call wins on collision.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "label",
				Type:          "[]string",
				Description:   "Bake label k=v entries. Per-call --label merges by key; per-call wins on collision.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "allow",
				Type:          "[]string",
				Description:   "Bake permissions.allow entries. Per-call --allow CONCATENATES (does not replace).",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "deny",
				Type:          "[]string",
				Description:   "Bake permissions.deny entries. Per-call --deny CONCATENATES.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "ask",
				Type:          "[]string",
				Description:   "Bake permissions.ask entries. Per-call --ask CONCATENATES.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "overwrite",
				Type:          "bool",
				Description:   "Replace any existing template at this name atomically. Default false preserves O_EXCL create-only semantics.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "path", Type: "string", Description: "Absolute path of the written template file.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
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
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "state",
				Type:          "[]string",
				Description:   "Filter by state. Multiple values OR together. Comma-separated on CLI; JSON array via MCP.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "label",
				Type:          "[]string",
				Description:   "Filter by label k=v. Repeatable on CLI; each entry must contain a literal `=`. Multiple entries AND together.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "parent",
				Type:          "string",
				Description:   "Filter by parent_id exact match.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "cwd",
				Type:          "string",
				Description:   "Filter by canonicalized cwd exact match.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "tmux-session-name",
				Type:          "string",
				Description:   "Filter by tmux session name exact match. Returns any live or ended row whose tmux_session_name equals the value byte-for-byte; correlation across re-uses, not uniqueness enforcement.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
			{
				Name:          "limit",
				Type:          "int",
				Description:   "Cap result count. 0 / omitted means no cap.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "spawns", Type: "[]Spawn", Description: "Matching rows. Empty array when none match (never null).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrListInvalidLabel",
		},
	},
	{
		Name:        "pause",
		Description: "Politely shut down a waiting Spawn by sending `/exit` and waiting up to pause.timeout_seconds for the row to reach `ended`. One-shot — no caller-side polling. Terminal states (ended/missing) are no-op success.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the Spawn to pause.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
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
		Description: "Start the stdio MCP server. Long-lived process that exposes every other verb as an MCP tool over JSON-RPC on stdin/stdout. Typically registered with `claude mcp add agent-director <binary-path> serve --stdio`.",
		Callable:    false,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "stdio",
				Type:          "bool",
				Description:   "Enter the stdio MCP loop (required for v1; other transports may land in future Epics).",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames:   []string{},
	},
	{
		Name:        "version",
		Description: "Print the binary's build-time version stamp as JSON ({version, commit}). Used by install.sh to verify a local binary matches the current source tree before installing it.",
		Callable:    true,
		HandleFree:  true,
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{
				Name:          "version",
				Type:          "string",
				Description:   "Human-readable version stamp from `git describe --tags --always --dirty` at build time. \"dev\" for unstamped builds.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "commit",
				Type:          "string",
				Description:   "Full git SHA the binary was built from. \"unknown\" for unstamped builds.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "hook",
		Description: "Internal: invoked by Claude Code on lifecycle events via the per-Spawn --settings hooks. Reads payload JSON from stdin, writes a row UPSERT, exits 0 (state-tracking fail-open).",
		Callable:    false,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "stdin",
				Type:          "json",
				Description:   "Claude Code hook payload (hook_event_name, transcript_path, tool_name, reason, ...).",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames:   []string{},
	},
}

// CallableVerbs returns the subset of Verbs that the pkg/api.Client
// exposes as synchronous methods. help, serve, and hook are excluded:
// help is informational and rendered cmd-side; serve is a long-running
// server (not a one-shot verb); hook is SRD §3.2 fail-open.
// Library callers wanting the verb list iterate this slice directly.
func CallableVerbs() []VerbDef {
	out := make([]VerbDef, 0, len(Verbs))
	for _, v := range Verbs {
		if v.Callable {
			out = append(out, v)
		}
	}
	return out
}

// HandleFreeVerbs returns the subset of Verbs that can be invoked without
// a *pkg/api.Client handle. SR-2.1: this is the single source of truth
// for handle-free dispatch — used by downstream language bindings (e.g. the
// TS subprocess client's version() call) to bypass the open-client
// requirement for verbs that don't need session state.
func HandleFreeVerbs() []VerbDef {
	out := make([]VerbDef, 0, len(Verbs))
	for _, v := range Verbs {
		if v.HandleFree {
			out = append(out, v)
		}
	}
	return out
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
