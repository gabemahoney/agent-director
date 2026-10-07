/**
 * stagedScript — runs a copy of a pkg/ts-bun-client/scripts/ file in a staged
 * <root>/pkg/ts-bun-client/ tree, so its ../ paths resolve inside the stage.
 * Stages live under the OS temp dir, never the repo tree: a versioned
 * package.json there trips a sibling run's source-of-truth scan (b.9qj).
 */
import { cpSync, mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const PKG_DIR = resolve(import.meta.dir, "../..");
const roots: string[] = [];

export interface Staged {
  root: string;
  pkgDir: string;
  script: string;
}

/** Stages scripts/<script> plus files (paths relative to the staged pkg/ts-bun-client/). */
export function stageScript(script: string, files: Record<string, string>): Staged {
  // realpath: the copied script reports paths from its resolved location.
  const root = realpathSync(mkdtempSync(join(tmpdir(), "ad-stage-")));
  roots.push(root);
  const pkgDir = join(root, "pkg", "ts-bun-client");
  for (const [rel, content] of Object.entries(files)) {
    mkdirSync(dirname(join(pkgDir, rel)), { recursive: true });
    writeFileSync(join(pkgDir, rel), content);
  }
  const scriptPath = join(pkgDir, "scripts", script);
  mkdirSync(dirname(scriptPath), { recursive: true });
  cpSync(join(PKG_DIR, "scripts", script), scriptPath);
  return { root, pkgDir, script: scriptPath };
}

/** Removes every stage made so far (call from afterEach). */
export function removeStaged(): void {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
}

/** `bun run <script> ...args` with env merged over process.env. */
export function runScript(script: string, args: string[], env: Record<string, string> = {}) {
  const r = Bun.spawnSync(["bun", "run", script, ...args], { env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe" });
  return { exitCode: r.exitCode, stdout: r.stdout.toString(), stderr: r.stderr.toString() };
}

/** package.json for the umbrella package at version. */
export const umbrellaPkg = (version: string) => JSON.stringify({ name: "agent-director", version }, null, 2) + "\n";
