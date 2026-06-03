/**
 * release-version-coherence.test.ts — end-to-end regression test for the
 * release version-coherence flow (Plan Bee b.xsh, Epic 5).
 *
 * Regression guarded: a staged tree stamped to 9.9.9 via version-bump.ts,
 * packed via bun pm pack, installed in a consumer fixture, and verified via
 * client.version() must return exactly "9.9.9". Any deviation indicates that
 * a version site was missed in the stamp pass or that client.version() is
 * reading from a source other than the installed package.json.
 *
 * Caveat: version-bump.ts enforces /^\d+\.\d+\.\d+$/ (strict X.Y.Z, line 60).
 * The SRD's "9.9.9-test" would fail validation at the version-bump step. This
 * test uses plain "9.9.9".
 *
 * Platform guard: test runs only on linux-x64 or darwin-arm64, mirroring
 * setup.ts. Other platforms emit console.warn and skip silently. Likewise if
 * CLI_PATH is absent (setup.ts preload not run).
 */

import { test, expect } from "bun:test";
import {
  mkdtempSync,
  rmSync,
  readFileSync,
  readdirSync,
  writeFileSync,
  mkdirSync,
  cpSync,
  chmodSync,
  existsSync,
} from "node:fs";
import { createHash } from "node:crypto";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const TARGET_VERSION = "9.9.9";

const PKG_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const REPO_ROOT = resolve(PKG_DIR, "../..");

// Live script paths — existence-checked before staging; staged copies are run.
const COHERENCE_SCRIPT = join(PKG_DIR, "scripts", "check-version-coherence.ts");
// (VERSION_BUMP_SCRIPT existence is implied by the source tree at this point)

// ---------------------------------------------------------------------------
// Platform detection (mirrors setup.ts)
// ---------------------------------------------------------------------------

const platformTuple = (() => {
  if (process.platform === "linux" && process.arch === "x64") return "linux-x64";
  if (process.platform === "darwin" && process.arch === "arm64") return "darwin-arm64";
  return null;
})();

// ---------------------------------------------------------------------------
// Sentinel snapshot helpers
// ---------------------------------------------------------------------------

function sha256OfFile(filePath: string): string {
  const h = createHash("sha256");
  h.update(readFileSync(filePath));
  return h.digest("hex");
}

function readSkillFrontmatterVersion(skillMdPath: string): string {
  const lines = readFileSync(skillMdPath, "utf8").split("\n");
  let inFrontmatter = false;
  for (const line of lines) {
    if (line.trim() === "---") {
      if (!inFrontmatter) { inFrontmatter = true; continue; }
      break;
    }
    if (inFrontmatter) {
      const m = line.match(/^version:\s*(.+)$/);
      if (m) return m[1].trim();
    }
  }
  throw new Error(`No frontmatter version found in ${skillMdPath}`);
}

// ---------------------------------------------------------------------------
// Subprocess helper — async so tests can use Bun.spawn (not spawnSync)
// ---------------------------------------------------------------------------

interface SpawnResult {
  exitCode: number | null;
  stdout: string;
  stderr: string;
}

async function spawn(
  cmd: string[],
  opts: { cwd?: string; env?: Record<string, string> } = {}
): Promise<SpawnResult> {
  const proc = Bun.spawn(cmd, {
    cwd: opts.cwd,
    env: opts.env ? { ...process.env, ...opts.env } : process.env,
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  await proc.exited;
  return { exitCode: proc.exitCode, stdout, stderr };
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

test(
  "release-version-coherence: staged tree → pack → install → client.version() === 9.9.9",
  async () => {
    // Guard: unsupported platform.
    if (platformTuple === null) {
      console.warn(
        "release-version-coherence.test.ts: unsupported platform " +
          `${process.platform}/${process.arch} — skipping`
      );
      return;
    }

    // Guard: CLI not built (setup.ts preload not run).
    const cliPath = process.env.CLI_PATH ?? "";
    if (!cliPath || !existsSync(cliPath)) {
      console.warn(
        "release-version-coherence.test.ts: CLI_PATH not set or binary absent — skipping"
      );
      return;
    }

    // Guard: check-version-coherence.ts must exist. NOT a silent skip —
    // its absence is the regression this test is designed to catch.
    if (!existsSync(COHERENCE_SCRIPT)) {
      throw new Error(
        `release-version-coherence: missing required script: ${COHERENCE_SCRIPT} — ` +
          "Epic 3 (check-version-coherence.ts) has not landed"
      );
    }

    // -----------------------------------------------------------------------
    // Step 2: Snapshot live sentinel values BEFORE staging.
    // -----------------------------------------------------------------------

    const liveUmbrellaPkg  = join(PKG_DIR, "package.json");
    const liveLinuxPkg     = join(PKG_DIR, "platforms", "linux-x64", "package.json");
    const liveDarwinPkg    = join(PKG_DIR, "platforms", "darwin-arm64", "package.json");
    const liveSkillMd      = join(REPO_ROOT, "skills", "install-agent-director", "SKILL.md");
    const liveDistIndexJs  = join(PKG_DIR, "dist", "index.js");

    const sentinels = {
      umbrellaRaw:  readFileSync(liveUmbrellaPkg, "utf8"),
      linuxPkgRaw:  readFileSync(liveLinuxPkg, "utf8"),
      darwinPkgRaw: readFileSync(liveDarwinPkg, "utf8"),
      skillMdRaw:   readFileSync(liveSkillMd, "utf8"),
      distHash:     existsSync(liveDistIndexJs) ? sha256OfFile(liveDistIndexJs) : null,
    };

    // -----------------------------------------------------------------------
    // Steps 3–12 wrapped in try/finally for reliable cleanup.
    // -----------------------------------------------------------------------

    // Allocate all three temp dirs before the try so cleanup can reference them.
    const stageDir       = mkdtempSync(join(tmpdir(), "ad-rvc-stage-"));
    const consumerDir    = mkdtempSync(join(tmpdir(), "ad-rvc-consumer-"));
    const sandboxedHome  = mkdtempSync(join(tmpdir(), "ad-rvc-home-"));

    try {
      // -----------------------------------------------------------------------
      // Step 4: Stage the live tree into stageDir.
      // -----------------------------------------------------------------------

      const stagePkgDir = join(stageDir, "pkg", "ts-bun-client");
      mkdirSync(stagePkgDir, { recursive: true });
      cpSync(join(PKG_DIR, "."), stagePkgDir, { recursive: true });

      mkdirSync(join(stageDir, "skills"), { recursive: true });
      cpSync(
        join(REPO_ROOT, "skills", "install-agent-director"),
        join(stageDir, "skills", "install-agent-director"),
        { recursive: true }
      );

      mkdirSync(join(stageDir, "pkg", "api", "errnames"), { recursive: true });
      cpSync(
        join(REPO_ROOT, "pkg", "api", "errnames", "catalog.json"),
        join(stageDir, "pkg", "api", "errnames", "catalog.json")
      );

      // Wipe stowaway dev artifacts.
      rmSync(join(stagePkgDir, "node_modules"), { recursive: true, force: true });
      rmSync(join(stagePkgDir, "skills"), { recursive: true, force: true });

      // Wipe staged platform binaries — setup.ts stages the dev binary into
      // platforms/*/bin/; check-version-coherence.ts site-1 would fail against
      // a dev-versioned binary. With binaries absent, site-1 is skipped as
      // "binary not present", which is the verify_phase semantic.
      // (release.sh builds the binary with -X ...Version=vX.Y.Z ldflags in
      // build_phase BEFORE verify_phase runs the gate; we can't replicate that.)
      // The dev binary is re-injected below after the coherence gate for the
      // consumer install step.
      rmSync(join(stagePkgDir, "platforms", "linux-x64", "bin"), { recursive: true, force: true });
      rmSync(join(stagePkgDir, "platforms", "darwin-arm64", "bin"), { recursive: true, force: true });

      // -----------------------------------------------------------------------
      // Step 5: Stamp version sites (no opt-deps — file: paths must survive for
      // the consumer install to resolve platforms/ locally).
      //
      // Run the STAGED copy via a relative path so import.meta.url resolves
      // within the stage dir — mirrors release.sh `cd $stage_dir && bun run
      // scripts/version-bump.ts`.
      // -----------------------------------------------------------------------

      const bumpResult = await spawn(
        [
          "bun", "run", "scripts/version-bump.ts",
          "--version", TARGET_VERSION,
          "--target", "umbrella-version",
          "--target", "platform-version",
          "--target", "skill-frontmatter",
        ],
        { cwd: stagePkgDir }
      );
      expect(bumpResult.exitCode).toBe(0);

      // -----------------------------------------------------------------------
      // Step 6: Run version-coherence gate (--scope verify).
      // Run the staged copy so path resolution is scoped to the stage dir.
      // -----------------------------------------------------------------------

      const coherenceResult = await spawn(
        [
          "bun", "run", "scripts/check-version-coherence.ts",
          "--scope", "verify",
          "--expected-version", TARGET_VERSION,
        ],
        { cwd: stagePkgDir }
      );
      expect(coherenceResult.exitCode).toBe(0);

      // Re-inject the dev binary into the staged platform dir so the consumer
      // install step can wire the CLI into node_modules. The binary version
      // stamp is irrelevant here: client.version() reads the npm package
      // version from package.json (9.9.9), not the CLI binary version.
      const stageBinDir = join(stagePkgDir, "platforms", platformTuple, "bin");
      mkdirSync(stageBinDir, { recursive: true });
      const stageBinPath = join(stageBinDir, "agent-director");
      cpSync(cliPath, stageBinPath);
      chmodSync(stageBinPath, 0o755); // cpSync does not preserve mode

      // -----------------------------------------------------------------------
      // Step 7: bun install → bun run build → bun pm pack --ignore-scripts
      // -----------------------------------------------------------------------

      const installResult = await spawn(
        ["bun", "install", "--no-progress"],
        { cwd: stagePkgDir }
      );
      expect(installResult.exitCode).toBe(0);

      const buildResult = await spawn(
        ["bun", "run", "build"],
        { cwd: stagePkgDir }
      );
      expect(buildResult.exitCode).toBe(0);

      // stage-skill.ts copies skills/ into pkg/ts-bun-client/skills/ so the
      // postinstall script finds the skill body inside the tarball. release.sh
      // calls this explicitly because bun pm pack --ignore-scripts skips prepack.
      const stageSkillResult = await spawn(
        ["bun", "run", "scripts/stage-skill.ts"],
        { cwd: stagePkgDir }
      );
      expect(stageSkillResult.exitCode).toBe(0);

      const packResult = await spawn(
        ["bun", "pm", "pack", "--ignore-scripts"],
        { cwd: stagePkgDir }
      );
      expect(packResult.exitCode).toBe(0);

      // Locate the produced tarball.
      const tgzFiles = readdirSync(stagePkgDir).filter((f) =>
        f.startsWith("agent-director-") && f.endsWith(".tgz")
      );

      expect(tgzFiles.length).toBeGreaterThan(0);
      const tgzPath = join(stagePkgDir, tgzFiles[0] as string);

      // -----------------------------------------------------------------------
      // Step 8: Consumer fixture — sandboxed HOME, bun add via file: URLs only.
      // -----------------------------------------------------------------------

      const stagePlatformDir = join(stagePkgDir, "platforms", platformTuple);

      writeFileSync(
        join(consumerDir, "package.json"),
        JSON.stringify(
          {
            name: "ad-rvc-consumer",
            version: "0.0.1",
            type: "module",
            trustedDependencies: ["agent-director", `@agent-director/${platformTuple}`],
          },
          null,
          2
        ) + "\n",
        "utf8"
      );

      const addResult = await spawn(
        [
          "bun", "add",
          `file:${tgzPath}`,
          `@agent-director/${platformTuple}@file:${stagePlatformDir}`,
        ],
        {
          cwd: consumerDir,
          env: { HOME: sandboxedHome },
        }
      );
      expect(addResult.exitCode).toBe(0);

      // -----------------------------------------------------------------------
      // Step 9: One-shot consumer.ts — invoke client.version(), emit JSON.
      // -----------------------------------------------------------------------

      // Stage the dev CLI binary at $sandboxedHome/.agent-director/bin/agent-director
      // so the consumer's Client.create() discovery (SR-1.1 step 1) finds it.
      // After b.ue3 the consumer is a system-install consumer; no vendored binary.
      const sandboxedAdBinDir = join(sandboxedHome, ".agent-director", "bin");
      mkdirSync(sandboxedAdBinDir, { recursive: true });
      const sandboxedAdBin = join(sandboxedAdBinDir, "agent-director");
      cpSync(cliPath, sandboxedAdBin);
      chmodSync(sandboxedAdBin, 0o755);

      const consumerStorePath = join(consumerDir, "state.db");
      writeFileSync(
        join(consumerDir, "consumer.ts"),
        [
          `import { Client } from "agent-director";`,
          `const c = await Client.create({ storePath: ${JSON.stringify(consumerStorePath)}, createIfMissing: true });`,
          `const result = await c.version({});`,
          `console.log(JSON.stringify(result));`,
          `c.close();`,
        ].join("\n") + "\n",
        "utf8"
      );

      const consumerResult = await spawn(
        ["bun", "run", "consumer.ts"],
        { cwd: consumerDir, env: { HOME: sandboxedHome } }
      );
      expect(consumerResult.exitCode).toBe(0);

      // Parse the first JSON line from stdout.
      const jsonLine = consumerResult.stdout
        .split("\n")
        .map((l) => l.trim())
        .find((l) => l.startsWith("{"));
      expect(jsonLine).toBeDefined();

      const parsed = JSON.parse(jsonLine!) as Record<string, unknown>;
      expect(parsed.version).toBe(TARGET_VERSION);

      // -----------------------------------------------------------------------
      // Step 10: Scan installed dist/index.js for the target version literal.
      //
      // The original spec says /\b\d+\.\d+\.\d+\b/g → length === 0, but that
      // regex also catches MIN_BUN_VERSION ("1.0.21") in platformResolve.ts —
      // a legitimate constant that is NOT a version-stamp inlining bug.
      // The actual regression we guard is: bun build must NOT inline the npm
      // package version ("9.9.9") into dist/. Epic 3 makes version() resolve
      // from package.json at runtime, so TARGET_VERSION must be absent here.
      // -----------------------------------------------------------------------

      const installedDistJs = join(
        consumerDir,
        "node_modules",
        "agent-director",
        "dist",
        "index.js"
      );
      expect(existsSync(installedDistJs)).toBe(true);

      const distSrc = readFileSync(installedDistJs, "utf8");
      // Must not contain the package version literal — indicates bun build
      // inlined it rather than deferring to the package.json runtime read.
      expect(distSrc).not.toContain(`"${TARGET_VERSION}"`);
      expect(distSrc).not.toContain(`'${TARGET_VERSION}'`);

    } finally {
      // -----------------------------------------------------------------------
      // Step 11: Cleanup — remove all three temp dirs on both pass and fail.
      // -----------------------------------------------------------------------
      rmSync(stageDir, { recursive: true, force: true });
      rmSync(consumerDir, { recursive: true, force: true });
      rmSync(sandboxedHome, { recursive: true, force: true });
    }

    // -----------------------------------------------------------------------
    // Step 12: Post-cleanup sentinel assertion — live tree must be unchanged.
    // -----------------------------------------------------------------------

    expect(readFileSync(liveUmbrellaPkg, "utf8")).toBe(sentinels.umbrellaRaw);
    expect(readFileSync(liveLinuxPkg, "utf8")).toBe(sentinels.linuxPkgRaw);
    expect(readFileSync(liveDarwinPkg, "utf8")).toBe(sentinels.darwinPkgRaw);
    expect(readFileSync(liveSkillMd, "utf8")).toBe(sentinels.skillMdRaw);

    if (sentinels.distHash !== null) {
      expect(sha256OfFile(liveDistIndexJs)).toBe(sentinels.distHash);
    }
  },
  120_000
);
