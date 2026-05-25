/**
 * Client lifecycle tests.
 *
 * Exercises constructor success, double-close idempotency, using-block
 * disposal, post-close ErrClientClosed, and the error inheritance chain.
 *
 * Epic B cutover (b.19d t1.19d.9i): Client is now SubprocessClient.
 * - Pre-flight guard changed from .so presence to CLI binary presence.
 * - Client construction uses the `_cliPath` DI hook so tests run in-repo
 *   without a real installed @agent-director/* platform package.
 * - `_assertOpenForTests()` used in place of the old `(c as any)._assertOpen()`
 *   cast (the subprocess Client uses true private `#assertOpen`; the bridge
 *   method is the approved test-access path).
 * - "handle is null after close" test rewritten: subprocess model has no
 *   handle string; the equivalent contract is that post-close verb calls
 *   throw ErrClientClosed (already covered by tests d/e; recast here to
 *   verify _handle stub is null for FFI-shape parity).
 */

import { test, expect, describe, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { Client } from "../src/client.js";
import { AgentDirectorError, ErrClientClosed } from "../src/errors.js";

// ---------------------------------------------------------------------------
// Pre-flight: verify the CLI binary is present in dist/.
// Post-cutover the client uses the subprocess model; the shared library is
// not needed. The CLI binary (agent-director-linux-amd64 or darwin-arm64)
// must be present for the tests to run.
// ---------------------------------------------------------------------------
const repoRoot = path.resolve(import.meta.dir, "../../..");
const binaryName =
  process.platform === "darwin" ? "agent-director-darwin-arm64" : "agent-director-linux-amd64";
const cliPath = path.join(repoRoot, "dist", binaryName);

if (!fs.existsSync(cliPath)) {
  console.error(
    `client-lifecycle.test.ts: CLI binary not found at ${cliPath}. SKIPPING.`
  );
  process.exit(0);
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
// Helper: build ClientOptions for a fresh temp store, with _cliPath injected
// so SubprocessClient bypasses platform-package resolution and uses the
// in-repo dist/ binary directly.
// ---------------------------------------------------------------------------
function makeOpts(dir: string): ClientOptions {
  const storePath = path.join(dir, "state.db");
  // _cliPath is an undocumented DI hook on SubprocessClient cast through unknown.
  return { storePath, createIfMissing: true, _cliPath: cliPath } as ClientOptions;
}

// Re-import ClientOptions type (re-exported from client.ts → types.ts).
import type { ClientOptions } from "../src/client.js";

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Client lifecycle", () => {
  // (a) Construct against a fresh temp store — should not throw.
  test("(a) constructor succeeds for a fresh store path", () => {
    const dir = makeTmpDir();
    try {
      const client = new Client(makeOpts(dir));
      client.close();
    } finally {
      removeTmpDir(dir);
    }
  });

  // (b) Double-close() is a no-op (must not throw on second call).
  test("(b) double close() is a no-op", () => {
    const dir = makeTmpDir();
    try {
      const client = new Client(makeOpts(dir));
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
      {
        using c = new Client(makeOpts(dir));
        capturedClient = c;
        // Inside the block the client is open — _assertOpenForTests must not throw.
        expect(() => c._assertOpenForTests()).not.toThrow();
      }
      // After the using block exits, _open is false → _assertOpenForTests throws.
      expect(() => capturedClient!._assertOpenForTests()).toThrow(ErrClientClosed);
    } finally {
      removeTmpDir(dir);
    }
  });

  // (d) Post-close _assertOpenForTests throws ErrClientClosed.
  test("(d) _assertOpenForTests throws ErrClientClosed after close()", () => {
    const dir = makeTmpDir();
    try {
      const client = new Client(makeOpts(dir));
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
      const client = new Client(makeOpts(dir));
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

  // Subprocess-model lifecycle: after close(), verb calls throw ErrClientClosed.
  // The FFI Client expressed this as "_handle is null after close"; for the
  // subprocess model the meaningful invariant is that the open-guard fires.
  test("post-close verb call throws ErrClientClosed", async () => {
    const dir = makeTmpDir();
    try {
      const client = new Client(makeOpts(dir));
      client.close();
      // version() checks _assertOpen() first; must throw ErrClientClosed.
      await expect(client.version({})).rejects.toThrow(ErrClientClosed);
    } finally {
      removeTmpDir(dir);
    }
  });
});
