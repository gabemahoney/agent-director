/**
 * prepublishOnly guard — aborts npm publish if the package name still contains
 * the CHANGEME-H3 placeholder scope.
 *
 * Invoked as: bun run scripts/check-not-placeholder.ts
 * Works from any of the four package directories by resolving package.json
 * relative to process.cwd().
 *
 * See docs/release-blockers.md for the H3 resolution checklist.
 */

import { readFileSync } from "fs";
import { join } from "path";

const pkgPath = join(process.cwd(), "package.json");

let pkg: { name?: string };
try {
  pkg = JSON.parse(readFileSync(pkgPath, "utf8"));
} catch (err) {
  console.error(`check-not-placeholder: failed to read ${pkgPath}: ${err}`);
  process.exit(1);
}

if (typeof pkg.name === "string" && pkg.name.includes("CHANGEME-H3")) {
  console.error(
    `Publish blocked: package.json name contains CHANGEME-H3 placeholder; see docs/release-blockers.md`
  );
  process.exit(1);
}

process.exit(0);
