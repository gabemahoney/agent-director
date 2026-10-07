/**
 * client-lifecycle.test.ts — Client.create() over the in-repo CLI, close() is
 * idempotent, a `using` block closes at scope exit, and every call after close
 * throws ErrClientClosed.
 */

import { test, expect } from "bun:test";
import * as path from "node:path";
import { ErrClientClosed } from "../src/errors.js";
import { openClient } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

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
