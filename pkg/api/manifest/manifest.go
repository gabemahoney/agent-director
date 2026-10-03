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

// liveRowSequence is SR-18.6's bounded, paced live-row sequence in its short
// form (decision-0930b Q6): the same six steps and limits, with no rationale.
// Only kill's Description states it (it ends killDescription); find-missing
// and spawn point to it with liveRowSequencePointer, so the sequence has one
// home and cannot drift. The full sequence with its rationale (steps 2 and 6)
// lives in the README's "Caller contract" and in docs/architecture.md. It
// names kill as a documented procedure, which SR-1.4 allows here; it states
// no timeout other than the pending grace default and the wait between
// find-missing runs (SR-13.2 is the one source of the ceilings).
const liveRowSequence = "Live-row sequence (a pending row included): " +
	"1. kill and check the result; on an error follow its class, never delete the row. " +
	"2. If the row is pending, wait until its launch start (status) plus the pending grace period (60 s unless configured); inside it, wait and check again, never escalate. " +
	"3. Run find-missing, then check status; repeat about 5 s apart until the row is ended or missing, at most three runs. " +
	"4. Still live: kill once more, wait about 5 s, run find-missing once more and check. " +
	"5. Still live: escalate to a human. " +
	"6. Then resume the row if it has a session id and the caller wants the conversation back; otherwise spawn with --reuse-finished (callers whose ids agent-director mints spawn fresh)."

// liveRowSequencePointer is SR-18.6's one-sentence pointer to the live-row
// sequence, verbatim. It ends the find-missing and spawn Descriptions (after
// find-missing's grace sentence), which restate no step: every surface that
// shows one Description shows kill's too, so the pointer always resolves.
// kill's Description carries liveRowSequence itself; the full sequence with
// its rationale lives in the README's "Caller contract" and in
// docs/architecture.md.
const liveRowSequencePointer = "To end a live row (pending included) and relaunch its id, follow the live-row sequence in kill's description."

// missingNotProofShort is SR-18.2's short form (decision-0930e), carried by
// the kill, resume, pause, expire and delete Descriptions in place of the
// full sentence, since every verb Description reaches help and MCP. The full
// sentence ("`missing` is the sweep's judgement on the evidence available to
// it, not proof that the agent has exited") stays in find-missing's
// Description, where missing is produced, and in the status/get state, list
// spawns and find-missing ids result fields, which reach neither.
const missingNotProofShort = "`missing` is not proof the agent exited (see find-missing)."

// unusableNamePointer is the short form of SR-1.7's unusable recorded-name
// ErrInternal trigger (SR-3.2, SR-1.4; Epic 19 build-lead decision 1),
// carried by the read-pane, send-keys and pause Descriptions in place of the
// full sentence, since every verb Description reaches help and MCP; resume's
// and spawn's Descriptions merge the same pointer into their own ErrInternal
// sentences. The full sentence, defining the three kinds and pointing to
// "Operator actions", stays in killDescription.
const unusableNamePointer = "Unusable recorded name: ErrInternal (see kill)."

// expireCleanupPointer is SR-18.7's cleanup guidance in short form (Epic 15
// build-lead decision 1, after decision-0930e's precedent), carried by the
// kill and find-missing Descriptions in place of the full cleanup sentence,
// since every verb Description reaches help and MCP. The full sentence stays
// in expireDescription. Keep it at or under 80 bytes.
const expireCleanupPointer = "Agents never run expire (operator-scheduled finished-row cleanup)."

// expireDescription is expire's Description (SR-12.1, SR-18.2, SR-18.7,
// SR-18.9; Epic 15 build-lead decision 1): only what SR-18 requires there,
// plus the selection. Per-row detail (the deleted meaning, kept reasons,
// sweep_budget_seconds, what fails the run) lives in the result-field texts,
// which reach neither help nor MCP tools/list.
const expireDescription = "Delete finished rows (ended/missing) whose ended_at is older than the retention window (defaults.expire_retention_days; --older-than overrides). " +
	"It reads tmux to decide, deleting a row only if its agent's recorded process is not seen running and tmux shows no session of the agent. " +
	"It never kills a session or touches transcripts. " + missingNotProofShort + " " +
	"Finished rows are removed by an operator-scheduled expire at the default retention, run as the same user and in the same tmux environment as the agents; it keeps and reports rows whose session runs or cannot be checked. " +
	"Agents never run it, least of all with a zero window. " +
	"A run as another user, as root or against another tmux server can wrongly delete rows."

// sameEnvConsequences is SR-18.7's two consequences of the same-user,
// same-tmux-environment requirement, stated wherever kill, find-missing and
// the reuse parameter are described (expire states its own wrong-server
// sentence). One constant keeps the three sites identical.
const sameEnvConsequences = "Two consequences: kill's success on a finished row is not verification that the agent exited; " +
	"and on the wrong tmux server, a row wrongly marked missing, kill's no-op success and a reuse together start a second agent for the same id."

// reuseFinishedDescription is spawn's reuse-finished parameter text
// (SR-18.10, SR-10.7, SR-18.7, SR-18.16). Parameter texts reach MCP
// tools/list, surface.json and the generated references, never help. The Go
// (SpawnParams.ReuseFinished), CLI (--reuse-finished) and TypeScript
// (reuse_finished) texts are short forms of it.
const reuseFinishedDescription = "Opt in to reusing an explicit claude_instance_id whose row is finished (ended or missing): the id starts again on that row. " +
	"A live row (pending included) still collides with ErrInstanceIdCollision, as does a row that changed or was removed after this spawn examined it; nothing is changed then. " +
	"No effect without an explicit claude_instance_id (a minted id cannot collide). " +
	"Applies to this call only; no template or config setting carries it. The default (off) is unchanged: any existing row with the id collides. " +
	"A successful spawn does not say whether it created a fresh row or reset a finished one. " +
	"A reused id starts with no memory of its earlier lives: resume and get never use or show an earlier life's history, so the earlier conversation cannot be resumed through agent-director after a reuse. " +
	"Retrying a failed plain spawn: after a held-name refusal (duplicate session) the row is already ended unless the error says otherwise, so the retry's lookup decides it at once; after any other failed launch the row stays pending and an opted-in retry collides until find-missing marks it missing, which happens only after the pending grace period (60 s by default). " +
	"Feature detection: read the version of the binary that serves the caller: on the CLI the version verb; over MCP the version tool, which reports the running serve process's version until that process restarts; in the TypeScript client binaryVersion (from Client.create() or resolveSystemBinary()), never version(), which is the npm package's version. " +
	"A release candidate X.Y.Z-rc.N counts as X.Y.Z. A build without a release stamp reports 0.0.0-dev (a make build) or dev (a plain go build); a caller cannot compare it and relies on the older-binary behaviour: " +
	"an older binary returns ErrInvalidFlags on the CLI and in the TypeScript client, and over MCP silently ignores the parameter, so a finished row gives ErrInstanceIdCollision. " +
	"Use it as the same user and in the same tmux environment as the agents. " + sameEnvConsequences

// killDescription is kill's Description (SR-6.1, SR-1.7, SR-18.1, SR-18.2,
// SR-18.7, SR-18.9; decision-0930b Q4), ending with the live-row sequence.
const killDescription = "End the agent of a live row's current launch (pending included). " +
	"kill finds the session carrying the row's current launch label on its recorded socket, ends the agent's pane and that session by tmux id, and succeeds only once the agent process is gone; otherwise it returns ErrTmuxKillFailed. kill_sent says whether a kill was sent. " +
	"With no session of the launch found, an agent process that is gone or not recorded is success, nothing sent; one running in another session's pane has that pane ended and is checked again. " +
	"On a finished row (ended or missing) kill is a no-op success with kill_sent false and no tmux call; that is not verification that the agent exited. " + missingNotProofShort + " " +
	"kill never changes the row's state: find-missing marks the row once its agent process is gone. kill never signals a process itself; success means the agent process exited, not every process it started. " +
	"On a pending row kill aborts only the current launch; before the launch created its session it returns kill_sent false and does not stop the launch. kill never ends an earlier launch's session. " +
	"Success is judged per call: kill succeeds when the agent process and every other process it found in the session's panes are gone. If ErrTmuxKillFailed named another process that outlived the kill (by pid), it is not the agent and later calls do not track it: a retried kill checks only the agent process. A retry's success means only that the agent is gone; the named process needs a human (see the README's \"Operator actions\"). If the row finishes while kill waits and the agent outlives the wait, kill returns ErrTmuxKillFailed, and a retried kill is a finished-row no-op. " +
	"Errors (for kill, GONE is success): ErrTmuxKillFailed (UNAVAILABLE): after a kill the agent process, or another process of the session's panes, ran past the kill exit wait, or the process cannot be checked and its labelled session remains, or no session or pane of this launch was found while the agent process runs; retry later. ErrTmuxUnresponsive (UNAVAILABLE): tmux did not answer usably, before or after a kill was sent; retry later with backoff. ErrTmuxSessionConflict (CONFLICT, permanent until a human looks): the session found is not this launch's session, or tmux holds conflicting labels; no kill was sent (see the README's \"Operator actions\"). ErrTmuxNotAvailable (ENVIRONMENT): tmux could not be run, its socket is not accessible to this user, or this is not the tmux server the agent was launched on; an operator must fix it. A repeated kill right after the last session on its tmux server ends can get ErrTmuxUnresponsive or ErrTmuxNotAvailable while the server exits; the caller waits and checks again. ErrSpawnNotFound: no row has this id. None of these errors means that the agent is dead. Never delete a row after a kill that did not succeed. " +
	"kill must run as the same user and in the same tmux environment as the agents. " + sameEnvConsequences + " " + expireCleanupPointer + " " +
	"A live row whose recorded tmux session name cannot be used (it is empty, contains a control character, or contains a character tmux stores differently) gets ErrInternal with no tmux call; removing the row is a human's decision (see \"Operator actions\" in the agent-director README). " +
	liveRowSequence

// Verbs is the canonical, ordered list of verbs implemented by this binary.
// Epic 2+ workers append entries here as they implement new verbs.
var Verbs = []VerbDef{
	{
		Name:        "help",
		Description: "Print the verb list as JSON (for SessionStart and SessionEnd reason=compact hooks).",
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
		Description: "Launch a tracked Claude Code agent in a new tmux session. Returns the claude_instance_id and pre_trust (ok, skipped or failed) without waiting for the agent; the row is pending from its insert until the agent reports in, then waiting. Creating its labelled session is bounded by the create timeout; on a timeout spawn returns ErrTmuxUnresponsive (UNAVAILABLE, transient): the session may have been created and the new row stays pending; do not retry until get shows the row ended or missing, since a retry without an explicit id would start a second agent; then retry an explicit id with the reuse opt-in. With an explicit id that has no row, a session of this store labelled with that id, left over from an earlier life, refuses the spawn with ErrTmuxSessionConflict and nothing is written. If the session name is already held, spawn ends its new row at once (unless the error says otherwise) and returns ErrTmuxSessionConflict naming the holder's tmux id and whether its label names this id; ErrTmuxSessionCreate if the holder vanished first, ErrTmuxUnresponsive or ErrTmuxNotAvailable if tmux could not be read or run. ErrTmuxSessionConflict is CONFLICT (permanent until a human looks): another row's or another agent-director store's session is another agent and must not be ended; a leftover of an earlier life, or a session with no valid instance id, is a human's to end (see the README's \"Operator actions\"); then, if the refusal was for a held name, spawn the id again with --reuse-finished. ErrTmuxNotAvailable is ENVIRONMENT and ErrTmuxSessionCreate a LAUNCH FAILURE. ErrInternal, nothing created or changed: the collision pre-check could not read the store (which says nothing about whether the id is in use), or a reuse's archive of the previous session or its change failed (a busy store included), or a reused row's recorded name is unusable (see kill). " + liveRowSequencePointer,
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
				Description:   "Optional explicit id; an empty or absent id mints a fresh UUID4. An explicit id containing an ASCII control character (0x00-0x1f or 0x7f) is rejected with ErrInvalidFlags. An explicit id that already has a row returns ErrInstanceIdCollision: without the reuse opt-in any existing row collides, in any state; with it, only a live row (pending included) or a row that changed or was removed after this spawn examined it.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    true,
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
				Description:   "Optional explicit tmux session name. Empty/omitted falls back to <basename(cwd)>-<id[:8]>. Validated app-side: rejects empty (when supplied), '#' ':' '.' '$' '\\' (backslash), ASCII control chars, non-UTF-8, and >64 bytes. NO DB uniqueness check; a name already held by a tmux session when spawn creates its session ends the new row at once and returns the classified error spawn's description states (ErrTmuxSessionConflict naming the holding session), unless spawn refused earlier and wrote nothing. Name reuse across ended spawns is supported.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
			{
				Name:          "reuse-finished",
				Type:          "bool",
				Description:   reuseFinishedDescription,
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
		Description: "Return a row's state (pending/waiting/working/ask_user/check_permission/ended/missing). pending means a launch (spawn, reuse or resume) is in progress and the agent has not reported in yet (Claude Code's SessionStart); it may be loading or at a startup prompt. A resumed pending row keeps its session id and history.",
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
				Description:   "Current state, one of the allowed values. pending: a launch (spawn, reuse or resume) is in progress and the agent has not reported in yet (Claude Code's SessionStart); it may be loading or waiting at a startup prompt. A resumed pending row keeps its session id and history (non-empty claude_session_id). `missing` is the sweep's judgement on the evidence available to it, not proof that the agent has exited.",
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
		Description: "Return a row in full (id, parent, state, cwd, session name, tmux socket, args, relay mode, session_id, labels, timestamps).",
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
			{Name: "state", Type: "string", Description: "Current state, one of the allowed values. pending: a launch (spawn, reuse or resume) is in progress and the agent has not reported in yet (Claude Code's SessionStart); it may be loading or waiting at a startup prompt. A resumed pending row keeps its session id and history (non-empty claude_session_id). `missing` is the sweep's judgement on the evidence available to it, not proof that the agent has exited.", Nullable: false, AllowEmpty: false, AllowedValues: stateEnum},
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
			{Name: "liveness_unverified_since", Type: "timestamp?", Description: "RFC3339 timestamp of the first sweep that left this live row unverified; kept while later sweeps change liveness_note. Cleared to NULL together with liveness_note when a sweep finds the agent process alive; null/omitted while not unverified.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "liveness_note", Type: "string?", Description: "Reason token of the latest sweep that left this live row unverified (its agent process could not be checked and tmux did not settle it), for example process_not_seen_session_present, process_not_seen_tmux_unchecked, probe_eacces, tmux_server_changed or provenance_conflict; overwritten when the reason changes. Cleared to NULL together with liveness_unverified_since when a sweep finds the agent process alive; null/omitted while not unverified.", Nullable: true, AllowEmpty: false, AllowedValues: nil},
			{Name: "permission_requests", Type: "[]object", Description: "All open (undecided) permission requests awaiting orchestrator decision. Always a non-null array ([] when empty). Populated only when state == check_permission; empty array for all other states. Each element: request_id (int) — autoincrement row id; request_token (string) — UUIDv4 token minted by runRelay, pass to decide verb to target this row; tool_name (string) — Claude Code tool that triggered the request; tool_input (string) — raw JSON string of the tool's input, NOT a nested object (consumers parse it themselves); requested_at (RFC3339 timestamp) — created_at of the row.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "transcript_status", Type: "string", Description: "Derived operator-facing summary of the current session's transcript state (b.v2c): 'present' (jsonl_path recorded), 'never_written' (session id but NULL jsonl_path and prior_sessions is empty — nothing was ever written in the current life), 'rotated' (NULL jsonl_path but prior_sessions is non-empty — the current life has history under a different session id), or 'no_session' (no claude_session_id yet). Session history belongs to a life; 'never_written' and 'rotated' are decided on the same entries prior_sessions lists. A reuse starts a new life with no history; a failed reuse's restore returns the pre-reuse life.", Nullable: false, AllowEmpty: false, AllowedValues: []string{"present", "never_written", "rotated", "no_session"}},
			{Name: "prior_sessions", Type: "[]object", Description: "Archived prior sessions of the current life, newest first, excluding the row's current session id — the queryable link back to sessions orphaned by a rotation (b.v2c). Session history belongs to a life: after a reuse, which starts a new life, no earlier life's session appears; a failed reuse's restore returns the pre-reuse life's. Always a non-null array ([] when empty). Each element: claude_session_id (string) — archived session id; jsonl_path (string) — archived transcript path (may be empty); recorded_at (timestamp) — when the archive was written (the rotation moment).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrSpawnNotFound",
		},
	},
	{
		Name:        "send-keys",
		Description: "Send text into the agent's own pane: `\\r` stripped, `\\n` kept as a newline in the input box, and one Enter appended to submit. tmux errors: ErrTmuxSendKeys (GONE: only it means the row's session is not there), ErrTmuxUnresponsive (UNAVAILABLE; after a timeout the keys may have been delivered), ErrTmuxSessionConflict (CONFLICT), ErrTmuxNotAvailable (ENVIRONMENT). " + unusableNamePointer,
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
				Description:   "When true, also allows a pending row: a launch (spawn, reuse or resume) whose agent has not reported in yet. Keys are delivered only to a session started by the row's current launch. ended and missing rows are still rejected.",
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
			"ErrTmuxUnresponsive",
			"ErrTmuxSessionConflict",
		},
	},
	{
		Name:        "read-pane",
		Description: "Capture the last N lines of the agent's own pane (default 25, no upper cap). ANSI escapes are stripped, unicode glyphs kept, unless `ansi=true` (raw bytes). tmux errors: ErrTmuxCaptureFailed (GONE: only it means the row's session is not there), ErrTmuxUnresponsive (UNAVAILABLE), ErrTmuxSessionConflict (CONFLICT), ErrTmuxNotAvailable (ENVIRONMENT). " + unusableNamePointer,
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
			"ErrTmuxUnresponsive",
			"ErrTmuxSessionConflict",
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
		Description: "Caller's allow/deny verdict on an open PermissionRequest. One atomic write records it only while the request is open and deliverable, so the first call wins; an open request past its relay window is refused with ErrRelayFallenBack (answer at the pane) and no verdict is recorded. Only for rows with relay_mode=on.",
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
		Description: "Fetch one permission_requests row by request_token alone. decision, decision_reason and decided_at are null while it is open.",
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
		Description: "Relaunch a finished (ended/missing) row via `claude --resume`. " + missingNotProofShort + " Same id and JSONL transcript, fresh tmux session. Before its launch, resume runs spawn's best-effort pre-trust for the row's cwd, unless the spawn that began the row's life turned it off with no-pre-trust; a pre-trust failure never fails the resume. Returns the claude_instance_id and pre_trust (ok, skipped or failed). Before creating the session, resume moves the row to pending, keeping its session id and history, and writes parent_id from the caller's AGENT_DIRECTOR_INSTANCE_ID. The row stays pending until the agent reports in, then waiting. If the launch fails other than by timing out (ErrTmuxNotAvailable is ENVIRONMENT and ErrTmuxSessionCreate a LAUNCH FAILURE), resume restores the row to its prior ended or missing state; if the restore cannot be applied, the error says so. Creating the session is bounded by the create timeout; on a timeout resume returns ErrTmuxUnresponsive (UNAVAILABLE, transient): the session may have been created and the row stays pending; do not retry until get shows the row ended or missing. A refusal before the move writes nothing; resume can be re-issued. A session in the way (holding the name, left over, or this row's own) is never touched: ErrTmuxSessionConflict (CONFLICT, until a human looks; see the README's \"Operator actions\"), or ErrTmuxUnresponsive while it appears to still be stopping or starting. A name held at the create is refused likewise, after the restore. A pending row (a launch in progress, a resumed one included) is refused with ErrSpawnNotResumable. A row whose instance id contains a control character (its session could never be labelled) or whose recorded name is unusable (see kill) is refused with ErrInternal and no tmux call. If the store cannot record the launch, resume returns ErrInternal and launches nothing.",
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "claude_instance_id",
				Type:          "string",
				Description:   "Id of the finished (ended or missing) row to resume.",
				Required:      true,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "claude_instance_id", Type: "string", Description: "The same id passed in (resume keeps the instance id).", Nullable: false, AllowEmpty: false, AllowedValues: nil},
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
			"ErrTmuxSessionConflict",
		},
	},
	{
		Name:        "find-missing",
		Description: "Reconcile live rows with their agents. A row's liveness comes only from its agent process, checked by its recorded start time; no process environment is read, and no child process or copy of the row's id keeps it alive. A live process keeps the row live; a dead one marks it `missing` whatever tmux shows, with no tmux call. Only a row whose process cannot be checked or was never recorded is looked up in tmux, once per socket, on its recorded socket (else the caller's): its own labelled session leaves it unverified (a row with no recorded pane is judged by the pane carrying its launch token, and marked if none does), as does a tmux answer that cannot be used; no session of its current launch marks it `missing` whatever holds its name. A session holding the name is never touched (see the README's \"Operator actions\"). tmux problems never fail the sweep but leave rows unverified. `missing` is the sweep's judgement on the evidence available to it, not proof that the agent has exited. Run it as the same user and in the same tmux environment as the agents: a run as another user, as root or against another tmux server can mark live rows `missing`. " + sameEnvConsequences + " " + expireCleanupPointer + " ErrProbeUnsupported is no longer returned. A pending row (a spawn's, a reuse's or a resume's launch) is not judged while inside the pending grace period (pending_grace_seconds, 60 s by default), measured from its launch start, not its started_at: it is left as it is, in neither list; a pending row with no readable launch start is judged at once. " + liveRowSequencePointer,
		Callable:    true,
		HandleFree:  false,
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows this sweep marked `missing` (the length of ids). Zero is a legitimate happy-path result when nothing needed marking.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of rows this sweep marked `missing`, on process or tmux evidence: rows whose agent process (the SessionStart one or the recorded pane's) is dead, and rows whose process could not be checked and tmux found no session or pane of their current launch; a pending row only past the pending grace period. A row whose guarded write found it changed or gone, or failed, is in neither list. `missing` is the sweep's judgement on the evidence available to it, not proof that the agent has exited. Never null; [] when none.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "unverified", Type: "int", Description: "Number of live rows this sweep left unverified (the length of unverified_ids).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "unverified_ids", Type: "[]string", Description: "Sorted IDs of live rows this sweep left unverified with a liveness note, whether it wrote the note or found it already current: rows whose agent process could not be checked and which tmux did not mark, for example because their own session is present, tmux could not tell or was unavailable, or tmux was not called, or their recorded tmux session name cannot be used (empty, a control character, or a character tmux stores differently: no tmux call, and a liveness note of its own; removing such a row is a human's decision, see \"Operator actions\" in the agent-director README). Never a pending row inside the pending grace period. A row whose guarded write found it changed or gone, or failed, and a row whose note was cleared, is in neither list. Never null; [] when none.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrProbeUnsupported",
		},
	},
	{
		Name:        "expire",
		Description: expireDescription,
		Callable:    true,
		HandleFree:  false,
		Params: []ParamDef{
			{
				Name:          "older_than",
				Type:          "duration",
				Description:   "Duration override (e.g. `7d`, `2h`). When omitted, defaults.expire_retention_days from config applies.",
				Required:      false,
				Nullable:      false,
				AllowEmpty:    false,
				AllowedValues: nil,
			},
		},
		ResultFields: []FieldDef{
			{Name: "count", Type: "int", Description: "Number of rows deleted (the length of ids): rows deleted after tmux showed no session of the agent (Gone) and its recorded process was not seen running. Zero is a legitimate result when no row was deleted.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "ids", Type: "[]string", Description: "Sorted IDs of the rows deleted after tmux showed no session of the agent (Gone) and its recorded process was not seen running, each only while the row was unchanged since expire examined it. Never null; [] when none.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "kept", Type: "int", Description: "Number of selected rows kept rather than deleted (the length of kept_ids), for example because the agent's process or a session of the agent (its own, or a leftover of an earlier launch) may still run, tmux could not be checked or answered from a different server, the run's tmux time budget (sweep_budget_seconds) was spent, the row changed after it was examined, or its recorded tmux session name cannot be used (kept on every run, before any other check, with no tmux call).", Nullable: false, AllowEmpty: true, AllowedValues: nil},
			{Name: "kept_ids", Type: "[]string", Description: "Sorted IDs of the selected rows kept rather than deleted, each reported with its reason in the trail (ad.expire.kept). A row whose recorded tmux session name cannot be used (empty, a control character, or a character tmux stores differently) is kept on every run, before any other check, with no tmux call and a reason of its own; removing it is a human's decision (see \"Operator actions\" in the agent-director README). tmux problems and a failed delete of one row never fail the run: the row is kept. A failed read of the selected rows fails the run. A row another caller removed first is in neither list. Never null; [] when none.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{},
	},
	{
		Name:        "delete",
		Description: "DEPRECATED, removal planned (b.tep). Not for cleanup or recovery: expire removes finished rows; respawn with spawn --reuse-finished; for a stuck live row, kill then find-missing. Never delete after a failed kill, or assuming a finished row's agent exited. Batch removal by claude_instance_id, bypassing all guards; touches no tmux session or transcript. Per-id result map (ok or an error); a partial failure never aborts the batch. " + missingNotProofShort,
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
		Description: "Save a reusable spawn preset as ~/.agent-director/templates/NAME.toml. Per-invocation params (template, claude_instance_id, tmux_session_name, reuse_finished) are refused.",
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
		Description: "Enumerate rows. All filters AND together. Order is unspecified; callers sort.",
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
			{Name: "spawns", Type: "[]Spawn", Description: "Matching rows. Empty array when none match (never null). Each row carries liveness_unverified_since (timestamp?, the first unverified sweep's time) and liveness_note (string?, the latest sweep's reason token, overwritten when the reason changes), both omitted while NULL and cleared together when a sweep finds the agent process alive, and launch_started_at (timestamp?), the start of the launch in progress (RFC3339 UTC with millisecond precision), omitted unless the row is pending. Each row's state takes the same values as status, with the same meaning of pending: a launch (spawn, reuse or resume) in progress whose agent has not reported in yet; a resumed pending row keeps its session id and history. `missing` is the sweep's judgement on the evidence available to it, not proof that the agent has exited.", Nullable: false, AllowEmpty: true, AllowedValues: nil},
		},
		ErrorNames: []string{
			"ErrListInvalidLabel",
		},
	},
	{
		Name:        "pause",
		Description: "Shut down a waiting row: send `/exit` to the agent's own pane and wait up to pause.timeout_seconds for `ended`. A finished row (ended/missing) is a no-op success. " + missingNotProofShort + " tmux errors: ErrTmuxSendKeys (GONE: only it means the row's session is not there), ErrTmuxUnresponsive (UNAVAILABLE), ErrTmuxSessionConflict (CONFLICT), ErrTmuxNotAvailable (ENVIRONMENT). " + unusableNamePointer,
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
			"ErrTmuxUnresponsive",
			"ErrTmuxSessionConflict",
		},
	},
	{
		Name:        "serve",
		Description: "Start the long-lived MCP server: the CLI verbs except `hook`, `serve` and `trail-emit` as MCP tools, JSON-RPC on stdio.",
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
		Description: "Print the build-time version stamp as JSON ({version, commit}).",
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
		Description: "Emit an ad.* trail event (sub-verb relay-attempt) without opening state.db, so it works during corrupted-state recovery.",
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
		Description: "Internal: run by each Spawn's --settings hooks with payload JSON on stdin; writes the row only when the hook's parent process is the row's recorded pane process (the agent); any other process's hook, or a subagent's SessionStart or SessionEnd, changes nothing and is logged as ad.hook.ignored. Exits 0 (fail-open).",
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
