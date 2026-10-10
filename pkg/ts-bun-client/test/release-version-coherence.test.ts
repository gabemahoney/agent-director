/**
 * release-version-coherence.test.ts — end-to-end release version flow (b.xsh
 * Epic 5): a staged tree stamped 9.9.9 by version-bump.ts passes
 * check-version-coherence, packs, installs into a consumer, and
 * client.version() there returns exactly "9.9.9" with the version not inlined
 * into dist/. The install carries no @agent-director/* sub-package and does
 * ship dist/version-floor.json (SR-8.7). The live tree is left unchanged.
 */

import { test, expect } from "bun:test";
import { mkdtempSync, rmSync, readFileSync, readdirSync, writeFileSync, mkdirSync, cpSync, chmodSync, existsSync } from "node:fs";
import { createHash } from "node:crypto";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";

const TARGET_VERSION = "9.9.9";
const PKG_DIR = resolve(import.meta.dir, "..");
const REPO_ROOT = resolve(PKG_DIR, "../..");

const sha256 = (p: string) => createHash("sha256").update(readFileSync(p)).digest("hex");

/** Runs cmd in cwd (env merged over process.env) and expects exit 0; returns stdout. */
async function run(cmd: string[], cwd: string, env: Record<string, string> = {}): Promise<string> {
  const proc = Bun.spawn(cmd, { cwd, env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe", stdin: "ignore" });
  const [stdout, stderr] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text()]);
  await proc.exited;
  expect(proc.exitCode, `${cmd.join(" ")} failed:\n${stderr}`).toBe(0);
  return stdout;
}

test(
  "release-version-coherence: staged tree → pack → install → client.version() === 9.9.9",
  async () => {
    const livePkg = join(PKG_DIR, "package.json");
    const liveDist = join(PKG_DIR, "dist", "index.js");
    const before = { pkg: readFileSync(livePkg, "utf8"), dist: existsSync(liveDist) ? sha256(liveDist) : null };

    const stageDir = mkdtempSync(join(tmpdir(), "ad-rvc-stage-"));
    const consumerDir = mkdtempSync(join(tmpdir(), "ad-rvc-consumer-"));
    const home = mkdtempSync(join(tmpdir(), "ad-rvc-home-"));
    try {
      // Stage the package (plus the catalog the bundle inlines), without dev artifacts.
      const stagePkg = join(stageDir, "pkg", "ts-bun-client");
      mkdirSync(stagePkg, { recursive: true });
      cpSync(PKG_DIR, stagePkg, { recursive: true });
      mkdirSync(join(stageDir, "pkg", "api", "errnames"), { recursive: true });
      cpSync(join(REPO_ROOT, "pkg/api/errnames/catalog.json"), join(stageDir, "pkg/api/errnames/catalog.json"));
      rmSync(join(stagePkg, "node_modules"), { recursive: true, force: true });
      rmSync(join(stagePkg, "skills"), { recursive: true, force: true });

      // The /release flow, run on the staged copies.
      await run(["bun", "run", "scripts/version-bump.ts", "--version", TARGET_VERSION, "--target", "umbrella-version"], stagePkg);
      await run(["bun", "run", "scripts/check-version-coherence.ts", "--scope", "verify", "--expected-version", TARGET_VERSION], stagePkg);
      await run(["bun", "install", "--no-progress"], stagePkg);
      await run(["bun", "run", "build"], stagePkg);
      await run(["bun", "pm", "pack", "--ignore-scripts"], stagePkg);
      const tgz = readdirSync(stagePkg).find((f) => f.startsWith("agent-director-") && f.endsWith(".tgz"));
      expect(tgz).toBeDefined();

      // A consumer in a sandboxed HOME, whose discovery finds the dev CLI at the standard install path.
      writeFileSync(join(consumerDir, "package.json"), JSON.stringify({ name: "ad-rvc-consumer", version: "0.0.1", type: "module" }));
      await run(["bun", "add", `file:${join(stagePkg, tgz!)}`], consumerDir, { HOME: home });
      mkdirSync(join(home, ".agent-director", "bin"), { recursive: true });
      cpSync(process.env.CLI_PATH!, join(home, ".agent-director", "bin", "agent-director"));
      chmodSync(join(home, ".agent-director", "bin", "agent-director"), 0o755);
      const store = JSON.stringify(join(consumerDir, "state.db"));
      writeFileSync(
        join(consumerDir, "consumer.ts"),
        `import { Client } from "agent-director";\n` +
          `const c = await Client.create({ storePath: ${store} });\n` +
          `console.log(JSON.stringify(await c.version({})));\nc.close();\n`
      );
      const out = await run(["bun", "run", "consumer.ts"], consumerDir, { HOME: home });
      const line = out.split("\n").map((l) => l.trim()).find((l) => l.startsWith("{"));
      expect((JSON.parse(line!) as { version: string }).version).toBe(TARGET_VERSION);

      const installed = join(consumerDir, "node_modules", "agent-director");
      const distSrc = readFileSync(join(installed, "dist", "index.js"), "utf8");
      expect(distSrc).not.toContain(`"${TARGET_VERSION}"`);
      expect(distSrc).not.toContain(`'${TARGET_VERSION}'`);
      expect(existsSync(join(installed, "dist", "version-floor.json"))).toBe(true);
      expect(existsSync(join(consumerDir, "node_modules", "@agent-director"))).toBe(false);
    } finally {
      for (const d of [stageDir, consumerDir, home]) rmSync(d, { recursive: true, force: true });
    }

    expect(readFileSync(livePkg, "utf8")).toBe(before.pkg);
    if (before.dist !== null) expect(sha256(liveDist)).toBe(before.dist);
  },
  120_000
);
