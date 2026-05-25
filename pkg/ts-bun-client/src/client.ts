/**
 * client.ts — public Client export.
 *
 * Epic B cutover (b.19d t1.19d.9i): chosen shape (b) — thin re-export.
 *
 * Rationale: keeps the diff minimal and preserves a clean undo path. The
 * subprocess implementation lives in internal/subprocessClient.ts; this file
 * simply aliases it as the public `Client` and re-exports the two error
 * symbols that client.ts previously declared locally so callers don't need
 * separate imports.
 *
 * FFI source files (ffi.ts, internal/worker.ts, internal/workerProxy.ts,
 * internal/bootstrapFfi.ts, internal/bindingSpec.ts, internal/freeGuard.ts)
 * remain on disk but are no longer imported from this module or from
 * index.ts. Final deletion is Epic E.
 */

// Re-export SubprocessClient as the public Client.
export { SubprocessClient as Client } from "./internal/subprocessClient.js";

// Re-export the two error symbols that callers commonly import alongside
// Client (previously declared in this file; now sourced from errors.ts).
export { AgentDirectorError, ErrClientClosed } from "./errors.js";
export type { ClientOptions } from "./types.js";
