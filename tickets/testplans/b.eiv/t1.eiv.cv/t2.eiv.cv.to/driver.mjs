// subprocess-coverage-2-timeout driver.
//
// Validates that callTimeoutMs (default 30000 ms; here forced to 500 ms)
// fires SIGTERM → 2 s grace → SIGKILL on a non-exiting subprocess and
// rejects the call with ErrCallTimeout (SRD SR-6.1 / SR-6.2 / SR-6.5).
//
// The stub CLI binary `exec sleep 5`s; SIGTERM lands on the sleep process
// directly (no bash trapping). The SIGKILL fallback path is exercised only
// when SIGTERM is ignored — here it is not, so the grace timer cancels via
// proc.exited and the run wraps in well under 1 s.

import * as path from "node:path";
import { Client, ErrCallTimeout } from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const TIMEOUT_MS = 500;

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

const client = new Client({
  storePath: STORE,
  createIfMissing: true,
  callTimeoutMs: TIMEOUT_MS,
});
try {
  const startedAt = Date.now();
  let caught = null;
  try {
    await client.version({});
  } catch (e) {
    caught = e;
  }
  const elapsed = Date.now() - startedAt;

  if (caught === null) fail(`call-timeout: call resolved instead of rejecting (elapsed=${elapsed}ms)`);
  if (!(caught instanceof ErrCallTimeout)) {
    fail(`call-timeout: expected ErrCallTimeout, got ${caught?.constructor?.name}: ${caught?.message}`);
  }
  if (caught.verb !== "version") fail(`call-timeout: verb=${caught.verb} (expected "version")`);
  if (caught.timeoutMs !== TIMEOUT_MS) {
    fail(`call-timeout: timeoutMs=${caught.timeoutMs} (expected ${TIMEOUT_MS})`);
  }
  // elapsedMs should reflect roughly the configured timeout; allow generous
  // slack on both sides for CI jitter and SIGTERM teardown.
  if (caught.elapsedMs < 400 || caught.elapsedMs > 5000) {
    fail(`call-timeout: elapsedMs=${caught.elapsedMs} outside [400, 5000]`);
  }
  // Wall-clock should not have required the full SIGKILL grace window.
  if (elapsed > 3500) {
    fail(`call-timeout: wall-clock elapsed=${elapsed}ms exceeded ~3.5 s (SIGTERM not honored?)`);
  }
  console.log(
    `OK call-timeout rejects with ErrCallTimeout (elapsedMs=${caught.elapsedMs}, wall=${elapsed}ms)`
  );
} finally {
  client[Symbol.dispose]?.();
}
