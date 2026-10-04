/**
 * underSeedsLock — wraps a tree-reading build command in the seeds flock.
 *
 * Seeds flock (b.3jn / b.2y5 seeds-flock protocol): readers and builders of
 * walk-reachable tree sources hold the flock on
 * pkg/api/apitest/.seeds-mutation.lock while reading, and tests that mutate
 * those sources hold it exclusively. The wrap was added because
 * helper-tag-replay, one of coverage.go-root's synthetic-regression tests,
 * rewrote pkg/api/apitest/seeds.go in place under that exclusive lock and a
 * build here compiled it mid-mutation ("seeds.go:222: assignment mismatch:
 * 1 variable but SeedSpawn returns 2 values"). Since b.jct no test writes
 * seeds.go, and since b.9qj no Go test takes the lock: the source-of-truth
 * tests, its last Go holders, stage their fixtures in temp git repos instead.
 * Its remaining holders are these builds (test/setup.ts's make calls and
 * rc-stamp.test.ts's `make release-binaries`) and the coverage.docker-epic-*
 * gate children, which take it shared (`flock -s`, see
 * skills/release-agent-director/gates/coverage/docker-epics.sh).
 * /usr/bin/flock (util-linux, present in the sandbox image) creates the lock
 * file if missing and blocks until free; without -s it locks exclusively, so
 * these builds exclude each other and the docker-epic children.
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
