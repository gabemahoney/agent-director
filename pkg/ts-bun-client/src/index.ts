/**
 * Public entry point for @CHANGEME-H3/agent-director.
 *
 * ffi.ts and platform.ts are internal — they are NOT re-exported here.
 */
export { Client } from "./client.js";
export { AgentDirectorError, ErrClientClosed, errorFromEnvelope } from "./errors.js";
export type { ClientOptions, Logger } from "./types.js";
