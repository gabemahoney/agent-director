/**
 * verify-installed-pkg.test.ts — the scripts/verify-installed-pkg.ts driver's
 * shape: --smoke and --full pass against the in-repo Client (AD_VERIFY_AGAINST)
 * and CLI (AD_CLI_PATH), a bad module fails cleanly, the flags are validated,
 * and each --full gauntlet sub-step reports its own FAIL line when the
 * fake-client fixture breaks it.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const PKG_ROOT = path.resolve(import.meta.dir, "..");
const SRC_INDEX = path.join(PKG_ROOT, "src/index.ts");
const FAKE_CLIENT = path.join(import.meta.dir, "fixtures/fake-client/index.ts");
const CRASH = /UnhandledPromiseRejection|at Object\.<anonymous>|Error: Cannot find module/;

async function runDriver(args: string[], env: Record<string, string> = {}) {
  const proc = Bun.spawn(["bun", "run", "scripts/verify-installed-pkg.ts", ...args], {
    cwd: PKG_ROOT,
    env: { ...process.env, ...env },
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
  });
  const [stdout, stderr] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text()]);
  await proc.exited;
  return { exitCode: proc.exitCode, stdout, stderr };
}

test.each(["--smoke", "--full"])(
  "%s happy path: exits 0 against the in-repo Client and CLI",
  async (flag) => {
    const r = await runDriver([flag], { AD_VERIFY_AGAINST: SRC_INDEX, AD_CLI_PATH: process.env.CLI_PATH! });
    expect(r.exitCode, r.stderr).toBe(0);
    expect(r.stdout + r.stderr).not.toMatch(CRASH);
  },
  30_000
);

test("--smoke error path: AD_VERIFY_AGAINST at a non-module file → non-zero exit, no unhandled rejection", async () => {
  const bad = path.join(os.tmpdir(), `not-executable-${Date.now()}.bin`);
  fs.writeFileSync(bad, "I am not a binary", { mode: 0o644 });
  try {
    const r = await runDriver(["--smoke"], { AD_VERIFY_AGAINST: bad });
    expect(r.exitCode).not.toBe(0);
    expect(r.stderr.length).toBeGreaterThan(0);
    expect(r.stderr).not.toMatch(/UnhandledPromiseRejection/);
  } finally {
    fs.rmSync(bad, { force: true });
  }
}, 15_000);

test.each([
  [["--smoke", "--full"], ["mutually exclusive"]],
  [[], ["--smoke", "--full"]],
])("flags %p → exit 1, stderr names %p", async (args, wants) => {
  const r = await runDriver(args);
  expect(r.exitCode).toBe(1);
  for (const want of wants) expect(r.stderr).toContain(want);
}, 10_000);

test.each([
  "makeTemplate-create",
  "makeTemplate-collision",
  "makeTemplate-overwrite",
  "makeTemplate-reread",
  "getPermission-not-found",
  "decide-missing-request-token",
])("--full gauntlet: FAKE_CLIENT_FAIL_STEP=%s → non-zero exit and its FAIL line", async (step) => {
  const r = await runDriver(["--full"], { AD_VERIFY_AGAINST: FAKE_CLIENT, FAKE_CLIENT_FAIL_STEP: step });
  expect(r.exitCode).not.toBe(0);
  expect(r.stderr).toContain(`FAIL ${step}`);
}, 15_000);
