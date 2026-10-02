/**
 * runHelper — thin Bun.spawnSync wrapper around bin/ts-helper.
 *
 * Builds argv from the subcommand plus `--key value` pairs (or `--key` alone
 * for boolean flags), syncs stdout, parses the JSON result, and throws on
 * non-zero exit.
 *
 * REQUIRES: process.env.TS_HELPER_PATH must be set by the preload (setup.ts).
 */

import * as fs from "fs";
import * as path from "path";

/**
 * Args value type:
 *   string  → --key value
 *   true    → --key  (standalone boolean flag, no value)
 */
export type HelperArgValue = string | true;

/**
 * runHelper shells out to bin/ts-helper and returns the parsed JSON result.
 *
 * @param subcommand - ts-helper subcommand (e.g. "seed-spawn", "seed-empty-store").
 * @param args       - flag key→value map. Use `true` for boolean flags (e.g.
 *                     `{"create-store": true}`).
 * @returns Parsed JSON object from stdout.
 * @throws  If TS_HELPER_PATH is unset, exit code is non-zero, or stdout is not
 *          valid JSON. The thrown Error includes the captured stderr.
 */
export function runHelper(
  subcommand: string,
  args: Record<string, HelperArgValue> = {}
): Record<string, unknown> {
  const helperPath = process.env.TS_HELPER_PATH;
  if (!helperPath) {
    throw new Error(
      "TS_HELPER_PATH is not set; ensure pkg/ts-bun-client/test/setup.ts preload ran"
    );
  }

  // Build flat argv: --key value pairs (or --key for boolean flags).
  const flatArgs: string[] = [];
  for (const [key, val] of Object.entries(args)) {
    if (val === true) {
      flatArgs.push(`--${key}`);
    } else {
      flatArgs.push(`--${key}`, val);
    }
  }

  const proc = Bun.spawnSync({
    cmd: [helperPath, subcommand, ...flatArgs],
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env },
  });

  const stderr = new TextDecoder().decode(proc.stderr);

  if (proc.exitCode !== 0) {
    throw new Error(
      `ts-helper ${subcommand} failed (exit ${proc.exitCode ?? "null"}): ${stderr.trim()}`
    );
  }

  const stdout = new TextDecoder().decode(proc.stdout).trim();
  try {
    return JSON.parse(stdout) as Record<string, unknown>;
  } catch {
    throw new Error(
      `ts-helper ${subcommand} returned non-JSON stdout: ${stdout}; stderr: ${stderr.trim()}`
    );
  }
}

/**
 * privateTmuxSocket makes `<dir>/tmux` (mode 0700) and returns the socket path
 * `<dir>/tmux/default` for `seed-spawn --socket`: a resume launches on the
 * row's recorded socket and refuses one whose directory is missing.
 */
export function privateTmuxSocket(dir: string): string {
  const sockDir = path.join(dir, "tmux");
  fs.mkdirSync(sockDir, { recursive: true, mode: 0o700 });
  return path.join(sockDir, "default");
}

/** The argv (argv[0] included) of each fake-tmux invocation in a FAKE_TMUX_LOG file; [] when nothing was logged. */
export function fakeTmuxCalls(logPath: string): string[][] {
  if (!fs.existsSync(logPath)) return [];
  return fs
    .readFileSync(logPath, "utf8")
    .split("---\n")
    .filter((rec) => rec !== "")
    .map((rec) => rec.split("\n").slice(0, -1));
}

/**
 * Runs fn with each process.env variable set (undefined unsets it) and puts
 * the prior values back afterwards; a Client's CLI inherits process.env per call.
 */
export async function withProcessEnv<T>(
  vars: Record<string, string | undefined>,
  fn: () => T | Promise<T>
): Promise<T> {
  const prior: Record<string, string | undefined> = {};
  for (const [k, v] of Object.entries(vars)) {
    prior[k] = process.env[k];
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  try {
    return await fn();
  } finally {
    for (const [k, v] of Object.entries(prior)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
}

/** A temp HOME's .claude.json as planted before a launch: no trust entry for any folder. */
export const CLAUDE_JSON = '{"projects": {}}\n';

/** projects[cwd].hasTrustDialogAccepted in claudeJsonPath (undefined when absent). */
export function trustEntry(claudeJsonPath: string, cwd: string): unknown {
  return JSON.parse(fs.readFileSync(claudeJsonPath, "utf8")).projects?.[cwd]?.hasTrustDialogAccepted;
}

/**
 * Seeds the outer agent's row (when the tests run inside an agent, whose
 * AGENT_DIRECTOR_INSTANCE_ID a launch records as parent_id) so the FK holds.
 */
export function seedOuterParent(storePath: string): void {
  const outer = process.env.AGENT_DIRECTOR_INSTANCE_ID;
  if (!outer) return;
  runHelper("seed-spawn", { store: storePath, id: outer, state: "working", "create-store": true });
}
