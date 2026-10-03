/**
 * adviceFollow.test.ts — b.fji literal-follow test for the TS client's own
 * advice (inventory K1): trigger the callTimeoutMs refusal, assert its advice,
 * then omit the field as told and assert the client constructs and works.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";

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
      await expect(client.version()).resolves.toBeDefined();
    } finally {
      client.close();
    }
  });
}
