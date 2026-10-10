/**
 * client-lifecycle.test.ts — Client.create() over the in-repo CLI, a call creates
 * a missing store, close() is idempotent, a `using` block closes at scope exit,
 * and every call after close throws ErrClientClosed.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { ErrClientClosed } from "../src/errors.js";
import { openClient } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

test("a call creates a missing store and its parent directories; no option is needed (b.78b)", () =>
  withTempHome(async (home) => {
    const store = path.join(home, "a", "b", "state.db");
    using client = await openClient(store);
    expect((await client.list({})).spawns).toEqual([]);
    expect(fs.existsSync(store)).toBe(true);
  }));

test("close() is idempotent and a closed Client refuses calls with ErrClientClosed", () =>
  withTempHome(async (home) => {
    const client = await openClient(path.join(home, "state.db"));
    expect(() => client._assertOpenForTests()).not.toThrow();
    client.close();
    expect(() => client.close()).not.toThrow();
    expect(() => client._assertOpenForTests()).toThrow(ErrClientClosed);
    await expect(client.version({})).rejects.toBeInstanceOf(ErrClientClosed);
  }));

test("a `using` block closes the Client at scope exit", () =>
  withTempHome(async (home) => {
    let captured: { _assertOpenForTests(): void } | undefined;
    {
      using client = await openClient(path.join(home, "state.db"));
      captured = client;
      expect(() => client._assertOpenForTests()).not.toThrow();
    }
    expect(() => captured!._assertOpenForTests()).toThrow(ErrClientClosed);
  }));
