/**
 * subprocess-client.test.ts — unit tests for SubprocessClient (Task A6).
 *
 * Tests SRD SR-3 (per-Client serialization queue), SR-5.2 (ErrConsumerSignal),
 * SR-6.1–6.5 (callTimeoutMs validation + ErrCallTimeout), plus a public-surface
 * smoke check that index.ts exports are unchanged.
 *
 * Six behaviour cases:
 *   1. callTimeoutMs <= 0 at construction → throws config error.
 *   2. Serialization: 5 parallel calls execute strictly in order (no overlap).
 *   3. Rejection-does-not-wedge-queue: call N rejects, call N+1 succeeds.
 *   4. Timeout: fixture sleeping > callTimeoutMs → rejects with ErrCallTimeout.
 *   5. Signal: fixture self-SIGINTs → rejects with ErrConsumerSignal.
 *   6. Public-surface smoke: index.ts exports unchanged (Client + typed errors present).
 *
 * IMPORT NOTE: Expected exports from src/internal/subprocessClient.ts:
 *   SubprocessClient class — constructor takes ClientOptions (with callTimeoutMs)
 *     plus a test-only _cliPath?: string option to inject a fixture binary
 *     instead of resolving via platformResolve.
 *   If the engineer names the test hook differently (e.g. _testCliPath or
 *   cliPathOverride), update the opts object in the helper below.
 *
 * The SubprocessClient must be distinct from the public Client (which still
 * uses FFI during Epic A); this file imports from src/internal/ directly.
 *
 * ENV INHERITANCE NOTE (Bun-specific, SR-1.4): Bun.spawn without an explicit
 * `env` option snapshots the OS-level env at process start, not at spawn time.
 * Runtime changes to process.env (e.g., LOG_FILE set in a test) are NOT seen
 * by subprocesses unless the spawner passes `env: { ...process.env }`.
 * SR-1.4 requires the subprocess to inherit the consumer's env at call time;
 * the engineer must use `env: { ...process.env }` in the Bun.spawn call.
 * Tests in this file depend on this behaviour for LOG_FILE / CALL_MARKER_FILE.
 */

import { test, expect, describe, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { SubprocessClient } from "../src/internal/subprocessClient.js";
import { ErrCallTimeout, ErrConsumerSignal } from "../src/errors.js";
// b.6o1: version() now overrides the CLI's version field with the npm
// package version. Import it here so we can assert the post-fix shape.
import pkgJson from "../package.json" with { type: "json" };

// Case 6 reads src/index.ts as source text to avoid triggering the module-level
// FFI bootstrap (bootstrapFfi.ts → resolveNativePath() throws when the native
// .so is absent). The source-text check is a reliable proxy: src/index.ts is a
// pure re-export barrel, so verifying each export name is listed there is
// sufficient to catch accidental removals without loading the FFI module.
const INDEX_SRC = fs.readFileSync(
  path.resolve(import.meta.dir, "../src/index.ts"),
  "utf-8"
);

const FIXTURES = path.resolve(import.meta.dir, "fixtures/epic-a");

// ---------------------------------------------------------------------------
// Temp dir management
// ---------------------------------------------------------------------------
const tmpDirs: string[] = [];

function makeTmpDir(): string {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), "ad-sct-"));
  tmpDirs.push(d);
  return d;
}

afterAll(() => {
  for (const d of tmpDirs.splice(0)) {
    try { fs.rmSync(d, { recursive: true, force: true }); } catch { /* ignore */ }
  }
});

/** Builds a valid ClientOptions object pointing at a fresh temp store. */
function makeOpts(
  overrides: Record<string, unknown> = {}
): Record<string, unknown> {
  const dir = makeTmpDir();
  return {
    storePath: path.join(dir, "state.db"),
    createIfMissing: true,
    callTimeoutMs: 5000,
    ...overrides,
  };
}

/** Constructs a SubprocessClient pointed at a fixture binary for testing. */
function makeClient(fixturePath: string, overrides: Record<string, unknown> = {}): SubprocessClient {
  return new SubprocessClient(
    makeOpts({
      // Test hook: inject a custom binary path so the client uses our fixture
      // instead of the real platformResolve path.
      // Engineer: if you named this differently (e.g. _testCliPath), update here.
      _cliPath: fixturePath,
      ...overrides,
    }) as unknown as ConstructorParameters<typeof SubprocessClient>[0]
  );
}

// ---------------------------------------------------------------------------
// Case 1: callTimeoutMs <= 0 → throws at construction
// ---------------------------------------------------------------------------
describe("SubprocessClient — callTimeoutMs validation", () => {
  test("callTimeoutMs: 0 → throws config error at construction", () => {
    const fixturePath = path.join(FIXTURES, "success.sh");
    expect(() => makeClient(fixturePath, { callTimeoutMs: 0 })).toThrow();
  });

  test("callTimeoutMs: -1 → throws config error at construction", () => {
    const fixturePath = path.join(FIXTURES, "success.sh");
    expect(() => makeClient(fixturePath, { callTimeoutMs: -1 })).toThrow();
  });

  test("callTimeoutMs: 1 (positive) → does NOT throw at construction", () => {
    const fixturePath = path.join(FIXTURES, "success.sh");
    expect(() => makeClient(fixturePath, { callTimeoutMs: 1 })).not.toThrow();
  });

  test("callTimeoutMs omitted → uses default (30000), does NOT throw", () => {
    const fixturePath = path.join(FIXTURES, "success.sh");
    const opts = makeOpts({ _cliPath: fixturePath });
    delete (opts as Record<string, unknown>)["callTimeoutMs"];
    expect(() => new SubprocessClient(opts as unknown as ConstructorParameters<typeof SubprocessClient>[0])).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Case 2: Serialization — 5 parallel calls execute in strictly serial order
// ---------------------------------------------------------------------------
describe("SubprocessClient — per-Client call serialization (SR-3)", () => {
  test(
    "5 concurrent version() calls execute strictly serially (no start/end overlap)",
    async () => {
      const logFile = path.join(makeTmpDir(), "serial.log");
      const prevLogFile = process.env.LOG_FILE;
      process.env.LOG_FILE = logFile;

      try {
        const fixturePath = path.join(FIXTURES, "serialization-recorder.js");
        // The fixture has #!/usr/bin/env bun + chmod +x, so passing the path
        // directly is correct; Bun.spawn() will use the shebang interpreter.
        const client = makeClient(fixturePath, { callTimeoutMs: 15000 });
        // Fire 5 calls simultaneously.
        await Promise.all([
          (client as unknown as { version(p: object): Promise<unknown> }).version({}),
          (client as unknown as { version(p: object): Promise<unknown> }).version({}),
          (client as unknown as { version(p: object): Promise<unknown> }).version({}),
          (client as unknown as { version(p: object): Promise<unknown> }).version({}),
          (client as unknown as { version(p: object): Promise<unknown> }).version({}),
        ]);
      } finally {
        if (prevLogFile === undefined) {
          delete process.env.LOG_FILE;
        } else {
          process.env.LOG_FILE = prevLogFile;
        }
      }

      // Parse the log file: each call writes "T START" then "T END" lines.
      const lines = fs.readFileSync(logFile, "utf-8").trim().split("\n");
      // 5 calls × 2 lines = 10 lines.
      expect(lines.length).toBe(10);

      // Verify no START comes before the previous END.
      interface Entry { ts: number; kind: "START" | "END" }
      const entries: Entry[] = lines.map((l) => {
        const [ts, kind] = l.split(" ");
        return { ts: parseInt(ts, 10), kind: kind as "START" | "END" };
      });

      for (let i = 1; i < entries.length; i++) {
        const prev = entries[i - 1];
        const curr = entries[i];
        if (curr.kind === "START" && prev.kind === "END") {
          // A new call starting after the previous one ended — correct.
          expect(curr.ts).toBeGreaterThanOrEqual(prev.ts);
        } else if (curr.kind === "END" && prev.kind === "START") {
          // Within a single call: END after START — correct.
          expect(curr.ts).toBeGreaterThanOrEqual(prev.ts);
        }
        // Two consecutive STARTs would indicate parallel execution — should not happen.
        expect(!(curr.kind === "START" && prev.kind === "START")).toBe(true);
      }
    },
    { timeout: 30000 }
  );
});

// ---------------------------------------------------------------------------
// Case 3: Rejection does not wedge the queue (SR-3.3)
// ---------------------------------------------------------------------------
describe("SubprocessClient — rejection does not wedge queue", () => {
  test(
    "call N rejects (subprocess-crash) → call N+1 still succeeds",
    async () => {
      const markerFile = path.join(makeTmpDir(), "first-call-marker");
      const prevMarker = process.env.CALL_MARKER_FILE;
      process.env.CALL_MARKER_FILE = markerFile;

      try {
        // Use "bun <script>" as the binary so the fixture JS runs.
        const fixturePath = path.join(FIXTURES, "first-call-fails.js");
        const client = makeClient(fixturePath, { callTimeoutMs: 5000 });

        type ClientWithVersion = { version(p: object): Promise<unknown> };
        const c = client as unknown as ClientWithVersion;

        // First call: fixture exits 1 → spawner throws subprocess-crash error.
        let firstError: unknown;
        try {
          await c.version({});
        } catch (e) {
          firstError = e;
        }
        expect(firstError).toBeDefined();
        expect(firstError).toBeInstanceOf(Error);

        // Second call: fixture succeeds (marker file exists now).
        const result = await c.version({});
        expect(result).toBeDefined();
        // The fixture returns {"version":"ok-after-fail","commit":"abc123"}.
        // b.6o1: the wrapper overrides .version with the npm package version,
        // but .commit still passes through unchanged.
        const r = result as Record<string, unknown>;
        expect(r["version"]).toBe(pkgJson.version);
        expect(r["commit"]).toBe("abc123");
      } finally {
        if (prevMarker === undefined) {
          delete process.env.CALL_MARKER_FILE;
        } else {
          process.env.CALL_MARKER_FILE = prevMarker;
        }
      }
    },
    { timeout: 15000 }
  );
});

// ---------------------------------------------------------------------------
// Case 4: Timeout → ErrCallTimeout (SR-6.2, NOT ErrConsumerSignal)
// ---------------------------------------------------------------------------
describe("SubprocessClient — timeout", () => {
  test(
    "fixture sleeping > callTimeoutMs → rejects with ErrCallTimeout within callTimeoutMs + 4s",
    async () => {
      const prevSleepMs = process.env.SLEEP_MS;
      process.env.SLEEP_MS = "10000"; // 10 s — much longer than callTimeoutMs

      const start = Date.now();
      let caught: unknown;

      try {
        const fixturePath = path.join(FIXTURES, "sleep-and-respond.js");
        const client = makeClient(fixturePath, { callTimeoutMs: 300 });
        type ClientV = { version(p: object): Promise<unknown> };
        await (client as unknown as ClientV).version({});
      } catch (e) {
        caught = e;
      } finally {
        if (prevSleepMs === undefined) {
          delete process.env.SLEEP_MS;
        } else {
          process.env.SLEEP_MS = prevSleepMs;
        }
      }

      const elapsed = Date.now() - start;

      expect(caught).toBeDefined();
      expect(caught).toBeInstanceOf(ErrCallTimeout);
      expect(caught).not.toBeInstanceOf(ErrConsumerSignal);

      const err = caught as InstanceType<typeof ErrCallTimeout>;
      expect(err.name).toBe("ErrCallTimeout");
      expect(err.errName).toBe("ErrCallTimeout");

      // Must have resolved within callTimeoutMs(300) + 2s graceful + 2s test margin.
      expect(elapsed).toBeLessThan(300 + 2000 + 2000);
    },
    { timeout: 10000 }
  );
});

// ---------------------------------------------------------------------------
// Case 5: Signal → ErrConsumerSignal (SR-5.2)
// ---------------------------------------------------------------------------
describe("SubprocessClient — signal handling", () => {
  test(
    "fixture self-SIGINTs → rejects with ErrConsumerSignal",
    async () => {
      let caught: unknown;
      try {
        const fixturePath = path.join(FIXTURES, "self-sigint.sh");
        const client = makeClient(fixturePath, { callTimeoutMs: 5000 });
        type ClientV = { version(p: object): Promise<unknown> };
        await (client as unknown as ClientV).version({});
      } catch (e) {
        caught = e;
      }

      expect(caught).toBeDefined();
      expect(caught).toBeInstanceOf(ErrConsumerSignal);

      const err = caught as InstanceType<typeof ErrConsumerSignal>;
      expect(err.name).toBe("ErrConsumerSignal");
      expect(err.errName).toBe("ErrConsumerSignal");

      // Signal name must be surfaced somewhere.
      const e = err as unknown as Record<string, unknown>;
      const hasSignalInfo =
        err.message.includes("SIGINT") ||
        (typeof e["signal"] === "string" && e["signal"] === "SIGINT");
      expect(hasSignalInfo).toBe(true);
    },
    { timeout: 15000 }
  );
});

// ---------------------------------------------------------------------------
// b.6o1 regression: version() returns npm package version, not CLI build stamp
// ---------------------------------------------------------------------------
describe("SubprocessClient — version() returns npm package version (b.6o1)", () => {
  test(
    "CLI returns git-describe version → wrapper overrides with pkg.json version; commit passes through",
    async () => {
      // sleep-and-respond.js emits {"version":"fixture-1.0.0","commit":"aabbccddeeff"}.
      // With SLEEP_MS=0 it responds immediately, simulating a CLI build-stamp
      // version distinct from the npm package version.
      const prevSleepMs = process.env.SLEEP_MS;
      process.env.SLEEP_MS = "0";

      try {
        const fixturePath = path.join(FIXTURES, "sleep-and-respond.js");
        const client = makeClient(fixturePath, { callTimeoutMs: 5000 });
        type ClientV = { version(p: object): Promise<{ version: string; commit: string }> };
        const result = await (client as unknown as ClientV).version({});

        // The wrapper substitutes its own pkg.json version regardless of what
        // the CLI emitted ("fixture-1.0.0" here).
        expect(result.version).toBe(pkgJson.version);
        expect(result.version).not.toBe("fixture-1.0.0");
        // commit is passed through unchanged from the CLI envelope.
        expect(result.commit).toBe("aabbccddeeff");
      } finally {
        if (prevSleepMs === undefined) {
          delete process.env.SLEEP_MS;
        } else {
          process.env.SLEEP_MS = prevSleepMs;
        }
      }
    },
    { timeout: 10000 }
  );
});

// ---------------------------------------------------------------------------
// Case 6: Public surface smoke — index.ts exports unchanged
// ---------------------------------------------------------------------------
describe("SubprocessClient — public index.ts surface unchanged", () => {
  // These are the exports that existed before Epic A. All must still be present.
  const expectedExports = [
    "Client",
    "AgentDirectorError",
    "ErrClientClosed",
    "ErrUnsupportedPlatform",
    "ErrPlatformPackageMissing",
    "ErrBunVersionTooOld",
    "errorFromEnvelope",
    // Catalog-derived (33 entries).
    "ErrCwdMissing",
    "ErrCwdNotAPath",
    "ErrCwdNotFound",
    "ErrCwdNotADirectory",
    "ErrRelayModeInvalid",
    "ErrSpawnDeniedFlag",
    "ErrReservedEnvKey",
    "ErrInstanceIdCollision",
    "ErrTmuxSessionNameEmpty",
    "ErrTmuxSessionNameInvalid",
    "ErrTmuxSessionNameTooLong",
    "ErrSpawnNotFound",
    "ErrTmuxNotAvailable",
    "ErrTmuxSessionCreate",
    "ErrTmuxSendKeys",
    "ErrTmuxCaptureFailed",
    "ErrSpawnNotInteractive",
    "ErrSendKeysWhileRelayed",
    "ErrSpawnNotPausable",
    "ErrPauseTimeout",
    "ErrListInvalidLabel",
    "ErrTemplateNameUnsafe",
    "ErrTemplateNotFound",
    "ErrTemplateMalformed",
    "ErrTemplateExists",
    "ErrProbeUnsupported",
    "ErrSpawnNotResumable",
    "ErrNoSessionId",
    "ErrJsonlMissing",
    "ErrRelayModeOff",
    "ErrInvalidDecision",
    "ErrNoOpenPermissionRequest",
    "ErrAlreadyDecided",
  ] as const;

  test.each([...expectedExports])("index.ts still exports %s", (name) => {
    expect(INDEX_SRC).toContain(name);
  });

  test("Epic-A additions are ALSO exported (additive only)", () => {
    // These should be present after Task A1.
    expect(INDEX_SRC).toContain("ErrCliNotExecutable");
    expect(INDEX_SRC).toContain("ErrConsumerSignal");
    expect(INDEX_SRC).toContain("ErrCallTimeout");
    expect(INDEX_SRC).toContain("ErrUnknownErrorName");
  });
});
