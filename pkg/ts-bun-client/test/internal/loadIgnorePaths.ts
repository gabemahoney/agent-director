/**
 * loadIgnorePaths — the envelope-diff ignore selectors per verb, read once from
 * the Go harness's test/envelope-diff/nondeterministic.json so the two
 * harnesses cannot drift.
 */

import { resolve } from "path";
import { readFileSync } from "fs";

const nondetPath = resolve(import.meta.dir, "../../../../test/envelope-diff/nondeterministic.json");
let cache: Record<string, string[]> | null = null;

/** The full verb → selectors map. */
export function loadAllIgnorePaths(): Record<string, string[]> {
  cache ??= JSON.parse(readFileSync(nondetPath, "utf-8")) as Record<string, string[]>;
  return cache;
}

/** The selectors for verb; [] when it has none. */
export function loadIgnorePathsForVerb(verb: string): string[] {
  return loadAllIgnorePaths()[verb] ?? [];
}
