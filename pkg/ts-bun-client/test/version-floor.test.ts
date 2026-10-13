/**
 * version-floor.test.ts — SR-8.6: version-floor.json is the single source of
 * the binary floor (SR-5): strict SemVer, never the dev sentinel (SR-5.2),
 * byte-identical in dist and equal to the bundle's MIN_BINARY_VERSION (SR-5.4),
 * and readable with `jq` alone (SR-5.5). The confirmed floor is exactly 0.12.0,
 * the first binary that takes the agent-generic keys (SR-11.5), and a binary
 * below it is refused (SR-14.4).
 */

import { test, expect, afterAll } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import type { Client } from "../src/client.js";
import { ErrSystemInstallTooOld, MIN_BINARY_VERSION } from "../src/index.js";
import { parseVersion } from "../src/internal/semver.js";
import { requireBuiltDist } from "./internal/builtDist.js";
import { openClient } from "./internal/helper.js";

const PKG_DIR = resolve(import.meta.dir, "..");
const SRC_PATH = resolve(PKG_DIR, "version-floor.json");
const DIST_PATH = resolve(PKG_DIR, "dist/version-floor.json");
const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});
const floor = (JSON.parse(readFileSync(SRC_PATH, "utf8")) as { min_binary_version: string }).min_binary_version;

test("the floor parses as strict SemVer and is not the dev sentinel; src MIN_BINARY_VERSION is it", () => {
  expect(parseVersion(floor).ok).toBe(true);
  expect(floor).not.toBe("0.0.0-dev");
  expect(MIN_BINARY_VERSION).toBe(floor);
});

test("dist/version-floor.json is byte-identical and the bundle's MIN_BINARY_VERSION equals it (SR-5.4)", async () => {
  requireBuiltDist();
  expect(Buffer.compare(readFileSync(SRC_PATH), readFileSync(DIST_PATH))).toBe(0);
  const dist = (await import("../dist/index.js")) as { MIN_BINARY_VERSION: string };
  expect(dist.MIN_BINARY_VERSION).toBe(floor);
});

test("`jq -r .min_binary_version` reads the shipped floor byte-exact (SR-5.5)", () => {
  requireBuiltDist();
  const proc = Bun.spawnSync(["jq", "-r", ".min_binary_version", DIST_PATH]);
  expect(proc.exitCode).toBe(0);
  expect(new TextDecoder().decode(proc.stdout)).toBe(`${floor}\n`);
});

// User/Gabe 2026-10-13 confirmed stable 0.12.0 as the SR-11.5 floor.
test("the floor is exactly the confirmed stable release 0.12.0", () => {
  expect(floor).toBe("0.12.0");
});

/** A stand-in agent-director in a throwaway dir that reports `version`; the Client opened on it, or its rejection. */
async function openOnBinaryReporting(version: string): Promise<{ client?: Client; error?: unknown }> {
  const dir = mkdtempSync(join(tmpdir(), "ad-floor-"));
  dirs.push(dir);
  const bin = join(dir, "agent-director");
  writeFileSync(bin, `#!/bin/sh\nprintf '{"version":"${version}","commit":"deadbeef"}\\n'\n`, { mode: 0o755 });
  try {
    return { client: await openClient(join(dir, "state.db"), { _cliPath: bin }) };
  } catch (error) {
    return { error };
  }
}

test.each(["0.11.0", "0.11.1", "0.11.99", "0.11.9-rc.1", "0.12.0-0", "0.12.0-rc.1"])(
  "a binary reporting %s is refused with ErrSystemInstallTooOld naming it and the floor (SR-14.4)",
  async (version) => {
    const { client, error } = await openOnBinaryReporting(version);
    client?.close();

    expect(error).toBeInstanceOf(ErrSystemInstallTooOld);
    expect(error).toMatchObject({ actualVersion: version, requiredVersion: floor });
  },
);

test("a binary reporting exactly the floor is accepted", async () => {
  const { client, error } = await openOnBinaryReporting(floor);
  client?.close();

  expect(error).toBeUndefined();
  expect(client?.binaryVersion).toBe(floor);
});
