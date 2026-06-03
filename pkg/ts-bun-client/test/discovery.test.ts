/**
 * discovery.test.ts — SR-8.1 coverage for the SR-1.1 system-install
 * discovery pipeline.  Cases:
 *   - standard install path success
 *   - PATH lookup success
 *   - standard install path wins when both present
 *   - HOME unset / empty / non-absolute → standard-install-path step skipped
 *   - both missing → ErrSystemInstallNotFound with checkedLocations
 *   - standard-install-path candidate exists but fails validation → no
 *     fallthrough to PATH; surfaces ErrSystemInstallUnreachable
 */

import { test, expect, describe } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync, chmodSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  discoverSystemBinary,
} from "../src/internal/discovery.js";
import {
  ErrSystemInstallNotFound,
  ErrSystemInstallUnreachable,
} from "../src/errors.js";

const STUB_VERSION = '#!/bin/sh\necho \'{"version":"0.0.0-dev","commit":"deadbeef"}\'\n';

interface Cleanup {
  paths: string[];
}

function stageDirHelper(): Cleanup {
  return { paths: [] };
}

function cleanup(c: Cleanup): void {
  for (const p of c.paths) {
    try {
      rmSync(p, { recursive: true, force: true });
    } catch {
      /* best-effort */
    }
  }
}

function tmpDir(c: Cleanup, prefix: string): string {
  const d = mkdtempSync(join(tmpdir(), prefix));
  c.paths.push(d);
  return d;
}

function stageBin(dir: string, content: string = STUB_VERSION, mode: number = 0o755): string {
  const binDir = join(dir, ".agent-director", "bin");
  mkdirSync(binDir, { recursive: true });
  const binPath = join(binDir, "agent-director");
  writeFileSync(binPath, content);
  chmodSync(binPath, mode);
  return binPath;
}

function stagePathBin(c: Cleanup, content: string = STUB_VERSION, mode: number = 0o755): string {
  const dir = mkdtempSync(join(tmpdir(), "ad-path-"));
  c.paths.push(dir);
  const binPath = join(dir, "agent-director");
  writeFileSync(binPath, content);
  chmodSync(binPath, mode);
  return dir;
}

describe("discoverSystemBinary: standard install path (SR-1.1 step 1)", () => {
  test("returns standard-install-path kind when HOME has the binary", () => {
    const c = stageDirHelper();
    try {
      const home = tmpDir(c, "ad-home-");
      stageBin(home);
      const got = discoverSystemBinary({ HOME: home, PATH: "" });
      expect(got.kind).toBe("standard-install-path");
      expect(got.path.endsWith("/agent-director")).toBe(true);
    } finally {
      cleanup(c);
    }
  });

  test("standard install path wins over PATH", () => {
    const c = stageDirHelper();
    try {
      const home = tmpDir(c, "ad-home-");
      const standardBin = stageBin(home);
      const pathDir = stagePathBin(c);
      const got = discoverSystemBinary({ HOME: home, PATH: pathDir });
      expect(got.kind).toBe("standard-install-path");
      // realpath of staged file equals the absolute we wrote (no symlinks).
      expect(got.path).toBe(standardBin);
    } finally {
      cleanup(c);
    }
  });
});

describe("discoverSystemBinary: PATH fallback (SR-1.1 step 2)", () => {
  test("HOME-without-AD + AD on PATH → resolves via PATH", () => {
    const c = stageDirHelper();
    try {
      const home = tmpDir(c, "ad-home-no-ad-");
      const pathDir = stagePathBin(c);
      const got = discoverSystemBinary({ HOME: home, PATH: pathDir });
      expect(got.kind).toBe("path-lookup");
      expect(got.path).toBe(join(pathDir, "agent-director"));
    } finally {
      cleanup(c);
    }
  });

  test("HOME unset → step 1 skipped, PATH used", () => {
    const c = stageDirHelper();
    try {
      const pathDir = stagePathBin(c);
      const got = discoverSystemBinary({ HOME: undefined, PATH: pathDir });
      expect(got.kind).toBe("path-lookup");
    } finally {
      cleanup(c);
    }
  });

  test("HOME empty string → step 1 skipped, PATH used", () => {
    const c = stageDirHelper();
    try {
      const pathDir = stagePathBin(c);
      const got = discoverSystemBinary({ HOME: "", PATH: pathDir });
      expect(got.kind).toBe("path-lookup");
    } finally {
      cleanup(c);
    }
  });

  test("HOME non-absolute → step 1 skipped, PATH used", () => {
    const c = stageDirHelper();
    try {
      const pathDir = stagePathBin(c);
      const got = discoverSystemBinary({ HOME: "relative/path", PATH: pathDir });
      expect(got.kind).toBe("path-lookup");
    } finally {
      cleanup(c);
    }
  });
});

describe("discoverSystemBinary: both missing (SR-1.1 + SR-3.1)", () => {
  test("rejects with ErrSystemInstallNotFound; checkedLocations names both", () => {
    const c = stageDirHelper();
    try {
      const home = tmpDir(c, "ad-home-empty-");
      let caught: unknown;
      try {
        discoverSystemBinary({ HOME: home, PATH: "" });
      } catch (e) {
        caught = e;
      }
      expect(caught).toBeInstanceOf(ErrSystemInstallNotFound);
      const err = caught as ErrSystemInstallNotFound;
      expect(err.checkedLocations.length).toBe(2);
      const kinds = err.checkedLocations.map((l) => l.kind).sort();
      expect(kinds).toEqual(["path-lookup", "standard-install-path"]);
    } finally {
      cleanup(c);
    }
  });

  test("HOME unset + PATH unset → both locations recorded as null detail", () => {
    let caught: unknown;
    try {
      discoverSystemBinary({ HOME: undefined, PATH: undefined });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSystemInstallNotFound);
    const err = caught as ErrSystemInstallNotFound;
    expect(err.checkedLocations.length).toBe(2);
    for (const loc of err.checkedLocations) {
      expect(loc.detail).toBeNull();
    }
  });
});

describe("discoverSystemBinary: SR-1.2 validation, no PATH fallthrough", () => {
  test("standard-install-path candidate is not executable → ErrSystemInstallUnreachable; PATH NOT consulted", () => {
    const c = stageDirHelper();
    try {
      const home = tmpDir(c, "ad-home-bad-");
      stageBin(home, STUB_VERSION, 0o644); // mode 644 — no exec bit
      const pathDir = stagePathBin(c);
      let caught: unknown;
      try {
        discoverSystemBinary({ HOME: home, PATH: pathDir });
      } catch (e) {
        caught = e;
      }
      expect(caught).toBeInstanceOf(ErrSystemInstallUnreachable);
      const err = caught as ErrSystemInstallUnreachable;
      expect(err.reason).toBe("not-executable");
    } finally {
      cleanup(c);
    }
  });

  test("standard-install-path candidate is a directory → not-a-regular-file; PATH NOT consulted", () => {
    const c = stageDirHelper();
    try {
      const home = tmpDir(c, "ad-home-dir-");
      // Make $HOME/.agent-director/bin/agent-director a DIRECTORY.
      mkdirSync(join(home, ".agent-director", "bin", "agent-director"), { recursive: true });
      const pathDir = stagePathBin(c);
      let caught: unknown;
      try {
        discoverSystemBinary({ HOME: home, PATH: pathDir });
      } catch (e) {
        caught = e;
      }
      expect(caught).toBeInstanceOf(ErrSystemInstallUnreachable);
      const err = caught as ErrSystemInstallUnreachable;
      expect(err.reason).toBe("not-a-regular-file");
    } finally {
      cleanup(c);
    }
  });
});
