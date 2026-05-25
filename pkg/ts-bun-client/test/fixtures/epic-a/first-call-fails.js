#!/usr/bin/env bun
// first-call-fails.js — exits 1 on the first invocation, then succeeds.
// State is stored in CALL_MARKER_FILE (set as env var).
// Used by subprocess-client.test.ts to verify rejection-does-not-wedge-queue.
import { existsSync, writeFileSync } from "fs";

const marker = process.env.CALL_MARKER_FILE;
if (!marker) {
  process.stderr.write("[first-call-fails] CALL_MARKER_FILE env var not set\n");
  process.exit(2);
}

if (!existsSync(marker)) {
  // First call: create the marker file and exit non-zero.
  writeFileSync(marker, "called");
  process.stderr.write("intentional first-call failure\n");
  process.exit(1);
}

// Subsequent calls: succeed.
process.stdout.write('{"version":"ok-after-fail","commit":"abc123"}\n');
