/**
 * underSeedsLock — wraps a tree-reading build (setup.ts's make calls,
 * rc-stamp.test.ts's `make release-binaries`) in an exclusive flock on
 * pkg/api/apitest/.seeds-mutation.lock (the b.2y5 / b.3jn seeds-flock
 * protocol). The coverage.docker-epic-* gate children take the same lock shared
 * (`flock -s`, gates/coverage/docker-epics.sh), so these builds exclude them and
 * each other. Where flock is absent (darwin) the argv is returned unwrapped.
 */

import { resolve } from "path";

const seedsLockPath = resolve(import.meta.dir, "../../../../pkg/api/apitest/.seeds-mutation.lock");
const hasFlock = Bun.which("flock") !== null;

/** Returns argv prefixed with `flock <seeds lock>` where flock exists. */
export function underSeedsLock(argv: string[]): string[] {
  return hasFlock ? ["flock", seedsLockPath, ...argv] : argv;
}
