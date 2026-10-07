/**
 * version-floor.test.ts — SR-8.6: version-floor.json is the single source of
 * the binary floor (SR-5): strict SemVer, never the dev sentinel (SR-5.2),
 * byte-identical in dist and equal to the bundle's MIN_BINARY_VERSION (SR-5.4),
 * and readable with `jq` alone (SR-5.5).
 */

import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { MIN_BINARY_VERSION } from "../src/index.js";
import { parseVersion } from "../src/internal/semver.js";

const PKG_DIR = resolve(import.meta.dir, "..");
const SRC_PATH = resolve(PKG_DIR, "version-floor.json");
const DIST_PATH = resolve(PKG_DIR, "dist/version-floor.json");
const floor = (JSON.parse(readFileSync(SRC_PATH, "utf8")) as { min_binary_version: string }).min_binary_version;

test("the floor parses as strict SemVer and is not the dev sentinel; src MIN_BINARY_VERSION is it", () => {
  expect(parseVersion(floor).ok).toBe(true);
  expect(floor).not.toBe("0.0.0-dev");
  expect(MIN_BINARY_VERSION).toBe(floor);
});

test("dist/version-floor.json is byte-identical and the bundle's MIN_BINARY_VERSION equals it (SR-5.4)", async () => {
  expect(Buffer.compare(readFileSync(SRC_PATH), readFileSync(DIST_PATH))).toBe(0);
  const dist = (await import("../dist/index.js")) as { MIN_BINARY_VERSION: string };
  expect(dist.MIN_BINARY_VERSION).toBe(floor);
});

test("`jq -r .min_binary_version` reads the shipped floor byte-exact (SR-5.5)", () => {
  const proc = Bun.spawnSync(["jq", "-r", ".min_binary_version", DIST_PATH]);
  expect(proc.exitCode).toBe(0);
  expect(new TextDecoder().decode(proc.stdout)).toBe(`${floor}\n`);
});
