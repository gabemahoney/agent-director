/**
 * Install-resolution test — SR-3.1 consumer-side invariant.
 *
 * Asserts that a CONSUMER of the agent-director npm package only sees the
 * host-platform CLI binary in its node_modules — the wrong-platform binary
 * must be absent. This is the end-to-end enforcement of the `os`/`cpu`
 * declarations on each `@agent-director/<platform>` sub-package.
 *
 * Why a consumer fixture (not in-tree node_modules):
 *   bun ignores `os`/`cpu` on `file:`-resolved optional dependencies when
 *   installing in the workspace. The workspace node_modules will contain both
 *   platform sub-packages whenever both source dirs have a binary present
 *   (e.g. after `stage_cli_into_platforms` runs as part of the release pipeline).
 *   Asserting against the workspace node_modules therefore gives a false failure.
 *
 *   A real consumer installs the packed tarball from npm, and only adds the
 *   host-platform sub-package — this is what `verify_phase` step 2/4 in
 *   release.sh models. We mirror that fixture here so the test reflects what
 *   consumers actually experience.
 *
 * Fixture shape (mirrors release.sh verify_phase steps 1/4 and 2/4):
 *   1. stage_dir — a copy of pkg/ts-bun-client + cross-pkg catalog.json, with
 *      node_modules and skills wiped. Used as the pack source.
 *   2. `bun install --no-progress`, `bun run build`, `bun pm pack` in
 *      stage_dir/pkg/ts-bun-client/ to produce agent-director-*.tgz.
 *   3. consumer_dir — an empty consumer project. `bun add file:<tarball>` plus
 *      the HOST's matching platform sub-package via `file:`. The wrong-platform
 *      sub-package is intentionally NOT added.
 *   4. Assertions are against consumer_dir/node_modules, not workspace
 *      node_modules.
 *
 * Assertions (SR-3.1 contract — must not be weakened):
 *   1. consumer node_modules/@agent-director/<host>/bin/agent-director EXISTS.
 *   2. consumer node_modules/@agent-director/<wrong-platform>/bin/agent-director
 *      does NOT exist (strict false — not "exists but empty").
 *
 * Skipped (not failed) on non-linux/x64 and non-darwin/arm64 hosts.
 *
 * v1 platform set is {linux-x64, darwin-arm64}; darwin-x64 was dropped
 * 2026-05-24.
 */

import { test, expect, describe } from "bun:test";
import { existsSync, mkdtempSync, mkdirSync, cpSync, rmSync, writeFileSync } from "node:fs";
import { resolve, join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import * as os from "node:os";

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
// Repo root: two levels up from pkg/ts-bun-client
const repoRoot = resolve(pkgDir, "../..");

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Run a command synchronously via Bun.spawnSync; throw on non-zero exit. */
function run(cmd: string[], cwd: string, label: string): void {
  const result = Bun.spawnSync(cmd, {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
  });
  if (result.exitCode !== 0) {
    const stdout = result.stdout ? new TextDecoder().decode(result.stdout) : "";
    const stderr = result.stderr ? new TextDecoder().decode(result.stderr) : "";
    throw new Error(
      `[install-resolution] ${label} failed (exit ${result.exitCode}):\nstdout: ${stdout}\nstderr: ${stderr}`
    );
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("install resolution — platform-specific optional dependencies (consumer fixture)", () => {
  test(
    "consumer install places only the host-platform CLI binary (SR-3.1)",
    () => {
      // Determine host platform tuple
      let hostSubpkg: string;
      let wrongSubpkg: string;

      if (process.platform === "linux" && process.arch === "x64") {
        hostSubpkg = "linux-x64";
        wrongSubpkg = "darwin-arm64";
      } else if (process.platform === "darwin" && process.arch === "arm64") {
        hostSubpkg = "darwin-arm64";
        wrongSubpkg = "linux-x64";
      } else {
        console.log(
          `install-resolution.test.ts: skipping (not linux/x64 or darwin/arm64; got ${process.platform}/${process.arch})`
        );
        return;
      }

      // Create temp dirs
      const stageBase = mkdtempSync(join(os.tmpdir(), "install-res-stage-"));
      const consumerDir = mkdtempSync(join(os.tmpdir(), "install-res-consumer-"));

      try {
        // ----------------------------------------------------------------
        // Step 1/2: Stage umbrella + cross-pkg catalog, then bun pm pack
        // ----------------------------------------------------------------

        // Mirror release.sh verify_phase lines 571–581
        const stagePkgDir = join(stageBase, "pkg", "ts-bun-client");
        mkdirSync(stagePkgDir, { recursive: true });
        mkdirSync(join(stageBase, "skills"), { recursive: true });

        // cp -a pkg/ts-bun-client/. → stage
        cpSync(pkgDir, stagePkgDir, { recursive: true });

        // Copy install-agent-director skill (mirrors release.sh line 574;
        // required by prepack → stage-skill.ts)
        mkdirSync(join(stageBase, "skills"), { recursive: true });
        cpSync(
          join(repoRoot, "skills/install-agent-director"),
          join(stageBase, "skills/install-agent-director"),
          { recursive: true }
        );

        // Wipe any stray dev artifacts the cp -a dragged in (mirrors release.sh lines 580–581)
        if (existsSync(join(stagePkgDir, "node_modules"))) {
          rmSync(join(stagePkgDir, "node_modules"), { recursive: true, force: true });
        }
        if (existsSync(join(stagePkgDir, "skills"))) {
          rmSync(join(stagePkgDir, "skills"), { recursive: true, force: true });
        }

        // Mirror cross-pkg catalog.json (release.sh lines 576–577)
        // src/internal/errorMap.ts imports it via a cross-pkg relative path
        mkdirSync(join(stageBase, "pkg", "api", "errnames"), { recursive: true });
        cpSync(
          join(repoRoot, "pkg/api/errnames/catalog.json"),
          join(stageBase, "pkg/api/errnames/catalog.json")
        );

        // bun install + bun run build + bun pm pack (mirrors release.sh lines 598–601)
        run(["bun", "install", "--no-progress"], stagePkgDir, "bun install in stage");
        run(["bun", "run", "build"], stagePkgDir, "bun run build in stage");
        run(["bun", "pm", "pack"], stagePkgDir, "bun pm pack");

        // Find the produced tarball
        const tgzFiles = Bun.spawnSync(
          ["find", stagePkgDir, "-maxdepth", "1", "-name", "agent-director-*.tgz"],
          { stdout: "pipe", stderr: "pipe" }
        );
        const tgzList = new TextDecoder().decode(tgzFiles.stdout).trim().split("\n").filter(Boolean);
        if (tgzList.length === 0) {
          throw new Error("[install-resolution] bun pm pack produced no tarball");
        }
        const tgz = tgzList[0];

        // ----------------------------------------------------------------
        // Step 2/2: Consumer fixture — bun add tarball + host platform only
        // ----------------------------------------------------------------

        // Write a minimal consumer package.json (no trustedDependencies needed
        // since we're not exercising postinstall — only binary presence)
        writeFileSync(
          join(consumerDir, "package.json"),
          JSON.stringify({ name: "install-res-consumer", version: "0.0.0" }, null, 2)
        );

        // Add umbrella tarball + host platform sub-package — NOT the wrong platform
        const hostPlatformPath = join(stagePkgDir, "platforms", hostSubpkg);
        run(
          [
            "bun", "add",
            `file:${tgz}`,
            `@agent-director/${hostSubpkg}@file:${hostPlatformPath}`,
          ],
          consumerDir,
          "bun add tarball + host platform sub-package"
        );

        // ----------------------------------------------------------------
        // Assertions (SR-3.1 contract)
        // ----------------------------------------------------------------

        const consumerNm = join(consumerDir, "node_modules", "@agent-director");

        // 1. Host platform CLI binary MUST exist
        const hostBin = join(consumerNm, hostSubpkg, "bin", "agent-director");
        expect(
          existsSync(hostBin),
          `Host platform (${hostSubpkg}) CLI binary must exist in consumer node_modules: ${hostBin}`
        ).toBe(true);

        // 2. Wrong-platform CLI binary must NOT exist (strict — no "exists but empty" tolerance)
        const wrongBin = join(consumerNm, wrongSubpkg, "bin", "agent-director");
        expect(
          existsSync(wrongBin),
          `${wrongSubpkg} CLI binary must not exist on ${process.platform}/${process.arch} host`
        ).toBe(false);
      } finally {
        // Cleanup both temp dirs
        try { rmSync(stageBase, { recursive: true, force: true }); } catch { /* ignore */ }
        try { rmSync(consumerDir, { recursive: true, force: true }); } catch { /* ignore */ }
      }
    },
    90_000
  );
});
