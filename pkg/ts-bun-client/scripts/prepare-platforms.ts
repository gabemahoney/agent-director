/**
 * prepare-platforms.ts — copies the built native library into each
 * matching platform sub-package directory for local development.
 *
 * Run from pkg/ts-bun-client/ after `make libagent_director`:
 *
 *   bun run prepare-platforms
 *
 * This script only copies the linux-x64 binary (since we are on linux/amd64).
 * Darwin binaries must be built on a macOS host and placed manually.
 * The binary files are gitignored — they are never committed.
 */

import { copyFileSync, existsSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(scriptDir, "..");
const repoRoot = resolve(pkgDir, "../../..");

// Map of: dist source → destination in platforms/
const copies: { src: string; dest: string; optional: boolean }[] = [
  {
    src: resolve(repoRoot, "dist", "libagent_director.so"),
    dest: resolve(pkgDir, "platforms", "linux-x64", "libagent_director.so"),
    optional: false,
  },
  {
    src: resolve(repoRoot, "dist", "libagent_director.dylib"),
    dest: resolve(pkgDir, "platforms", "darwin-x64", "libagent_director.dylib"),
    optional: true,
  },
  {
    src: resolve(repoRoot, "dist", "libagent_director.dylib"),
    dest: resolve(pkgDir, "platforms", "darwin-arm64", "libagent_director.dylib"),
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
        `prepare-platforms: ERROR — ${src} not found. Run 'make libagent_director' first.`
      );
      errors++;
    }
    continue;
  }
  copyFileSync(src, dest);
  console.log(`prepare-platforms: copied ${src} → ${dest}`);
}

if (errors > 0) {
  process.exit(1);
}
