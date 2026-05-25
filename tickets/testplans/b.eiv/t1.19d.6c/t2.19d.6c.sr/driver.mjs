// subprocess-client-2-serialization driver — Epic C / SR-10.4 + CSCB SR-0.
//
// Mirrors pkg/ts-bun-client/test/client-serialization.test.ts against the
// installed-tarball layout.
//
// Two assertions:
//   1. "OK serialization-5-parallel": 5 parallel spawn() calls on one Client
//      resolve in submission order, with min consecutive gap > 5 ms (rules
//      out actual parallel execution).
//   2. "OK two-client-overlap": two independent Clients can both have a
//      verb in flight at once without deadlock.

import * as path from "node:path";
import { Client } from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const FAKE_TMUX = process.env.FAKE_TMUX_BIN ?? "/work/source/test/fake-tmux/tmux";

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

const client = new Client({ storePath: STORE, createIfMissing: true, tmuxCommand: FAKE_TMUX });
try {
  const N = 5;
  const labels = Array.from({ length: N }, (_, i) => `seq=${i}`);
  const resolutionTimes = new Array(N).fill(null);

  const calls = labels.map((lbl, idx) =>
    client.spawn({ cwd: HOME, label: [lbl] }).then((r) => {
      resolutionTimes[idx] = performance.now();
      return r;
    }),
  );
  const results = await Promise.all(calls);

  if (results.length !== N) fail(`serialization: expected ${N} results, got ${results.length}`);
  for (const r of results) {
    if (typeof r.claude_instance_id !== "string" || r.claude_instance_id.length === 0) {
      fail("serialization: bad claude_instance_id");
    }
  }
  for (let i = 0; i < N; i++) {
    if (resolutionTimes[i] === null) fail(`serialization: missing resolution time at i=${i}`);
  }

  // Resolution order must match submission order — strict monotonic increase.
  for (let i = 1; i < N; i++) {
    if (!(resolutionTimes[i] > resolutionTimes[i - 1])) {
      fail(
        `serialization: out-of-order resolution at i=${i}: ` +
          `${resolutionTimes[i]} <= ${resolutionTimes[i - 1]}`,
      );
    }
  }

  // No-overlap floor: each subprocess invocation has a measurable cost. A
  // parallel run would yield near-simultaneous resolutions (< 1 ms gaps).
  let minGap = Infinity;
  for (let i = 1; i < N; i++) {
    minGap = Math.min(minGap, resolutionTimes[i] - resolutionTimes[i - 1]);
  }
  if (!(minGap > 5)) fail(`serialization: minGap=${minGap}ms too small (parallel execution suspected)`);

  // Cross-check the store: every seq label maps to a distinct row, and
  // started_at is monotonically non-decreasing in submission index
  // (CURRENT_TIMESTAMP has second-level resolution, so equality is allowed).
  const listed = await client.list({});
  const indexOf = new Map();
  for (const row of listed.spawns) {
    const seq = row.labels?.seq;
    if (typeof seq === "string" && /^\d+$/.test(seq)) {
      indexOf.set(parseInt(seq, 10), row.started_at);
    }
  }
  if (indexOf.size !== N) fail(`serialization: expected ${N} seq rows in store, got ${indexOf.size}`);
  const storeTimes = [];
  for (let i = 0; i < N; i++) {
    const ts = indexOf.get(i);
    if (!ts) fail(`serialization: missing store row for seq=${i}`);
    storeTimes.push(ts);
  }
  for (let i = 1; i < N; i++) {
    if (!(storeTimes[i] >= storeTimes[i - 1])) {
      fail(`serialization: store started_at out of order at i=${i}: ${storeTimes[i]} < ${storeTimes[i - 1]}`);
    }
  }
  console.log("OK serialization-5-parallel");

  // ── two-Client overlap (SR-3.2) ─────────────────────────────────────────
  const clientB = new Client({ storePath: STORE, createIfMissing: true, tmuxCommand: FAKE_TMUX });
  try {
    const [vA, vB] = await Promise.all([client.version({}), clientB.version({})]);
    if (typeof vA.version !== "string" || typeof vB.version !== "string") {
      fail("two-client-overlap: missing version string");
    }
    console.log("OK two-client-overlap");
  } finally {
    clientB[Symbol.dispose]?.();
  }
} finally {
  client[Symbol.dispose]?.();
}
