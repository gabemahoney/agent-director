/**
 * version-bump.ts — rewrites the two optionalDependencies `file:` entries
 * in pkg/ts-bun-client/package.json to `^<version>` pins for publishing.
 *
 * Usage:
 *   bun run scripts/version-bump.ts --version X.Y.Z
 *
 * ─── Local development vs. publish-time flow ──────────────────────────────
 *
 * During local development the two optional sub-packages are wired via
 * `file:` paths so `bun install` resolves them from the workspace:
 *
 *   "@agent-director/linux-x64":  "file:./platforms/linux-x64"
 *   "@agent-director/darwin-arm64": "file:./platforms/darwin-arm64"
 *
 * Before publishing to npm, CI must:
 *   1. Build each sub-package binary for its target platform.
 *   2. Run `bun run version-bump-publish --version X.Y.Z` to rewrite the
 *      `file:` entries to `^X.Y.Z` registry pins.
 *   3. Publish the two sub-packages first (`npm publish` in each platforms/* subdir).
 *   4. Publish the top-level package.
 *
 * Running this script with the same version twice is a no-op on the second
 * run (idempotent): if all three entries already contain `^X.Y.Z` the file
 * is not rewritten.
 *
 * Restoring `file:` paths after the script runs:
 *   - Direct script use: `git checkout pkg/ts-bun-client/package.json`
 *     restores the `file:` entries for local development.
 *   - Via release.sh pipeline: no restore needed — publish_phase operates
 *     exclusively on a temporary stage directory (T4A invariant); the live
 *     working tree is never modified.
 * ─────────────────────────────────────────────────────────────────────────
 */

import { readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Parse --version argument
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);
const versionIdx = args.indexOf("--version");
if (versionIdx === -1 || !args[versionIdx + 1]) {
  console.error("Usage: bun run scripts/version-bump.ts --version X.Y.Z");
  process.exit(1);
}
const version = args[versionIdx + 1];

// Validate: must be X.Y.Z with optional leading zeros.
if (!/^\d+\.\d+\.\d+$/.test(version)) {
  console.error(`Error: version "${version}" is not valid semver (expected X.Y.Z)`);
  process.exit(1);
}

const pin = `^${version}`;

// ---------------------------------------------------------------------------
// Locate and read package.json
// ---------------------------------------------------------------------------

const scriptDir = dirname(fileURLToPath(import.meta.url));
const pkgPath = resolve(scriptDir, "../package.json");

const raw = readFileSync(pkgPath, "utf8");
// Use JSON.parse / JSON.stringify to preserve semantic correctness.
// We do a string replacement on the serialized output to preserve formatting
// as closely as possible rather than re-serializing with a fresh stringify.
const pkg = JSON.parse(raw) as {
  optionalDependencies: Record<string, string>;
  [k: string]: unknown;
};

const optDeps = pkg.optionalDependencies ?? {};
const OPTIONAL_NAMES = [
  "@agent-director/linux-x64",
  "@agent-director/darwin-arm64",
];

// ---------------------------------------------------------------------------
// Idempotency check
// ---------------------------------------------------------------------------

const alreadyPinned = OPTIONAL_NAMES.every((name) => optDeps[name] === pin);
if (alreadyPinned) {
  console.log(`version-bump: already at ${pin} — no changes needed.`);
  process.exit(0);
}

// ---------------------------------------------------------------------------
// Rewrite
// ---------------------------------------------------------------------------

for (const name of OPTIONAL_NAMES) {
  if (name in optDeps) {
    optDeps[name] = pin;
  }
}

// Re-serialize with 2-space indent (matches the existing file style).
const updated = JSON.stringify(pkg, null, 2) + "\n";
writeFileSync(pkgPath, updated, "utf8");

console.log(`version-bump: rewrote optionalDependencies to ${pin} in ${pkgPath}`);
