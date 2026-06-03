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
 * The process-count signal uses `pgrep -c agent-director` because pgrep
 * filters by command name and ignores unrelated host processes, so the
 * assertion can be strict equality (not a tolerance).
 *
 * Runtime budget: ~50ms/call × 1000 calls = ~50s, comfortably under the
 * SRD's ~3-minute ceiling.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { Client } from "../src/index.js";

const isLinux = process.platform === "linux";

/** Counts running agent-director processes via pgrep. Returns 0 when none. */
function countAgentDirectorProcesses(): number {
  // pgrep -c <name> prints the count of matching processes and exits 0 even
  // when count is 0 (on modern util-linux >= 2.36; earlier versions exit 1
  // on zero matches). Bun.spawnSync handles either case.
  const proc = Bun.spawnSync({
    cmd: ["pgrep", "-c", "agent-director"],
    stdout: "pipe",
    stderr: "pipe",
  });
  const out = new TextDecoder().decode(proc.stdout).trim();
  const n = parseInt(out, 10);
  return Number.isFinite(n) ? n : 0;
}

test.skipIf(!isLinux)(
  "no-leak: 1000 sequential list({state: 'check_permission'}) calls leak zero processes",
  async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");

      // Baseline count BEFORE the client is constructed. This includes any
      // pre-existing agent-director processes (e.g. an outer parent in CI
      // running these tests under itself). The post-loop count must equal
      // this baseline exactly — net delta zero.
      const baseline = countAgentDirectorProcesses();

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

      const after = countAgentDirectorProcesses();
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
