// subprocess-coverage-1-signal driver.
//
// Validates that a consumer-delivered OS signal on the in-flight CLI
// subprocess surfaces as ErrConsumerSignal (SRD SR-5.2 / SR-5.4) and that
// the Client's per-instance serialization queue is not wedged afterwards
// (SR-3.3) — a subsequent call must succeed.
//
// The stub CLI binary (installed by the case's bash script at
// ${HOME}/proj/node_modules/@agent-director/linux-x64/bin/agent-director)
// reads /tmp/sub-cov-sg-mode to decide its behavior per call.

import * as path from "node:path";
import { writeFileSync } from "node:fs";
import { Client, ErrConsumerSignal } from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const MODE_FILE = "/tmp/sub-cov-sg-mode";

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

const client = new Client({ storePath: STORE, createIfMissing: true });
try {
  // ── Call 1: stub self-kills with SIGTERM ─────────────────────────────────
  writeFileSync(MODE_FILE, "signal");
  let caught = null;
  try {
    await client.version({});
  } catch (e) {
    caught = e;
  }
  if (caught === null) fail("signal-kill: call resolved instead of rejecting");
  if (!(caught instanceof ErrConsumerSignal)) {
    fail(
      `signal-kill: expected ErrConsumerSignal, got ${caught?.constructor?.name}: ${caught?.message}`
    );
  }
  if (caught.signal !== "SIGTERM") {
    fail(`signal-kill: expected signal=SIGTERM, got ${caught.signal}`);
  }
  console.log(`OK signal-kill rejects with ErrConsumerSignal (signal=${caught.signal})`);

  // ── Call 2: queue must accept follow-up ──────────────────────────────────
  writeFileSync(MODE_FILE, "ok");
  const v = await client.version({});
  if (typeof v.version !== "string" || v.version.length === 0) {
    fail(`follow-up: bad version envelope: ${JSON.stringify(v)}`);
  }
  if (typeof v.commit !== "string") {
    fail(`follow-up: bad commit field: ${JSON.stringify(v)}`);
  }
  console.log(`OK follow-up call succeeded after signal (version=${v.version})`);
} finally {
  client[Symbol.dispose]?.();
}
