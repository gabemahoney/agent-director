/**
 * discovery.test.ts — SR-8.1 for the SR-1.1 discovery pipeline: the standard
 * install path ($HOME/.config/agent-director/bin) wins, then PATH (the old
 * $HOME/.agent-director/bin and XDG_CONFIG_HOME are never consulted); a HOME that is
 * unset, empty or relative skips step 1; nothing found → ErrSystemInstallNotFound
 * naming both locations; a standard-path candidate that fails validation is
 * ErrSystemInstallUnreachable with no fall-through to PATH (SR-1.2).
 */

import { test, expect, afterEach } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync, chmodSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { discoverSystemBinary } from "../src/internal/discovery.js";
import { ErrSystemInstallNotFound, ErrSystemInstallUnreachable } from "../src/errors.js";
import { thrownBy, withProcessEnv } from "./internal/helper.js";

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

function tmp(): string {
  const d = mkdtempSync(join(tmpdir(), "ad-disc-"));
  dirs.push(d);
  return d;
}

/** Writes an agent-director stub in dir (mode as given) and returns its path. */
function stub(dir: string, mode = 0o755): string {
  mkdirSync(dir, { recursive: true });
  const bin = join(dir, "agent-director");
  writeFileSync(bin, '#!/bin/sh\necho \'{"version":"0.0.0-dev","commit":"deadbeef"}\'\n');
  chmodSync(bin, mode);
  return bin;
}

const homeWithBin = (mode?: number) => {
  const home = tmp();
  return { home, bin: stub(join(home, ".config", "agent-director", "bin"), mode) };
};

test("the standard install path wins over PATH", () => {
  const { home, bin } = homeWithBin();
  expect(discoverSystemBinary({ HOME: home, PATH: tmp() })).toMatchObject({ kind: "standard-install-path", path: bin });
  const pathDir = tmp();
  stub(pathDir);
  expect(discoverSystemBinary({ HOME: home, PATH: pathDir })).toMatchObject({ kind: "standard-install-path", path: bin });
});

test("a binary only at the old install path is not found by step 1", () => {
  const home = tmp();
  stub(join(home, ".agent-director", "bin"));
  const pathDir = tmp();
  const bin = stub(pathDir);
  expect(discoverSystemBinary({ HOME: home, PATH: pathDir })).toMatchObject({ kind: "path-lookup", path: bin });
  const err = thrownBy(() => discoverSystemBinary({ HOME: home, PATH: "" })) as ErrSystemInstallNotFound;
  expect(err).toBeInstanceOf(ErrSystemInstallNotFound);
  expect(err.checkedLocations.find((l) => l.kind === "standard-install-path")?.detail).toBe(
    join(home, ".config", "agent-director", "bin", "agent-director")
  );
});

test("XDG_CONFIG_HOME is ignored: only $HOME/.config/agent-director/bin is the standard path", async () => {
  const home = tmp();
  const xdg = tmp();
  stub(join(xdg, "agent-director", "bin"));
  const pathDir = tmp();
  const bin = stub(pathDir);
  await withProcessEnv({ XDG_CONFIG_HOME: xdg }, () => {
    expect(discoverSystemBinary({ HOME: home, PATH: pathDir })).toMatchObject({ kind: "path-lookup", path: bin });
  });
  const { home: homeWithStd, bin: std } = homeWithBin();
  await withProcessEnv({ XDG_CONFIG_HOME: xdg }, () => {
    expect(discoverSystemBinary({ HOME: homeWithStd, PATH: pathDir })).toMatchObject({ kind: "standard-install-path", path: std });
  });
});

test.each([
  ["HOME without the binary", "home"],
  ["HOME unset", undefined],
  ["HOME empty", ""],
  ["HOME relative", "relative/path"],
])("%s → found by PATH lookup", (_label, home) => {
  const pathDir = tmp();
  const bin = stub(pathDir);
  const got = discoverSystemBinary({ HOME: home === "home" ? tmp() : home, PATH: pathDir });
  expect(got).toMatchObject({ kind: "path-lookup", path: bin });
});

test("nothing found → ErrSystemInstallNotFound naming both locations (SR-3.1)", () => {
  const err = thrownBy(() => discoverSystemBinary({ HOME: tmp(), PATH: "" })) as ErrSystemInstallNotFound;
  expect(err).toBeInstanceOf(ErrSystemInstallNotFound);
  expect(err.checkedLocations.map((l) => l.kind).sort()).toEqual(["path-lookup", "standard-install-path"]);
  const unset = thrownBy(() => discoverSystemBinary({ HOME: undefined, PATH: undefined })) as ErrSystemInstallNotFound;
  expect(unset.checkedLocations.map((l) => l.detail)).toEqual([null, null]);
});

test.each([
  ["not executable", "not-executable", (home: string) => stub(join(home, ".config", "agent-director", "bin"), 0o644)],
  ["a directory", "not-a-regular-file", (home: string) => mkdirSync(join(home, ".config", "agent-director", "bin", "agent-director"), { recursive: true })],
] as const)("standard-path candidate %s → ErrSystemInstallUnreachable(%s); PATH not consulted", (_label, reason, plant) => {
  const home = tmp();
  plant(home);
  const pathDir = tmp();
  stub(pathDir);
  const err = thrownBy(() => discoverSystemBinary({ HOME: home, PATH: pathDir }));
  expect(err).toBeInstanceOf(ErrSystemInstallUnreachable);
  expect((err as ErrSystemInstallUnreachable).reason).toBe(reason);
});
