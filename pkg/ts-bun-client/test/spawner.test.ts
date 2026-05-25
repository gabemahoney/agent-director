/**
 * spawner.test.ts — unit tests for src/internal/spawner.ts (Task A4).
 *
 * Tests SRD SR-1.1 (Bun.spawn, detached:false), SR-1.3 (concurrent stdout/stderr
 * drain), SR-1.5 (exit semantics), SR-7.1 (concurrent drain via Response),
 * SR-7.2 (no artificial size cap), SR-7.3 (stderr preserved for diagnostics).
 *
 * Four behaviour cases:
 *   1. success     — valid JSON stdout + exit 0 → returns {stdout, stderr, exitCode:0, signalCode:null}
 *   2. large-stdout — >64 KB stdout + exit 0 → no deadlock, all bytes captured
 *   3. nonzero-exit — exit 1 + garbage stdout → throws subprocess-crash error
 *                     (exitCode === 1, stderr field populated)
 *   4. non-JSON    — "not json\n" on stdout + exit 0 → throws subprocess-crash error
 *                     (parse failure)
 *
 * IMPORT NOTE: Expected export from src/internal/spawner.ts:
 *   runSubprocess(argv: string[]): Promise<{ stdout: string; stderr: string;
 *                                            exitCode: number | null;
 *                                            signalCode: string | null }>
 * Throws (exact class TBD by engineer) on non-zero exit or non-JSON stdout.
 * If the engineer uses a different function name, update the import below.
 */

import { test, expect, describe } from "bun:test";
import * as path from "node:path";
import { runSubprocess } from "../src/internal/spawner.js";

const FIXTURES = path.resolve(import.meta.dir, "fixtures/epic-a");

// ---------------------------------------------------------------------------
// Case 1: success
// ---------------------------------------------------------------------------
describe("spawner — success path", () => {
  test("valid JSON on stdout + exit 0 → returns {stdout, stderr, exitCode:0, signalCode:null}", async () => {
    const result = await runSubprocess([path.join(FIXTURES, "success.sh")]);

    expect(result).toBeDefined();
    expect(result.exitCode).toBe(0);
    expect(result.signalCode).toBeNull();

    // stdout must be a non-empty string containing the JSON we emitted.
    expect(typeof result.stdout).toBe("string");
    expect(result.stdout.trim()).toContain("ok");

    // Verify it is valid JSON (the spawner may return raw string or parsed).
    let parsed: unknown;
    expect(() => {
      parsed = JSON.parse(typeof result.stdout === "string" ? result.stdout : "{}");
    }).not.toThrow();
    expect((parsed as Record<string, unknown>)["result"]).toBe("ok");
  });
});

// ---------------------------------------------------------------------------
// Case 2: large stdout (SR-7.1, SR-7.2 — no deadlock, no size cap)
// ---------------------------------------------------------------------------
describe("spawner — large stdout", () => {
  test(
    ">64 KB stdout → no deadlock, all bytes captured",
    async () => {
      // large-stdout.js emits ~100 KB of JSON.  If the spawner doesn't drain
      // concurrently it will deadlock (kernel pipe buffer fills).
      const result = await runSubprocess(["bun", path.join(FIXTURES, "large-stdout.js")]);

      expect(result.exitCode).toBe(0);
      expect(result.signalCode).toBeNull();

      // The returned stdout must contain at least 64 KB worth of 'x' characters.
      // (Either the raw JSON string or the parsed data field.)
      const rawOrData =
        typeof result.stdout === "string"
          ? result.stdout
          : JSON.stringify(result.stdout);
      expect(rawOrData.length).toBeGreaterThan(64 * 1024);
    },
    { timeout: 15000 }
  );
});

// ---------------------------------------------------------------------------
// Case 3: non-zero exit → subprocess-crash throw
// ---------------------------------------------------------------------------
describe("spawner — non-zero exit", () => {
  test("exit 1 + garbage stdout → throws with exitCode === 1 and stderr info", async () => {
    let caught: unknown;
    try {
      await runSubprocess([path.join(FIXTURES, "nonzero-exit.sh")]);
    } catch (e) {
      caught = e;
    }

    expect(caught).toBeDefined();
    expect(caught).toBeInstanceOf(Error);

    const err = caught as Error & Record<string, unknown>;
    // exitCode must be surfaced — either as a field or in the message.
    const hasExitCode =
      err["exitCode"] === 1 ||
      err["exit_code"] === 1 ||
      err.message.includes("1");
    expect(hasExitCode).toBe(true);

    // Stderr content ("something went wrong in subprocess") must be present
    // somewhere in the thrown error (message or dedicated field).
    const stderrContent = "something went wrong in subprocess";
    const hasStderr =
      err.message.includes(stderrContent) ||
      (typeof err["stderr"] === "string" &&
        (err["stderr"] as string).includes(stderrContent));
    expect(hasStderr).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Case 4: non-JSON stdout + exit 0 → subprocess-crash throw (parse failed)
// ---------------------------------------------------------------------------
describe("spawner — non-JSON stdout", () => {
  test("'not json\\n' on stdout + exit 0 → throws (parse failure)", async () => {
    let caught: unknown;
    try {
      await runSubprocess([path.join(FIXTURES, "non-json.sh")]);
    } catch (e) {
      caught = e;
    }

    expect(caught).toBeDefined();
    expect(caught).toBeInstanceOf(Error);

    // The error must be identifiable as a parse failure, not an exit-code failure.
    // Accept either a dedicated field or a message hint.
    const err = caught as Error & Record<string, unknown>;
    const isParseError =
      err.message.toLowerCase().includes("parse") ||
      err.message.toLowerCase().includes("json") ||
      err["exitCode"] === 0 || // exit 0 + throw → must be a parse issue
      err["reason"] === "parse_failed";
    expect(isParseError).toBe(true);
  });
});
