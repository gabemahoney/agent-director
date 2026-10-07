/**
 * spawn-hook-stability.test.ts — SR-1.8 / SR-8.8 (b.ue3 Epic 5), driving the
 * built CLI's spawn directly: every exec-form hook (SR-22.9: args ["hook"])
 * names the running binary by its resolved absolute path, never PATH-relative
 * or via a shell variable, so the path survives an in-place re-install, and a
 * symlinked install resolves to the real binary.
 */

import { test, expect, afterEach } from "bun:test";
import { mkdtempSync, mkdirSync, rmSync, chmodSync, symlinkSync, copyFileSync, realpathSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fakeTmuxCalls } from "./internal/helper.js";

const cliPath = process.env.CLI_PATH!;
const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

function tmp(prefix: string): string {
  const d = mkdtempSync(join(tmpdir(), prefix));
  dirs.push(d);
  return d;
}

/**
 * Runs `<binary> spawn` under home with fake tmux first on PATH and no
 * AGENT_DIRECTOR_* / AD_* variables (no parent row), and returns the `command`
 * of each exec-form hook in the `--settings` JSON the fake tmux recorded.
 */
function spawnHookCommands(binary: string, home: string): string[] {
  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (v !== undefined && !k.startsWith("AGENT_DIRECTOR_") && !k.startsWith("AD_")) env[k] = v;
  }
  const log = join(home, "fake-tmux.log");
  Object.assign(env, { HOME: home, PATH: `${process.env.FAKE_TMUX_DIR}:${process.env.PATH ?? ""}`, FAKE_TMUX_LOG: log });
  const proc = Bun.spawnSync([binary, "spawn", "--cwd", tmp("ad-hook-stab-cwd-")], { env });
  expect(proc.exitCode, new TextDecoder().decode(proc.stderr)).toBe(0);

  const argv = fakeTmuxCalls(log).find((a) => a.includes("--settings"))!;
  const settings = JSON.parse(argv[argv.indexOf("--settings") + 1]!) as {
    hooks: Record<string, Array<{ hooks?: Array<{ command?: string; args?: unknown }> }>>;
  };
  const commands = Object.values(settings.hooks)
    .flat()
    .flatMap((entry) => entry.hooks ?? [])
    .filter((h) => JSON.stringify(h.args) === '["hook"]')
    .map((h) => h.command!);
  expect(commands.length).toBeGreaterThan(0);
  return commands;
}

test("hooks name the installed binary's absolute path, which survives an in-place re-install", () => {
  const home = tmp("ad-hook-stab-home-");
  const binDir = join(home, ".agent-director", "bin");
  mkdirSync(binDir, { recursive: true });
  const installed = join(binDir, "agent-director");
  copyFileSync(cliPath, installed);
  chmodSync(installed, 0o755);

  const commands = spawnHookCommands(installed, home);
  for (const cmd of commands) {
    expect(cmd).toBe(realpathSync(installed));
    expect(cmd).not.toMatch(/^agent-director\b|\$0|\$\{0\}|\$\(command -v/);
  }

  // install.sh re-running overwrites the binary at the same path; the hook path still runs.
  copyFileSync(cliPath, installed);
  chmodSync(installed, 0o755);
  expect(Bun.spawnSync([commands[0]!, "version", "--json"]).exitCode).toBe(0);
}, 30_000);

test("a symlinked install: hooks name the resolved real binary, not the symlink", () => {
  const home = tmp("ad-hook-stab-home-");
  const real = join(tmp("ad-hook-stab-real-"), "real-agent-director");
  copyFileSync(cliPath, real);
  chmodSync(real, 0o755);
  const binDir = join(home, ".agent-director", "bin");
  mkdirSync(binDir, { recursive: true });
  const link = join(binDir, "agent-director");
  symlinkSync(real, link);

  for (const cmd of spawnHookCommands(link, home)) expect(cmd).toBe(realpathSync(real));
}, 30_000);
