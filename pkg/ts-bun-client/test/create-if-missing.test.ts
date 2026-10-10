/**
 * create-if-missing.test.ts — b.78b: ClientOptions.createIfMissing reaches the
 * in-repo CLI. false refuses a missing store with ErrStoreOpen and creates none
 * of it; true and omitted create it; false still opens an existing store.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";

import { ErrStoreOpen } from "../src/errors.js";
import { openClient, rejection, runHelper } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

test("createIfMissing: false → list() rejects with ErrStoreOpen; no store or parent dir is created, version() still works", () =>
  withTempHome(async (home) => {
    using client = await openClient(path.join(home, "sub", "state.db"), { createIfMissing: false });
    const err = await rejection(client.list({}));
    expect(err).toBeInstanceOf(ErrStoreOpen);
    expect((err as ErrStoreOpen).errDescription).toContain("database not initialized");
    await client.version({});
    // Neither the store, its parent dir, nor the default ~/.agent-director.
    expect(fs.readdirSync(home)).toEqual([]);
  }));

test.each([
  ["true", true],
  ["omitted", undefined],
] as const)("createIfMissing %s → list() creates the missing store and its parent dir", (_label, createIfMissing) =>
  withTempHome(async (home) => {
    const storePath = path.join(home, "sub", "state.db");
    using client = await openClient(storePath, { createIfMissing });
    expect((await client.list({})).spawns).toEqual([]);
    expect(fs.existsSync(storePath)).toBe(true);
  }));

test("createIfMissing: false on an existing store → list() reads it", () =>
  withTempHome(async (home) => {
    const storePath = path.join(home, "state.db");
    runHelper("seed-empty-store", { store: storePath });
    using client = await openClient(storePath, { createIfMissing: false });
    expect((await client.list({})).spawns).toEqual([]);
  }));
