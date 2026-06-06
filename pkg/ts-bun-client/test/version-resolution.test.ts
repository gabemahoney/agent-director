/**
 * version-resolution.test.ts — cache test for loadNpmPackageVersion().
 *
 * Verifies that the per-instance #npmPkgVersion cache in SubprocessClient
 * causes package.json to be read from disk exactly once per instance, even
 * when version() is called multiple times (SR-2.3).
 *
 * Strategy: mock node:fs/promises to count readFile calls. The mock uses
 * Bun.file() internally rather than delegating to the real readFile because
 * Bun applies mock.module to the current file's own imports as well — any
 * attempt to import and call the "original" readFile from within this file
 * causes infinite recursion. Bun.file() is a Bun-native API that is not
 * routed through node:fs/promises and therefore is unaffected by the mock.
 */

import { test, expect, describe, beforeEach, afterAll, mock } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";

const FIXTURES = path.resolve(import.meta.dir, "fixtures/epic-a");

// ---------------------------------------------------------------------------
// readFile spy — counts calls made by loadNpmPackageVersion() inside
// subprocessClient.ts. Reset in beforeEach so each test starts at zero.
//
// `throwOnNextReadFile`, when set, makes the next readFile call throw the
// supplied error and resets the latch. Used by the b.vod regression to
// inject a non-fallback error and assert it propagates.
// ---------------------------------------------------------------------------
let readFileCallCount = 0;
let throwOnNextReadFile: Error | null = null;

mock.module("node:fs/promises", () => ({
  readFile: async (
    url: string | URL,
    options?: string | { encoding?: string }
  ) => {
    readFileCallCount++;
    if (throwOnNextReadFile) {
      const e = throwOnNextReadFile;
      throwOnNextReadFile = null;
      throw e;
    }
    // Use Bun.file (not node:fs/promises) to avoid circular recursion.
    const text = await Bun.file(url).text();
    const enc =
      typeof options === "string" ? options : options?.encoding;
    return enc ? text : Buffer.from(text);
  },
}));

// ---------------------------------------------------------------------------
// Temp dir management
// ---------------------------------------------------------------------------
const tmpDirs: string[] = [];

function makeTmpDir(): string {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), "ad-vr-"));
  tmpDirs.push(d);
  return d;
}

afterAll(() => {
  for (const d of tmpDirs.splice(0)) {
    try { fs.rmSync(d, { recursive: true, force: true }); } catch { /* ignore */ }
  }
});

// Release the mock.module spy so it doesn't leak into other test files loaded
// in the same bun process after this suite finishes.
afterAll(() => {
  mock.restore();
});

function makeClient(fixturePath: string): Promise<Client> {
  const dir = makeTmpDir();
  return Client.create({
    storePath: path.join(dir, "state.db"),
    createIfMissing: true,
    callTimeoutMs: 5000,
    _cliPath: fixturePath,
  } as unknown as Parameters<typeof Client.create>[0]);
}

// ---------------------------------------------------------------------------
// Cache tests
// ---------------------------------------------------------------------------
describe("loadNpmPackageVersion cache (SR-3.3)", () => {
  beforeEach(() => {
    readFileCallCount = 0;
  });

  test(
    "version() reads package.json exactly once across two calls (second is cache hit)",
    async () => {
      const fixturePath = path.join(FIXTURES, "sleep-and-respond.js");
      const client = await makeClient(fixturePath);

      const prevSleepMs = process.env.SLEEP_MS;
      process.env.SLEEP_MS = "0";

      try {
        type ClientV = { version(p: object): Promise<{ version: string; commit: string }> };
        const c = client as unknown as ClientV;

        // Read expected version via Bun.file (not through the mocked readFile)
        // so the assertion is independent of the spy.
        const pkgVersion = (
          JSON.parse(
            await Bun.file(new URL("../package.json", import.meta.url)).text()
          ) as { version: string }
        ).version;

        const r1 = await c.version({});
        const r2 = await c.version({});

        // Both calls return the correct npm package version.
        expect(r1.version).toBe(pkgVersion);
        expect(r2.version).toBe(pkgVersion);

        // package.json was read exactly once: loadNpmPackageVersion() runs on
        // the first version() call and caches the result in #npmPkgVersion.
        // The second call skips loadNpmPackageVersion() entirely.
        expect(readFileCallCount).toBe(1);
      } finally {
        if (prevSleepMs === undefined) {
          delete process.env.SLEEP_MS;
        } else {
          process.env.SLEEP_MS = prevSleepMs;
        }
      }
    },
    { timeout: 15000 }
  );

  test(
    "b.vod: non-fallback readFile error propagates instead of being silently swallowed",
    async () => {
      const fixturePath = path.join(FIXTURES, "sleep-and-respond.js");
      const client = await makeClient(fixturePath);

      const prevSleepMs = process.env.SLEEP_MS;
      process.env.SLEEP_MS = "0";

      try {
        // Inject a real (non-MODULE_NOT_FOUND / non-ENOENT) error. Pre-fix the
        // catch in loadNpmPackageVersion would swallow this and fall back; post-
        // fix the catch is narrowed to import.meta.resolve() only and any
        // readFile error must propagate.
        const eacces = new Error("EACCES: permission denied");
        (eacces as unknown as { code: string }).code = "EACCES";
        throwOnNextReadFile = eacces;

        type ClientV = { version(p: object): Promise<unknown> };
        const c = client as unknown as ClientV;

        await expect(c.version({})).rejects.toThrow(/EACCES/);
      } finally {
        throwOnNextReadFile = null;
        if (prevSleepMs === undefined) {
          delete process.env.SLEEP_MS;
        } else {
          process.env.SLEEP_MS = prevSleepMs;
        }
      }
    },
    { timeout: 15000 }
  );

  test(
    "two independent SubprocessClient instances each read package.json once",
    async () => {
      const fixturePath = path.join(FIXTURES, "sleep-and-respond.js");
      const c1 = (await makeClient(fixturePath)) as unknown as { version(p: object): Promise<{ version: string }> };
      const c2 = (await makeClient(fixturePath)) as unknown as { version(p: object): Promise<{ version: string }> };

      const prevSleepMs = process.env.SLEEP_MS;
      process.env.SLEEP_MS = "0";

      try {
        await c1.version({});
        await c1.version({}); // cache hit on c1

        await c2.version({});
        await c2.version({}); // cache hit on c2

        // Two instances, each reads once: total = 2.
        expect(readFileCallCount).toBe(2);
      } finally {
        if (prevSleepMs === undefined) {
          delete process.env.SLEEP_MS;
        } else {
          process.env.SLEEP_MS = prevSleepMs;
        }
      }
    },
    { timeout: 15000 }
  );
});
