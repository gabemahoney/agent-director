#!/usr/bin/env bun
// sleep-and-respond.js — sleeps SLEEP_MS milliseconds then emits version JSON.
// Used by subprocess-client.test.ts for timeout and signal tests.
// Set process.env.SLEEP_MS before spawning (default: 5000).
const ms = parseInt(process.env.SLEEP_MS ?? "5000", 10);
await new Promise((resolve) => setTimeout(resolve, ms));
process.stdout.write('{"version":"fixture-1.0.0","commit":"aabbccddeeff"}\n');
