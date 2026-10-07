/**
 * Smoke test — version verb
 *
 * version needs no store row; it reports the package version (b.6o1) and the
 * binary's commit. Error path: none; version declares no ErrorNames
 * (smoke-invariants allow-list).
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { PKG_VERSION, homeStore, openClient } from "../internal/helper.js";

test("version: happy path — returns version and commit strings", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    const result = await client.version({});
    expect(result.version).toBe(PKG_VERSION);
    expect(result.commit.length).toBeGreaterThan(0);
  });
}, 10_000);
