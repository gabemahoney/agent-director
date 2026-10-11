/**
 * requireBuiltDist — fails a test that reads dist/ with a clear message when
 * dist/ is not built, not an ENOENT or a short tarball list (b.2b3).
 */

import { existsSync } from "node:fs";
import { resolve } from "node:path";

const DIST_DIR = resolve(import.meta.dir, "../../dist");

export function requireBuiltDist(): void {
  const missing = ["index.js", "index.d.ts", "version-floor.json"].filter((f) => !existsSync(resolve(DIST_DIR, f)));
  if (missing.length > 0) {
    throw new Error(
      `dist/ is not built (missing ${missing.map((f) => `dist/${f}`).join(", ")}). ` +
        "make test-sandbox builds it before the suites (b.2b3). To run this file alone, run bun install " +
        "--frozen-lockfile and bun run build in pkg/ts-bun-client first, inside the sandbox (make sandbox CMD=...)."
    );
  }
}
