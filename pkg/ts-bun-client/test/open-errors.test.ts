/**
 * open-errors.test.ts — b.vma: the real CLI's ErrStoreOpen and
 * ErrConfigMalformed refusals reach a TS caller as those classes, not ErrUnknownErrorName.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";
import { ErrConfigMalformed, ErrStoreOpen, ErrUnknownErrorName } from "../src/errors.js";
import { rejection, withProcessEnv } from "./internal/helper.js";
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
    fs.mkdirSync(path.join(homeDir, ".agent-director"));
    fs.writeFileSync(path.join(homeDir, ".agent-director", "config.toml"), "[pause]\ntimeout_seconds = -1\n");
    const err = await listRejection();
    expect(err).toBeInstanceOf(ErrConfigMalformed);
    expect(err).not.toBeInstanceOf(ErrUnknownErrorName);
    expect((err as ErrConfigMalformed).errDescription).toContain("[pause] timeout_seconds = -1");
  });
}, 10_000);
