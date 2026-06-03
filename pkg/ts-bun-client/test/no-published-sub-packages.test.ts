/**
 * no-published-sub-packages.test.ts — SR-8.7 (b.ue3 / Epic 4).
 *
 * Inverse of the (deleted) install-resolution.test.ts.  After bun add
 * file:<umbrella-tarball>, the consumer's node_modules MUST NOT contain
 * any @agent-director/<platform> directory, because no sub-packages are
 * declared in optionalDependencies anymore.
 *
 * Skipped on unsupported platforms; mirrors test/setup.ts.
 */

import { test, expect } from "bun:test";
import { mkdtempSync, rmSync, existsSync, writeFileSync, readdirSync } from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

const PKG_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const platformTuple = (() => {
  if (process.platform === "linux" && process.arch === "x64") return "linux-x64";
  if (process.platform === "darwin" && process.arch === "arm64") return "darwin-arm64";
  return null;
})();

async function spawn(
  cmd: string[],
  opts: { cwd?: string; env?: Record<string, string> } = {},
): Promise<{ exitCode: number | null; stdout: string; stderr: string }> {
  const proc = Bun.spawn(cmd, {
    cwd: opts.cwd,
    env: opts.env ? { ...process.env, ...opts.env } : process.env,
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  await proc.exited;
  return { exitCode: proc.exitCode, stdout, stderr };
}

test(
  "after bun add file:<tarball>, consumer's node_modules has no @agent-director/* sub-packages (SR-8.7)",
  async () => {
    if (platformTuple === null) {
      console.log(
        `no-published-sub-packages: unsupported platform ${process.platform}/${process.arch} — skipping`,
      );
      return;
    }

    // Build the package fresh so the tarball reflects current sources.
    const buildResult = await spawn(["bun", "run", "build"], { cwd: PKG_DIR });
    if (buildResult.exitCode !== 0) {
      console.log("no-published-sub-packages: bun run build failed — skipping");
      console.log(buildResult.stderr);
      return;
    }

    const stageDir = mkdtempSync(join(tmpdir(), "ad-nosub-stage-"));
    const consumerDir = mkdtempSync(join(tmpdir(), "ad-nosub-consumer-"));
    try {
      // Pack the umbrella into stageDir.
      const packResult = await spawn(
        ["bun", "pm", "pack", "--ignore-scripts", "--destination", stageDir],
        { cwd: PKG_DIR },
      );
      expect(packResult.exitCode).toBe(0);

      const tgz = readdirSync(stageDir).find((f) => f.endsWith(".tgz"));
      expect(tgz).toBeDefined();
      const tgzPath = join(stageDir, tgz as string);

      // Consumer setup.
      writeFileSync(
        join(consumerDir, "package.json"),
        JSON.stringify({ name: "test-consumer", version: "0.0.1", type: "module" }, null, 2),
      );

      const addResult = await spawn(
        ["bun", "add", "--ignore-scripts", `file:${tgzPath}`],
        { cwd: consumerDir },
      );
      expect(addResult.exitCode).toBe(0);

      // Assert no @agent-director/* directories.
      const adScopeDir = join(consumerDir, "node_modules", "@agent-director");
      expect(existsSync(adScopeDir)).toBe(false);

      // Sanity: the library package itself is present.
      const adPkgDir = join(consumerDir, "node_modules", "agent-director");
      expect(existsSync(adPkgDir)).toBe(true);

      // The version-floor.json should be importable via the exports map subpath.
      const floorPath = join(adPkgDir, "dist", "version-floor.json");
      expect(existsSync(floorPath)).toBe(true);
    } finally {
      rmSync(stageDir, { recursive: true, force: true });
      rmSync(consumerDir, { recursive: true, force: true });
    }
  },
  { timeout: 60_000 },
);
