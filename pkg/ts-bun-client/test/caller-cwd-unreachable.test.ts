/**
 * caller-cwd-unreachable.test.ts — b.cot: with the caller's cwd deleted,
 * Client.create() and resolveSystemBinary() throw ErrCallerCwdUnreachable
 * rather than a misleading ENOENT for the binary path.
 *
 * resolveSystemBinary() has no _cliPath hook (SR-4.3), so these tests put the
 * repo binary's directory first on PATH for discovery's PATH lookup (b.12w).
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { resolveSystemBinary } from "../src/client.js";
import { ErrCallerCwdUnreachable } from "../src/errors.js";
import { openClient, withDeletedCwd, withProcessEnv } from "./internal/helper.js";

const cliPath = process.env.CLI_PATH!;

/** Runs fn with the repo CLI's directory first on PATH. */
const withRepoBinaryOnPath = <T>(fn: () => Promise<T>) =>
  withProcessEnv({ PATH: `${path.dirname(cliPath)}${path.delimiter}${process.env.PATH ?? ""}` }, fn);

test("Client.create() throws ErrCallerCwdUnreachable when cwd is deleted (b.cot)", async () => {
  const storePath = path.join(os.tmpdir(), `b-cot-${crypto.randomUUID()}.db`);
  try {
    await withDeletedCwd(async () => {
      await expect(openClient(storePath)).rejects.toBeInstanceOf(ErrCallerCwdUnreachable);
    });
  } finally {
    fs.rmSync(storePath, { force: true });
  }
});

test("resolveSystemBinary() throws ErrCallerCwdUnreachable when cwd is deleted (b.cot)", async () => {
  await withDeletedCwd(() =>
    withRepoBinaryOnPath(async () => {
      await expect(resolveSystemBinary()).rejects.toBeInstanceOf(ErrCallerCwdUnreachable);
    })
  );
});

test("resolveSystemBinary() finds the PATH binary from a valid cwd (no false positive)", async () => {
  const result = await withRepoBinaryOnPath(() => resolveSystemBinary());
  // Discovery canonicalizes with realpath: this is the injected repo binary, not an ambient one.
  expect(result.path).toBe(fs.realpathSync(cliPath));
  expect(result.version).toBeTruthy();
});
