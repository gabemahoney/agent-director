/**
 * postinstall.ts — runs after `bun add agent-director` on a supported host.
 *
 * Copies the bundled install skill body from
 *   node_modules/agent-director/skills/install-agent-director/
 * into
 *   ${HOME}/.claude/skills/install-agent-director/
 * via an atomic-rename with a three-way decision
 * (identical-noop / older-or-absent-overwrite-with-backup / newer-leave-alone).
 *
 * Authoritative contract: SRD `t1.fg3.7i` §SR-1 (the postinstall surface),
 * Plan Bee `b.3d3` Epic 1 (postinstall-skill-copy).
 *
 * Constraints:
 *  - Bun runtime only. Pure node stdlib imports (SR-1.1) — no third-party
 *    deps, no import from `src/` (the FFI native binary may not be linked
 *    into `node_modules` at postinstall time).
 *  - Never writes outside `${HOME}/.claude/skills/install-agent-director/`
 *    (plus its sibling tmp + backup paths). Never touches PATH, the state
 *    DB, ~/.claude/settings.json, or install.sh.
 *
 * This file lands the helpers (T2) — source/destination resolution,
 * semver compare, tree-hash, frontmatter `version:` extractor,
 * AD_POSTINSTALL_VERBOSE reader, and a stub `main()`. The three-way
 * branches and atomic-write algorithm land in T3; output budget in T4;
 * the platform refusal lands in Epic 2 T2.
 */

import { randomBytes, createHash } from "node:crypto";
import {
  closeSync,
  copyFileSync,
  existsSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const PACKAGE_NAME = "agent-director";
const SKILL_REL_PATH = join("skills", "install-agent-director");
const DEST_REL_FROM_HOME = join(".claude", "skills", "install-agent-director");

// ---------------------------------------------------------------------------
// AD_POSTINSTALL_VERBOSE env reader (SR-1.6)
// Truthy iff "1", "true", or "yes" (case-insensitive). Anything else
// (including empty/unset) is falsy.
// ---------------------------------------------------------------------------

export function isVerbose(env: NodeJS.ProcessEnv = process.env): boolean {
  const raw = env["AD_POSTINSTALL_VERBOSE"];
  if (raw === undefined) return false;
  const v = raw.trim().toLowerCase();
  return v === "1" || v === "true" || v === "yes";
}

// ---------------------------------------------------------------------------
// Source resolution (SR-1.2)
// Walk up from import.meta.url to the package root, then descend into
// skills/install-agent-director/. The package root is recognized by a
// sibling package.json whose `name` is "agent-director".
// ---------------------------------------------------------------------------

export function resolvePackageRoot(startFile: string): string {
  let dir = dirname(resolve(startFile));
  // Bounded walk so a misplaced script never traverses to "/".
  for (let i = 0; i < 32; i++) {
    const pkgJson = join(dir, "package.json");
    try {
      const raw = readFileSync(pkgJson, "utf8");
      const parsed = JSON.parse(raw) as { name?: string };
      if (parsed.name === PACKAGE_NAME) return dir;
    } catch {
      // Either missing package.json or not the umbrella — keep walking.
    }
    const parent = dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error(
    `postinstall: could not locate ${PACKAGE_NAME} package.json walking up from ${startFile}`,
  );
}

export function resolveSourceSkillDir(packageRoot: string): string {
  return join(packageRoot, SKILL_REL_PATH);
}

export function resolveDestSkillDir(): string {
  return join(homedir(), DEST_REL_FROM_HOME);
}

// ---------------------------------------------------------------------------
// Frontmatter `version:` extractor (SR-1.3)
// Reads a SKILL.md, parses the `---`-delimited YAML frontmatter block, and
// returns the `version:` value as a string. Missing file, missing
// frontmatter, or missing `version` key → "0.0.0" (the older-or-absent
// fallback).
// ---------------------------------------------------------------------------

export function readFrontmatterVersion(skillMdPath: string): string {
  let raw: string;
  try {
    raw = readFileSync(skillMdPath, "utf8");
  } catch {
    return "0.0.0";
  }
  // Frontmatter must be the very first thing in the file. The opening fence
  // is "---" on its own line; the closing fence is the next "---" on its
  // own line.
  const lines = raw.split(/\r?\n/);
  if (lines[0] !== "---") return "0.0.0";
  for (let i = 1; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line === "---") break;
    // Flat YAML only: `key: value` per line.
    const m = /^version:\s*(.+?)\s*$/.exec(line);
    if (m && m[1] !== undefined) {
      // Strip surrounding quotes if any (YAML allows both).
      return m[1].replace(/^["']|["']$/g, "");
    }
  }
  return "0.0.0";
}

// ---------------------------------------------------------------------------
// Semver comparator (SR-1.3, semver §11)
// Parses M.m.p[-prerelease][+build]. Returns -1/0/+1. Inputs that fail to
// parse as semver are treated as "0.0.0".
// ---------------------------------------------------------------------------

type ParsedSemver = {
  major: number;
  minor: number;
  patch: number;
  prerelease: string[]; // empty array means no prerelease (higher precedence)
};

function parseSemver(s: string): ParsedSemver {
  // Strip build metadata (after "+") — ignored for precedence per semver §10.
  const noBuild = s.split("+", 1)[0] ?? s;
  const dashIdx = noBuild.indexOf("-");
  const core = dashIdx >= 0 ? noBuild.slice(0, dashIdx) : noBuild;
  const pre = dashIdx >= 0 ? noBuild.slice(dashIdx + 1) : "";
  const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(core);
  if (!m) return { major: 0, minor: 0, patch: 0, prerelease: [] };
  const prerelease = pre === "" ? [] : pre.split(".");
  // Validate prerelease identifiers — alphanumerics + hyphens only per §9.
  for (const id of prerelease) {
    if (id === "" || !/^[0-9A-Za-z-]+$/.test(id)) {
      return { major: 0, minor: 0, patch: 0, prerelease: [] };
    }
  }
  return {
    major: Number(m[1]),
    minor: Number(m[2]),
    patch: Number(m[3]),
    prerelease,
  };
}

function compareIds(a: string, b: string): number {
  const aNum = /^\d+$/.test(a);
  const bNum = /^\d+$/.test(b);
  if (aNum && bNum) {
    const an = Number(a);
    const bn = Number(b);
    return an < bn ? -1 : an > bn ? 1 : 0;
  }
  // Numeric identifiers have lower precedence than non-numeric (§11).
  if (aNum) return -1;
  if (bNum) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

export function compareSemver(a: string, b: string): -1 | 0 | 1 {
  const pa = parseSemver(a);
  const pb = parseSemver(b);
  if (pa.major !== pb.major) return pa.major < pb.major ? -1 : 1;
  if (pa.minor !== pb.minor) return pa.minor < pb.minor ? -1 : 1;
  if (pa.patch !== pb.patch) return pa.patch < pb.patch ? -1 : 1;
  // §11.4: a version with prerelease has lower precedence than one without.
  const aPre = pa.prerelease.length > 0;
  const bPre = pb.prerelease.length > 0;
  if (aPre && !bPre) return -1;
  if (!aPre && bPre) return 1;
  if (!aPre && !bPre) return 0;
  // Compare prerelease identifiers left-to-right.
  const len = Math.min(pa.prerelease.length, pb.prerelease.length);
  for (let i = 0; i < len; i++) {
    const cmp = compareIds(pa.prerelease[i]!, pb.prerelease[i]!);
    if (cmp !== 0) return cmp < 0 ? -1 : 1;
  }
  // Longer prerelease wins (§11.4.4).
  if (pa.prerelease.length < pb.prerelease.length) return -1;
  if (pa.prerelease.length > pb.prerelease.length) return 1;
  return 0;
}

// ---------------------------------------------------------------------------
// Tree-hash (SR-1.4)
// SHA-256 of `<rel-path>\n<sha256-of-file-bytes>\n` concatenated in sorted
// order. Stable across operator chmod/chown. Cheaper than mtime walks.
// Directories that don't exist hash to the empty string's SHA-256 of the
// canonical empty manifest — they cannot match a non-empty source, so the
// caller's "destination missing" branch never collides with identical.
// ---------------------------------------------------------------------------

function listFilesRecursive(root: string): string[] {
  const out: string[] = [];
  const walk = (dir: string): void => {
    let entries;
    try {
      entries = readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const ent of entries) {
      const full = join(dir, ent.name);
      if (ent.isDirectory()) walk(full);
      else if (ent.isFile()) out.push(full);
      // Symlinks in the *source* are followed (they're package content); the
      // copy step in T3 materializes them as regular files. For tree-hashing
      // we only ever read source/destination trees that are themselves
      // regular files post-copy, so symlinks here would be source-side only.
      else if (ent.isSymbolicLink()) {
        try {
          const st = statSync(full);
          if (st.isFile()) out.push(full);
        } catch {
          // dangling symlink — skip
        }
      }
    }
  };
  walk(root);
  return out;
}

export function treeHash(root: string): string {
  const files = listFilesRecursive(root).sort();
  const outer = createHash("sha256");
  for (const abs of files) {
    const rel = relative(root, abs);
    const inner = createHash("sha256");
    inner.update(readFileSync(abs));
    outer.update(rel);
    outer.update("\n");
    outer.update(inner.digest("hex"));
    outer.update("\n");
  }
  return outer.digest("hex");
}

// ---------------------------------------------------------------------------
// Defensive tmp cleanup (SR-1.5)
// Scan ${HOME}/.claude/skills/ for siblings whose name begins with the literal
// prefix ".install-agent-director.tmp." and rmSync them recursively. Must
// NOT match the destination "install-agent-director" itself, nor any backup
// sibling "install-agent-director.bak.<ts>".
// ---------------------------------------------------------------------------

const TMP_PREFIX = ".install-agent-director.tmp.";

function cleanupOrphanedTmpSiblings(skillsParent: string): void {
  let entries;
  try {
    entries = readdirSync(skillsParent, { withFileTypes: true });
  } catch {
    return; // parent doesn't exist yet — nothing to clean
  }
  for (const ent of entries) {
    if (!ent.name.startsWith(TMP_PREFIX)) continue;
    const full = join(skillsParent, ent.name);
    try {
      rmSync(full, { recursive: true, force: true });
    } catch {
      // best-effort; don't abort the install over a leftover we can't remove
    }
  }
}

// ---------------------------------------------------------------------------
// Recursive copy (SR-1.5)
// Mirrors the source tree under a fresh destination directory. Symlinks in
// the source (package content, not operator-supplied) are followed and
// materialized as regular files. fsyncs every written file and the containing
// directories so a crash-before-rename leaves nothing half-flushed.
// ---------------------------------------------------------------------------

function fsyncDir(dir: string): void {
  // POSIX: open + fsync the directory inode so the entry rename is durable.
  try {
    const fd = openSync(dir, "r");
    try {
      fsyncSync(fd);
    } finally {
      closeSync(fd);
    }
  } catch {
    // fsync on a directory may not be supported on every fs; non-fatal.
  }
}

function fsyncFile(path: string): void {
  try {
    const fd = openSync(path, "r");
    try {
      fsyncSync(fd);
    } finally {
      closeSync(fd);
    }
  } catch {
    // non-fatal
  }
}

function copyTree(src: string, dest: string): void {
  const st = statSync(src); // follows symlinks → regular files materialized
  if (st.isDirectory()) {
    mkdirSync(dest, { recursive: true, mode: st.mode & 0o777 });
    const entries = readdirSync(src, { withFileTypes: true });
    for (const ent of entries) {
      const childSrc = join(src, ent.name);
      const childDest = join(dest, ent.name);
      copyTree(childSrc, childDest);
    }
    fsyncDir(dest);
    return;
  }
  if (st.isFile()) {
    copyFileSync(src, dest);
    fsyncFile(dest);
    return;
  }
  // Anything else (FIFO, socket, char/block device) is not expected in a
  // skill body and is silently skipped.
}

// ---------------------------------------------------------------------------
// Atomic-write algorithm (SR-1.5)
//   1. Compute tmp dir under skillsParent: ".install-agent-director.tmp.<pid>.<8hex>"
//   2. Copy source tree into tmp dir; fsync files + dir.
//   3. If prior dest exists, copy to "install-agent-director.bak.<unix-ts>" (best-effort).
//   4. If prior dest exists, rmSync it.
//   5. rename(tmp, dest).
//   6. On rename failure: rmSync tmp and propagate.
// ---------------------------------------------------------------------------

type AtomicWriteResult = {
  backupPath: string | null;
  backupError: Error | null;
};

function atomicWriteSkillTree(
  source: string,
  dest: string,
  skillsParent: string,
): AtomicWriteResult {
  const pid = process.pid;
  const rand = randomBytes(8).toString("hex").slice(0, 8);
  const tmpDir = join(skillsParent, `${TMP_PREFIX}${pid}.${rand}`);

  // 1+2: stage tmp tree
  try {
    copyTree(source, tmpDir);
  } catch (err) {
    try {
      rmSync(tmpDir, { recursive: true, force: true });
    } catch {
      // ignore
    }
    throw err;
  }

  // 3: best-effort backup of prior dest, if any
  let backupPath: string | null = null;
  let backupError: Error | null = null;
  const priorExists = existsSync(dest);
  if (priorExists) {
    const ts = Math.floor(Date.now() / 1000);
    backupPath = join(skillsParent, `install-agent-director.bak.${ts}`);
    try {
      // If a same-second backup somehow already exists, fall back to a
      // collision-safe sibling so we never overwrite an older backup.
      let target = backupPath;
      let suffix = 0;
      while (existsSync(target)) {
        suffix += 1;
        target = `${backupPath}.${suffix}`;
      }
      backupPath = target;
      copyTree(dest, backupPath);
    } catch (err) {
      backupError = err instanceof Error ? err : new Error(String(err));
      // continue per SR-1.5: backup is best-effort; never abort the overwrite
    }
  }

  // 4: remove prior dest so rename target is clear
  if (priorExists) {
    try {
      rmSync(dest, { recursive: true, force: true });
    } catch (err) {
      try {
        rmSync(tmpDir, { recursive: true, force: true });
      } catch {
        // ignore
      }
      throw err;
    }
  }

  // 5: rename tmp into place
  try {
    renameSync(tmpDir, dest);
  } catch (err) {
    try {
      rmSync(tmpDir, { recursive: true, force: true });
    } catch {
      // ignore
    }
    throw err;
  }
  fsyncDir(dirname(dest));

  return { backupPath, backupError };
}

// ---------------------------------------------------------------------------
// OutputBudget (SR-1.6)
// Default-quiet: ≤ 5 lines combined across stdout + stderr. Verbose mode
// (AD_POSTINSTALL_VERBOSE=1|true|yes) lifts the cap. Every emitted line is
// prefixed `agent-director: `. The newer-branch warning carries
// neverSuppress=true so it always reaches stderr (still counts against the
// cap; defensive in case future code emits before reaching the warning).
// ---------------------------------------------------------------------------

const OUTPUT_LINE_PREFIX = "agent-director: ";
const DEFAULT_LINE_CAP = 5;

class OutputBudget {
  private emitted = 0;

  constructor(
    private readonly verbose: boolean,
    private readonly cap: number = DEFAULT_LINE_CAP,
  ) {}

  info(message: string): void {
    this.emit("stdout", message, false);
  }

  warn(message: string, options: { neverSuppress?: boolean } = {}): void {
    this.emit("stderr", message, options.neverSuppress === true);
  }

  error(message: string, options: { neverSuppress?: boolean } = {}): void {
    this.emit("stderr", message, options.neverSuppress === true);
  }

  private emit(
    stream: "stdout" | "stderr",
    message: string,
    neverSuppress: boolean,
  ): void {
    if (!this.verbose && !neverSuppress && this.emitted >= this.cap) {
      return; // budget exhausted; drop silently per SR-1.6
    }
    const line = `${OUTPUT_LINE_PREFIX}${message}\n`;
    if (stream === "stdout") {
      process.stdout.write(line);
    } else {
      process.stderr.write(line);
    }
    this.emitted += 1;
  }
}

// ---------------------------------------------------------------------------
// main() — three-way decision + atomic-write
// All output flows through OutputBudget. The identical branch never reaches
// the budget at all (zero output by construction).
// ---------------------------------------------------------------------------

const HOME_TILDE_DEST = "~/.claude/skills/install-agent-director/";

function main(): number {
  const verbose = isVerbose();
  const out = new OutputBudget(verbose);

  if (verbose) {
    out.info("postinstall start");
  }

  const here = fileURLToPath(import.meta.url);
  let packageRoot: string;
  try {
    packageRoot = resolvePackageRoot(here);
  } catch (err) {
    out.error(
      `postinstall: ${err instanceof Error ? err.message : String(err)}`,
    );
    return 1;
  }
  const sourceDir = resolveSourceSkillDir(packageRoot);
  const destDir = resolveDestSkillDir();
  const skillsParent = dirname(destDir);

  if (verbose) {
    out.info(`source=${sourceDir}`);
    out.info(`dest=${destDir}`);
  }

  // Source must exist — otherwise the published tarball is malformed.
  if (!existsSync(sourceDir)) {
    out.error(`postinstall: bundled skill body missing at ${sourceDir}`);
    return 1;
  }

  // Ensure ${HOME}/.claude/skills/ exists.
  mkdirSync(skillsParent, { recursive: true });

  // Defensive cleanup of any prior interrupted run's tmp tree.
  cleanupOrphanedTmpSiblings(skillsParent);

  // Three-way decision.
  const sourceSkillMd = join(sourceDir, "SKILL.md");
  const destSkillMd = join(destDir, "SKILL.md");
  const sourceVersion = readFrontmatterVersion(sourceSkillMd);
  const destExists = existsSync(destDir);

  if (verbose) {
    out.info(`source-version=${sourceVersion}`);
  }

  if (destExists) {
    // identical branch: tree-hash match → silent no-op (no emits)
    const sourceHash = treeHash(sourceDir);
    const destHash = treeHash(destDir);
    if (verbose) {
      out.info(`source-tree-hash=${sourceHash}`);
      out.info(`dest-tree-hash=${destHash}`);
    }
    if (sourceHash === destHash) {
      return 0;
    }
    // newer branch: dest version strictly greater than source per semver §11
    const destVersion = readFrontmatterVersion(destSkillMd);
    if (verbose) {
      out.info(`dest-version=${destVersion}`);
    }
    const cmp = compareSemver(destVersion, sourceVersion);
    if (cmp > 0) {
      out.warn(
        `skill at ${HOME_TILDE_DEST} is version ${destVersion} (newer than package's ${sourceVersion}); leaving operator copy in place`,
        { neverSuppress: true },
      );
      return 0;
    }
    // older-or-absent branch: fall through to atomic write
  }

  // older-or-absent branch — atomic write + best-effort backup
  try {
    const result = atomicWriteSkillTree(sourceDir, destDir, skillsParent);
    if (result.backupError !== null) {
      out.warn(
        `backup of prior skill failed: ${result.backupError.message}; proceeding with overwrite`,
        { neverSuppress: true },
      );
    }
    if (verbose) {
      if (result.backupPath !== null && result.backupError === null) {
        out.info(`backed up prior skill to ${result.backupPath}`);
      }
      out.info(`installed skill at ${destDir}`);
    }
    return 0;
  } catch (err) {
    out.error(
      `postinstall: atomic write failed: ${err instanceof Error ? err.message : String(err)}`,
    );
    return 1;
  }
}

// Run if invoked directly (not when imported by a sibling test).
// `import.meta.main` is a Bun-ism; fall back to argv comparison for portability.
const isMain =
  // @ts-expect-error import.meta.main is Bun-specific and may be undefined
  (typeof import.meta.main === "boolean" && import.meta.main) ||
  // Best-effort fallback: invoked as `bun run scripts/postinstall.ts`.
  process.argv[1] !== undefined &&
    resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  process.exit(main());
}
