// subprocess-coverage-3-large-stdout driver.
//
// Validates that the Client drains stdout concurrently with the subprocess
// (SRD SR-7.1). The stub CLI prints ~200 KB of JSON in a single write,
// well past the Linux pipe-buffer size (~64 KB). A Client that drained
// stdout *after* waiting for `proc.exited` would deadlock here; the
// implementation must `Promise.all` the drains with the exit wait.

import * as path from "node:path";
import { Client } from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const EXPECTED_MIN_PAD = 180_000;

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

const HARD_TIMEOUT_MS = 10_000;

const client = new Client({ storePath: STORE, createIfMissing: true });
try {
  // Wrap the call in a race against a wall-clock guard. If the Client
  // genuinely deadlocks on a 200 KB write, the harness's overall budget
  // would still catch it eventually, but we want a clean test-level
  // diagnostic well before that.
  const callPromise = client.version({});
  const guardPromise = new Promise((_, reject) =>
    setTimeout(
      () => reject(new Error(`hard-timeout: call did not return within ${HARD_TIMEOUT_MS}ms`)),
      HARD_TIMEOUT_MS,
    ),
  );

  let v;
  try {
    v = await Promise.race([callPromise, guardPromise]);
  } catch (e) {
    fail(`large-stdout call failed: ${e?.message ?? e}`);
  }

  // b.6o1: the TS Client overrides .version with its own npm package version,
  // so we no longer assert the stub's emitted "0.0.0-stub" string. The point
  // of this case is that the stdout drain survives ~200 KB without deadlock —
  // proven by the _pad field below — not the version field's contents.
  if (typeof v.version !== "string" || v.version.length === 0) {
    fail(`large-stdout: bad version: ${JSON.stringify(v.version)}`);
  }
  if (typeof v.commit !== "string") {
    fail(`large-stdout: bad commit: ${JSON.stringify(v.commit)}`);
  }
  if (typeof v._pad !== "string" || v._pad.length < EXPECTED_MIN_PAD) {
    fail(
      `large-stdout: pad length ${v?._pad?.length ?? "(missing)"} below threshold ${EXPECTED_MIN_PAD}`,
    );
  }
  console.log(`OK large-stdout ${v._pad.length} bytes of pad parsed without deadlock`);
} finally {
  client[Symbol.dispose]?.();
}
