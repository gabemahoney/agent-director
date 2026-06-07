/**
 * system-install-disappeared.test.ts — integration tests for b.xht.
 *
 * Without this fix, the binary-gone test would surface a raw ENOENT error
 * blaming the binary path; the cwd-gone test would surface the same misleading
 * ENOENT. This file pins typed-error routing for both.
 *
 * Three buckets per the b.xht three-bucket rule:
 *   1. Binary gone mid-life     → ErrSystemInstallDisappeared
 *   2. CWD gone mid-life        → ErrCallerCwdUnreachable
 *   3. Neither gone (happy path)→ succeeds (pinned by client-lifecycle.test.ts)
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { Client } from "../src/client.js";
import {
  ErrSystemInstallDisappeared,
  ErrCallerCwdUnreachable,
} from "../src/index.js";
import type { ClientOptions } from "../src/client.js";

// ---------------------------------------------------------------------------
// Locate the CLI binary (same pattern as client-lifecycle.test.ts).
// ---------------------------------------------------------------------------
const repoRoot = path.resolve(import.meta.dir, "../../..");
const cliPath = process.env.CLI_PATH ?? path.join(repoRoot, "bin", "agent-director");
const cliMissing = !fs.existsSync(cliPath);

if (cliMissing) {
  console.warn(
    `system-install-disappeared.test.ts: CLI binary not found at ${cliPath}; integration tests will be skipped.`
  );
}

// ---------------------------------------------------------------------------
// Scratch directory for this bug's test artifacts.
// All scratch dirs are under tickets/Bugs/b.xht/scratch/ (NOT /tmp).
// ---------------------------------------------------------------------------
const scratchBase = path.join(
  repoRoot,
  "tickets",
  "Bugs",
  "b.xht",
  "scratch",
);

/** Create an absolute store path under scratchBase so chdir does not affect it. */
function makeScratchStorePath(tag: string): string {
  const dir = path.join(scratchBase, `${tag}-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  fs.mkdirSync(dir, { recursive: true });
  return path.join(dir, "state.db");
}

/** Best-effort removal of a scratch directory. Swallows EBUSY/ENOENT. */
function cleanupDir(p: string): void {
  try {
    fs.rmSync(path.dirname(p), { recursive: true, force: true });
  } catch {
    // best-effort: swallow EBUSY/ENOENT
  }
}

/** Build ClientOptions with the DI _cliPath hook. */
function makeOpts(storePath: string, overrideCli?: string): ClientOptions {
  return {
    storePath,
    createIfMissing: true,
    _cliPath: overrideCli ?? cliPath,
  } as ClientOptions;
}

// ---------------------------------------------------------------------------
// Bucket 1: binary gone mid-life → ErrSystemInstallDisappeared
// ---------------------------------------------------------------------------
test.skipIf(cliMissing)(
  "bucket 1: verb call throws ErrSystemInstallDisappeared when binary disappears after construction (b.xht)",
  async () => {
    // Copy the real binary to a temp location under scratch so we can delete it.
    const copyDir = path.join(
      scratchBase,
      `bin-gone-${Date.now()}-${Math.random().toString(36).slice(2)}`,
    );
    fs.mkdirSync(copyDir, { recursive: true });
    const tempBin = path.join(copyDir, "ad-bin");
    const storePath = makeScratchStorePath("bin-gone-store");

    try {
      fs.copyFileSync(cliPath, tempBin);
      fs.chmodSync(tempBin, 0o755);

      // Construction succeeds: binary exists at this point.
      const client = await Client.create(makeOpts(storePath, tempBin));

      // Disappear the binary.
      fs.unlinkSync(tempBin);

      // Verb call must now throw ErrSystemInstallDisappeared.
      let caught: unknown;
      try {
        await client.list({});
      } catch (e) {
        caught = e;
      }

      expect(caught).toBeInstanceOf(ErrSystemInstallDisappeared);
      const err = caught as ErrSystemInstallDisappeared;
      expect(err.binaryPath).toBe(tempBin);
      expect(err.verb).toBe("list");
      expect(err.cause).toBeTruthy();

      client.close();
    } finally {
      try { fs.rmSync(copyDir, { recursive: true, force: true }); } catch { /* ignore */ }
      cleanupDir(storePath);
    }
  }
);

// ---------------------------------------------------------------------------
// Bucket 2: cwd gone mid-life → ErrCallerCwdUnreachable
// ---------------------------------------------------------------------------
test.skipIf(cliMissing)(
  "bucket 2: verb call throws ErrCallerCwdUnreachable when cwd disappears after construction (b.xht)",
  async () => {
    // Use an absolute storePath so the chdir below does not affect it.
    const storePath = makeScratchStorePath("cwd-gone-store");
    const orig = process.cwd();

    // Create a workdir under scratch and chdir into it.
    const workdir = fs.mkdtempSync(
      path.join(scratchBase, "cwd-gone-"),
    );

    process.chdir(workdir);

    try {
      // Construction succeeds: chdir does NOT delete cwd.
      const client = await Client.create(makeOpts(storePath));

      // Delete the workdir so cwd becomes dangling.
      fs.rmSync(workdir, { recursive: true, force: true });

      // Verb call must now throw ErrCallerCwdUnreachable.
      let caught: unknown;
      try {
        await client.list({});
      } catch (e) {
        caught = e;
      }

      expect(caught).toBeInstanceOf(ErrCallerCwdUnreachable);
      const err = caught as ErrCallerCwdUnreachable;
      expect(err.cwd).toBe(workdir);

      client.close();
    } finally {
      // MUST restore cwd before any other test runs.
      process.chdir(orig);
      cleanupDir(storePath);
      try { fs.rmSync(workdir, { recursive: true, force: true }); } catch { /* ignore */ }
    }
  }
);

// ---------------------------------------------------------------------------
// Bucket 3: neither gone — happy path
// ---------------------------------------------------------------------------
// The full happy-path verb lifecycle is covered by test/client-lifecycle.test.ts.
// A single explicit assertion here confirms no regression for the direct path.
test.skipIf(cliMissing)(
  "bucket 3: verb call succeeds when binary and cwd are both present (b.xht no-regression)",
  async () => {
    const storePath = makeScratchStorePath("happy-path");
    try {
      const client = await Client.create(makeOpts(storePath));
      // list({}) is a low-impact verb that works against an empty store.
      const result = await client.list({});
      expect(result).toBeDefined();
      client.close();
    } finally {
      cleanupDir(storePath);
    }
  }
);
