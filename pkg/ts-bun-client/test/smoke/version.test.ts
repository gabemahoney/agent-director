/**
 * Smoke test — version verb
 *
 * version is handle-free: it does not require a seeded store row and returns
 * the binary's build-time stamp regardless of the store path.
 *
 * Error path: version declares no ErrorNames in the manifest. This verb is in
 * the smoke-invariants allow-list for missing error-case tests.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { Client } from "../../src/index.js";
import type { VersionResult } from "../../src/index.js";
// b.6o1: version() returns the npm package version (not the CLI's git-describe
// stamp). Import package.json so we can assert the exact string.
import pkgJson from "../../package.json" with { type: "json" };

test("version: happy path — returns version and commit strings", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = new Client({ storePath, createIfMissing: true });
    const result: VersionResult = await client.version({});
    expect(typeof result.version).toBe("string");
    expect(result.version.length).toBeGreaterThan(0);
    // b.6o1: .version must equal the npm package version.
    expect(result.version).toBe(pkgJson.version);
    expect(typeof result.commit).toBe("string");
    expect(result.commit.length).toBeGreaterThan(0);
  });
}, 10_000);

// Error path: version declares no ErrorNames in the manifest.
// No error-case test is included for this verb. See smoke-invariants.test.ts
// for the allow-list that exempts it from the mandatory-error-case check.
