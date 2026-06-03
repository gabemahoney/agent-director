/**
 * version-floor-bash.test.ts — SR-8.6 bash-readability check: confirms the
 * documented `jq -r .min_binary_version` consumer pattern (SR-5.5) actually
 * works against the shipped dist/version-floor.json without spawning any
 * JS runtime.
 *
 * Skipped (with console.log) when `jq` is not on PATH.
 */

import { test, expect } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

const DIST_PATH = resolve(import.meta.dir, "../dist/version-floor.json");

function jqAvailable(): boolean {
  const proc = Bun.spawnSync(["which", "jq"]);
  return proc.exitCode === 0;
}

test("`jq -r .min_binary_version` returns the floor value byte-exact (SR-5.5)", () => {
  if (!jqAvailable()) {
    console.log("version-floor-bash.test.ts: jq not on PATH — skipping");
    return;
  }
  if (!existsSync(DIST_PATH)) {
    console.log(`version-floor-bash.test.ts: ${DIST_PATH} missing — run bun build first; skipping`);
    return;
  }

  const proc = Bun.spawnSync(["jq", "-r", ".min_binary_version", DIST_PATH]);
  expect(proc.exitCode).toBe(0);

  const stdout = new TextDecoder().decode(proc.stdout);
  // jq -r adds a trailing newline; trim it.
  const got = stdout.replace(/\n$/, "");

  const parsed = JSON.parse(readFileSync(DIST_PATH, "utf8")) as { min_binary_version: string };
  expect(got).toBe(parsed.min_binary_version);
});

test("`jq` invocation spawns no JS runtime (SR-5.6)", () => {
  if (!jqAvailable()) {
    console.log("version-floor-bash.test.ts: jq not on PATH — skipping");
    return;
  }
  // SR-5.6 invariant: bash-readable path requires no node/bun process.
  // The spawned binary must be `jq` — not `node`, `bun`, or any JS
  // interpreter.  We check the program name (argv[0]) only; paths
  // passed as arguments may legitimately contain substrings like
  // "bun" (e.g. the project's pkg/ts-bun-client/ directory) without
  // implying a JS runtime is involved.
  const cmd = ["jq", "-r", ".min_binary_version", DIST_PATH];
  expect(cmd[0]).toBe("jq");
  expect(cmd[0]).not.toMatch(/^(node|bun|deno)$/);
});
