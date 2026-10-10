/**
 * adviceFollow.test.ts — b.fji literal-follow tests for the TS client's own
 * advice: K1 triggers the callTimeoutMs refusal, asserts its advice, then omits
 * the field as told and asserts the client constructs and works; K2 (b.vpb)
 * does the same for spawn's malformed extra_env key.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";
import { ErrReservedEnvKey } from "../src/errors.js";
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

// K2 (b.vpb): "remove \"CLAUDE_CONFIG_DIR=...\" from extra_env, and give each variable its own name as the key (...) and its value as the value"
test("K2 extra_env key CLAUDE_CONFIG_DIR=<dir>: naming the variable as its own key, as advised, launches and pre-trusts <dir>", async () => {
  await withTempHome(async (homeDir) => {
    const cfg = fs.mkdtempSync(path.join(homeDir, "cfg-"));
    const claudeJson = path.join(cfg, ".claude.json");
    fs.writeFileSync(claudeJson, CLAUDE_JSON);
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const bad = `CLAUDE_CONFIG_DIR=${cfg}`;

    const refusal = await rejection(client.spawn({ cwd: homeDir, extra_env: { [bad]: "" } }));

    expect(refusal).toBeInstanceOf(ErrReservedEnvKey);
    expect((refusal as ErrReservedEnvKey).errName).toBe("ErrReservedEnvKey");
    expect((refusal as Error).message).toContain(`${JSON.stringify(bad)} is not a valid env-var name: it contains '='`);
    expect((refusal as Error).message).toContain(
      `remove ${JSON.stringify(bad)} from extra_env, and give each variable its own name as the key (not empty, with no '=' and no NUL byte) and its value as the value`,
    );
    const res = await client.spawn({ cwd: homeDir, extra_env: { CLAUDE_CONFIG_DIR: cfg } });
    expect(res.pre_trust).toBe("ok");
    expect(trustEntry(claudeJson, fs.realpathSync(homeDir))).toBe(true);
  });
}, 10_000);
