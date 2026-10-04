/**
 * empty-global-options.test.ts — b.pu2: an empty `storePath`, `home` or
 * `tmuxCommand` is forwarded to the CLI, which refuses it, so every verb call
 * rejects with ErrInvalidFlags and no default store is opened or created.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";

import { Client } from "../src/client.js";
import { ErrInvalidFlags } from "../src/errors.js";
import { withTempHome } from "./internal/tempHome.js";

type CreateOpts = Parameters<typeof Client.create>[0];

const cases = [
  { option: "storePath", flag: "--store-path" },
  { option: "home", flag: "--home" },
  { option: "tmuxCommand", flag: "--tmux-command" },
] as const;

for (const { option, flag } of cases) {
  test(`${option}: "" → every verb call rejects with ErrInvalidFlags "${flag} requires a value"; nothing under HOME`, async () => {
    await withTempHome(async (homeDir) => {
      const opts = { [option]: "", createIfMissing: true, _cliPath: process.env.CLI_PATH };
      using client = await Client.create(opts as unknown as CreateOpts);

      for (const call of [() => client.version({}), () => client.list({})]) {
        let err: unknown;
        try {
          await call();
        } catch (e) {
          err = e;
        }
        expect(err).toBeInstanceOf(ErrInvalidFlags);
        expect((err as ErrInvalidFlags).errName).toBe("ErrInvalidFlags");
        expect((err as ErrInvalidFlags).errDescription).toBe(`${flag} requires a value`);
      }
      // No default store (~/.agent-director/state.db), config or trail.
      expect(fs.readdirSync(homeDir)).toEqual([]);
    });
  });
}
