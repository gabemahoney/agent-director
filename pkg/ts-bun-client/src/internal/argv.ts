/**
 * argv.ts — verb-driven argv builder for the subprocess Client.
 *
 * Exports a single pure function `buildArgv(cliPath, verb, params, globalOpts?)`
 * that converts typed verb parameters into a complete CLI argv array:
 *
 *   [cliPath, ...globalFlags, verbName, ...verbFlags]
 *
 * Rules:
 *   - Long-flag form only (e.g. --cwd, --claude-instance-id).
 *   - No shell interpolation, no sh -c.
 *   - Mapping between camelCase method name and kebab CLI verb follows the
 *     same mapping in src/internal/verbs.ts.
 *   - Optional fields are omitted when undefined / falsy.
 *   - Boolean flags (--no-pre-trust, --ansi, --overwrite) are only appended
 *     when the field is explicitly true.
 *   - Global flags (b.32k: --store-path, --home, --tmux-command) appear
 *     BEFORE the verb token so the CLI's global-flag parser in
 *     cmd/agent-director/global_flags.go strips them prior to verb dispatch.
 *
 * Implements SRD SR-1.2 (argv construction is verb-driven and shell-free).
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import type { VerbName } from "./verbs.js";
import type {
  SpawnParams,
  StatusParams,
  GetParams,
  SendKeysParams,
  ReadPaneParams,
  KillParams,
  DecideParams,
  ResumeParams,
  ExpireParams,
  DeleteParams,
  MakeTemplateParams,
  ListParams,
  PauseParams,
} from "../types.js";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * GlobalArgvOptions — values for the three global CLI flags introduced by
 * bug b.32k. Each field is optional; when undefined the corresponding flag
 * is omitted from argv and the CLI's own default-resolution kicks in.
 *
 * Tilde-expansion is the caller's responsibility (typically done at Client
 * construction time via src/internal/tilde.ts).
 */
export interface GlobalArgvOptions {
  /** Tilde-expanded path forwarded to the CLI as `--store-path`. */
  storePath?: string;
  /** Tilde-expanded path forwarded to the CLI as `--home`. */
  home?: string;
  /** Tilde-expanded path forwarded to the CLI as `--tmux-command`. */
  tmuxCommand?: string;
}

/**
 * buildArgv converts a CLI binary path, verb name, and typed parameters into
 * a complete CLI argv array:
 *
 *   [cliPath, ...globalFlags, verbName, ...verbFlags]
 *
 * Global flags are inserted BEFORE the verb token because the CLI's
 * `parseGlobalFlags` pre-scan operates on os.Args[1:] before verb dispatch
 * (see cmd/agent-director/global_flags.go). b.32k.
 *
 * @param cliPath    The absolute path to the CLI binary (argv[0]).
 * @param verb       The kebab-case verb name (must be a VerbName).
 * @param params     The typed params object for the verb.
 * @param globalOpts Optional global-flag values (--store-path / --home /
 *                   --tmux-command). When undefined or all-fields-omitted,
 *                   no global flags appear in argv and the CLI applies its
 *                   own default-resolution.
 * @returns          The full argv array starting with cliPath.
 */
export function buildArgv(
  cliPath: string,
  verb: VerbName,
  params: unknown,
  globalOpts?: GlobalArgvOptions
): string[] {
  const globalFlags = buildGlobalFlags(globalOpts);
  const verbFlags = buildVerbFlags(verb, params);
  return [cliPath, ...globalFlags, ...verbFlags];
}

/**
 * buildGlobalFlags returns the global-flag tokens that go BEFORE the verb
 * name. Each flag is emitted in two-token `--flag value` form only when the
 * corresponding field on globalOpts is set.
 */
function buildGlobalFlags(opts: GlobalArgvOptions | undefined): string[] {
  if (!opts) return [];
  const f: string[] = [];
  if (opts.storePath !== undefined) f.push("--store-path", opts.storePath);
  if (opts.home !== undefined) f.push("--home", opts.home);
  if (opts.tmuxCommand !== undefined) f.push("--tmux-command", opts.tmuxCommand);
  return f;
}

/**
 * buildVerbFlags returns [verbName, ...flags] without the cliPath prefix.
 * Used internally and exposed for testing.
 */
function buildVerbFlags(verb: VerbName, params: unknown): string[] {
  switch (verb) {
    case "version":
      return buildVersion();
    case "spawn":
      return buildSpawn(params as SpawnParams);
    case "status":
      return buildStatus(params as StatusParams);
    case "get":
      return buildGet(params as GetParams);
    case "send-keys":
      return buildSendKeys(params as SendKeysParams);
    case "read-pane":
      return buildReadPane(params as ReadPaneParams);
    case "kill":
      return buildKill(params as KillParams);
    case "decide":
      return buildDecide(params as DecideParams);
    case "resume":
      return buildResume(params as ResumeParams);
    case "find-missing":
      return buildFindMissing();
    case "expire":
      return buildExpire(params as ExpireParams);
    case "delete":
      return buildDelete(params as DeleteParams);
    case "make-template":
      return buildMakeTemplate(params as MakeTemplateParams);
    case "list":
      return buildList(params as ListParams);
    case "pause":
      return buildPause(params as PauseParams);
    default: {
      // Exhaustive check — TypeScript will error here if a VerbName case is
      // missing. At runtime this branch is unreachable.
      const _exhaustive: never = verb;
      throw new Error(`buildVerbFlags: unhandled verb "${String(_exhaustive)}"`);
    }
  }
}

// ---------------------------------------------------------------------------
// Per-verb builders
// ---------------------------------------------------------------------------

function buildVersion(): string[] {
  return ["version"];
}

function buildSpawn(p: SpawnParams): string[] {
  const f: string[] = ["spawn"];

  f.push("--cwd", p.cwd);

  if (p.template !== undefined) f.push("--template", p.template);
  if (p.claude_instance_id !== undefined)
    f.push("--claude-instance-id", p.claude_instance_id);
  if (p.tmux_session_name !== undefined)
    f.push("--tmux-session-name", p.tmux_session_name);
  if (p.relay_mode !== undefined && p.relay_mode !== "")
    f.push("--relay-mode", p.relay_mode);
  if (p.no_pre_trust === true) f.push("--no-pre-trust");

  // Repeatable --label k=v
  if (p.label) {
    for (const lv of p.label) f.push("--label", lv);
  }

  // Repeatable --extra-env K=V (Record<string, string> → K=V strings)
  if (p.extra_env) {
    for (const [k, v] of Object.entries(p.extra_env))
      f.push("--extra-env", `${k}=${v}`);
  }

  // Repeatable permission flags
  if (p.allow) {
    for (const a of p.allow) f.push("--allow", a);
  }
  if (p.deny) {
    for (const d of p.deny) f.push("--deny", d);
  }
  if (p.ask) {
    for (const a of p.ask) f.push("--ask", a);
  }

  // claude_args go after '--' as positional args
  if (p.claude_args && p.claude_args.length > 0) {
    f.push("--", ...p.claude_args);
  }

  return f;
}

function buildStatus(p: StatusParams): string[] {
  return ["status", "--claude-instance-id", p.claude_instance_id];
}

function buildGet(p: GetParams): string[] {
  return ["get", "--claude-instance-id", p.claude_instance_id];
}

function buildSendKeys(p: SendKeysParams): string[] {
  return [
    "send-keys",
    "--claude-instance-id",
    p.claude_instance_id,
    "--text",
    p.text,
  ];
}

function buildReadPane(p: ReadPaneParams): string[] {
  const f: string[] = ["read-pane", "--claude-instance-id", p.claude_instance_id];
  if (p.n_lines !== undefined && p.n_lines > 0)
    f.push("--n-lines", String(p.n_lines));
  if (p.ansi === true) f.push("--ansi");
  return f;
}

function buildKill(p: KillParams): string[] {
  return ["kill", "--claude-instance-id", p.claude_instance_id];
}

function buildDecide(p: DecideParams): string[] {
  const f: string[] = [
    "decide",
    "--claude-instance-id",
    p.claude_instance_id,
    "--decision",
    p.decision,
  ];
  if (p.reason !== undefined) f.push("--reason", p.reason);
  return f;
}

function buildResume(p: ResumeParams): string[] {
  return ["resume", "--claude-instance-id", p.claude_instance_id];
}

function buildFindMissing(): string[] {
  // The CLI exposes no flags for find-missing; timeout_ms is not a CLI flag.
  return ["find-missing"];
}

function buildExpire(p: ExpireParams): string[] {
  const f: string[] = ["expire"];
  if (p.older_than !== undefined) f.push("--older-than", p.older_than);
  return f;
}

function buildDelete(p: DeleteParams): string[] {
  const f: string[] = ["delete"];
  for (const id of p.claude_instance_id) f.push("--claude-instance-id", id);
  return f;
}

function buildMakeTemplate(p: MakeTemplateParams): string[] {
  const f: string[] = ["make-template", "--name", p.name];

  if (p.cwd !== undefined) f.push("--cwd", p.cwd);
  if (p.relay_mode !== undefined && p.relay_mode !== "")
    f.push("--relay-mode", p.relay_mode);
  if (p.overwrite === true) f.push("--overwrite");

  // Repeatable --label k=v
  if (p.label) {
    for (const lv of p.label) f.push("--label", lv);
  }

  // Repeatable --extra-env K=V
  if (p.extra_env) {
    for (const [k, v] of Object.entries(p.extra_env))
      f.push("--extra-env", `${k}=${v}`);
  }

  // Repeatable permission flags
  if (p.allow) {
    for (const a of p.allow) f.push("--allow", a);
  }
  if (p.deny) {
    for (const d of p.deny) f.push("--deny", d);
  }
  if (p.ask) {
    for (const a of p.ask) f.push("--ask", a);
  }

  // Repeatable --claude-args (each element is a separate flag invocation)
  if (p.claude_args) {
    for (const a of p.claude_args) f.push("--claude-args", a);
  }

  return f;
}

function buildList(p: ListParams): string[] {
  const f: string[] = ["list"];

  // --state takes a comma-separated list as a single flag value
  if (p.state && p.state.length > 0) f.push("--state", p.state.join(","));

  // Repeatable --label k=v
  if (p.label) {
    for (const lv of p.label) f.push("--label", lv);
  }

  if (p.parent !== undefined) f.push("--parent", p.parent);
  if (p.cwd !== undefined) f.push("--cwd", p.cwd);
  if (p.tmux_session_name !== undefined)
    f.push("--tmux-session-name", p.tmux_session_name);
  if (p.limit !== undefined && p.limit > 0)
    f.push("--limit", String(p.limit));

  return f;
}

function buildPause(p: PauseParams): string[] {
  return ["pause", "--claude-instance-id", p.claude_instance_id];
}
