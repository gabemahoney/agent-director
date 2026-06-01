/**
 * check-version-coherence.ts — verifies that all version-stamp sites agree
 * with the expected release version.
 *
 * Usage:
 *   bun run scripts/check-version-coherence.ts --scope <verify|publish> --expected-version X.Y.Z
 *
 * Exits 0 when all checked sites agree with --expected-version.
 * Exits non-zero when any site disagrees; all sites are checked before exiting
 * so the full failure set is reported in one pass.
 *
 * Path resolution mirrors version-bump.ts: anchored at import.meta.url so the
 * script works in both the source tree and a cp -a'd stage dir.
 */

import { existsSync, readFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);

if (args.length === 0) {
  console.error(
    "Usage: bun run scripts/check-version-coherence.ts --scope <verify|publish> --expected-version X.Y.Z"
  );
  process.exit(1);
}

const scopeIdx = args.indexOf("--scope");
if (scopeIdx === -1 || !args[scopeIdx + 1]) {
  console.error("Error: --scope <verify|publish> is required");
  process.exit(1);
}
const scope = args[scopeIdx + 1];
if (scope !== "verify" && scope !== "publish") {
  console.error(
    `Error: invalid --scope "${scope}": must be "verify" or "publish"`
  );
  process.exit(1);
}

const evIdx = args.indexOf("--expected-version");
if (evIdx === -1 || !args[evIdx + 1]) {
  console.error("Error: --expected-version X.Y.Z is required");
  process.exit(1);
}
const expectedVersion = args[evIdx + 1];
if (expectedVersion.startsWith("v")) {
  console.error(
    `Error: --expected-version must be bare semver (e.g. 1.2.3), not "${expectedVersion}" (remove leading v)`
  );
  process.exit(1);
}
if (!/^\d+\.\d+\.\d+$/.test(expectedVersion)) {
  console.error(
    `Error: --expected-version "${expectedVersion}" is not valid semver (expected X.Y.Z)`
  );
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Path resolution — works from both source tree and stage-dir copy
// ---------------------------------------------------------------------------

const scriptDir = dirname(fileURLToPath(import.meta.url));

interface RepoPaths {
  umbrellaJson: string;
  platformJsons: [string, string];
  platformBins: [string, string];
  skillMd: string;
  distIndexJs: string;
}

function resolveRepoPaths(dir: string): RepoPaths {
  return {
    umbrellaJson: resolve(dir, "../package.json"),
    platformJsons: [
      resolve(dir, "../platforms/linux-x64/package.json"),
      resolve(dir, "../platforms/darwin-arm64/package.json"),
    ],
    platformBins: [
      resolve(dir, "../platforms/linux-x64/bin/agent-director"),
      resolve(dir, "../platforms/darwin-arm64/bin/agent-director"),
    ],
    skillMd: resolve(dir, "../../../skills/install-agent-director/SKILL.md"),
    distIndexJs: resolve(dir, "../dist/index.js"),
  };
}

const paths = resolveRepoPaths(scriptDir);

// ---------------------------------------------------------------------------
// Failure accumulator — collect all failures before exiting
// ---------------------------------------------------------------------------

const failures: string[] = [];

function fail(
  siteId: string,
  filePath: string,
  actual: string,
  expected: string
): void {
  failures.push(
    `[${siteId}] ${filePath}: got "${actual}", expected "${expected}"`
  );
}

// ---------------------------------------------------------------------------
// Site check functions
// ---------------------------------------------------------------------------

// Site 1: CLI binary `version --json` output — `version == "v${ver}"`.
// A platform whose bin/agent-director is absent is silently skipped; the
// binary is only guaranteed present on the host platform during verify_phase.
function checkSite1(ver: string): void {
  const platformEntries: Array<[string, string]> = [
    ["site-1/linux-x64", paths.platformBins[0]],
    ["site-1/darwin-arm64", paths.platformBins[1]],
  ];
  const expected = `v${ver}`;
  for (const [siteId, binPath] of platformEntries) {
    if (!existsSync(binPath)) {
      console.log(`check-version-coherence [${siteId}]: skipped — binary not present at ${binPath}`);
      continue;
    }
    let proc: ReturnType<typeof Bun.spawnSync>;
    try {
      proc = Bun.spawnSync([binPath, "version", "--json"]);
    } catch (e) {
      // ENOEXEC: binary is for a different architecture — skip rather than fail.
      // This is normal in verify_phase where only the host platform is executable.
      const msg = e instanceof Error ? e.message : String(e);
      if (msg.includes("ENOEXEC") || (e as NodeJS.ErrnoException).code === "ENOEXEC") {
        console.log(`check-version-coherence [${siteId}]: skipped — cannot execute binary on this platform (${binPath})`);
        continue;
      }
      fail(siteId, binPath, `spawn error: ${msg}`, expected);
      continue;
    }
    if (proc.exitCode !== 0) {
      const stderr = proc.stderr ? new TextDecoder().decode(proc.stderr) : "";
      fail(siteId, binPath, `binary exited ${proc.exitCode}: ${stderr.trim()}`, expected);
      continue;
    }
    const stdout = new TextDecoder().decode(proc.stdout).trim();
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(stdout) as Record<string, unknown>;
    } catch {
      fail(siteId, binPath, `non-JSON output: ${stdout}`, expected);
      continue;
    }
    const got = typeof parsed.version === "string" ? parsed.version : JSON.stringify(parsed.version);
    if (got !== expected) {
      fail(siteId, binPath, got, expected);
    }
  }
}

// Site 3a: umbrella package.json::version == ver.
function checkSite3a(ver: string): void {
  const pkgPath = paths.umbrellaJson;
  const pkg = JSON.parse(readFileSync(pkgPath, "utf8")) as { version?: string; [k: string]: unknown };
  const got = typeof pkg.version === "string" ? pkg.version : String(pkg.version);
  if (got !== ver) {
    fail("site-3a", pkgPath, got, ver);
  }
}

// Site 3b: platform package.json::version == ver for each present platform dir.
function checkSite3b(ver: string): void {
  const platformEntries: Array<[string, string]> = [
    ["site-3b/linux-x64", paths.platformJsons[0]],
    ["site-3b/darwin-arm64", paths.platformJsons[1]],
  ];
  for (const [siteId, pkgPath] of platformEntries) {
    if (!existsSync(pkgPath)) {
      console.log(`check-version-coherence [${siteId}]: skipped — package.json not present at ${pkgPath}`);
      continue;
    }
    const pkg = JSON.parse(readFileSync(pkgPath, "utf8")) as { version?: string; [k: string]: unknown };
    const got = typeof pkg.version === "string" ? pkg.version : String(pkg.version);
    if (got !== ver) {
      fail(siteId, pkgPath, got, ver);
    }
  }
}

// Site 4: umbrella optionalDependencies pins.
// --scope verify: skip when all entries are still file: paths (verify_phase
//   deliberately does not stamp opt-deps). Fail on any other non-pin value.
// --scope publish: all entries must be ^${ver}.
function checkSite4(ver: string, currentScope: string): void {
  const pkgPath = paths.umbrellaJson;
  const pkg = JSON.parse(readFileSync(pkgPath, "utf8")) as {
    optionalDependencies?: Record<string, string>;
    [k: string]: unknown;
  };
  const optDeps = pkg.optionalDependencies ?? {};
  const OPTIONAL_NAMES = [
    "@agent-director/linux-x64",
    "@agent-director/darwin-arm64",
  ];
  const expected = `^${ver}`;

  if (currentScope === "verify") {
    const allFile = OPTIONAL_NAMES.every((n) =>
      (optDeps[n] ?? "").startsWith("file:")
    );
    if (allFile) {
      console.log(
        `check-version-coherence [site-4]: skipped — verify scope leaves opt-deps as file: paths`
      );
      return;
    }
    // Any non-file: value in verify scope that is not the expected pin is a failure.
    for (const name of OPTIONAL_NAMES) {
      const got = optDeps[name] ?? "(missing)";
      if (!got.startsWith("file:") && got !== expected) {
        fail("site-4", pkgPath, `${name}=${got}`, `${name}=${expected}`);
      }
    }
    return;
  }

  // publish scope: every entry must be ^${ver}.
  for (const name of OPTIONAL_NAMES) {
    const got = optDeps[name] ?? "(missing)";
    if (got !== expected) {
      fail("site-4", pkgPath, `${name}=${got}`, `${name}=${expected}`);
    }
  }
}

// Site 5: SKILL.md frontmatter version: == ver.
// Parses frontmatter the same way version-bump.ts does.
function checkSite5(ver: string): void {
  const skillMdPath = paths.skillMd;
  const raw = readFileSync(skillMdPath, "utf8");
  const lines = raw.split("\n");

  if (lines[0] !== "---") {
    fail("site-5", skillMdPath, "(no frontmatter opening ---)", ver);
    return;
  }
  const closeIdx = lines.indexOf("---", 1);
  if (closeIdx === -1) {
    fail("site-5", skillMdPath, "(no closing --- in frontmatter)", ver);
    return;
  }

  const frontmatter = lines.slice(1, closeIdx);
  const versionLine = frontmatter.find((l) => /^version:\s*/.test(l));
  if (!versionLine) {
    fail("site-5", skillMdPath, "(no version: line in frontmatter)", ver);
    return;
  }

  const match = versionLine.match(/^version:\s*(.+)$/);
  const rawVal = match ? match[1].trim() : "";
  const got = rawVal.replace(/^["']|["']$/g, "");

  if (got !== ver) {
    fail("site-5", skillMdPath, got, ver);
  }
}

// Site dist-no-inline: dist/index.js must not contain the literal identifier
// NPM_PACKAGE_VERSION or the placeholder string "0.0.0" — both indicate the
// build-time JSON import was not fully replaced by the runtime loader (SR-2.3).
// dist/index.js must exist; if missing, bun build was not run before the gate.
function checkSiteDistNoInline(): void {
  const distPath = paths.distIndexJs;
  if (!existsSync(distPath)) {
    failures.push(
      `[site-dist-no-inline] ${distPath}: file not found — run bun build before the version-coherence gate`
    );
    return;
  }
  const src = readFileSync(distPath, "utf8");
  const FORBIDDEN: Array<[string, string]> = [
    ["NPM_PACKAGE_VERSION", 'identifier "NPM_PACKAGE_VERSION" must be absent after bun build'],
    ['"0.0.0"', 'placeholder "0.0.0" must be absent after bun build'],
  ];
  for (const [substr, reason] of FORBIDDEN) {
    if (src.includes(substr)) {
      failures.push(`[site-dist-no-inline] ${distPath}: found ${substr} — ${reason}`);
    }
  }
}

// ---------------------------------------------------------------------------
// ADD NEW VERSION SITES HERE — omitting a site here means it is never checked at release time
const SITES = [
  { id: "site-1",  label: "CLI binary version output (linux-x64, darwin-arm64)", check: (v: string) => checkSite1(v) },
  { id: "site-3a", label: "umbrella package.json::version",                       check: (v: string) => checkSite3a(v) },
  { id: "site-3b", label: "platform package.json::version (linux-x64, darwin-arm64)", check: (v: string) => checkSite3b(v) },
  { id: "site-4",  label: "umbrella optionalDependencies pin (^X.Y.Z)",            check: (v: string) => checkSite4(v, scope) },
  { id: "site-5",  label: "SKILL.md frontmatter version:",                         check: (v: string) => checkSite5(v) },
] as const;

// ---------------------------------------------------------------------------
// Scope → sites map
// Both verify and publish run the standard SITES checks.
// verify additionally runs the dist negative-grep (site-dist-no-inline).
// Epic 4 extends publish without reorganizing dispatch by adding entries here.
// ---------------------------------------------------------------------------

const SCOPE_SITES: Record<string, typeof SITES> = {
  verify: SITES,
  publish: SITES,
};

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

const sitesToRun = SCOPE_SITES[scope];
for (const site of sitesToRun) {
  site.check(expectedVersion);
}

// SR-2.3: negative-grep on dist/index.js — verify scope only.
// publish scope skips this because dist/ is not part of the published artifact
// path checked at publish time; the gate already ran during verify_phase.
if (scope === "verify") {
  checkSiteDistNoInline();
}

if (failures.length > 0) {
  for (const f of failures) {
    console.error(f);
  }
  process.exit(1);
}

console.log(
  `check-version-coherence [--scope ${scope}]: all sites agree at ${expectedVersion}`
);
