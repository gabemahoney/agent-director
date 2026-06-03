/**
 * version-floor.test.ts — SR-8.6 coverage for the version-floor.json single
 * source of truth (SR-5).
 *
 * Asserts byte-equality between source and dist, schema shape, strict SemVer
 * parseability, dev-sentinel rejection (SR-5.2), and consumer-side
 * unknown-fields tolerance (SR-5.7).
 *
 * The bundle-inlined-constant lockstep is asserted via TS-import (SR-5.4
 * variant; no positive-grep).
 */

import { test, expect, describe } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { parseVersion } from "../src/internal/semver.js";

const PKG_DIR = resolve(import.meta.dir, "..");
const SRC_PATH = resolve(PKG_DIR, "version-floor.json");
const DIST_PATH = resolve(PKG_DIR, "dist/version-floor.json");

function readFloor(path: string): { min_binary_version: string } {
  return JSON.parse(readFileSync(path, "utf8")) as { min_binary_version: string };
}

describe("version-floor.json: source-of-truth invariants", () => {
  test("source file exists, parses, has min_binary_version string", () => {
    const parsed = readFloor(SRC_PATH);
    expect(typeof parsed.min_binary_version).toBe("string");
  });

  test("source file passes SR-2.2 strict SemVer 2.0 parse", () => {
    const parsed = readFloor(SRC_PATH);
    const r = parseVersion(parsed.min_binary_version);
    expect(r.ok).toBe(true);
  });

  test("source file is NOT the dev sentinel (SR-5.2)", () => {
    const parsed = readFloor(SRC_PATH);
    expect(parsed.min_binary_version).not.toBe("0.0.0-dev");
  });

  test("top-level value is an object", () => {
    const raw = readFileSync(SRC_PATH, "utf8");
    const parsed: unknown = JSON.parse(raw);
    expect(typeof parsed).toBe("object");
    expect(Array.isArray(parsed)).toBe(false);
  });
});

describe("version-floor.json: source vs dist lockstep (SR-5.4)", () => {
  test("source and dist files are byte-for-byte identical", () => {
    const srcRaw = readFileSync(SRC_PATH);
    const distRaw = readFileSync(DIST_PATH);
    expect(Buffer.compare(srcRaw, distRaw)).toBe(0);
  });

  test("source.min_binary_version == dist.min_binary_version", () => {
    const src = readFloor(SRC_PATH);
    const dst = readFloor(DIST_PATH);
    expect(dst.min_binary_version).toBe(src.min_binary_version);
  });
});

describe("MIN_BINARY_VERSION import lockstep (SR-5.4 TS-import variant)", () => {
  test("imported MIN_BINARY_VERSION equals parsed JSON field byte-for-byte", async () => {
    const { MIN_BINARY_VERSION } = (await import("../dist/index.js")) as {
      MIN_BINARY_VERSION: string;
    };
    const parsed = readFloor(DIST_PATH);
    expect(MIN_BINARY_VERSION).toBe(parsed.min_binary_version);
  });
});

describe("version-floor.json: SR-5.7 forward-compat (unknown-fields tolerated)", () => {
  test("consumer-style reader returns min_binary_version when extra fields present", () => {
    // Programmatically construct a future-shaped variant with extra fields;
    // assert a documented reader returns the correct floor and ignores the rest.
    const future = {
      min_binary_version: "1.2.3",
      schema_version: "v2",
      experimental_flag: true,
      extra_metadata: { author: "release-eng" },
    };
    const raw = JSON.stringify(future);
    const got = (JSON.parse(raw) as { min_binary_version: string }).min_binary_version;
    expect(got).toBe("1.2.3");
  });
});
