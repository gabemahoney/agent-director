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
	// 16 verbs are Callable: true.
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

// liveRowSequence is SR-18.6's bounded, paced live-row sequence. The kill,
// find-missing and spawn Descriptions end with it, so its wording is the same
// in all three and cannot drift. It names kill as a documented procedure,
// which SR-1.4 allows in these three Descriptions; it states no timeout other
// than the pending grace default and the pause between find-missing runs
// (SR-13.2 is the one source of the ceilings).
const liveRowSequence = "To end a live row (pending included) and relaunch its id, a caller follows this bounded, paced sequence: " +
	"(1) kill, and check the result; on any error follow its class and never delete the row. " +
	"(2) If the row is pending, wait until its launch start (shown by status) plus the pending grace period (60 s unless the operator configured another value) has passed; a pending row inside its grace period means wait and check again later, never escalate. " +
	"(3) Run find-missing, then confirm with status or get that the row is ended or missing; if not, wait about 5 s and repeat, up to three find-missing runs in all. " +
	"(4) If still live, kill once more, wait about 5 s, run find-missing once more and check. " +
	"(5) If still live, stop and escalate to a human. " +
	"(6) Once the row is ended or missing, resume it if it has a session id and the caller wants the conversation back; otherwise spawn with --reuse-finished. A caller whose ids agent-director mints spawns fresh instead of reusing. A pending row, a resumed one included, enters this sequence, so a stuck resume handled this way can still get its conversation back; a reuse makes that conversation unreachable for good."

// killDescription is kill's Description (SR-6.1, SR-1.7, SR-18.1, SR-18.2,
// SR-18.7, SR-18.9; decision-0930b Q4), ending with the live-row sequence.
const killDescription = "End the agent of a live row's current launch (pending included). " +
	"kill finds the tmux session that carries the row's current launch label on the row's recorded tmux socket, ends the agent's pane and that session by their tmux ids, and succeeds only once the agent process is gone; otherwise it returns ErrTmuxKillFailed. kill_sent says whether a kill was sent. " +
	"If no session of the launch is found, kill checks the agent process: gone, or none recorded, is success with kill_sent false and nothing sent; if it still runs and its pane is still shown in another session, kill ends that pane and checks the process; if no pane of it is found, ErrTmuxKillFailed with no kill sent. " +
	"On a finished row (ended or missing) kill is a no-op success with kill_sent false and no tmux call; that is not verification that the agent exited. " +
	"kill never changes the row's state: find-missing marks the row once its agent process is gone. kill never signals a process itself; success means the agent process exited, not that every process it started did. " +
	"On a pending row kill aborts only the current launch, and the row stays pending until find-missing marks it; a kill made before the launch created its session returns kill_sent false and does not stop the launch. kill never ends a session that an earlier launch left behind. " +
	"Success is judged per call: kill succeeds when the agent process and every other process it found in the panes of the agent's session are gone. If ErrTmuxKillFailed named another process that outlived the kill (its pid is in the error), that process is not the agent and later calls do not track it: a retried kill checks only the agent process, so once the agent is gone it succeeds with kill_sent false whether or not that process still runs. A retry's success means only that the agent is gone; the named process needs a human (see \"Operator actions\" in the agent-director README). If the row finishes while kill waits and the agent outlives the wait, kill returns ErrTmuxKillFailed, and a retried kill is a finished-row no-op. " +
	"Errors and what the caller does (for kill, GONE is success): ErrTmuxKillFailed (UNAVAILABLE): a kill was sent and the agent process, or another process of the session's panes, still ran after the kill exit wait, or the process cannot be checked and its labelled session is still there, or no session or pane of this launch was found while the agent process runs; retry later. ErrTmuxUnresponsive (UNAVAILABLE): tmux did not answer usably, before or after a kill was sent; retry later with backoff. ErrTmuxSessionConflict (CONFLICT, permanent until a human looks): the session found is not this launch's session, or tmux holds conflicting labels; no kill was sent, and a human must look (see \"Operator actions\" in the agent-director README). ErrTmuxNotAvailable (ENVIRONMENT): tmux could not be run, its socket is not accessible to this user, or this is not the tmux server the agent was launched on; an environment problem for an operator to fix. ErrSpawnNotFound: no row has this id. None of these errors means that the agent is dead. Never delete a row after a kill that did not succeed. " +
	"kill must run as the same user and in the same tmux environment as the agents. Two consequences: kill's success on a finished row is not verification that the agent exited; and on the wrong tmux server, a row wrongly marked missing, kill's no-op success and a reuse together start a second agent for the same id. " +
	"A live row whose recorded tmux session name cannot be used (it is empty, contains a control character, or contains a character tmux stores differently) gets ErrInternal with no tmux call; removing the row is a human's decision (see \"Operator actions\" in the agent-director README). " +
	liveRowSequence

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
		Description: "Launch a tracked Claude Code instance inside a new tmux session. Returns the claude_instance_id and pre_trust (ok, skipped or failed) without waiting for the agent; the row is pending from its insert until the agent reports in (Claude Code's SessionStart), then waiting. The session is labelled for this launch when it is created, and the session-creating call is bounded by the create timeout. If it times out, spawn returns ErrTmuxUnresponsive (UNAVAILABLE, transient): the session may have been created and the new row stays pending; do not retry until get shows the row ended or missing, since a retried spawn without an explicit id would start a second agent. With an explicit claude_instance_id that has no row, spawn first looks for a tmux session of this agent-director store still labelled with that id; one left over from an earlier life refuses the spawn with ErrTmuxSessionConflict (CONFLICT: permanent until a human looks; see the README's \"Operator actions\"), and nothing is written. ErrTmuxNotAvailable is ENVIRONMENT and ErrTmuxSessionCreate a LAUNCH FAILURE. When an explicit claude_instance_id is supplied and the collision pre-check cannot read the store, spawn returns ErrInternal and creates nothing; this is a store fault and says nothing about whether the id is in use. " + liveRowSequence,
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
				Description:   "Optional explicit id; an empty or absent id mints a fresh UUID4. An explicit id containing an ASCII control character (0x00-0x1f or 0x7f) is rejected with ErrInvalidFlags. Collision against a live row returns ErrInstanceIdCollision.",
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
				Description:   "Skip pre-writing projects.<cwd>.hasTrustDialogAccepted=true into the spawn's .claude.json (resolves to <CLAUDE_CONFIG_DIR>/.claude.json if CLAUDE_CONFIG_DIR is set in extra-env, otherwise ~/.claude.json). Default off (pre-trust IS performed so Claude Code skips its workspace-trust dialog and the Spawn becomes interactive immediately). The choice is recorded on the row for its life, and every resume of that life follows it: no pre-trust is attempted over an opt-out.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "tmux-session-name",
				Type:          "string",
				Description:   "Optional explicit tmux session name. Empty/omitted falls back to <basename(cwd)>-<id[:8]>. Validated app-side: rejects empty (when supplied), '#' ':' '.' '$' '\\' (backslash), ASCII control chars, non-UTF-8, and >64 bytes. NO DB uniqueness check; live-collision surfaces as the wrapped tmux new-session error. Name reuse across ended spawns is supported.",
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
			{
				Name:          "pre_trust",
				Type:          "string",
				Description:   "What the launch's folder-trust pre-trust did. ok = the folder-trust entry was written; skipped = pre-trust was off for this launch because the caller passed no-pre-trust, so nothing was attempted; failed = pre-trust was attempted and the entry was not written (the .claude.json file is missing, or could not be read, parsed or written); the launch still proceeds and the agent may stop at Claude Code's folder-trust prompt.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: []string{"ok", "skipped", "failed"},
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
			"ErrInvalidFlags",
			"ErrInstanceIdCollision",
			"ErrTmuxSessionNameEmpty",
			"ErrTmuxSessionNameInvalid",
			"ErrTmuxSessionNameTooLong",
			"ErrTmuxNotAvailable",
			"ErrTmuxSessionCreate",
			"ErrTmuxUnresponsive",
			"ErrTmuxSessionConflict",
			"ErrTemplateNotFound",
			"ErrTemplateMalformed",
			"ErrTemplateNameUnsafe",
		},
	},
	{
		Name:        "status",
		Description: "Return the current state of a tracked Spawn (pending/waiting/working/ask_user/check_permission/ended/missing). pending means a launch (spawn, reuse or resume) is in progress and the agent has not reported in yet (Claude Code's SessionStart); it may be loading or waiting at a startup prompt. A resumed pending row keeps its session id and history; a caller tells it from a fresh one by its non-empty claude_session_id (shown by get).",
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
				Description:   "Current state, one of the allowed values. pending: a launch (spawn, reuse or resume) is in progress and the agent has not reported in yet (Claude Code's SessionStart); it may be loading or waiting at a startup prompt. A resumed pending row keeps its session id and history (non-empty claude_session_id).",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: stateEnum,
			},
			{
				Name:          "launch_started_at",
				Type:          "timestamp?",
				Description:   "Start of the launch in progress: RFC3339 UTC with millisecond precision. Present only while the row is pending; omitted otherwise.",
				Nullable:      true,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "get",
		Description: "Return the full DB row for a tracked Spawn (id, parent, state, cwd, session name, tmux socket, args, relay mode, session_id, labels, timestamps).",
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
			{Name: "state", Type: "string", Description: "Current state, one of the allowed values. pending: a launch (spawn, reuse or resume) is in progress and the agent has not reported in yet (Claude Code's SessionStart); it may be loading or waiting at a startup prompt. A resumed pending row keeps its session id and history (non-empty claude_session_id).", Nullable: false, AllowEmpty: false, AllowedValues: stateEnum},
			{Name: "cwd", Type: "string", Description: "Canonicalized cwd.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "tmux_session_name", Type: "string", Description: "tmux session under which the Spawn is running.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "tmux_socket", Type: "string?", Description: "The tmux socket the row's launch uses; omitted for a row from before this release.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "claude_args", Type: "[]string", Description: "Verbatim argv passed through to claude after --settings.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "relay_mode", Type: "string", Description: "on / off.", Nullable: false, AllowEmpty: false, AllowedValues: []string{"on", "off"}},
			{Name: "jsonl_path", Type: "string", Description: "Last known transcript path, persisted by the SessionStart hook; legacy rows may be empty. When empty, resume composes the path on demand from cwd + claude_session_id.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "claude_session_id", Type: "string", Description: "Claude Code session UUID, extracted from SessionStart hook's transcript_path.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "labels", Type: "map[string]string", Description: "Caller-supplied labels.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "started_at", Type: "timestamp", Description: "Row insert time.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "last_seen_at", Type: "timestamp", Description: "Last hook UPSERT time.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "ended_at", Type: "timestamp?", Description: "Set when state moves to ended (omitted while live).", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "launch_started_at", Type: "timestamp?", Description: "Start of the launch in progress: RFC3339 UTC with millisecond precision. Present only while the row is pending; omitted otherwise.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "liveness_unverified_since", Type: "timestamp?", Description: "RFC3339 timestamp of the first sweep that could not verify this live row's liveness (an unknown verdict, e.g. a permission wall). Cleared to NULL once liveness is re-established; null/omitted when never unverified.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "liveness_note", Type: "string?", Description: "Human-readable reason the row's liveness could not be verified on the most recent unverified sweep. Cleared to NULL once liveness is re-established; null/omitted when never unverified.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "permission_requests", Type: "[]object", Description: "All open (undecided) permission requests awaiting orchestrator decision. Always a non-null array ([] when empty). Populated only when state == check_permission; empty array for all other states. Each element: request_id (int) — autoincrement row id; request_token (string) — UUIDv4 token minted by runRelay, pass to decide verb to target this row; tool_name (string) — Claude Code tool that triggered the request; tool_input (string) — raw JSON string of the tool's input, NOT a nested object (consumers parse it themselves); requested_at (RFC3339 timestamp) — created_at of the row.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "transcript_status", Type: "string", Description: "Derived operator-facing summary of the current session's transcript state (b.v2c): 'present' (jsonl_path recorded), 'never_written' (session id but NULL jsonl_path and prior_sessions is empty — nothing was ever written in the current life), 'rotated' (NULL jsonl_path but prior_sessions is non-empty — the current life has history under a different session id), or 'no_session' (no claude_session_id yet). Session history belongs to a life; 'never_written' and 'rotated' are decided on the same entries prior_sessions lists.", Nullable: false, AllowEmpty: false, AllowedValues: []string{"present", "never_written", "rotated", "no_session"}},
			{Name: "prior_sessions", Type: "[]object", Description: "Archived prior sessions of the current life, newest first, excluding the row's current session id — the queryable link back to sessions orphaned by a rotation (b.v2c). Session history belongs to a life. Always a non-null array ([] when empty). Each element: claude_session_id (string) — archived session id; jsonl_path (string) — archived transcript path (may be empty); recorded_at (timestamp) — when the archive was written (the rotation moment).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
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
		Description: killDescription,
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
		ResultFields: []FieldDef{
			{
				Name:          "kill_sent",
				Type:          "bool",
				Description:   "True exactly when a pane kill or a session kill was sent, including one whose call failed; false when no kill was sent, for example for a finished row.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrTmuxNotAvailable",
			"ErrTmuxKillFailed",
			"ErrTmuxUnresponsive",
			"ErrTmuxSessionConflict",
		},
	},
	{
		Name:        "decide",
		Description: "Orchestrator's allow/deny verdict on an open PermissionRequest. Deliver-or-refuse: a single-statement UPDATE atomically writes the verdict only when the row is still open and deliverable (`decision IS NULL AND request_token = ? AND created_at > cutoff`), making the write race-free first-call-wins; an open request whose relay window has already elapsed is refused with ErrRelayFallenBack (answer at the pane) rather than recording a verdict into a void. Only callable on Spawns with relay_mode=on.",
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
				Description:   "Currently discarded on deny; the canonical DecisionReasonOperator is persisted regardless. Reserved for future schema additions.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames: []string{
			"ErrMissingRequestToken",
			"ErrSpawnNotFound",
			"ErrRelayModeOff",
			"ErrRelayFallenBack",
			"ErrNoOpenPermissionRequest",
			"ErrAlreadyDecided",
			"ErrAmbiguousRequest",
			"ErrInvalidDecision",
		},
	},
	{
		Name:        "get-permission",
		Description: "Fetch a single permission_requests row by request_token. Token-only lookup (SR-3.5: UUIDv4 is globally selective); no claude_instance_id required. Nullable columns (decision, decision_reason, decided_at) surface as JSON null while the row is open.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "request_token",
				Type:          "string",
				Description:   "UUIDv4 token identifying the permission_requests row to fetch.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "request_token", Type: "string", Description: "UUIDv4 token the row is keyed under (echoed back).", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "request_id", Type: "int", Description: "Autoincrement primary key of the permission_requests row.", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "tool_name", Type: "string", Description: "Claude Code tool that triggered the permission request (e.g. \"Bash\", \"Write\").", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "tool_input", Type: "string", Description: "Raw JSON string of the tool's input as stored in the DB; NOT a nested JSON object. Passes through byte-identical from the DB column — consumers parse it themselves.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "requested_at", Type: "timestamp", Description: "RFC3339 timestamp when the row was created (maps from the created_at DB column).", Nullable: false, AllowEmpty: false, AllowedValues: nil},
			{Name: "decision", Type: "string?", Description: "\"allow\" or \"deny\" once decided; null while the row is open (decision IS NULL in the DB).", Nullable: true, AllowEmpty: false, AllowedValues: []string{"allow", "deny"}},
			{Name: "decision_reason", Type: "string?", Description: "Canonical decision-reason string for deny rows (operator / timeout / find_missing per SR-1.3); null for open rows AND for allow rows (closed-allow carries no reason).", Nullable: true, AllowEmpty: false, AllowedValues: []string{"operator", "timeout", "find_missing"}},
			{Name: "decided_at", Type: "timestamp?", Description: "RFC3339 timestamp when the verdict was written; null while the row is open.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrPermissionRequestNotFound",
		},
	},
	{
		Name:        "resume",
		Description: "Bring a finished (ended/missing) Spawn back to life via `claude --resume`. Same claude_instance_id, fresh tmux session, same JSONL transcript. Before its launch, resume runs the same best-effort pre-trust as spawn for the row's working directory, unless the spawn that began the row's life turned it off with no-pre-trust; a pre-trust failure never fails the resume. Returns the claude_instance_id and pre_trust (ok, skipped or failed). Before it creates the session, resume moves the row to pending, keeping its session id and history, and writes parent_id, re-derived from the caller's AGENT_DIRECTOR_INSTANCE_ID env var on every resume. The row stays pending until the agent reports in (Claude Code's SessionStart), then becomes waiting. If the launch fails other than by timing out (ErrTmuxNotAvailable is ENVIRONMENT and ErrTmuxSessionCreate a LAUNCH FAILURE), resume restores the row to its prior ended or missing state; if the restore cannot be applied, the error says so. The session-creating call is bounded by the create timeout. If it times out, resume returns ErrTmuxUnresponsive (UNAVAILABLE, transient): the session may have been created and the row stays pending; do not retry until get shows the row ended or missing, since a retried resume of the pending row is refused and changes nothing. A pending row (a launch in progress, including a resumed one) is refused with ErrSpawnNotResumable and nothing is written. A row whose instance id contains a control character is refused with ErrInternal before any tmux call or write, because its session could never be labelled. If the launch cannot be recorded in the store, resume returns ErrInternal and launches nothing.",
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
			{
				Name:          "pre_trust",
				Type:          "string",
				Description:   "What the launch's folder-trust pre-trust did. ok = the folder-trust entry was written; skipped = pre-trust was off for this launch because the spawn that began the row's life turned it off with no-pre-trust, so nothing was attempted; failed = pre-trust was attempted and the entry was not written (the .claude.json file is missing, or could not be read, parsed or written); the launch still proceeds and the agent may stop at Claude Code's folder-trust prompt.",
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: []string{"ok", "skipped", "failed"},
			},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
			"ErrSpawnNotResumable",
			"ErrNoSessionId",
			"ErrJsonlMissing",
			"ErrJsonlNeverWritten",
			"ErrTmuxNotAvailable",
			"ErrTmuxSessionCreate",
			"ErrTmuxUnresponsive",
		},
	},
	{
		Name:        "find-missing",
		Description: "Reconcile DB state against live processes. Scans live-state rows (including pending) and reaches a per-row, evidence-based liveness verdict: rows carrying a full recorded identity (pid + proc_starttime) are checked against the OS (Linux /proc / macOS sysctl) — provably-dead rows transition to `missing`, verified-alive rows are left as-is, and rows whose liveness cannot be established (e.g. a permission wall) are left untouched and flagged unverified. Rows with a partial or absent recorded identity fall back to the environ probe-set diff. Each row is judged in isolation; a row is never marked missing on ambiguous evidence. " + liveRowSequence,
		Callable:    true,
		HandleFree:  false,
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows transitioned to missing on this sweep. Zero is a legitimate happy-path result when nothing needed reaping.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of rows transitioned to missing.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "unverified", Type: "int", Description: "Number of live rows left untouched this sweep because their liveness could not be established (an unknown verdict, e.g. a permission wall).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "unverified_ids", Type: "[]string", Description: "Sorted IDs of rows left untouched as unverified.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
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
			{Name: "spawns", Type: "[]Spawn", Description: "Matching rows. Empty array when none match (never null). Each row carries liveness_unverified_since (timestamp?) and liveness_note (string?), both omitted while NULL (never unverified), and launch_started_at (timestamp?), the start of the launch in progress (RFC3339 UTC with millisecond precision), omitted unless the row is pending. Each row's state takes the same values as status, with the same meaning of pending: a launch (spawn, reuse or resume) in progress whose agent has not reported in yet; a resumed pending row keeps its session id and history.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
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
		Description: "Start the stdio MCP server. Long-lived process that exposes the CLI verbs (except `hook`, `serve`, and `trail-emit`) as MCP tools over JSON-RPC on stdin/stdout. Typically registered with `claude mcp add agent-director <binary-path> serve --stdio`.",
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
		Name:        "trail-emit",
		Description: "Emit an ad.* audit-trail event directly. Sub-verb: relay-attempt. Does not open state.db — works in corrupted-state recovery scenarios (SR-A-2.3).",
		Callable:    false,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "sub_verb",
				Type:          "string",
				Description:   "Sub-verb to invoke. Currently: relay-attempt.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "token",
				Type:          "string",
				Description:   "request_token. Required.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "endpoint",
				Type:          "string",
				Description:   "target_endpoint (URL or socket path). Required.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "outcome",
				Type:          "string|int",
				Description:   "3-digit HTTP status code (100-599, emitted as integer) or named error class: connection_refused, timeout, dns_failure (emitted as string). Required.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: []string{"connection_refused", "timeout", "dns_failure"},
			},
			{
				Name:          "bytes_sent",
				Type:          "int",
				Description:   "Bytes sent. Default 0.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "bytes_received",
				Type:          "int",
				Description:   "Bytes received. Default 0.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "instance_id",
				Type:          "string",
				Description:   "claude_instance_id. Required.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{},
		ErrorNames:   []string{"ErrInvalidFlags", "ErrTrailWrite"},
	},
	{
		Name:        "hook",
		Description: "Internal: invoked by Claude Code on lifecycle events via the per-Spawn --settings hooks. Reads payload JSON from stdin and writes the row only when the hook's parent process is the row's recorded pane process, the agent itself; a hook from any other process changes nothing and is logged as ad.hook.ignored. Exits 0 (state-tracking fail-open).",
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
