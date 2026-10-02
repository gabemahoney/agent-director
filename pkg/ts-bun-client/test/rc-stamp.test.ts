/**
 * rc-stamp.test.ts — AC-REL-03 / SR-21.5: the client accepts a release-candidate
 * binary (package version + "-rc.1") and orders it between the floor and its release.
 */

import { test, expect } from "bun:test";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { Client } from "../src/client.js";
import type { ClientOptions } from "../src/client.js";
import { MIN_BINARY_VERSION } from "../src/internal/constants.js";
import { compareVersions, parseVersion } from "../src/internal/semver.js";
import { underSeedsLock } from "./internal/seedsLock.js";
import { withTempHome } from "./internal/tempHome.js";
const repoRoot = resolve(import.meta.dir, "../../..");
const release = (
  JSON.parse(readFileSync(resolve(import.meta.dir, "../package.json"), "utf-8")) as { version: string }
).version;
const rc = `${release}-rc.1`;

// `make release-binaries` artifact suffix for this host; null skips the build test.
const releaseTarget = (() => {
  if (process.platform === "linux" && process.arch === "x64") return "linux-amd64";
  if (process.platform === "linux" && process.arch === "arm64") return "linux-arm64";
  if (process.platform === "darwin" && process.arch === "arm64") return "darwin-arm64";
  return null;
})();

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
        throw new Error(
          `make release-binaries failed (exit ${build.exitCode}):\n${build.stdout}\n${build.stderr}`,
        );
      }
      const cliPath = join(dist, `agent-director-${releaseTarget}`);
      await withTempHome(async (home) => {
        const client = await Client.create({
          storePath: join(home, "state.db"),
          createIfMissing: true,
          _cliPath: cliPath,
        } as ClientOptions);
        try {
          expect(client.binaryVersion).toBe(rc);
        } finally {
          client.close();
        }
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
  expect(compareVersions(rc, release)).toBe(-1);
});
