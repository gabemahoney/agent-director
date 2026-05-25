#!/usr/bin/env bun
// large-stdout.js — emits a >64KB valid JSON payload on stdout, exits 0.
// Used by spawner.test.ts to verify no deadlock when stdout fills kernel pipe buffer.
// SR-7.1: concurrent drain via Response; SR-7.2: no artificial stdout cap.
const data = "x".repeat(102400); // 100 KB of 'x'
process.stdout.write(JSON.stringify({ result: "ok", data }) + "\n");
