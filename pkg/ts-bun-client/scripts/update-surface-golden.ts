/**
 * update-surface-golden.ts — regenerate the public-surface golden snapshots.
 *
 * Usage:
 *   bun run build && bun run scripts/update-surface-golden.ts
 *
 * Reads dist/{index,errors,types}.d.ts and copies each to
 * test/fixtures/public-surface/<name>.golden. The test
 * `public-surface.test.ts` then performs byte-identity checks against
 * these snapshots.
 *
 * Regenerate when a PR intentionally changes the public surface (e.g.
 * adds a new error class or verb result field). The reviewer is
 * responsible for confirming the diff is purely additive and that no
 * pre-existing export was renamed, removed, or had its signature reshaped.
 */

import * as path from "path";
import * as fs from "fs";

const pkgRoot = path.resolve(import.meta.dir, "..");
const distDir = path.join(pkgRoot, "dist");
const fixtureDir = path.join(pkgRoot, "test", "fixtures", "public-surface");

const TRACKED = ["index.d.ts", "errors.d.ts", "types.d.ts"];

fs.mkdirSync(fixtureDir, { recursive: true });

for (const f of TRACKED) {
  const src = path.join(distDir, f);
  const dst = path.join(fixtureDir, `${f}.golden`);
  if (!fs.existsSync(src)) {
    console.error(`update-surface-golden: ${src} not found; run 'bun run build' first`);
    process.exit(1);
  }
  fs.copyFileSync(src, dst);
  console.log(`update-surface-golden: ${src} -> ${dst}`);
}
