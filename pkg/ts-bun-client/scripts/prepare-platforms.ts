/**
 * prepare-platforms.ts — copies the built CLI binary into each matching
 * platform sub-package directory for local development.
 *
 * Run from pkg/ts-bun-client/ after `make release-binaries`:
 *
 *   bun run prepare-platforms
 *
 * The CLI binaries are gitignored under platforms/*/bin/ — they are never
 * committed. release.sh handles the same staging during publish.
 */

import { copyFileSync, existsSync, mkdirSync, chmodSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(scriptDir, "..");
const repoRoot = resolve(pkgDir, "../../..");

const copies: { src: string; dest: string; optional: boolean }[] = [
  {
    src: resolve(repoRoot, "dist", "agent-director-linux-amd64"),
    dest: resolve(pkgDir, "platforms", "linux-x64", "bin", "agent-director"),
    optional: false,
  },
  {
    src: resolve(repoRoot, "dist", "agent-director-darwin-arm64"),
    dest: resolve(pkgDir, "platforms", "darwin-arm64", "bin", "agent-director"),
    optional: true,
  },
];

let errors = 0;
for (const { src, dest, optional } of copies) {
  if (!existsSync(src)) {
    if (optional) {
      console.log(`prepare-platforms: skipping ${src} (not present on this host)`);
    } else {
      console.error(
        `prepare-platforms: ERROR — ${src} not found. Run 'make release-binaries' first.`
      );
      errors++;
    }
    continue;
  }
  mkdirSync(dirname(dest), { recursive: true });
  copyFileSync(src, dest);
  chmodSync(dest, 0o755);
  console.log(`prepare-platforms: copied ${src} → ${dest}`);
}

if (errors > 0) {
  process.exit(1);
}
