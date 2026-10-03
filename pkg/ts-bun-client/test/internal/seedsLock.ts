/**
 * underSeedsLock — wraps a tree-reading build command in the seeds flock.
 *
 * Seeds flock (b.3jn / b.2y5 seeds-flock protocol): readers and builders of
 * walk-reachable tree sources hold the flock on
 * pkg/api/apitest/.seeds-mutation.lock while reading, and coverage.go-root's
 * tree-mutating synthetic-regression tests hold it exclusively
 * (acquireSeedsLock, LOCK_EX). The wrap was added because helper-tag-replay
 * rewrote pkg/api/apitest/seeds.go in place and a build here compiled it
 * mid-mutation ("seeds.go:222: assignment mismatch: 1 variable but SeedSpawn
 * returns 2 values"). Since b.jct no test writes seeds.go; the remaining
 * exclusive holders (the source-of-truth tests) create and remove non-Go
 * fixture directories, which these named-package builds do not read.
 * /usr/bin/flock (util-linux, present in the sandbox image) creates the lock
 * file if missing and blocks until free, with the same flock(2) semantics as
 * acquireSeedsLock, so these builds and go-root's mutators mutually exclude.
 *
 * flock does NOT exist on darwin, where the suite also runs via skip patterns;
 * where flock is absent the argv is returned unwrapped.
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
