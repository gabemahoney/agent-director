/**
 * Install-resolution test — confirms that `bun install` in pkg/ts-bun-client/
 * resolves the matching platform sub-package in node_modules/.
 *
 * On linux/amd64, asserts:
 *   - node_modules/@agent-director/linux-x64/ EXISTS (symlink/dir)
 *   - node_modules/@agent-director/linux-x64/bin/agent-director EXISTS
 *   - node_modules/@agent-director/darwin-arm64/ either does not exist
 *     OR exists but lacks the CLI binary (bun may install all file: optional
 *     deps regardless of os/cpu, but binaries are only present for the host
 *     platform after test/setup.ts stages them)
 *
 * v1 platform set is {linux-x64, darwin-arm64}; darwin-x64 was dropped
 * 2026-05-24.
 *
 * Skipped (not failed) on non-linux hosts with a console.log.
 *
 * The test does NOT create an isolated consumer project, because bun does not
 * transitively resolve `file:` optional dependencies from a linked package
 * into a consumer's node_modules. Instead, it validates the state of the main
 * package's own node_modules (populated by `bun install` at the package root)
 * which is the authoritative environment for running this package's code.
 */

import { test, expect, describe } from "bun:test";
import { existsSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const nmBase = resolve(pkgDir, "node_modules", "@agent-director");

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("install resolution — platform-specific optional dependencies", () => {
  test("bun install resolves linux-x64 sub-package on linux/amd64", () => {
    if (process.platform !== "linux" || process.arch !== "x64") {
      console.log(
        `install-resolution.test.ts: skipping (not linux/x64; got ${process.platform}/${process.arch})`
      );
      return;
    }

    // Re-run bun install to ensure the state is current.
    const installResult = spawnSync(
      "bun",
      ["install", "--no-progress"],
      { cwd: pkgDir, encoding: "utf8", timeout: 60_000 }
    );

    if (installResult.status !== 0) {
      throw new Error(
        `bun install failed:\nstdout: ${installResult.stdout}\nstderr: ${installResult.stderr}`
      );
    }

    // linux-x64 MUST be present in node_modules.
    const linuxX64 = resolve(nmBase, "linux-x64");
    expect(
      existsSync(linuxX64),
      `Expected ${linuxX64} to exist after bun install`
    ).toBe(true);

    // The CLI binary must exist within it (test/setup.ts stages it).
    const linuxBinary = resolve(linuxX64, "bin", "agent-director");
    expect(
      existsSync(linuxBinary),
      `Expected ${linuxBinary} to exist (test/setup.ts stages it from bin/agent-director)`
    ).toBe(true);

    // darwin-arm64: if bun installed it (it does for file: deps), it must
    // NOT have a CLI binary on a linux host — that's a cross-platform
    // protection check.
    const darwinArm64Bin = resolve(nmBase, "darwin-arm64", "bin", "agent-director");

    expect(
      existsSync(darwinArm64Bin),
      "darwin-arm64 CLI binary must not exist on linux/x64 host"
    ).toBe(false);
  });
});
