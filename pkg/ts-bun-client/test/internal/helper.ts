/**
 * runHelper — thin Bun.spawnSync wrapper around bin/ts-helper.
 *
 * Builds argv from the subcommand plus `--key value` pairs (or `--key` alone
 * for boolean flags), syncs stdout, parses the JSON result, and throws on
 * non-zero exit.
 *
 * REQUIRES: process.env.TS_HELPER_PATH must be set by the preload (setup.ts).
 */

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
