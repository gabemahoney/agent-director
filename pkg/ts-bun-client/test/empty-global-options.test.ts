/**
 * empty-global-options.test.ts — b.pu2: an empty `storePath`, `home` or
 * `tmuxCommand` is forwarded to the CLI, which refuses it, so every verb call
 * rejects with ErrInvalidFlags and no default store is opened or created.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";

import { Client } from "../src/client.js";
import { ErrInvalidFlags } from "../src/errors.js";
import { rejection } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

test.each([
  ["storePath", "--store-path"],
  ["home", "--home"],
  ["tmuxCommand", "--tmux-command"],
] as const)('%s: "" → every verb call rejects with ErrInvalidFlags "%s requires a value"; nothing under HOME', async (option, flag) => {
  await withTempHome(async (homeDir) => {
    const opts = { [option]: "", _cliPath: process.env.CLI_PATH };
    using client = await Client.create(opts as unknown as Parameters<typeof Client.create>[0]);
    for (const err of [await rejection(client.version({})), await rejection(client.list({}))]) {
      expect(err).toBeInstanceOf(ErrInvalidFlags);
      expect((err as ErrInvalidFlags).errDescription).toBe(`${flag} requires a value`);
    }
    // No default store (~/.agent-director/state.db), config or trail.
    expect(fs.readdirSync(homeDir)).toEqual([]);
  });
});
