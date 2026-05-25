/**
 * client.ts — public Client export.
 *
 * The subprocess implementation lives in internal/subprocessClient.ts; this
 * file aliases it as the public `Client` and re-exports the two error symbols
 * commonly imported alongside it so callers don't need separate imports.
 */

// Re-export SubprocessClient as the public Client.
export { SubprocessClient as Client } from "./internal/subprocessClient.js";

// Re-export the two error symbols that callers commonly import alongside
// Client (previously declared in this file; now sourced from errors.ts).
export { AgentDirectorError, ErrClientClosed } from "./errors.js";
export type { ClientOptions } from "./types.js";
