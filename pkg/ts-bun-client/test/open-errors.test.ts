/**
 * open-errors.test.ts — b.vma, b.cm7: the real CLI's store-open, config and
 * schema refusals, and the old-layout refusal, reach a TS caller as their own classes, not ErrUnknownErrorName.
 */

import { test, expect } from "bun:test";
import { Database } from "bun:sqlite";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";
import {
  type AgentDirectorError,
  ErrConfigMalformed,
  ErrOldLayout,
  ErrSchemaMigrationRequired,
  ErrSchemaMismatch,
  ErrStoreOpen,
  ErrUnknownErrorName,
} from "../src/errors.js";
import { homeStore, rejection, runHelper, withProcessEnv } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

/** What list({}) rejects with on a Client over the real CLI, given storePath (or no options). */
async function listRejection(storePath?: string): Promise<unknown> {
  const opts = { ...(storePath === undefined ? {} : { storePath }), _cliPath: process.env.CLI_PATH };
  using client = await Client.create(opts as unknown as Parameters<typeof Client.create>[0]);
  return await rejection(client.list({}));
}

// The refusal happens whatever storePath is: the CLI cannot expand its config path first.
test.each([
  ["no storePath", false],
  ["an absolute storePath", true],
] as const)("HOME unset, no home, %s → list() rejects with ErrStoreOpen", async (_label, withStorePath) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "agentdirector-open-errors-"));
  try {
    const err = await withProcessEnv({ HOME: undefined }, () => listRejection(withStorePath ? path.join(dir, "state.db") : undefined));
    expect(err).toBeInstanceOf(ErrStoreOpen);
    expect(err).not.toBeInstanceOf(ErrUnknownErrorName);
    expect((err as ErrStoreOpen).errDescription).toBe("api: expand config path: expand tilde: $HOME is not defined");
    expect(fs.readdirSync(dir)).toEqual([]);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}, 10_000);

test("refused [pause] timeout_seconds → list() rejects with ErrConfigMalformed", async () => {
  await withTempHome(async (homeDir) => {
    fs.mkdirSync(path.join(homeDir, ".config", "agent-director"), { recursive: true });
    fs.writeFileSync(path.join(homeDir, ".config", "agent-director", "config.toml"), "[pause]\ntimeout_seconds = -1\n");
    const err = await listRejection();
    expect(err).toBeInstanceOf(ErrConfigMalformed);
    expect(err).not.toBeInstanceOf(ErrUnknownErrorName);
    expect((err as ErrConfigMalformed).errDescription).toContain("[pause] timeout_seconds = -1");
  });
}, 10_000);

// SR-11.7: a host that still holds only the old directory is refused, not silently given a new store.
test("the old directory only, no storePath → list() rejects with ErrOldLayout and creates nothing", async () => {
  await withTempHome(async (homeDir) => {
    fs.mkdirSync(path.join(homeDir, ".agent-director"));
    const err = await listRejection();
    expect(err).toBeInstanceOf(ErrOldLayout);
    expect(err).not.toBeInstanceOf(ErrUnknownErrorName);
    expect((err as ErrOldLayout).errDescription).toContain("install.sh");
    expect(fs.readdirSync(homeDir)).toEqual([".agent-director"]);
    expect(fs.readdirSync(path.join(homeDir, ".agent-director"))).toEqual([]);
  });
}, 10_000);

test("the old directory only, with a storePath → list() is not refused", async () => {
  await withTempHome(async (homeDir) => {
    fs.mkdirSync(path.join(homeDir, ".agent-director"));
    const storePath = path.join(homeDir, "elsewhere", "state.db");
    fs.mkdirSync(path.dirname(storePath));
    runHelper("seed-empty-store", { store: storePath });
    using client = await Client.create({ storePath, _cliPath: process.env.CLI_PATH } as unknown as Parameters<typeof Client.create>[0]);
    await client.list({});
    expect(fs.existsSync(path.join(homeDir, ".config"))).toBe(false);
  });
}, 10_000);

// b.cm7's repro: a store stamped with another schema version than the binary's.
test.each([
  ["newer", 99, ErrSchemaMismatch, "found user_version=99"],
  ["older", 1, ErrSchemaMigrationRequired, "state.db is schema v1"],
] as const)("store schema %s than the binary → list() rejects with its class", async (_label, version, cls, desc) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    runHelper("seed-empty-store", { store: storePath });
    const db = new Database(storePath);
    db.run(`PRAGMA user_version = ${version}`);
    db.close();
    const err = await listRejection(storePath);
    expect(err).toBeInstanceOf(cls);
    expect(err).not.toBeInstanceOf(ErrUnknownErrorName);
    expect((err as AgentDirectorError).errDescription).toContain(desc);
  });
}, 10_000);
