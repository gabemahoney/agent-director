/**
 * Packaging content assertions — verify tarball file lists via
 * `npm pack --dry-run --json`.
 *
 * Assertions:
 *   - Top-level tarball: zero .so/.dylib files.
 *   - linux-x64 sub-package: contains libagent_director.so, package.json,
 *     README-binary-source.md (binary may be absent on darwin CI — asserted
 *     per files array only when the file exists on disk).
 *   - darwin-x64/darwin-arm64: contain package.json + README-binary-source.md;
 *     binary asserted present only when it actually exists on this host.
 *
 * Each directory's pack output is fetched once and shared across sub-tests to
 * keep total subprocess overhead low.
 */

import { test, expect, describe } from "bun:test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

/** Memoized map: dir → file list. Pack is run once per dir. */
const _packCache = new Map<string, string[]>();

/**
 * Run `npm pack --dry-run --json` in `dir` and return the parsed file list
 * (cached per directory to avoid repeated subprocess spawns).
 */
function getPackFiles(dir: string): string[] {
  if (_packCache.has(dir)) return _packCache.get(dir)!;

  const result = spawnSync(
    "npm",
    ["pack", "--dry-run", "--json"],
    { cwd: dir, encoding: "utf8" }
  );

  if (result.status !== 0) {
    console.error(
      `packaging.test.ts: npm pack --dry-run failed in ${dir}:\n${result.stderr}`
    );
    _packCache.set(dir, []);
    return [];
  }

  // npm pack --dry-run --json may prefix warnings before the JSON array.
  const raw = result.stdout;
  const jsonStart = raw.indexOf("[");
  if (jsonStart === -1) {
    console.error(`packaging.test.ts: no JSON array in npm pack output:\n${raw}`);
    _packCache.set(dir, []);
    return [];
  }

  let files: string[] = [];
  try {
    const parsed = JSON.parse(raw.slice(jsonStart)) as Array<{
      files: Array<{ path: string }>;
    }>;
    files = parsed[0]?.files?.map((f) => f.path) ?? [];
  } catch (e) {
    console.error(`packaging.test.ts: failed to parse npm pack output: ${e}`);
  }
  _packCache.set(dir, files);
  return files;
}

// Pre-warm all four pack calls before tests run (avoids per-test subprocess
// overhead and lets bun run tests faster after the initial fetch).
const allDirs = [
  pkgDir,
  resolve(pkgDir, "platforms", "linux-x64"),
  resolve(pkgDir, "platforms", "darwin-x64"),
  resolve(pkgDir, "platforms", "darwin-arm64"),
];
for (const d of allDirs) getPackFiles(d);

// ---------------------------------------------------------------------------
// Top-level package
// ---------------------------------------------------------------------------

describe("packaging — top-level tarball", () => {
  test("top-level pack contains no .so or .dylib files", () => {
    const files = getPackFiles(pkgDir);
    expect(files.length).toBeGreaterThan(0);
    const nativeFiles = files.filter(
      (f) => f.endsWith(".so") || f.endsWith(".dylib")
    );
    expect(nativeFiles).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Sub-package definitions
// ---------------------------------------------------------------------------

interface SubPkgSpec {
  name: string;
  dir: string;
  binaryName: string;
}

const subPkgs: SubPkgSpec[] = [
  {
    name: "linux-x64",
    dir: resolve(pkgDir, "platforms", "linux-x64"),
    binaryName: "libagent_director.so",
  },
  {
    name: "darwin-x64",
    dir: resolve(pkgDir, "platforms", "darwin-x64"),
    binaryName: "libagent_director.dylib",
  },
  {
    name: "darwin-arm64",
    dir: resolve(pkgDir, "platforms", "darwin-arm64"),
    binaryName: "libagent_director.dylib",
  },
];

// ---------------------------------------------------------------------------
// Sub-package tests
// ---------------------------------------------------------------------------

describe("packaging — sub-package tarballs", () => {
  for (const spec of subPkgs) {
    test(`${spec.name}: pack includes package.json and README-binary-source.md`, () => {
      const files = getPackFiles(spec.dir);
      expect(files.length).toBeGreaterThan(0);
      expect(files.some((f) => f === "package.json")).toBe(true);
      expect(files.some((f) => f === "README-binary-source.md")).toBe(true);
    });

    test(`${spec.name}: pack includes binary when present on this host`, () => {
      const binaryPath = resolve(spec.dir, spec.binaryName);
      if (!existsSync(binaryPath)) {
        console.log(
          `packaging.test.ts: ${spec.name} binary absent on this host (${process.platform}) — skipping binary-in-tarball assertion`
        );
        return;
      }
      const files = getPackFiles(spec.dir);
      expect(files.some((f) => f === spec.binaryName)).toBe(true);
    });

    test(`${spec.name}: pack contains no binaries from other platforms`, () => {
      const files = getPackFiles(spec.dir);
      const nativeFiles = files.filter(
        (f) => f.endsWith(".so") || f.endsWith(".dylib")
      );
      const unexpected = nativeFiles.filter((f) => f !== spec.binaryName);
      expect(unexpected).toEqual([]);
    });
  }
});
