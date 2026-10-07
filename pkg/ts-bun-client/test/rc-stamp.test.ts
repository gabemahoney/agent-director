/**
 * rc-stamp.test.ts — the build's version stamp as the client sees it: a dev
 * build stamps the sentinel byte-exact (SR-8.3a / SR-2.6), and a
 * release-candidate build (package version + "-rc.1") is accepted and ordered
 * between the floor and its release (AC-REL-03 / SR-21.5).
 */

import { test, expect } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { MIN_BINARY_VERSION } from "../src/internal/constants.js";
import { compareVersions, parseVersion } from "../src/internal/semver.js";
import { PKG_VERSION, openClient } from "./internal/helper.js";
import { underSeedsLock } from "./internal/seedsLock.js";
import { withTempHome } from "./internal/tempHome.js";

const repoRoot = resolve(import.meta.dir, "../../..");
const rc = `${PKG_VERSION}-rc.1`;

// `make release-binaries` artifact suffix for this host; null skips the build test.
const releaseTarget = (() => {
  if (process.platform === "linux" && process.arch === "x64") return "linux-amd64";
  if (process.platform === "linux" && process.arch === "arm64") return "linux-arm64";
  if (process.platform === "darwin" && process.arch === "arm64") return "darwin-arm64";
  return null;
})();

test("the dev build setup.ts makes stamps version '0.0.0-dev' byte-exact", () => {
  const proc = Bun.spawnSync([process.env.CLI_PATH!, "version", "--json"]);
  expect(proc.exitCode).toBe(0);
  expect((JSON.parse(new TextDecoder().decode(proc.stdout)) as { version: unknown }).version).toBe("0.0.0-dev");
});

test.skipIf(releaseTarget === null)(
  "Client.create() accepts the RC-stamped binary with binaryVersion byte-exact",
  async () => {
    const dist = mkdtempSync(join(tmpdir(), "ad-rc-stamp-"));
    try {
      const build = Bun.spawnSync(underSeedsLock(["make", "-C", repoRoot, "release-binaries"]), {
        env: { ...process.env, AGENT_DIRECTOR_BUILD_VERSION: rc, RELEASE_DIST_DIR: dist },
        stdout: "pipe",
        stderr: "pipe",
      });
      if (build.exitCode !== 0) {
        throw new Error(`make release-binaries failed (exit ${build.exitCode}):\n${build.stdout}\n${build.stderr}`);
      }
      await withTempHome(async (home) => {
        using client = await openClient(join(home, "state.db"), { _cliPath: join(dist, `agent-director-${releaseTarget}`) });
        expect(client.binaryVersion).toBe(rc);
      });
    } finally {
      rmSync(dist, { recursive: true, force: true });
    }
  },
  { timeout: 180_000 },
);

test("comparator orders the RC at or above MIN_BINARY_VERSION and below its release", () => {
  expect(parseVersion(rc).ok).toBe(true);
  expect(compareVersions(rc, MIN_BINARY_VERSION)).toBeGreaterThanOrEqual(0);
  expect(compareVersions(rc, PKG_VERSION)).toBe(-1);
});
