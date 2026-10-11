/**
 * helper — shared test helpers: ts-helper seeding (runHelper; a relay-on row
 * with a fallen-back request, seedFallenBackRelayRow), Clients over the in-repo
 * CLI, process env and cwd scoping, the fake-tmux log (its send-keys calls,
 * paneSends), spawnClaude and makeTemplateClaude (the agent every spawn names),
 * and .claude.json pre-trust fixtures. setup.ts (the preload) sets
 * TS_HELPER_PATH, FAKE_TMUX_DIR and CLI_PATH.
 */

import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { Client, type ClientOptions } from "../../src/client.js";

/** The fake-tmux binary setup.ts builds, for a Client's tmuxCommand. */
export const FAKE_TMUX_BIN = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

/** This package's package.json version (what Client.version() reports, b.6o1). */
export const PKG_VERSION = (
  JSON.parse(fs.readFileSync(path.resolve(import.meta.dir, "../../package.json"), "utf8")) as { version: string }
).version;

/** The store path a test uses under its temp HOME. */
export function homeStore(home: string): string {
  return path.join(home, ".config", "agent-director", "state.db");
}

/**
 * A Client over the in-repo CLI (CLI_PATH) on storePath, created if missing,
 * running tmux as the fake; extra overrides any option (the `_cliPath` hook included).
 */
export function openClient(storePath: string, extra: Record<string, unknown> = {}): Promise<Client> {
  return Client.create({
    storePath,
    createIfMissing: true,
    tmuxCommand: FAKE_TMUX_BIN,
    _cliPath: process.env.CLI_PATH,
    ...extra,
  } as unknown as ClientOptions);
}

/** client.spawn naming the claude agent, which a spawn must name (SR-14.4); params may override it. */
export function spawnClaude(client: Client, params: Parameters<Client["spawn"]>[0]): ReturnType<Client["spawn"]> {
  return client.spawn({ agent: "claude", ...params });
}

/** client.makeTemplate naming the claude agent, which a template with agent_args must name; params may override it. */
export function makeTemplateClaude(client: Client, params: Parameters<Client["makeTemplate"]>[0]): ReturnType<Client["makeTemplate"]> {
  return client.makeTemplate({ agent: "claude", ...params });
}

/** Runs fn with process.cwd() at dir, restoring the prior cwd afterwards. */
export async function withCwd<T>(dir: string, fn: () => T | Promise<T>): Promise<T> {
  const orig = process.cwd();
  process.chdir(dir);
  try {
    return await fn();
  } finally {
    process.chdir(orig);
  }
}

/** Runs fn with process.cwd() a directory that no longer exists. */
export function withDeletedCwd<T>(fn: () => T | Promise<T>): Promise<T> {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-cwd-dead-"));
  return withCwd(dir, () => {
    fs.rmSync(dir, { recursive: true, force: true });
    return fn();
  });
}

/** What fn throws; fails the test when it returns. */
export function thrownBy(fn: () => unknown): unknown {
  try {
    fn();
  } catch (e) {
    return e;
  }
  throw new Error("expected a throw, but the call returned");
}

/** What promise rejects with; fails the test when it resolves. */
export async function rejection(promise: Promise<unknown>): Promise<unknown> {
  try {
    await promise;
  } catch (e) {
    return e;
  }
  throw new Error("expected a rejection, but the call resolved");
}

/** A ts-helper flag value: a string is `--key value`, true is a bare `--key`. */
export type HelperArgValue = string | true;

/** Runs `bin/ts-helper <subcommand> --key value…` and returns its parsed JSON stdout; throws on failure. */
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

  const flatArgs = Object.entries(args).flatMap(([key, val]) => (val === true ? [`--${key}`] : [`--${key}`, val]));

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

/** The send-keys invocations in a FAKE_TMUX_LOG file, each argv from "send-keys" on; [] when there were none. */
export function paneSends(logPath: string): string[][] {
  return fakeTmuxCalls(logPath)
    .filter((argv) => argv.includes("send-keys"))
    .map((argv) => argv.slice(argv.indexOf("send-keys")));
}

/**
 * Seeds, in homeDir's store (created if missing), relay-on row `id` in `state` on a private tmux socket under
 * homeDir, with one Bash permission request past its relay window (fallen back), its relay hook gone a minute
 * ago, and the row's own labelled session in the fake-tmux table, its pane showing "answered at tmux". Returns
 * the request's token and the pane's id.
 */
export function seedFallenBackRelayRow(homeDir: string, id: string, state: string): { token: string; paneId: string } {
  const store = homeStore(homeDir);
  runHelper("seed-spawn", { store, state, id, "relay-mode": "on", "create-store": true, socket: privateTmuxSocket(homeDir) });
  const token = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash",
    "created-ago-seconds": "172800", "hook-gone-ago-seconds": "60" })["request_token"] as string;
  const paneId = runHelper("seed-row-session", { store, id, capture: "answered at tmux\n" })["pane_id"] as string;
  return { token, paneId };
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
