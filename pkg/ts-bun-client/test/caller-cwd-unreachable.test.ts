/**
 * caller-cwd-unreachable.test.ts — regression test for b.cot.
 *
 * Without this fix, the cwd-deleted test would call Bun.spawn with an
 * inherited dead cwd and surface a misleading ENOENT for the binary path;
 * this test pins the typed-error behavior.
 *
 * Covers:
 *   1. Client.create() throws ErrCallerCwdUnreachable when cwd is deleted.
 *   2. resolveSystemBinary() throws ErrCallerCwdUnreachable when cwd is deleted.
 *   3. Happy-path: both succeed (no false positives) in a valid cwd.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { Client, resolveSystemBinary } from "../src/client.js";
import { ErrCallerCwdUnreachable } from "../src/errors.js";
import type { ClientOptions } from "../src/client.js";

// ---------------------------------------------------------------------------
// Locate the CLI binary (same pattern as client-lifecycle.test.ts).
// ---------------------------------------------------------------------------
const repoRoot = path.resolve(import.meta.dir, "../../..");
const cliPath = process.env.CLI_PATH ?? path.join(repoRoot, "bin", "agent-director");
const cliMissing = !fs.existsSync(cliPath);

if (cliMissing) {
  console.warn(
    `caller-cwd-unreachable.test.ts: CLI binary not found at ${cliPath}; integration tests will be skipped.`
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a temp store path under /tmp (absolute, cwd-independent). */
function makeTmpStorePath(): string {
  return path.join(os.tmpdir(), `b-cot-store-${Date.now()}-${Math.random().toString(36).slice(2)}.db`);
}

/** Build ClientOptions with the DI _cliPath hook. */
function makeOpts(storePath: string): ClientOptions {
  return { storePath, createIfMissing: true, _cliPath: cliPath } as ClientOptions;
}

// ---------------------------------------------------------------------------
// 1. Client.create() — deleted cwd
// ---------------------------------------------------------------------------
test.skipIf(cliMissing)(
  "Client.create() throws ErrCallerCwdUnreachable when cwd is deleted (b.cot)",
  async () => {
    const orig = process.cwd();
    const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-cwd-dead-"));
    const storePath = makeTmpStorePath();

    process.chdir(tmpDir);
    fs.rmSync(tmpDir, { recursive: true, force: true });

    try {
      await expect(Client.create(makeOpts(storePath))).rejects.toBeInstanceOf(
        ErrCallerCwdUnreachable
      );
    } finally {
      process.chdir(orig);
      // best-effort store cleanup
      try { fs.rmSync(storePath, { force: true }); } catch { /* ignore */ }
    }
  }
);

// ---------------------------------------------------------------------------
// 2. resolveSystemBinary() — deleted cwd
// ---------------------------------------------------------------------------
test.skipIf(cliMissing)(
  "resolveSystemBinary() throws ErrCallerCwdUnreachable when cwd is deleted (b.cot)",
  async () => {
    const orig = process.cwd();
    const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-cwd-dead-rsb-"));

    process.chdir(tmpDir);
    fs.rmSync(tmpDir, { recursive: true, force: true });

    try {
      await expect(resolveSystemBinary()).rejects.toBeInstanceOf(
        ErrCallerCwdUnreachable
      );
    } finally {
      process.chdir(orig);
    }
  }
);

// ---------------------------------------------------------------------------
// 3. Happy-path: no false positives from a valid cwd
// ---------------------------------------------------------------------------
test.skipIf(cliMissing)(
  "Client.create() succeeds in a valid cwd (no false positive)",
  async () => {
    const storePath = makeTmpStorePath();
    try {
      const client = await Client.create(makeOpts(storePath));
      client.close();
    } finally {
      try { fs.rmSync(storePath, { force: true }); } catch { /* ignore */ }
    }
  }
);

test.skipIf(cliMissing)(
  "resolveSystemBinary() succeeds in a valid cwd (no false positive)",
  async () => {
    const result = await resolveSystemBinary();
    expect(result.path).toBeTruthy();
    expect(result.version).toBeTruthy();
  }
);
