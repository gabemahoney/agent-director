/**
 * platform — minimal native library path resolver.
 *
 * T5 will refactor this into a full platform-aware resolver that knows about
 * optional npm packages, environment overrides, and all supported platforms.
 * Until then, this file provides the bare minimum needed by worker.ts.
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { suffix } from "bun:ffi";

/**
 * loadNative resolves the path to the native shared library for the current
 * platform.
 *
 * Returns `{ path }` if the library exists, or `{ path: "", missing }` if it
 * cannot be found. Callers should treat a non-empty `missing` as a fatal
 * startup error.
 *
 * Library location:
 *   {repo-root}/dist/libagent_director.{so|dylib}
 *
 * From `src/` (import.meta.dir), repo root is three levels up:
 *   import.meta.dir → pkg/ts-bun-client/src
 *   ../             → pkg/ts-bun-client/
 *   ../../          → pkg/
 *   ../../../       → {repo-root}/
 */
export function loadNative(): { path: string; missing?: string } {
  const libPath = resolve(
    import.meta.dir,
    "../../../dist/libagent_director." + suffix
  );

  if (!existsSync(libPath)) {
    return {
      path: "",
      missing: `${process.platform}-${process.arch} (looked at ${libPath})`,
    };
  }

  return { path: libPath };
}
