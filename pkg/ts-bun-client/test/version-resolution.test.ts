/**
 * version-resolution.test.ts — SR-2.3: each Client reads package.json once and
 * caches the version; b.vod: a readFile error other than not-found propagates.
 *
 * node:fs/promises is mocked to count readFile calls. The mock reads through
 * Bun.file, since mock.module also applies to this file's own imports.
 */

import { test, expect, beforeEach, afterAll, mock } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { PKG_VERSION, openClient, withProcessEnv } from "./internal/helper.js";

let readFileCalls = 0;
let failNextRead: Error | null = null;

mock.module("node:fs/promises", () => ({
  readFile: async (url: string | URL, options?: string | { encoding?: string }) => {
    readFileCalls++;
    if (failNextRead) {
      const e = failNextRead;
      failNextRead = null;
      throw e;
    }
    const text = await Bun.file(url).text();
    return (typeof options === "string" ? options : options?.encoding) ? text : Buffer.from(text);
  },
}));

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ad-vr-"));
afterAll(() => {
  mock.restore();
  fs.rmSync(tmp, { recursive: true, force: true });
});
beforeEach(() => {
  readFileCalls = 0;
});

const FIXTURE = path.resolve(import.meta.dir, "fixtures/epic-a/sleep-and-respond.js");
const makeClient = () => openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: FIXTURE, callTimeoutMs: 5000 });

test("two Clients each read package.json once across two version() calls", async () => {
  const [c1, c2] = [await makeClient(), await makeClient()];
  await withProcessEnv({ SLEEP_MS: "0" }, async () => {
    expect((await c1.version({})).version).toBe(PKG_VERSION);
    expect(readFileCalls).toBe(1);
    expect((await c1.version({})).version).toBe(PKG_VERSION);
    expect(readFileCalls).toBe(1);
    await c2.version({});
    await c2.version({});
    expect(readFileCalls).toBe(2);
  });
}, 15_000);

test("b.vod: a non-fallback readFile error propagates instead of being swallowed", async () => {
  const client = await makeClient();
  await withProcessEnv({ SLEEP_MS: "0" }, async () => {
    failNextRead = Object.assign(new Error("EACCES: permission denied"), { code: "EACCES" });
    try {
      await expect(client.version({})).rejects.toThrow(/EACCES/);
    } finally {
      failNextRead = null;
    }
  });
}, 15_000);
