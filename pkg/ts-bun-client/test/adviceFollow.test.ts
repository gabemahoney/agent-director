/**
 * adviceFollow.test.ts — b.fji literal-follow tests for the TS client's own
 * advice: K1 triggers the callTimeoutMs refusal, asserts its advice, then omits
 * the field as told and asserts the client constructs and works; K2 (b.vpb,
 * b.66q) does the same for a malformed extra_env key given to spawn, and to
 * makeTemplate, whose saved template's spawn then pre-trusts as advised.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";
import { ErrReservedEnvKey } from "../src/errors.js";
import type { SpawnResult } from "../src/types.js";
import { CLAUDE_JSON, homeStore, openClient, rejection, seedOuterParent, trustEntry } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

const fixturePath = path.resolve(import.meta.dir, "fixtures/epic-a/success.sh");
const tmpDirs: string[] = [];

afterAll(() => {
  for (const d of tmpDirs.splice(0)) fs.rmSync(d, { recursive: true, force: true });
});

type CreateOpts = Parameters<typeof Client.create>[0];

// K1: "omit the field to use the default (30000 ms)"
for (const bad of [0, -1]) {
  test(`K1 callTimeoutMs ${bad}: omitting the field as advised constructs a working client`, async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-advice-k1-"));
    tmpDirs.push(dir);
    const opts: Record<string, unknown> = {
      storePath: path.join(dir, "state.db"),
      createIfMissing: true,
      callTimeoutMs: bad,
      _cliPath: fixturePath,
    };

    let refusal: unknown;
    try {
      await Client.create(opts as unknown as CreateOpts);
    } catch (e) {
      refusal = e;
    }
    expect(refusal).toBeInstanceOf(Error);
    expect((refusal as Error).message).toContain(`must be positive (got ${bad})`);
    expect((refusal as Error).message).toContain("omit the field to use the default (30000 ms)");

    delete opts.callTimeoutMs;
    const client = await Client.create(opts as unknown as CreateOpts);
    try {
      await expect(client.version({})).resolves.toBeDefined();
    } finally {
      client.close();
    }
  });
}

// K2 (b.vpb, b.66q): "remove \"CLAUDE_CONFIG_DIR=...\" from extra_env, and give each variable its own name as the key (...) and its value as the value"
const k2Cases: {
  verb: string;
  outcome: string;
  launch: (c: Client, cwd: string, env: Record<string, string>) => Promise<SpawnResult>;
}[] = [
  { verb: "spawn", outcome: "launches and", launch: (c, cwd, env) => c.spawn({ cwd, extra_env: env }) },
  {
    verb: "make-template",
    outcome: "saves a template whose spawn",
    launch: async (c, cwd, env) => {
      await c.makeTemplate({ name: "k2", cwd, extra_env: env });
      return c.spawn({ cwd, template: "k2" });
    },
  },
];
for (const { verb, outcome, launch } of k2Cases) {
  test(`K2 ${verb} extra_env key CLAUDE_CONFIG_DIR=<dir>: naming the variable as its own key, as advised, ${outcome} pre-trusts <dir>`, async () => {
    await withTempHome(async (homeDir) => {
      const cfg = fs.mkdtempSync(path.join(homeDir, "cfg-"));
      const claudeJson = path.join(cfg, ".claude.json");
      fs.writeFileSync(claudeJson, CLAUDE_JSON);
      seedOuterParent(homeStore(homeDir));
      using client = await openClient(homeStore(homeDir));
      const bad = `CLAUDE_CONFIG_DIR=${cfg}`;

      const refusal = await rejection(launch(client, homeDir, { [bad]: "" }));

      expect(refusal).toBeInstanceOf(ErrReservedEnvKey);
      expect([(refusal as ErrReservedEnvKey).verb, (refusal as ErrReservedEnvKey).errName]).toEqual([verb, "ErrReservedEnvKey"]);
      expect((refusal as Error).message).toContain(`${JSON.stringify(bad)} is not a valid env-var name: it contains '='`);
      expect((refusal as Error).message).toContain(
        `remove ${JSON.stringify(bad)} from extra_env, and give each variable its own name as the key (not empty, with no '=' and no NUL byte) and its value as the value`,
      );
      const res = await launch(client, homeDir, { CLAUDE_CONFIG_DIR: cfg });
      expect(res.pre_trust).toBe("ok");
      expect(trustEntry(claudeJson, fs.realpathSync(homeDir))).toBe(true);
    });
  }, 10_000);
}
