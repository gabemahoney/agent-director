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
				Description: "Optional named template under ~/.claude-director/templates/. Epic 7 wires the real template merge.",
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
			{Name: "jsonl_path", Type: "string", Description: "Last known transcript path (populated by Epic 9)."},
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
