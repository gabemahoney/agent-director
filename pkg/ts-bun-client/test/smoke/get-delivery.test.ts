/**
 * Smoke test — get-delivery verb
 *
 * Happy path (SR-5.4): a pair no Message holds reads unknown, with a null
 * result.
 * Error path: an empty delivery_request_id → ErrInvalidFlags.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient } from "../internal/helper.js";
import { ErrInvalidFlags } from "../../src/index.js";

const CALLER = "0f8e2a34-5b6c-4d7e-8f90-a1b2c3d4e5f6";

test("get-delivery: happy path — an unused pair is unknown", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    expect(await client.getDelivery({ caller_uuid: CALLER, delivery_request_id: "msg 多 🦋" })).toEqual({ lookup: "unknown", result: null });
  });
}, 10_000);

test("get-delivery: error — an empty delivery_request_id → ErrInvalidFlags", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.getDelivery({ caller_uuid: CALLER, delivery_request_id: "" })).rejects.toBeInstanceOf(ErrInvalidFlags);
  });
}, 10_000);
