/**
 * Build-time constants sourced from the version-floor.json single source of
 * truth (SR-5.3).  Bun inlines the JSON import as a string literal in
 * dist/index.js; consumers never re-read the file at runtime (SR-5.0).
 *
 * The companion `dist/version-floor.json` (copied by build.ts) is the
 * bash-readable surface (SR-5.5).  Both sites trace to the same source
 * byte sequence and are kept in lockstep by the version-coherence script
 * (SR-5.4).
 */

import floor from "../../version-floor.json" with { type: "json" };

export const MIN_BINARY_VERSION: string = floor.min_binary_version;

export const DEV_SENTINEL_VERSION = "0.0.0-dev" as const;
