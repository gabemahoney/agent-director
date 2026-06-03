#!/usr/bin/env bun
// sleep-and-respond.js — sleeps SLEEP_MS milliseconds then emits a JSON
// envelope.  Used by subprocess-client.test.ts for timeout and signal tests.
// Set process.env.SLEEP_MS before spawning (default: 5000).
//
// Probe-aware (b.ue3 / SR-1.3): when invoked with the `version` verb,
// respond immediately with a strict-SemVer sentinel envelope so
// Client.create()'s construction-time probe passes without waiting.
// The sleep applies only to non-version invocations (the verb under test).
if (process.argv[2] === "version") {
  process.stdout.write('{"version":"0.0.0-dev","commit":"deadbeef"}\n');
  process.exit(0);
}
const ms = parseInt(process.env.SLEEP_MS ?? "5000", 10);
await new Promise((resolve) => setTimeout(resolve, ms));
process.stdout.write('{"version":"fixture-1.0.0","commit":"aabbccddeeff"}\n');
