/**
 * underSeedsLock — wraps a tree-reading build command in the seeds flock.
 *
 * Seeds flock (b.3jn / b.2y5 seeds-flock protocol): the coverage gates run
 * concurrently in one container, and coverage.go-root's synthetic-regression
 * test helper-tag-replay MUTATES pkg/api/apitest/seeds.go under an exclusive
 * flock on pkg/api/apitest/.seeds-mutation.lock (b.2y5's acquireSeedsLock,
 * LOCK_EX). Without the same lock, a build here can compile mid-mutation and
 * fail (observed: "seeds.go:222: assignment mismatch: 1 variable but SeedSpawn
 * returns 2 values"). Invariant: any cross-package reader/builder of
 * walk-reachable tree sources must hold the seeds flock while reading.
 * /usr/bin/flock (util-linux, present in the sandbox image) creates the lock
 * file if missing and blocks until free, with the same flock(2) semantics as
 * acquireSeedsLock, so these builds and go-root's mutators mutually exclude.
 *
 * flock does NOT exist on darwin, where the suite also runs via skip patterns;
 * the mutator we lock against only exists in the Linux sandbox, so where flock
 * is absent the argv is returned unwrapped.
 */

import { resolve } from "path";

// test/internal/seedsLock.ts → test/internal/ → test/ → pkg/ts-bun-client/ → pkg/ → repo root
const seedsLockPath = resolve(
  import.meta.dir,
  "../../../../pkg/api/apitest/.seeds-mutation.lock",
);
const hasFlock = Bun.which("flock") !== null;

/** Returns argv prefixed with `flock <seeds lock>` where flock exists. */
export function underSeedsLock(argv: string[]): string[] {
  return hasFlock ? ["flock", seedsLockPath, ...argv] : argv;
}
