#!/usr/bin/env bun
// serialization-recorder.js — records start/end timestamps to LOG_FILE,
// sleeps briefly, then emits version JSON.
// Used by subprocess-client.test.ts to verify per-Client call serialization.
// Set process.env.LOG_FILE to a writable path before spawning.
import { appendFileSync } from "fs";

const logFile = process.env.LOG_FILE;
if (!logFile) {
  process.stderr.write("[serialization-recorder] LOG_FILE env var not set\n");
  process.exit(1);
}

appendFileSync(logFile, `${Date.now()} START\n`);
await new Promise((resolve) => setTimeout(resolve, 50));
appendFileSync(logFile, `${Date.now()} END\n`);

process.stdout.write('{"version":"serial-test","commit":"0000000"}\n');
