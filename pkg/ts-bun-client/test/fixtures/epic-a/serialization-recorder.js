#!/usr/bin/env bun
// serialization-recorder.js — records start/end timestamps to LOG_FILE,
// sleeps briefly, then emits a JSON envelope.
// Used by subprocess-client.test.ts to verify per-Client call serialization.
// Set process.env.LOG_FILE to a writable path before spawning.
//
// Probe-aware (b.ue3 / SR-1.3): when invoked with the `version` verb,
// emit the sentinel envelope and exit immediately so Client.create()'s
// construction-time probe passes (the probe scrubs env per SR-1.3.3, so
// LOG_FILE would be absent during the probe regardless).
import { appendFileSync } from "fs";

if (process.argv[2] === "version") {
  process.stdout.write('{"version":"0.0.0-dev","commit":"deadbeef"}\n');
  process.exit(0);
}

const logFile = process.env.LOG_FILE;
if (!logFile) {
  process.stderr.write("[serialization-recorder] LOG_FILE env var not set\n");
  process.exit(1);
}

appendFileSync(logFile, `${Date.now()} START\n`);
await new Promise((resolve) => setTimeout(resolve, 50));
appendFileSync(logFile, `${Date.now()} END\n`);

process.stdout.write('{"version":"0.0.0-dev","commit":"deadbeef"}\n');
