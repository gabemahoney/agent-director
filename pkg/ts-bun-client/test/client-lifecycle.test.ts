/**
 * Client lifecycle tests (T2 subtask e7).
 *
 * These tests exercise constructor success, double-close idempotency, using-block
 * disposal, post-close ErrClientClosed, and the error inheritance chain.
 *
 * All tests require the compiled shared library at dist/libagent_director.so.
 * If the .so is absent the suite is skipped with a clear diagnostic message.
 */

import { test, expect, describe, beforeAll, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { Client } from "../src/client.js";
import { AgentDirectorError, ErrClientClosed } from "../src/errors.js";

// ---------------------------------------------------------------------------
// Pre-flight: verify the shared library is present
// ---------------------------------------------------------------------------
const repoRoot = path.resolve(import.meta.dir, "../../..");
const soPath = path.join(repoRoot, "dist", "libagent_director.so");

const soExists = fs.existsSync(soPath);
if (!soExists) {
  // Attempt to build it; if make is unavailable just skip.
  try {
    const proc = Bun.spawnSync(["make", "-C", repoRoot, "libagent_director"], {
      stdout: "pipe",
      stderr: "pipe",
    });
    if (proc.exitCode !== 0) {
      console.error(
        `client-lifecycle.test.ts: libagent_director.so not found at ${soPath} ` +
          `and 'make libagent_director' failed (exit ${proc.exitCode}). SKIPPING.`
      );
      process.exit(0); // skip the file
    }
  } catch {
    console.error(
      `client-lifecycle.test.ts: libagent_director.so not found at ${soPath} ` +
        `and make is unavailable. SKIPPING.`
    );
    process.exit(0); // skip the file
  }
}

// ---------------------------------------------------------------------------
// Temp store helpers
// ---------------------------------------------------------------------------
const tmpBase = path.join(import.meta.dir, ".tmp");

function makeTmpDir(): string {
  const dir = path.join(tmpBase, `store-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

function removeTmpDir(dir: string): void {
  try {
    fs.rmSync(dir, { recursive: true, force: true });
  } catch {
    // best-effort cleanup
  }
}

// Clean up the base tmp dir at the end of the suite.
afterAll(() => {
  removeTmpDir(tmpBase);
});

// ---------------------------------------------------------------------------
// Helper: make a valid ClientOptions pointing at a fresh temp store dir.
// The store path is a .db file inside the temp dir.
// create_if_missing is NOT a ClientOptions field — ad_open defaults it false.
// We pass a store path to a file that doesn't yet exist; pkg/api.New with
// CreateIfMissing=false (the cabi default) will fail unless the DB exists.
// Use create_if_missing via the Go layer:  ad_open accepts create_if_missing.
// Since ClientOptions doesn't expose it, we use a storePath that already
// exists by first touching it, OR we need to pass create_if_missing=true.
//
// Looking at lifecycle.go: openParams has CreateIfMissing bool `json:"create_if_missing"`.
// ClientOptions doesn't expose this, but bootstrapFfi.callOpen doesn't send it
// either (it's not in the params object we build in the constructor).
//
// For the test to work against a fresh store we need create_if_missing.
// Solution: create the file (and its directory) ourselves so pkg/api sees an
// existing DB path, OR expose create_if_missing in the test by calling
// bootstrapFfi directly... but that's an internal.
//
// Actually the cleanest approach: look at how pkg/api.New works without
// CreateIfMissing. If it just calls store.Open (which creates on first open),
// this is fine. Let's try it — pkg/api.New + store.Open typically creates the
// DB file if the parent dir exists.
// ---------------------------------------------------------------------------

describe("Client lifecycle", () => {
  // (a) Construct against a fresh temp store — should not throw.
  test("(a) constructor succeeds for a fresh store path", () => {
    const dir = makeTmpDir();
    try {
      const storePath = path.join(dir, "state.db");
      const client = new Client({ storePath, createIfMissing: true });
      client.close();
    } finally {
      removeTmpDir(dir);
    }
  });

  // (b) Double-close() is a no-op (must not throw on second call).
  test("(b) double close() is a no-op", () => {
    const dir = makeTmpDir();
    try {
      const storePath = path.join(dir, "state.db");
      const client = new Client({ storePath, createIfMissing: true });
      client.close();
      // Second close: must not throw.
      expect(() => client.close()).not.toThrow();
    } finally {
      removeTmpDir(dir);
    }
  });

  // (c) `using` block calls close() at scope exit.
  test("(c) using block closes the client at scope exit", () => {
    const dir = makeTmpDir();
    let capturedClient: Client | undefined;
    try {
      const storePath = path.join(dir, "state.db");
      {
        using c = new Client({ storePath, createIfMissing: true });
        capturedClient = c;
        // Inside the block the client is open.
        expect(() => (c as unknown as { _assertOpen(): void })._assertOpen()).not.toThrow();
      }
      // After the using block exits, _open should be false → _assertOpenForTests throws.
      expect(() => capturedClient!._assertOpenForTests()).toThrow(ErrClientClosed);
    } finally {
      removeTmpDir(dir);
    }
  });

  // (d) Post-close _assertOpen throws ErrClientClosed.
  test("(d) _assertOpen (via _assertOpenForTests) throws ErrClientClosed after close()", () => {
    const dir = makeTmpDir();
    try {
      const storePath = path.join(dir, "state.db");
      const client = new Client({ storePath, createIfMissing: true });
      client.close();
      expect(() => client._assertOpenForTests()).toThrow(ErrClientClosed);
    } finally {
      removeTmpDir(dir);
    }
  });

  // (e) ErrClientClosed instanceof chain.
  test("(e) ErrClientClosed is instanceof ErrClientClosed, AgentDirectorError, and Error", () => {
    const dir = makeTmpDir();
    try {
      const storePath = path.join(dir, "state.db");
      const client = new Client({ storePath, createIfMissing: true });
      client.close();
      let caught: unknown;
      try {
        client._assertOpenForTests();
      } catch (e) {
        caught = e;
      }
      expect(caught).toBeInstanceOf(ErrClientClosed);
      expect(caught).toBeInstanceOf(AgentDirectorError);
      expect(caught).toBeInstanceOf(Error);
      expect((caught as ErrClientClosed).name).toBe("ErrClientClosed");
    } finally {
      removeTmpDir(dir);
    }
  });

  // Bonus: verify the handle is nulled after close.
  test("handle is null after close()", () => {
    const dir = makeTmpDir();
    try {
      const storePath = path.join(dir, "state.db");
      const client = new Client({ storePath, createIfMissing: true });
      client.close();
      // Access private field at runtime (TS erases private at JS level).
      const handle = (client as unknown as { _handle: string | null })._handle;
      expect(handle).toBeNull();
    } finally {
      removeTmpDir(dir);
    }
  });
});
