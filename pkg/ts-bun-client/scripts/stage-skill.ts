/**
 * stage-skill.ts — copies the install skill body from the repo root into
 * pkg/ts-bun-client/skills/install-agent-director/ so `bun pm pack` (or
 * `npm pack`) can include it in the published tarball.
 *
 * Invoked as the umbrella's "prepack" script. The staged copy lives under
 * pkg/ts-bun-client/skills/ and is gitignored — it must never be committed.
 *
 * Source of truth: ${REPO_ROOT}/skills/install-agent-director/. Destination
 * (staging): pkg/ts-bun-client/skills/install-agent-director/. The
 * postinstall script (scripts/postinstall.ts) then resolves the skill at
 * runtime via the same relative path inside the published tarball.
 */

import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  rmSync,
  statSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(scriptDir, "..");
const repoRoot = resolve(pkgDir, "..", "..");

const src = join(repoRoot, "skills", "install-agent-director");
const dest = join(pkgDir, "skills", "install-agent-director");

if (!existsSync(src)) {
  console.error(`stage-skill: source not found: ${src}`);
  process.exit(1);
}

function copyTree(s: string, d: string): void {
  const st = statSync(s);
  if (st.isDirectory()) {
    mkdirSync(d, { recursive: true, mode: st.mode & 0o777 });
    for (const ent of readdirSync(s, { withFileTypes: true })) {
      copyTree(join(s, ent.name), join(d, ent.name));
    }
    return;
  }
  if (st.isFile()) {
    copyFileSync(s, d);
    return;
  }
  // Other file types are not expected in a skill body — silently skip.
}

// Clear any previous staging so a removed source file does not survive.
if (existsSync(dest)) {
  rmSync(dest, { recursive: true, force: true });
}
copyTree(src, dest);

console.log(`stage-skill: copied ${src} → ${dest}`);
