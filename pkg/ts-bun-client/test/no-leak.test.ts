/**
 * no-leak.test.ts — Epic C / SR-10.5.
 *
 * Linux-gated test that captures an agent-director process-count baseline,
 * issues 1000 sequential `client.list({state: "check_permission"})` calls,
 * and asserts the post-test process count is identical to the baseline
 * (delta = 0). Each subprocess invocation must be fully reaped before the
 * next call starts; any leak would manifest as a positive delta.
 *
 * Skipped on darwin via `process.platform === "linux"` per SR-10.5.
 *
 * The counter is parent-scoped to direct children of this bun test process so
 * sibling coverage gates' agent-director spawns don't perturb the delta — see
 * gates/README.md "Coverage phase (parallel)" and bee b.3jn (full rationale
 * lives with the shared counter in ./internal/processCount.ts).
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { countChildAgentDirectorProcesses } from "./internal/processCount.js";
import { Client } from "../src/index.js";

const isLinux = process.platform === "linux";

test.skipIf(!isLinux)(
  "no-leak: 1000 sequential list({state: 'check_permission'}) calls leak zero processes",
  async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");

      // Baseline count BEFORE the client is constructed. Scoped to direct
      // children of this process, so an outer parent running these tests under
      // itself is NOT a child of process.pid and is excluded from the baseline.
      // The post-loop count must equal this baseline exactly — net delta zero.
      const baseline = countChildAgentDirectorProcesses();

      using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

      // Issue 1000 sequential list calls. The list verb is handle-free, fast,
      // and exercises the full subprocess spawn → reap cycle each call.
      const iterations = 1000;
      for (let i = 0; i < iterations; i++) {
        // Use a check_permission filter as SR-10.5 specifies; it produces an
        // empty result against an empty store and exits exit-code 0.
        await client.list({ state: ["check_permission"] });
      }

      // Brief settling tick to let any in-flight reap finish before we sample.
      // Bun.spawn awaits proc.exited before resolving the call, so this is
      // belt-and-suspenders; the assertion is unaffected by removing it.
      await new Promise((r) => setTimeout(r, 50));

      const after = countChildAgentDirectorProcesses();
      expect(after).toBe(baseline);
    });
  },
  // Generous timeout: 1000 × ~80ms worst-case = 80s plus margin.
  180_000
);

test.skipIf(isLinux)(
  "no-leak: skipped on darwin (SR-10.5 Linux-only)",
  () => {
    expect(true).toBe(true);
  }
);
