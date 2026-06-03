/**
 * spawn-hook-stability.test.ts — SR-8.8 / SR-1.8 (b.ue3 / Epic 5).
 *
 * Integration tests for the spawn-side hook-writing pipeline:
 *
 *  - Hook commands are absolute paths.
 *  - No PATH-relative invocation (no bare token "agent-director", no
 *    shell-variable references).
 *  - In-place re-install at the standard install path survives — captured
 *    hook command paths remain callable after the binary is overwritten.
 *  - Symlink resolution — a symlink at the standard install path resolves
 *    to the real binary path in the captured hook command.
 *
 * Runs only when CLI_PATH is set (setup.ts preload ran).  Spawn is driven
 * directly via the CLI (subprocess) — the spawn verb writes a `claude
 * --settings <inline-json>` invocation; FAKE_TMUX_LOG captures the argv so
 * we can extract the JSON without launching real Claude.
 */

import { test, expect, describe } from "bun:test";
import {
  mkdtempSync,
  mkdirSync,
  existsSync,
  rmSync,
  readFileSync,
  writeFileSync,
  chmodSync,
  symlinkSync,
  copyFileSync,
  realpathSync,
} from "node:fs";
import { join, dirname } from "node:path";
import { tmpdir } from "node:os";

const cliPath = process.env.CLI_PATH;
const fakeTmuxDir = process.env.FAKE_TMUX_DIR;

interface SettingsJson {
  hooks?: Record<string, Array<{ hooks?: Array<{ command?: string }> }>>;
}

function runSpawn(opts: {
  cwd: string;
  cliBinary: string;
  homeDir: string;
  fakeTmuxLog: string;
}): { exitCode: number | null; stdout: string; stderr: string } {
  // PATH puts fake-tmux first so the CLI's `tmux` invocation hits the stub.
  const path = `${fakeTmuxDir}:${process.env.PATH ?? ""}`;
  // Scrub AGENT_DIRECTOR_* env vars so the spawn doesn't try to record
  // this test's outer Claude session as a parent (would fail FK).
  const cleanEnv: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (typeof v !== "string") continue;
    if (k.startsWith("AGENT_DIRECTOR_") || k.startsWith("AD_")) continue;
    cleanEnv[k] = v;
  }
  cleanEnv.HOME = opts.homeDir;
  cleanEnv.PATH = path;
  cleanEnv.FAKE_TMUX_LOG = opts.fakeTmuxLog;
  const proc = Bun.spawnSync(
    [opts.cliBinary, "spawn", "--cwd", opts.cwd],
    { env: cleanEnv },
  );
  return {
    exitCode: proc.exitCode,
    stdout: new TextDecoder().decode(proc.stdout),
    stderr: new TextDecoder().decode(proc.stderr),
  };
}

/**
 * Parse the JSON value that followed `--settings` in the fake-tmux argv log.
 * The log records one argv element per line, with "---" separators between
 * invocations.  Look for "--settings" followed by the JSON string.
 */
function extractSettingsJson(logPath: string): SettingsJson | null {
  const raw = readFileSync(logPath, "utf8");
  const lines = raw.split("\n");
  for (let i = 0; i < lines.length - 1; i++) {
    if (lines[i] === "--settings") {
      const jsonLine = lines[i + 1];
      try {
        return JSON.parse(jsonLine) as SettingsJson;
      } catch {
        return null;
      }
    }
  }
  return null;
}

function collectHookCommands(settings: SettingsJson): string[] {
  const out: string[] = [];
  for (const evt of Object.values(settings.hooks ?? {})) {
    for (const entry of evt) {
      for (const hook of entry.hooks ?? []) {
        if (typeof hook.command === "string") out.push(hook.command);
      }
    }
  }
  return out;
}

function setupStandardInstall(homeDir: string, cliBin: string): string {
  const adBinDir = join(homeDir, ".agent-director", "bin");
  mkdirSync(adBinDir, { recursive: true });
  const stdPath = join(adBinDir, "agent-director");
  copyFileSync(cliBin, stdPath);
  chmodSync(stdPath, 0o755);
  return stdPath;
}

describe("spawn-side hook stability (SR-1.8 / SR-8.8)", () => {
  const skip = !cliPath || !existsSync(cliPath) || !fakeTmuxDir;

  test(
    "hook commands are absolute paths; no PATH-relative or shell-var forms",
    () => {
      if (skip) {
        console.log("spawn-hook-stability: CLI_PATH or FAKE_TMUX_DIR not set — skipping");
        return;
      }
      const homeDir = mkdtempSync(join(tmpdir(), "ad-hook-stab-home-"));
      const fakeTmuxLog = join(homeDir, "fake-tmux.log");
      const projectCwd = mkdtempSync(join(tmpdir(), "ad-hook-stab-cwd-"));
      try {
        const stdPath = setupStandardInstall(homeDir, cliPath!);
        const result = runSpawn({
          cwd: projectCwd,
          cliBinary: stdPath,
          homeDir,
          fakeTmuxLog,
        });
        if (result.exitCode !== 0) {
          console.error("spawn stderr:", result.stderr);
        }
        expect(result.exitCode).toBe(0);

        const settings = extractSettingsJson(fakeTmuxLog);
        expect(settings).not.toBeNull();
        const commands = collectHookCommands(settings!);
        expect(commands.length).toBeGreaterThan(0);

        const expectedAbs = realpathSync(stdPath);
        for (const cmd of commands) {
          // Strip surrounding quotes if quoteIfWhitespace wrapped the path.
          const naked = cmd.startsWith('"') ? cmd : cmd;
          expect(naked.startsWith("/") || naked.startsWith('"/')).toBe(true);
          expect(naked).not.toMatch(/^agent-director\b/);
          expect(naked).not.toContain("$0");
          expect(naked).not.toContain("${0}");
          expect(naked).not.toContain("$(command -v");
          // Every command should reference the resolved binary path.
          expect(naked.includes(expectedAbs)).toBe(true);
        }
      } finally {
        rmSync(homeDir, { recursive: true, force: true });
        rmSync(projectCwd, { recursive: true, force: true });
      }
    },
    { timeout: 30_000 },
  );

  test(
    "in-place re-install survives: hook path remains callable after overwrite",
    () => {
      if (skip) {
        console.log("spawn-hook-stability: skipping — CLI_PATH or FAKE_TMUX_DIR not set");
        return;
      }
      const homeDir = mkdtempSync(join(tmpdir(), "ad-hook-stab-home-"));
      const fakeTmuxLog = join(homeDir, "fake-tmux.log");
      const projectCwd = mkdtempSync(join(tmpdir(), "ad-hook-stab-cwd-"));
      try {
        const stdPath = setupStandardInstall(homeDir, cliPath!);
        // First spawn.
        const r1 = runSpawn({ cwd: projectCwd, cliBinary: stdPath, homeDir, fakeTmuxLog });
        expect(r1.exitCode).toBe(0);
        const settings = extractSettingsJson(fakeTmuxLog);
        expect(settings).not.toBeNull();
        const commands = collectHookCommands(settings!);
        expect(commands.length).toBeGreaterThan(0);

        // Simulate install.sh re-running: overwrite the binary at the same path.
        copyFileSync(cliPath!, stdPath);
        chmodSync(stdPath, 0o755);

        // Pull the binary path token (first space-separated token of cmd[0]).
        // commands[i] is "<path> hook"; binary path = trimmed up to last space.
        const sample = commands[0]!;
        const stripped = sample.replace(/^"|"$/g, "");
        const sp = stripped.lastIndexOf(" ");
        const hookBinPath = stripped.slice(0, sp).replace(/^"|"$/g, "");
        expect(existsSync(hookBinPath)).toBe(true);

        // Confirm the binary at that path is callable.
        const probe = Bun.spawnSync([hookBinPath, "version", "--json"]);
        expect(probe.exitCode).toBe(0);
      } finally {
        rmSync(homeDir, { recursive: true, force: true });
        rmSync(projectCwd, { recursive: true, force: true });
      }
    },
    { timeout: 30_000 },
  );

  test(
    "symlink resolution: hook command references the resolved real path",
    () => {
      if (skip) {
        console.log("spawn-hook-stability: skipping — CLI_PATH or FAKE_TMUX_DIR not set");
        return;
      }
      const homeDir = mkdtempSync(join(tmpdir(), "ad-hook-stab-home-"));
      const fakeTmuxLog = join(homeDir, "fake-tmux.log");
      const projectCwd = mkdtempSync(join(tmpdir(), "ad-hook-stab-cwd-"));
      const realDir = mkdtempSync(join(tmpdir(), "ad-hook-stab-real-"));
      try {
        // Place the real binary outside the standard install path.
        const realBin = join(realDir, "real-agent-director");
        copyFileSync(cliPath!, realBin);
        chmodSync(realBin, 0o755);
        // Create the standard install dir but populate it with a symlink to
        // the real binary.
        const adBinDir = join(homeDir, ".agent-director", "bin");
        mkdirSync(adBinDir, { recursive: true });
        const symPath = join(adBinDir, "agent-director");
        symlinkSync(realBin, symPath);

        const result = runSpawn({
          cwd: projectCwd,
          cliBinary: symPath,
          homeDir,
          fakeTmuxLog,
        });
        if (result.exitCode !== 0) {
          console.error("spawn stderr:", result.stderr);
        }
        expect(result.exitCode).toBe(0);

        const settings = extractSettingsJson(fakeTmuxLog);
        expect(settings).not.toBeNull();
        const commands = collectHookCommands(settings!);
        const resolvedReal = realpathSync(symPath);

        // Help-hook entries use the standard install path (sym-link path
        // by design — they are written for the spawned session's lifetime
        // and reference $HOME/.agent-director/bin/agent-director).  The
        // regular hook commands use the running-binary's resolved path.
        // Filter to commands that reference agent-director (excluding
        // the help-hook variant).
        const regularHookCommands = commands.filter(
          (c) => c.endsWith(" hook") || c.endsWith(' hook"'),
        );
        expect(regularHookCommands.length).toBeGreaterThan(0);

        for (const cmd of regularHookCommands) {
          const naked = cmd.replace(/^"|"$/g, "");
          // The command should reference the resolved real path, not the symlink.
          expect(naked.includes(resolvedReal)).toBe(true);
          expect(naked.includes(symPath)).toBe(false);
        }
      } finally {
        rmSync(homeDir, { recursive: true, force: true });
        rmSync(projectCwd, { recursive: true, force: true });
        rmSync(realDir, { recursive: true, force: true });
      }
    },
    { timeout: 30_000 },
  );
});
