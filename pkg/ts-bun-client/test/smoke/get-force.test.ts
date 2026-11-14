/**
 * Smoke test — get-force verb
 *
 * Happy path (SR-4.1): a well-formed force_id no attempt holds reads unknown,
 * with a null force_result (force-delivery.test.ts reads a stored attempt back
 * as found).
 * Error path: a force_id that is not a hyphenated UUID → ErrInvalidFlags.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient } from "../internal/helper.js";
import { ErrInvalidFlags } from "../../src/index.js";

test("get-force: happy path — an unused force_id is unknown", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    expect(await client.getForce({ force_id: "ABCDEF01-2345-6789-ABCD-EF0123456789" })).toEqual({ lookup: "unknown", force_result: null });
  });
}, 10_000);

test("get-force: error — a braced force_id → ErrInvalidFlags", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.getForce({ force_id: "{abcdef01-2345-6789-abcd-ef0123456789}" })).rejects.toBeInstanceOf(ErrInvalidFlags);
  });
}, 10_000);
