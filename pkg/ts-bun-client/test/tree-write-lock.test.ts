/**
 * tree-write-lock.test.ts — b.k42: tree writers (the preload's builds, docker-epics.sh's bin/ pre-build) flock the
 * repo-root .tree-write.lock exclusively, every coverage.docker-epic-* child flocks it shared, and git ignores it.
 */

import { test, expect } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { underTreeWriteLock } from "./internal/treeWriteLock.js";

const repoRoot = resolve(import.meta.dir, "../../..");
const lockPath = resolve(repoRoot, ".tree-write.lock");
const dockerEpics = resolve(repoRoot, "skills/release-agent-director/gates/coverage/docker-epics.sh");
const hasFlock = Bun.which("flock") !== null;
const git = (...args: string[]) => Bun.spawnSync(["git", "-C", repoRoot, ...args], { stdout: "pipe", stderr: "pipe" });

test.skipIf(!hasFlock)("the preload's lock helper (exclusive) and every docker-epic child (flock -s) lock the same file", () => {
  const make = ["make", "-C", repoRoot, "agent-director"];
  expect(underTreeWriteLock(make)).toEqual(["flock", lockPath, ...make]);

  const r = Bun.spawnSync(["bash", dockerEpics, "--dry-run"], { cwd: repoRoot, stdout: "pipe", stderr: "pipe" });
  expect(r.exitCode, r.stderr.toString()).toBe(0);
  const gates: { name: string; command: string; cwd: string }[] = JSON.parse(r.stdout.toString()).gates;
  expect(gates.length).toBeGreaterThan(0);
  for (const g of gates) {
    // run-parallel.sh runs the command from the gate's cwd, so the lock path resolves there.
    const [flock, mode, lock, ...rest] = g.command.split(/\s+/);
    const slug = g.name.replace(/^coverage\.docker-epic-/, "");
    expect([flock, mode, resolve(repoRoot, g.cwd, lock!), ...rest]).toEqual(["flock", "-s", lockPath, "make", "test-docker", `EPIC=${slug}`]);
  }
});

/**
 * Runs docker-epics.sh's live path in a stand-in repo root whose `make build` records whether the tree-write lock is
 * held exclusively, then exits buildExit, and whose one epic's `make test-docker` leaves child.ran.
 */
function runDockerEpicsLive(buildExit: number) {
  const dir = mkdtempSync(join(tmpdir(), "ad-tree-write-"));
  const makefile = [
    "list-test-docker-epics:",
    "\t@echo fixture-epic",
    "build:",
    "\t@if flock -n -s .tree-write.lock true; then echo free; else echo exclusive; fi > build.lock-state",
    `\t@echo fixture-build-output; exit ${buildExit}`,
    "test-docker:",
    "\t@touch child.ran",
  ];
  writeFileSync(join(dir, "Makefile"), makefile.join("\n") + "\n");
  try {
    const r = Bun.spawnSync(["bash", dockerEpics], { cwd: dir, stdout: "pipe", stderr: "pipe" });
    return {
      exitCode: r.exitCode,
      stdout: r.stdout.toString(),
      stderr: r.stderr.toString(),
      buildLockState: existsSync(join(dir, "build.lock-state")) ? readFileSync(join(dir, "build.lock-state"), "utf8").trim() : "build never ran",
      childRan: existsSync(join(dir, "child.ran")),
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test.skipIf(!hasFlock)("docker-epics.sh builds bin/ under the exclusive lock, then runs the epics", () => {
  const r = runDockerEpicsLive(0);
  expect(r.exitCode, r.stderr).toBe(0);
  expect(r.buildLockState).toBe("exclusive");
  expect(r.childRan).toBe(true);
}, 15_000);

test.skipIf(!hasFlock)("docker-epics.sh with a failing bin/ build: one prebuild diagnostic, empty stdout, no epic run", () => {
  const r = runDockerEpicsLive(1);
  expect(r.exitCode, r.stderr).toBe(1);
  expect(r.buildLockState).toBe("exclusive");
  expect(r.childRan).toBe(false);
  expect(r.stdout).toBe("");
  const lines = r.stderr.trim().split("\n");
  expect(lines).toHaveLength(1);
  const diag = JSON.parse(lines[0]!);
  expect([diag.gate, diag.offending_file_or_artifact]).toEqual(["coverage.docker-epic-prebuild", "bin/"]);
  expect(diag.description).toContain("fixture-build-output");
}, 15_000);

test.skipIf(git("rev-parse", "--is-inside-work-tree").exitCode !== 0)("git ignores the tree-write lock file", () => {
  expect(git("check-ignore", "-q", lockPath).exitCode).toBe(0);
});
