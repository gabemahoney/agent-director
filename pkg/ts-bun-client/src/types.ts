/**
 * Structural logger interface — mirrors the four conventional log levels.
 * All methods are optional; callers may pass any object that implements the
 * subset they care about (e.g., `console`).
 */
export interface Logger {
  debug?: (message: string, ...args: unknown[]) => void;
  info?: (message: string, ...args: unknown[]) => void;
  warn?: (message: string, ...args: unknown[]) => void;
  error?: (message: string, ...args: unknown[]) => void;
}

/**
 * Options accepted by the `Client` constructor (SRD SR-1.2).
 *
 * `storePath` is the only required field. The others are optional overrides;
 * absent fields fall back to the same three-tier defaults as `pkg/api.Options`
 * (config-file value, then hardcoded fallback).
 *
 * Tilde expansion is handled TS-side by `src/internal/tilde.ts` before paths
 * cross the FFI boundary, so the C-ABI never receives a leading `~`.
 */
export interface ClientOptions {
  /** Path to the SQLite store file. Tilde-expanded before crossing FFI. */
  storePath: string;
  /** Path to the TOML config file. Tilde-expanded before crossing FFI. */
  configPath?: string;
  /** Override the tmux binary. Defaults to the binary on PATH. */
  tmuxCommand?: string;
  /**
   * When `true`, create the store and initialize the database schema if the
   * store file does not yet exist. When `false` (the default), opening a
   * non-existent store returns an error. Mirrors `pkg/api.Options.CreateIfMissing`.
   */
  createIfMissing?: boolean;
  /** Optional logger for client-side warnings (e.g., non-fatal close errors). */
  logger?: Logger;
}

// ---------------------------------------------------------------------------
// Shared sub-shapes
// ---------------------------------------------------------------------------

/** Mirrors pkg/api.VerbSummary — one entry in the `help` verb result. */
export interface VerbSummary {
  /** Canonical CLI/MCP verb name (e.g. "spawn", "send-keys"). */
  name: string;
  /** One-line verb description from the manifest. */
  description: string;
}

/** Mirrors pkg/api/get.go::PermissionRequestInfo — embedded in GetResult when state=check_permission. */
export interface PermissionRequestInfo {
  /** Autoincrement primary key of the permission_requests row. */
  request_id: number;
  /** Claude Code tool that triggered the permission request (e.g. "Bash"). */
  tool_name: string;
  /** Raw JSON string of the tool input as stored in the DB. NOT a nested object. */
  tool_input: string;
  /** RFC3339 timestamp when the permission request row was created. */
  requested_at: string;
}

/** Mirrors pkg/api/list.go::ListRow — one Spawn row returned by the list verb. */
export interface ListRow {
  /** Stable Spawn id. */
  claude_instance_id: string;
  /** Parent Spawn id (omitted when launched from a plain shell). */
  parent_id?: string;
  /** Current lifecycle state. */
  state: string;
  /** Canonicalized working directory. */
  cwd: string;
  /** tmux session name. */
  tmux_session_name: string;
  /** "on" or "off". */
  relay_mode: string;
  /** Caller-supplied key-value tags. */
  labels: Record<string, string>;
  /** Row insert time (RFC3339). */
  started_at: string;
  /** Most recent hook UPSERT time (RFC3339). */
  last_seen_at: string;
  /** Set when state moves to ended (omitted while live). */
  ended_at?: string | null;
}

// ---------------------------------------------------------------------------
// Verb Params / Result interfaces
//
// One pair per callable verb in src/internal/verbs.ts.
// Params wire names follow the JSON tags in pkg/cabi/verbs_*.go.
// Result wire names follow the json:"..." tags on pkg/api result structs.
// ---------------------------------------------------------------------------

/** Mirrors pkg/cabi/verbs_read.go::ad_spawn params */
export interface SpawnParams {
  /** Absolute (or ~/-prefixed) path the Spawn's Claude starts in. Required. */
  cwd: string;
  /** Named template under ~/.agent-director/templates/. */
  template?: string;
  /** Explicit instance id (UUID4 minted when absent). */
  claude_instance_id?: string;
  /** KEY=VALUE label pairs. */
  label?: string[];
  /** permissions.allow entries. */
  allow?: string[];
  /** permissions.deny entries. */
  deny?: string[];
  /** permissions.ask entries. */
  ask?: string[];
  /** "on" | "off" | "" (use config default). */
  relay_mode?: string;
  /** KEY→VALUE env-var overrides injected on the tmux session. */
  extra_env?: Record<string, string>;
  /** Pass-through argv to `claude` after --settings. */
  claude_args?: string[];
  /** Skip pre-writing workspace-trust into ~/.claude.json. Default false. */
  no_pre_trust?: boolean;
  /** Explicit tmux session name. Auto-derived when absent. */
  tmux_session_name?: string;
}

/** Mirrors pkg/api/spawn.go::SpawnResult */
export interface SpawnResult {
  /** The id the new Spawn is tracked under. */
  claude_instance_id: string;
}

/** Mirrors pkg/cabi/verbs_read.go::ad_status params */
export interface StatusParams {
  /** Id of the Spawn to inspect. */
  claude_instance_id: string;
}

/** Mirrors pkg/api/status.go::StatusResult */
export interface StatusResult {
  /** Current lifecycle state (pending/waiting/working/ask_user/check_permission/ended/missing). */
  state: string;
}

/** Mirrors pkg/cabi/verbs_read.go::ad_get params */
export interface GetParams {
  /** Id of the Spawn to fetch. */
  claude_instance_id: string;
}

/** Mirrors pkg/api/get.go::SpawnRow */
export interface GetResult {
  claude_instance_id: string;
  parent_id: string;
  state: string;
  cwd: string;
  tmux_session_name: string;
  claude_args: string[];
  relay_mode: string;
  jsonl_path: string;
  claude_session_id: string;
  labels: Record<string, string>;
  /** Row insert time (RFC3339). */
  started_at: string;
  /** Last hook UPSERT time (RFC3339). */
  last_seen_at: string;
  /** Set when state moves to ended; omitted while live. */
  ended_at?: string | null;
  /** Open permission request; present only when state=check_permission with an undecided row. */
  permission_request?: PermissionRequestInfo | null;
}

/** Mirrors pkg/cabi/verbs_read.go::ad_send_keys params (pkg/api.SendKeysParams json tags) */
export interface SendKeysParams {
  /** Id of the Spawn whose pane will receive the text. */
  claude_instance_id: string;
  /** Text to deliver to the pane. CR bytes stripped; LF preserved; Enter appended. */
  text: string;
}

/** Mirrors pkg/api/sendkeys.go::SendKeysResult (empty; reserved for future fields). */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type
export interface SendKeysResult {}

/** Mirrors pkg/cabi/verbs_read.go::ad_read_pane params (pkg/api.ReadPaneParams json tags) */
export interface ReadPaneParams {
  /** Id of the Spawn whose pane will be captured. */
  claude_instance_id: string;
  /** Trailing lines to return. Defaults to 25 when 0/omitted. */
  n_lines?: number;
  /** When true return raw bytes (ANSI preserved); when false strip ANSI (default). */
  ansi?: boolean;
}

/** Mirrors pkg/api/readpane.go::ReadPaneResult */
export interface ReadPaneResult {
  /** Captured pane text. */
  pane: string;
}

/** Mirrors pkg/cabi/verbs_control.go::ad_kill params (pkg/api.KillParams json tags) */
export interface KillParams {
  /** Id of the Spawn to kill. */
  claude_instance_id: string;
}

/** Mirrors pkg/api/kill.go::KillResult (empty; reserved for future fields). */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type
export interface KillResult {}

/** Mirrors pkg/cabi/verbs_control.go::ad_decide params (pkg/api.DecideParams json tags) */
export interface DecideParams {
  /** Id of the Spawn whose open permission request is being decided. */
  claude_instance_id: string;
  /** Orchestrator verdict. */
  decision: "allow" | "deny";
  /** Optional free-text message surfaced to Claude on deny. */
  reason?: string;
}

/** Mirrors pkg/api/decide.go::DecideResult (empty; reserved for future fields). */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type
export interface DecideResult {}

/** Mirrors pkg/cabi/verbs_control.go::ad_resume params (pkg/api.ResumeParams json tags) */
export interface ResumeParams {
  /** Id of the terminated Spawn to resurrect. */
  claude_instance_id: string;
}

/** Mirrors pkg/api/resume.go::ResumeResult */
export interface ResumeResult {
  /** Same id that was passed in (preserved across resurrection). */
  claude_instance_id: string;
}

/** Mirrors pkg/cabi/verbs_control.go::ad_find_missing params */
export interface FindMissingParams {
  /** Optional deadline for the OS probe sweep (milliseconds). 0/omitted = no deadline. */
  timeout_ms?: number;
}

/** Mirrors pkg/api/find_missing.go::FindMissingResult */
export interface FindMissingResult {
  /** Number of rows transitioned to missing on this sweep. */
  count: number;
  /** Sorted ids of rows transitioned to missing. */
  ids: string[];
}

/** Mirrors pkg/cabi/verbs_admin.go::ad_expire params */
export interface ExpireParams {
  /**
   * Duration override (e.g. "7d", "2h", "0d"). When omitted, the config
   * default (defaults.expire_retention_days) applies.
   */
  older_than?: string;
}

/** Mirrors pkg/api/expire.go::ExpireResult */
export interface ExpireResult {
  /** Number of terminal rows removed. */
  count: number;
  /** Sorted ids of rows removed. */
  ids: string[];
}

/** Mirrors pkg/cabi/verbs_admin.go::ad_delete params */
export interface DeleteParams {
  /** Id(s) to delete. */
  claude_instance_id: string[];
}

/** Mirrors pkg/api/delete.go::DeleteResult */
export interface DeleteResult {
  /** Per-id outcome: "ok" on success, err_name on failure. */
  results: Record<string, string>;
}

/** Mirrors pkg/cabi/verbs_admin.go::ad_make_template params */
export interface MakeTemplateParams {
  /** Template filename (without extension). Must be filename-safe. Required. */
  name: string;
  /** Default working directory to bake in. */
  cwd?: string;
  /** Default relay mode: "on" | "off" | "" (inherit). */
  relay_mode?: string;
  /** Default claude argv. Per-call --claude-args replaces wholesale. */
  claude_args?: string[];
  /** Env-var overrides to bake in. */
  extra_env?: Record<string, string>;
  /** Label k=v entries to bake in. */
  label?: string[];
  /** permissions.allow entries. */
  allow?: string[];
  /** permissions.deny entries. */
  deny?: string[];
  /** permissions.ask entries. */
  ask?: string[];
  /**
   * When true, an existing template at this name is replaced atomically
   * via a sibling-tempfile + rename. When omitted or false, the
   * existing-template rejection runs (v0.4.2 behaviour).
   */
  overwrite?: boolean;
}

/** Mirrors pkg/api/make_template.go::MakeTemplateResult */
export interface MakeTemplateResult {
  /** Absolute path of the written template TOML file. */
  path: string;
}

/** Mirrors pkg/cabi/verbs_read.go::ad_list params */
export interface ListParams {
  /** Filter by state (multiple values OR together). */
  state?: string[];
  /** Filter by label k=v pairs (multiple entries AND together). */
  label?: string[];
  /** Filter by parent_id exact match. */
  parent?: string;
  /** Filter by canonicalized cwd exact match. */
  cwd?: string;
  /** Filter by tmux session name exact match. */
  tmux_session_name?: string;
  /** Cap result count. 0/omitted means no cap. */
  limit?: number;
}

/** Mirrors pkg/api/list.go::ListResult */
export interface ListResult {
  /** Matching Spawn rows. Empty array when none match. */
  spawns: ListRow[];
}

/** Mirrors pkg/cabi/verbs_control.go::ad_pause params (pkg/api.PauseParams json tags) */
export interface PauseParams {
  /** Id of the Spawn to pause gracefully. */
  claude_instance_id: string;
}

/** Mirrors pkg/api/pause.go::PauseResult (empty; reserved for future fields). */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type
export interface PauseResult {}

/** Mirrors pkg/cabi/verbs_admin.go::ad_version params (handle-free; no user params). */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type
export interface VersionParams {}

/** Mirrors pkg/api/version.go::VersionResult */
export interface VersionResult {
  /** Human-readable version stamp. "dev" for unstamped builds. */
  version: string;
  /** Full git SHA. "unknown" for unstamped builds. */
  commit: string;
}
