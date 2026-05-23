/**
 * Install-resolution test — confirms that `bun install` in pkg/ts-bun-client/
 * resolves the matching platform sub-package in node_modules/.
 *
 * On linux/amd64, asserts:
 *   - node_modules/@CHANGEME-H3/agent-director-linux-x64/ EXISTS (symlink/dir)
 *   - node_modules/@CHANGEME-H3/agent-director-linux-x64/libagent_director.so EXISTS
 *   - node_modules/@CHANGEME-H3/agent-director-darwin-x64/ either does not exist
 *     OR exists but lacks the .dylib binary (bun may install all file: optional
 *     deps regardless of os/cpu, but binaries are only present for the host platform)
 *   - same for darwin-arm64
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
const nmBase = resolve(pkgDir, "node_modules", "@CHANGEME-H3");

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
    const linuxX64 = resolve(nmBase, "agent-director-linux-x64");
    expect(
      existsSync(linuxX64),
      `Expected ${linuxX64} to exist after bun install`
    ).toBe(true);

    // The .so binary must exist within it (we copy it in prepare-platforms).
    const linuxBinary = resolve(linuxX64, "libagent_director.so");
    expect(
      existsSync(linuxBinary),
      `Expected ${linuxBinary} to exist (run bun run prepare-platforms first)`
    ).toBe(true);

    // darwin packages: if bun installed them (it does for file: deps), they
    // must NOT have a .dylib binary — that's a cross-platform protection check.
    const darwinX64Dylib = resolve(nmBase, "agent-director-darwin-x64", "libagent_director.dylib");
    const darwinArm64Dylib = resolve(nmBase, "agent-director-darwin-arm64", "libagent_director.dylib");

    expect(
      existsSync(darwinX64Dylib),
      "darwin-x64 dylib must not exist on linux/x64 host"
    ).toBe(false);

    expect(
      existsSync(darwinArm64Dylib),
      "darwin-arm64 dylib must not exist on linux/x64 host"
    ).toBe(false);
  });
});
