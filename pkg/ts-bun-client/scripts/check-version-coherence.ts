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
 *
 * --scope publish additionally performs a SHA-256 round-trip check (SR-1.3 /
 * SR-1.5): reads AGENT_DIRECTOR_RELEASE_SHASUMS (the manifest written by
 * verify_phase) and re-hashes every listed tarball via node:crypto streaming
 * reads.  A hash mismatch means the tarball was mutated after verify_phase
 * packed it — abort before npm publish fires.
 */

import { existsSync, readFileSync, createReadStream } from "node:fs";
import { createHash } from "node:crypto";
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
  distIndexJs: string;
  versionFloorSrc: string;
  versionFloorDist: string;
}

function resolveRepoPaths(dir: string): RepoPaths {
  return {
    umbrellaJson: resolve(dir, "../package.json"),
    distIndexJs: resolve(dir, "../dist/index.js"),
    versionFloorSrc: resolve(dir, "../version-floor.json"),
    versionFloorDist: resolve(dir, "../dist/version-floor.json"),
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

// Site 3a: umbrella package.json::version == ver.
function checkSite3a(ver: string): void {
  const pkgPath = paths.umbrellaJson;
  const pkg = JSON.parse(readFileSync(pkgPath, "utf8")) as { version?: string; [k: string]: unknown };
  const got = typeof pkg.version === "string" ? pkg.version : String(pkg.version);
  if (got !== ver) {
    fail("site-3a", pkgPath, got, ver);
  }
}

// Site floor-lockstep: version-floor.json single-source-of-truth coherence
// per SR-5.4 / SR-5.2.  Asserts:
//   - source-of-truth pkg/ts-bun-client/version-floor.json exists and parses
//   - shipped pkg/ts-bun-client/dist/version-floor.json exists and parses
//   - both files are byte-for-byte identical
//   - min_binary_version passes SR-2.2 strict SemVer 2.0 (uses Epic 1 parser)
//   - min_binary_version is not the dev sentinel "0.0.0-dev"
//   - dist/index.js exports MIN_BINARY_VERSION equal to the parsed JSON field
//     (via dynamic import — the TS-import variant, no positive-grep)
async function checkSiteFloorLockstep(): Promise<void> {
  const srcPath = paths.versionFloorSrc;
  const dstPath = paths.versionFloorDist;
  const distIndexPath = paths.distIndexJs;

  if (!existsSync(srcPath)) {
    failures.push(`[site-floor-lockstep] ${srcPath}: source-of-truth version-floor.json missing`);
    return;
  }
  if (!existsSync(dstPath)) {
    failures.push(`[site-floor-lockstep] ${dstPath}: shipped dist/version-floor.json missing — run bun build`);
    return;
  }

  const srcRaw = readFileSync(srcPath);
  const dstRaw = readFileSync(dstPath);
  if (!srcRaw.equals(dstRaw)) {
    failures.push(
      `[site-floor-lockstep] source vs dist mismatch: ${srcPath} and ${dstPath} are not byte-equal — build.ts copy step drifted`,
    );
  }

  let parsed: { min_binary_version?: unknown };
  try {
    parsed = JSON.parse(srcRaw.toString("utf8")) as { min_binary_version?: unknown };
  } catch (e) {
    failures.push(`[site-floor-lockstep] ${srcPath}: not valid JSON: ${(e as Error).message}`);
    return;
  }
  if (typeof parsed.min_binary_version !== "string") {
    failures.push(`[site-floor-lockstep] ${srcPath}: min_binary_version must be a string`);
    return;
  }
  const floor = parsed.min_binary_version;

  if (floor === "0.0.0-dev") {
    failures.push(
      `[site-floor-lockstep] ${srcPath}: min_binary_version="0.0.0-dev" — the dev sentinel is forbidden as a release floor (SR-5.2)`,
    );
  }

  // Strict SemVer 2.0 check (SR-2.2). Reject any input the library's
  // discovery pipeline would classify as unparseable.
  if (!/^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.test(floor)) {
    failures.push(
      `[site-floor-lockstep] ${srcPath}: min_binary_version="${floor}" is not strict SemVer 2.0 (SR-2.2)`,
    );
  }

  // TS-import lockstep: MIN_BINARY_VERSION imported from the built bundle
  // must equal the parsed JSON's field. This is the SR-5.4 invariant
  // (positive-grep variant explicitly rejected).
  if (!existsSync(distIndexPath)) {
    // Reported by site-dist-no-inline already; skip the import here.
    return;
  }
  try {
    const mod = (await import(distIndexPath)) as { MIN_BINARY_VERSION?: unknown };
    if (typeof mod.MIN_BINARY_VERSION !== "string") {
      failures.push(
        `[site-floor-lockstep] ${distIndexPath}: MIN_BINARY_VERSION export missing or not a string`,
      );
    } else if (mod.MIN_BINARY_VERSION !== floor) {
      failures.push(
        `[site-floor-lockstep] ${distIndexPath}: MIN_BINARY_VERSION="${mod.MIN_BINARY_VERSION}" disagrees with version-floor.json field "${floor}"`,
      );
    }
  } catch (e) {
    failures.push(
      `[site-floor-lockstep] ${distIndexPath}: failed to import: ${(e as Error).message}`,
    );
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
// SHA-256 streaming helper (publish scope round-trip, SR-1.3 / SR-1.5)
// ---------------------------------------------------------------------------

function sha256Stream(filePath: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const hash = createHash("sha256");
    const stream = createReadStream(filePath);
    stream.on("data", (chunk: Buffer | string) => hash.update(chunk));
    stream.on("end", () => resolve(hash.digest("hex")));
    stream.on("error", (err: Error) => reject(err));
  });
}

// checkPublishShasums re-hashes every tarball listed in the verify_phase
// manifest and compares to the recorded SHA-256.  Failures accumulate in the
// shared `failures` array; caller exits after this returns.
async function checkPublishShasums(): Promise<void> {
  const shasumPath = process.env["AGENT_DIRECTOR_RELEASE_SHASUMS"];
  if (!shasumPath) {
    failures.push("publish-scope check requires AGENT_DIRECTOR_RELEASE_SHASUMS");
    return;
  }
  if (!existsSync(shasumPath)) {
    failures.push(`check-version-coherence: manifest not found: ${shasumPath}`);
    return;
  }

  const manifest = readFileSync(shasumPath, "utf8")
    .split("\n")
    .filter((l) => l.trim().length > 0);

  for (const line of manifest) {
    // Format: <sha256>  <abs-path>  (two spaces, coreutils convention)
    const twoSpaceIdx = line.indexOf("  ");
    if (twoSpaceIdx === -1) {
      failures.push(`check-version-coherence: malformed manifest line: ${line}`);
      continue;
    }
    const expectedHash = line.slice(0, twoSpaceIdx).trim();
    const filePath = line.slice(twoSpaceIdx + 2).trim();

    if (!existsSync(filePath)) {
      failures.push(
        `check-version-coherence: tarball not found (from manifest): ${filePath}`
      );
      continue;
    }

    let actualHash: string;
    try {
      actualHash = await sha256Stream(filePath);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      failures.push(`check-version-coherence: error hashing ${filePath}: ${msg}`);
      continue;
    }

    if (actualHash !== expectedHash) {
      failures.push(
        `check-version-coherence: tarball SHA-256 drift: ${filePath}\n  actual  : ${actualHash}\n  expected: ${expectedHash}`
      );
    }
  }
}

// ---------------------------------------------------------------------------
// ADD NEW VERSION SITES HERE — omitting a site here means it is never checked at release time.
// b.ue3 / Epic 4: site-1 / site-3b / site-4 dropped along with the vendored-
// binary surface.  site-1's release-time enforcement moves to release.sh's
// b.b3h anchor (which spawns the host's dist/ binary directly).  b.5ro:
// site-5 dropped because SKILL.md no longer carries a version field.
const SITES = [
  { id: "site-3a", label: "umbrella package.json::version", check: (v: string) => checkSite3a(v) },
] as const;

// ---------------------------------------------------------------------------
// Scope → sites map
// Both verify and publish run the standard SITES checks.
// verify additionally runs the dist negative-grep (site-dist-no-inline).
// publish additionally runs the SHA-256 round-trip check (SR-1.3 / SR-1.5).
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

// SR-2.3: negative-grep on dist/index.js — verify and publish scopes.
// SR-2.2: publish ⊇ verify; dist/index.js is present in the stage dir at
// publish time, so the check is runnable and required under both scopes.
checkSiteDistNoInline();

// SR-5.4: version-floor.json triple-source coherence — verify and publish.
// Source-of-truth JSON, shipped JSON, and bundle's MIN_BINARY_VERSION must
// agree byte-for-byte.  Runs under both scopes; release.sh always rebuilds
// dist/ before either scope, so the dynamic-import is always reachable.
await checkSiteFloorLockstep();

// SR-1.3 / SR-1.5: tarball SHA-256 round-trip — publish scope only.
// Re-hash every tarball from the verify_phase manifest; mismatch means bytes
// were mutated after verify_phase packed them.  Must run before npm publish.
if (scope === "publish") {
  await checkPublishShasums();
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
