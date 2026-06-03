/**
 * client-serialization.test.ts — Epic C / SR-10.4.
 *
 * Verifies the CSCB SR-0 invariant: each `Client` instance serializes verb
 * calls through a private chained-Promise queue, with at-most-one subprocess
 * in flight at a time. Two *independent* Clients (with separate queues) may
 * still overlap (SR-3.2).
 *
 * Observability hook (per SRD Open Tech Question #2):
 *   Picked: existing-verb side-effects + per-call client-side `.then()`
 *   timing. The SQLite store's `started_at` column has CURRENT_TIMESTAMP
 *   second-level resolution (see internal/store/schema.go), so two
 *   sequential subprocess invocations that complete within the same second
 *   yield equal `started_at` strings. To get sub-second observability we
 *   attach a `.then()` to each of the 5 spawn calls that records
 *   `performance.now()` when the call resolves. Under serial execution the
 *   resolution order matches submission order; under parallel execution
 *   resolution order would be non-deterministic and the gaps would be
 *   near-zero (well below subprocess startup cost).
 *
 *   The fallback `--debug-sleep-ms` flag was rejected because (a) it adds
 *   surface to the binary and (b) it diverges from the canonical
 *   observability path used by envelope-diff. The chosen hook needs no
 *   binary changes.
 */

import { test, expect, describe } from "bun:test";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper } from "./internal/helper.js";
import { Client } from "../src/index.js";
import type { SpawnResult, ListResult } from "../src/index.js";

const FAKE_TMUX_BIN = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../test/fake-tmux"),
  "tmux"
);

const OUTER_INSTANCE_ID = process.env.AGENT_DIRECTOR_INSTANCE_ID;

function maybeSeedOuterParent(storePath: string): void {
  if (OUTER_INSTANCE_ID) {
    runHelper("seed-spawn", {
      store: storePath,
      id: OUTER_INSTANCE_ID,
      state: "working",
      "create-store": true,
    });
  }
}

describe("Per-Client serialization (SR-10.4 / SR-3)", () => {
  test(
    "5 parallel spawn() calls on one Client execute serially in submission order",
    async () => {
      await withTempHome(async (homeDir) => {
        const storePath = path.join(homeDir, ".agent-director", "state.db");
        maybeSeedOuterParent(storePath);

        using client = await Client.create({
          storePath,
          createIfMissing: true,
          tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
        } as any);

        // Submit 5 spawn calls in deterministic order. Attach a .then() to
        // each that captures performance.now() at call-resolution time —
        // this is the sub-second client-side observability hook.
        const N = 5;
        const submissionLabels = Array.from({ length: N }, (_, i) => `seq=${i}`);
        const resolutionTimes: Array<number | null> = Array(N).fill(null);
        const calls = submissionLabels.map((lbl, idx) =>
          client
            .spawn({ cwd: homeDir, label: [lbl] })
            .then((r) => {
              resolutionTimes[idx] = performance.now();
              return r;
            })
        );
        const results: SpawnResult[] = await Promise.all(calls);

        // All 5 succeeded.
        expect(results).toHaveLength(N);
        for (const r of results) {
          expect(typeof r.claude_instance_id).toBe("string");
          expect(r.claude_instance_id.length).toBeGreaterThan(0);
        }
        for (let i = 0; i < N; i++) {
          expect(resolutionTimes[i]).not.toBeNull();
        }
        const times = resolutionTimes.map((t) => t as number);

        // Submission-order assertion: under serial execution each call
        // resolves *after* the previous one, so resolutionTimes must be
        // strictly increasing by submission index.
        for (let i = 1; i < N; i++) {
          expect(times[i]).toBeGreaterThan(times[i - 1]);
        }

        // No-overlap proxy: each subprocess invocation has a measurable
        // floor (build argv + Bun.spawn + CLI startup + SQLite open + row
        // write + reap). A parallel-execution variant would yield
        // near-simultaneous resolutions (< 1 ms apart, well below the
        // observed floor). Assert that the minimum consecutive gap is
        // above a conservative floor of 5 ms.
        let minGap = Infinity;
        for (let i = 1; i < N; i++) {
          minGap = Math.min(minGap, times[i] - times[i - 1]);
        }
        expect(minGap).toBeGreaterThan(5);

        // Cross-check against the store: every submitted seq label is
        // recorded in a distinct row, and `started_at` is non-decreasing
        // by submission index (second-level resolution, so equality is
        // permitted).
        const listed: ListResult = await client.list({});
        const indexOf = new Map<number, string>();
        for (const row of listed.spawns) {
          const seq = row.labels?.["seq"];
          if (typeof seq === "string" && /^\d+$/.test(seq)) {
            indexOf.set(parseInt(seq, 10), row.started_at);
          }
        }
        expect(indexOf.size).toBe(N);
        const storeTimes: string[] = [];
        for (let i = 0; i < N; i++) {
          const ts = indexOf.get(i);
          expect(ts).toBeDefined();
          storeTimes.push(ts!);
        }
        for (let i = 1; i < N; i++) {
          expect(storeTimes[i] >= storeTimes[i - 1]).toBe(true);
        }
      });
    },
    60_000
  );

  test(
    "two independent Clients may overlap (SR-3.2): both complete without deadlock",
    async () => {
      await withTempHome(async (homeDir) => {
        const storePath = path.join(homeDir, ".agent-director", "state.db");
        maybeSeedOuterParent(storePath);

        // Two clients, same store. Each has its own private queue.
        using clientA = await Client.create({
          storePath,
          createIfMissing: true,
          tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
        } as any);
        using clientB = await Client.create({
          storePath,
          createIfMissing: true,
          tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
        } as any);

        // Fire one verb call on each client simultaneously. Independent
        // queues mean both subprocess invocations may overlap; the test
        // asserts only that both complete (no deadlock, no cross-queue
        // serialization bug).
        const [vA, vB] = await Promise.all([
          clientA.version({}),
          clientB.version({}),
        ]);
        expect(typeof vA.version).toBe("string");
        expect(typeof vB.version).toBe("string");
      });
    },
    30_000
  );
});
