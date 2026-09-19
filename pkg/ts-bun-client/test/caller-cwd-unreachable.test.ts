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
// System-discovery gate (b.12w).
//
// resolveSystemBinary() discovers the SYSTEM install via process.env HOME/PATH
// (discoverSystemBinary), NOT via the _cliPath DI hook — ResolveSystemBinaryOptions
// deliberately exposes no _cliPath analogue (SR-4.3 intentional asymmetry). The
// sandbox HOME (/home/sandbox) has no ~/.agent-director install, so bare
// discovery throws ErrSystemInstallNotFound before the b.cot cwd behavior is
// reached. The repo `bin/agent-director` cannot satisfy discovery via HOME, but
// it CAN via the SR-1.1 step-2 PATH lookup as long as it is named
// "agent-director". We therefore make discovery find the repo binary by
// prepending its directory to PATH for the duration of each call.
//
// discoverSystemBinary's PATH lookup keys on the basename "agent-director", so
// only a binary with that exact name is discoverable this way. A custom
// CLI_PATH pointing at a differently-named file would not be found via PATH; gate
// those out so we never un-skip a test whose dependency discovery can't meet.
// ---------------------------------------------------------------------------
const cliDir = path.dirname(cliPath);
const cliDiscoverableViaPath =
  !cliMissing && path.basename(cliPath) === "agent-director";
const systemUndiscoverable = !cliDiscoverableViaPath;

if (!cliMissing && systemUndiscoverable) {
  console.warn(
    `caller-cwd-unreachable.test.ts: CLI at ${cliPath} is not named "agent-director"; ` +
      `resolveSystemBinary() PATH-discovery tests will be skipped.`
  );
}

/**
 * Run `fn` with the repo binary's directory injected on PATH so
 * discoverSystemBinary()'s step-2 PATH lookup finds it. Restores PATH after.
 */
async function withRepoBinaryOnPath<T>(fn: () => Promise<T>): Promise<T> {
  const origPath = process.env.PATH;
  process.env.PATH =
    origPath && origPath !== "" ? `${cliDir}${path.delimiter}${origPath}` : cliDir;
  try {
    return await fn();
  } finally {
    if (origPath === undefined) delete process.env.PATH;
    else process.env.PATH = origPath;
  }
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
test.skipIf(cliMissing || systemUndiscoverable)(
  "resolveSystemBinary() throws ErrCallerCwdUnreachable when cwd is deleted (b.cot)",
  async () => {
    const orig = process.cwd();
    const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-cwd-dead-rsb-"));

    process.chdir(tmpDir);
    fs.rmSync(tmpDir, { recursive: true, force: true });

    try {
      await withRepoBinaryOnPath(() =>
        expect(resolveSystemBinary()).rejects.toBeInstanceOf(
          ErrCallerCwdUnreachable
        )
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

test.skipIf(cliMissing || systemUndiscoverable)(
  "resolveSystemBinary() succeeds in a valid cwd (no false positive)",
  async () => {
    const result = await withRepoBinaryOnPath(() => resolveSystemBinary());
    expect(result.path).toBeTruthy();
    // Prove discovery found the PATH-injected repo binary specifically, not some
    // ambient agent-director on PATH/HOME. Discovery canonicalizes via realpath.
    expect(result.path).toBe(fs.realpathSync(cliPath));
    expect(result.version).toBeTruthy();
  }
);
