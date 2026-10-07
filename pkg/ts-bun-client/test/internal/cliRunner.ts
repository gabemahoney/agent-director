/**
 * runCli — runs the agent-director CLI (CLI_PATH, set by setup.ts, else
 * <repo>/bin/agent-director) synchronously with args and env, returning its
 * trimmed stdout (the JSON result on exit 0), stderr (the JSON error envelope
 * otherwise) and exit code.
 */

import { resolve } from "path";

export interface CliRunResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export function runCli(args: string[], env: NodeJS.ProcessEnv): CliRunResult {
  const cliPath = process.env.CLI_PATH ?? resolve(import.meta.dir, "../../../../bin/agent-director");
  const proc = Bun.spawnSync({ cmd: [cliPath, ...args], stdout: "pipe", stderr: "pipe", env });
  return {
    stdout: new TextDecoder().decode(proc.stdout).trim(),
    stderr: new TextDecoder().decode(proc.stderr).trim(),
    exitCode: proc.exitCode ?? 1,
  };
}
