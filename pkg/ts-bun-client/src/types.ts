/**
 * Structural logger interface — mirrors the four conventional log levels.
 * All methods are optional; callers may pass any object that implements the
 * subset they care about (e.g., `console`).
 */
export interface Logger {
  debug?: (message: string, ...args: unknown[]) => void;
  info?: (message: string, ...args: unknown[]) => void;
  warn?: (message: string, ...args: unknown[]) => void;
  error?: (message: string, ...args: unknown[]) => void;
}

/**
 * Options accepted by the `Client` constructor (SRD SR-1.2).
 *
 * `storePath` is the only required field. The others are optional overrides;
 * absent fields fall back to the same three-tier defaults as `pkg/api.Options`
 * (config-file value, then hardcoded fallback).
 *
 * Tilde expansion is handled TS-side by `src/internal/tilde.ts` before paths
 * cross the FFI boundary, so the C-ABI never receives a leading `~`.
 */
export interface ClientOptions {
  /** Path to the SQLite store file. Tilde-expanded before crossing FFI. */
  storePath: string;
  /** Path to the TOML config file. Tilde-expanded before crossing FFI. */
  configPath?: string;
  /** Override the tmux binary. Defaults to the binary on PATH. */
  tmuxCommand?: string;
  /**
   * When `true`, create the store and initialize the database schema if the
   * store file does not yet exist. When `false` (the default), opening a
   * non-existent store returns an error. Mirrors `pkg/api.Options.CreateIfMissing`.
   */
  createIfMissing?: boolean;
  /** Optional logger for client-side warnings (e.g., non-fatal close errors). */
  logger?: Logger;
}
