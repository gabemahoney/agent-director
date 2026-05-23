/**
 * loadIgnorePaths.ts — reads Epic 3's nondeterministic.json manifest.
 *
 * The TS envelope-diff harness reuses the same nondeterministic.json as the
 * Go harness (test/envelope-diff/nondeterministic.json) so the two harnesses
 * can never drift independently.
 *
 * The file is read exactly once at module load and cached as a singleton.
 */

import { resolve } from "path";
import { readFileSync } from "fs";

// test/internal/ → test/ → pkg/ts-bun-client/ → pkg/ → repo root
const repoRoot = resolve(import.meta.dir, "../../../..");
const nondetPath = resolve(repoRoot, "test/envelope-diff/nondeterministic.json");

// Singleton cache — loaded once, never re-read.
let _cache: Record<string, string[]> | null = null;

function loadNondeterministic(): Record<string, string[]> {
  if (_cache !== null) return _cache;
  let raw: string;
  try {
    raw = readFileSync(nondetPath, "utf-8");
  } catch {
    throw new Error(
      `loadIgnorePathsForVerb: Epic 3's nondeterministic.json not found at ` +
        `${nondetPath}. Ensure Epic 3 (test/envelope-diff/) is landed before ` +
        `running envelope-diff tests.`
    );
  }
  _cache = JSON.parse(raw) as Record<string, string[]>;
  return _cache;
}

/**
 * loadIgnorePathsForVerb returns the nondeterministic field selectors for the
 * given verb from Epic 3's shared manifest.
 *
 * Returns [] when the verb has no entry (no fields to ignore).
 */
export function loadIgnorePathsForVerb(verb: string): string[] {
  const map = loadNondeterministic();
  return map[verb] ?? [];
}

/**
 * loadAllIgnorePaths returns the full verb→selectors map.
 * Used by the meta-test to check for verb coverage drift.
 */
export function loadAllIgnorePaths(): Record<string, string[]> {
  return loadNondeterministic();
}
