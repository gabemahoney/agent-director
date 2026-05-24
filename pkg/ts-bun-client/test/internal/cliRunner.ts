/**
 * cliRunner.ts — thin wrapper that spawns bin/agent-director as a subprocess.
 *
 * Uses Bun.spawnSync for synchronous subprocess execution.  The CLI binary
 * path is resolved from CLI_PATH env var (set by setup.ts) or falls back to
 * repo-root resolution.
 *
 * Contract:
 *   - On success (exit 0): result is stdout (raw JSON from the CLI).
 *   - On error (exit non-zero): result includes stderr (JSON error envelope).
 *   - Callers interpret exitCode to distinguish success vs error paths.
 */

import { resolve } from "path";

export interface CliRunResult {
  /** Raw CLI stdout, trimmed. Contains the JSON result on exit 0. */
  stdout: string;
  /** Raw CLI stderr, trimmed. Contains JSON error envelope on non-zero exit. */
  stderr: string;
  /** Process exit code. */
  exitCode: number;
}

/**
 * getCliPath resolves the path to bin/agent-director.
 * Priority: CLI_PATH env var → repo-root fallback.
 */
function getCliPath(): string {
  if (process.env.CLI_PATH) return process.env.CLI_PATH;
  // test/internal/ → test/ → pkg/ts-bun-client/ → pkg/ → repo root
  const repoRoot = resolve(import.meta.dir, "../../../..");
  return resolve(repoRoot, "bin/agent-director");
}

/**
 * runCli spawns bin/agent-director with the given args and env.
 *
 * @param args  argv after the binary name (e.g. ["spawn", "--cwd", "/tmp"])
 * @param env   subprocess environment; pass HOME + PATH at minimum
 * @returns     { stdout, stderr, exitCode }
 */
export function runCli(
  args: string[],
  env: NodeJS.ProcessEnv
): CliRunResult {
  const cliPath = getCliPath();
  const proc = Bun.spawnSync({
    cmd: [cliPath, ...args],
    stdout: "pipe",
    stderr: "pipe",
    env,
  });
  return {
    stdout: new TextDecoder().decode(proc.stdout).trim(),
    stderr: new TextDecoder().decode(proc.stderr).trim(),
    exitCode: proc.exitCode ?? 1,
  };
}
