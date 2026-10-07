/**
 * underTreeWriteLock — wraps a command that writes inside the repo tree in an exclusive flock on the repo-root
 * .tree-write.lock (unwrapped where flock is missing). Why: gates/README.md "Coverage phase (parallel)".
 */

import { resolve } from "path";

// test/internal/ → test/ → pkg/ts-bun-client/ → pkg/ → (repo root)
const treeWriteLockPath = resolve(import.meta.dir, "../../../../.tree-write.lock");
const hasFlock = Bun.which("flock") !== null;

/** Returns argv prefixed with `flock <tree-write lock>` where flock exists. */
export function underTreeWriteLock(argv: string[]): string[] {
  return hasFlock ? ["flock", treeWriteLockPath, ...argv] : argv;
}
