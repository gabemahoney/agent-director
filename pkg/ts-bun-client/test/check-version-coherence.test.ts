/**
 * check-version-coherence.test.ts — tests for scripts/check-version-coherence.ts.
 *
 * Staging layout mirrors the path-resolution in check-version-coherence.ts
 * (script lives at <root>/pkg/ts-bun-client/scripts/; paths anchored via ../):
 *   <root>/
 *     pkg/ts-bun-client/
 *       scripts/check-version-coherence.ts  ← copied from real source
 *       package.json                         ← umbrella, seeded
 *       platforms/
 *         linux-x64/   bin/agent-director (shell stub), package.json
 *         darwin-arm64/ bin/agent-director (shell stub), package.json
 *     skills/install-agent-director/SKILL.md
 */

import { test, expect, describe } from "bun:test";
import {
  mkdirSync,
  writeFileSync,
  mkdtempSync,
  rmSync,
  chmodSync,
  cpSync,
} from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const PKG_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const REAL_SCRIPT = join(PKG_DIR, "scripts", "check-version-coherence.ts");

const EXPECTED = "9.9.9";
const WRONG = "0.0.1";
const STUB_COMMIT = "abc1234";

// ---------------------------------------------------------------------------
// Content helpers
// ---------------------------------------------------------------------------

/** Shell stub that emits a fixed version --json envelope regardless of args. */
function makeStub(version: string): string {
  return `#!/bin/sh\necho '{"version":"v${version}","commit":"${STUB_COMMIT}"}'\n`;
}

function makeSkillMd(version: string): string {
  return `---\nname: install-agent-director\nversion: ${version}\ndescription: Install agent-director\n---\nBody content.\n`;
}

// ---------------------------------------------------------------------------
// Staging tree factory
// ---------------------------------------------------------------------------

interface StagingTree {
  root: string;
  scriptPath: string;
  umbrellaPkgPath: string;
  linuxPkgPath: string;
  darwinPkgPath: string;   // path computed even when omitDarwin=true
  linuxBinPath: string;
  darwinBinPath: string;   // path computed even when omitDarwin=true
  skillMdPath: string;
  cleanup(): void;
}

interface StagingTreeOpts {
  /** Version written into shell stubs (site 1). Default: EXPECTED. */
  site1Version?: string;
  /** umbrella package.json::version (site 3a). Default: EXPECTED. */
  site3aVersion?: string;
  /** platform package.json::version (site 3b). Default: EXPECTED. */
  site3bVersion?: string;
  /** "file" = file: opt-dep paths; "pin" = ^X.Y.Z pins (site 4). Default: "file". */
  site4Mode?: "file" | "pin";
  /** SKILL.md frontmatter version (site 5). Default: EXPECTED. */
  site5Version?: string;
  /** When true, skip creating the darwin-arm64 platform directory. */
  omitDarwin?: boolean;
}

function makeStagingTree(opts: StagingTreeOpts = {}): StagingTree {
  const {
    site1Version = EXPECTED,
    site3aVersion = EXPECTED,
    site3bVersion = EXPECTED,
    site4Mode = "file",
    site5Version = EXPECTED,
    omitDarwin = false,
  } = opts;

  const root = mkdtempSync(join(import.meta.dir, ".tmp-cvc-"));
  const pkgDir = join(root, "pkg", "ts-bun-client");
  const scriptsDir = join(pkgDir, "scripts");
  const linuxDir = join(pkgDir, "platforms", "linux-x64");
  const darwinDir = join(pkgDir, "platforms", "darwin-arm64");
  const skillDir = join(root, "skills", "install-agent-director");

  const dirs = [scriptsDir, join(linuxDir, "bin"), skillDir];
  if (!omitDarwin) dirs.push(join(darwinDir, "bin"));
  for (const d of dirs) mkdirSync(d, { recursive: true });

  // Script copy — import.meta.url in the copy resolves paths relative to staging root.
  const scriptPath = join(scriptsDir, "check-version-coherence.ts");
  cpSync(REAL_SCRIPT, scriptPath);

  // Umbrella package.json (sites 3a + 4)
  const umbrellaPkgPath = join(pkgDir, "package.json");
  const optDeps =
    site4Mode === "pin"
      ? {
          "@agent-director/linux-x64": `^${EXPECTED}`,
          "@agent-director/darwin-arm64": `^${EXPECTED}`,
        }
      : {
          "@agent-director/linux-x64": "file:./platforms/linux-x64",
          "@agent-director/darwin-arm64": "file:./platforms/darwin-arm64",
        };
  writeFileSync(
    umbrellaPkgPath,
    JSON.stringify(
      { name: "agent-director", version: site3aVersion, optionalDependencies: optDeps },
      null,
      2
    ) + "\n",
    "utf8"
  );

  // Platform package.json files (site 3b)
  const linuxPkgPath = join(linuxDir, "package.json");
  writeFileSync(
    linuxPkgPath,
    JSON.stringify({ name: "@agent-director/linux-x64", version: site3bVersion }, null, 2) + "\n",
    "utf8"
  );

  const darwinPkgPath = join(darwinDir, "package.json");
  if (!omitDarwin) {
    writeFileSync(
      darwinPkgPath,
      JSON.stringify({ name: "@agent-director/darwin-arm64", version: site3bVersion }, null, 2) + "\n",
      "utf8"
    );
  }

  // Shell stubs (site 1) — executable shell scripts that emit a fixed version JSON.
  const linuxBinPath = join(linuxDir, "bin", "agent-director");
  writeFileSync(linuxBinPath, makeStub(site1Version), "utf8");
  chmodSync(linuxBinPath, 0o755);

  const darwinBinPath = join(darwinDir, "bin", "agent-director");
  if (!omitDarwin) {
    writeFileSync(darwinBinPath, makeStub(site1Version), "utf8");
    chmodSync(darwinBinPath, 0o755);
  }

  // SKILL.md (site 5)
  const skillMdPath = join(skillDir, "SKILL.md");
  writeFileSync(skillMdPath, makeSkillMd(site5Version), "utf8");

  return {
    root,
    scriptPath,
    umbrellaPkgPath,
    linuxPkgPath,
    darwinPkgPath,
    linuxBinPath,
    darwinBinPath,
    skillMdPath,
    cleanup() {
      try {
        rmSync(root, { recursive: true, force: true });
      } catch {
        /* best-effort */
      }
    },
  };
}

// ---------------------------------------------------------------------------
// Invocation helper
// ---------------------------------------------------------------------------

interface RunResult {
  exitCode: number | null;
  stdout: string;
  stderr: string;
}

function runCheck(scriptPath: string, args: string[]): RunResult {
  const r = Bun.spawnSync(["bun", "run", scriptPath, ...args], {
    stdout: "pipe",
    stderr: "pipe",
  });
  return {
    exitCode: r.exitCode,
    stdout: r.stdout.toString(),
    stderr: r.stderr.toString(),
  };
}

// ---------------------------------------------------------------------------
// 1. Happy paths
// ---------------------------------------------------------------------------

describe("check-version-coherence happy path", () => {
  test("--scope publish: all 5 sites stamped → exit 0, empty stderr", () => {
    const tree = makeStagingTree({ site4Mode: "pin" });
    try {
      const r = runCheck(tree.scriptPath, [
        "--scope", "publish",
        "--expected-version", EXPECTED,
      ]);
      expect(r.exitCode).toBe(0);
      expect(r.stderr).toBe("");
    } finally {
      tree.cleanup();
    }
  });

  test("--scope verify: sites 1/3a/3b/5 stamped, site-4 file: → exit 0, site-4 skip logged", () => {
    const tree = makeStagingTree(); // site4Mode defaults to "file"
    try {
      const r = runCheck(tree.scriptPath, [
        "--scope", "verify",
        "--expected-version", EXPECTED,
      ]);
      expect(r.exitCode).toBe(0);
      expect(r.stderr).toBe("");
      expect(r.stdout).toContain("site-4");
      expect(r.stdout).toContain("skipped");
    } finally {
      tree.cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// 2. Per-site failures (parametrized × 5)
// ---------------------------------------------------------------------------

const PER_SITE_CASES: Array<{
  label: string;
  opts: StagingTreeOpts;
  scope: "publish" | "verify";
  /** Returns the file path that must appear in the stderr failure line. */
  filePath: (t: StagingTree) => string;
  actual: string;
  expected: string;
}> = [
  {
    label: "site-1: binary reports wrong version",
    opts: { site1Version: WRONG, site4Mode: "pin" },
    scope: "publish",
    filePath: (t) => t.linuxBinPath,
    actual: `v${WRONG}`,
    expected: `v${EXPECTED}`,
  },
  {
    label: "site-3a: umbrella package.json has wrong version",
    opts: { site3aVersion: WRONG, site4Mode: "pin" },
    scope: "publish",
    filePath: (t) => t.umbrellaPkgPath,
    actual: WRONG,
    expected: EXPECTED,
  },
  {
    label: "site-3b: platform package.json has wrong version",
    opts: { site3bVersion: WRONG, site4Mode: "pin" },
    scope: "publish",
    filePath: (t) => t.linuxPkgPath,
    actual: WRONG,
    expected: EXPECTED,
  },
  {
    label: "site-4: --scope publish with file: opt-deps",
    opts: { site4Mode: "file" },
    scope: "publish",
    filePath: (t) => t.umbrellaPkgPath,
    actual: "@agent-director/linux-x64=file:./platforms/linux-x64",
    expected: `@agent-director/linux-x64=^${EXPECTED}`,
  },
  {
    label: "site-5: SKILL.md has wrong version",
    opts: { site5Version: WRONG, site4Mode: "pin" },
    scope: "publish",
    filePath: (t) => t.skillMdPath,
    actual: WRONG,
    expected: EXPECTED,
  },
];

describe("check-version-coherence per-site failures", () => {
  for (const c of PER_SITE_CASES) {
    test(`${c.label} → exit 1, stderr contains file path + actual + expected`, () => {
      const tree = makeStagingTree(c.opts);
      try {
        const r = runCheck(tree.scriptPath, [
          "--scope", c.scope,
          "--expected-version", EXPECTED,
        ]);
        expect(r.exitCode).not.toBe(0);
        expect(r.stderr).toContain(c.filePath(tree));
        expect(r.stderr).toContain(c.actual);
        expect(r.stderr).toContain(c.expected);
      } finally {
        tree.cleanup();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// 3. Multi-site failure — all sites checked before exit (no short-circuit)
// ---------------------------------------------------------------------------

describe("check-version-coherence multi-site failure", () => {
  test("site-3a and site-5 both wrong → both failure lines present in single stderr", () => {
    const tree = makeStagingTree({ site3aVersion: WRONG, site5Version: WRONG, site4Mode: "pin" });
    try {
      const r = runCheck(tree.scriptPath, [
        "--scope", "publish",
        "--expected-version", EXPECTED,
      ]);
      expect(r.exitCode).not.toBe(0);
      expect(r.stderr).toContain("[site-3a]");
      expect(r.stderr).toContain(tree.umbrellaPkgPath);
      expect(r.stderr).toContain("[site-5]");
      expect(r.stderr).toContain(tree.skillMdPath);
    } finally {
      tree.cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// 4. Missing-platform tolerance (parametrized × 2 scopes)
// ---------------------------------------------------------------------------

describe("check-version-coherence missing-platform tolerance", () => {
  for (const scope of ["verify", "publish"] as const) {
    test(`--scope ${scope}: darwin-arm64 dir absent → exit 0, darwin sites skipped`, () => {
      const tree = makeStagingTree({
        omitDarwin: true,
        site4Mode: scope === "publish" ? "pin" : "file",
      });
      try {
        const r = runCheck(tree.scriptPath, [
          "--scope", scope,
          "--expected-version", EXPECTED,
        ]);
        expect(r.exitCode).toBe(0);
        expect(r.stdout).toContain("darwin-arm64");
        expect(r.stdout).toContain("skipped");
      } finally {
        tree.cleanup();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// 5. Bad flags
// ---------------------------------------------------------------------------

describe("check-version-coherence bad flags", () => {
  test("--scope foo → exit 1, stderr mentions the bad value", () => {
    const tree = makeStagingTree();
    try {
      const r = runCheck(tree.scriptPath, [
        "--scope", "foo",
        "--expected-version", EXPECTED,
      ]);
      expect(r.exitCode).not.toBe(0);
      expect(r.stderr).toContain("foo");
    } finally {
      tree.cleanup();
    }
  });

  test("--expected-version with leading v → exit 1", () => {
    const tree = makeStagingTree();
    try {
      const r = runCheck(tree.scriptPath, [
        "--scope", "publish",
        "--expected-version", `v${EXPECTED}`,
      ]);
      expect(r.exitCode).not.toBe(0);
      expect(r.stderr).toContain(`v${EXPECTED}`);
    } finally {
      tree.cleanup();
    }
  });

  test("missing --expected-version → exit 1, stderr mentions the flag", () => {
    const tree = makeStagingTree();
    try {
      const r = runCheck(tree.scriptPath, ["--scope", "publish"]);
      expect(r.exitCode).not.toBe(0);
      expect(r.stderr).toContain("--expected-version");
    } finally {
      tree.cleanup();
    }
  });
});
