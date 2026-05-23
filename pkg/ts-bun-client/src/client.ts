import { expandTilde } from "./internal/tilde.js";
import { callOpen, callClose } from "./internal/bootstrapFfi.js";
import { ErrClientClosed, errorFromEnvelope } from "./errors.js";
import type { ClientOptions } from "./types.js";

// Re-export so callers don't need separate imports.
export { AgentDirectorError, ErrClientClosed } from "./errors.js";
export type { ClientOptions } from "./types.js";

/** Shape of a successful ad_open envelope. */
interface OpenSuccessEnvelope {
  handle: string;
}

/** Shape of an error envelope returned by ad_open or ad_close. */
interface ErrorEnvelope {
  err_name: string;
  err_description: string;
}

type OpenEnvelope = OpenSuccessEnvelope | ErrorEnvelope;
type CloseEnvelope = Record<string, unknown> | ErrorEnvelope;

function isErrorEnvelope(env: unknown): env is ErrorEnvelope {
  return (
    typeof env === "object" &&
    env !== null &&
    "err_name" in env &&
    typeof (env as ErrorEnvelope).err_name === "string"
  );
}

/**
 * Client — the public entry point for agent-director.
 *
 * Construction is synchronous: the constructor calls `ad_open` over Bun FFI,
 * receives an opaque handle, and stores it. Every subsequent verb call will
 * carry this handle across the FFI boundary.
 *
 * Lifecycle:
 *   1. `new Client(opts)` → calls ad_open; throws on error envelope.
 *   2. `client.close()` → calls ad_close; idempotent, never throws.
 *   3. `[Symbol.dispose]()` → delegates to close() for `using` blocks.
 *   4. Any verb method calls `_assertOpen()` first; post-close calls throw
 *      ErrClientClosed.
 *
 * Tilde expansion (`~` → home directory) is applied to `storePath` and
 * `configPath` TS-side before the paths cross the FFI boundary. The C-ABI
 * never receives a leading `~`.
 */
export class Client {
  /** Opaque handle returned by ad_open. Nulled after close(). */
  private _handle: string | null;
  /** Whether this client is still open and usable. */
  private _open: boolean;
  /** Optional logger for non-fatal warnings (e.g. ad_close error envelopes). */
  private _logger: ClientOptions["logger"];

  constructor(opts: ClientOptions) {
    this._handle = null;
    this._open = false;
    this._logger = opts.logger;

    // Tilde-expand path fields TS-side so the C-ABI receives absolute paths.
    const storePath = expandTilde(opts.storePath);
    const configPath =
      opts.configPath !== undefined ? expandTilde(opts.configPath) : undefined;

    // Build the ad_open params object (snake_case JSON keys per the Go wire shape).
    const params: {
      store_path: string;
      config_path?: string;
      tmux_command?: string;
      create_if_missing?: boolean;
    } = { store_path: storePath };
    if (configPath !== undefined) params.config_path = configPath;
    if (opts.tmuxCommand !== undefined) params.tmux_command = opts.tmuxCommand;
    if (opts.createIfMissing !== undefined) params.create_if_missing = opts.createIfMissing;

    // Call ad_open synchronously via the bootstrap FFI shim.
    // T3 will replace this with worker-proxy dispatch; the constructor keeps
    // its synchronous semantics regardless (brief, handle-acquisition call).
    const envelopeJSON = callOpen(params);
    const env = JSON.parse(envelopeJSON) as OpenEnvelope;

    if (isErrorEnvelope(env)) {
      throw errorFromEnvelope(env);
    }

    this._handle = (env as OpenSuccessEnvelope).handle;
    this._open = true;
  }

  /**
   * _assertOpen throws ErrClientClosed if the client has been closed.
   * Called at the top of every verb method. May also be invoked from tests
   * via `(client as any)._assertOpen()` — TS private fields are erased at
   * runtime, so the cast works.
   */
  private _assertOpen(): void {
    if (!this._open) {
      throw new ErrClientClosed();
    }
  }

  /**
   * close releases the ad_open handle via ad_close.
   *
   * - Idempotent: a second call is a no-op.
   * - Never throws: if ad_close returns an error envelope, the error is
   *   logged via the optional logger (warn level) but the handle is still
   *   nulled and _open set to false.
   */
  close(): void {
    if (!this._open) return;

    const handle = this._handle!;

    try {
      const envelopeJSON = callClose({ handle });
      const env = JSON.parse(envelopeJSON) as CloseEnvelope;
      if (isErrorEnvelope(env)) {
        this._logger?.warn?.(
          `Client.close(): ad_close returned error envelope`,
          { err_name: env.err_name, err_description: env.err_description }
        );
      }
    } catch (e) {
      this._logger?.warn?.("Client.close(): unexpected error calling ad_close", e);
    } finally {
      this._handle = null;
      this._open = false;
    }
  }

  /**
   * [Symbol.dispose] enables `using` block syntax (Explicit Resource Management).
   *
   *   {
   *     using client = new Client(opts);
   *     // ... client.someVerb() ...
   *   } // client.close() called automatically here
   */
  [Symbol.dispose](): void {
    this.close();
  }

  // -------------------------------------------------------------------------
  // Verb stubs — implemented in T3.
  // -------------------------------------------------------------------------
  // (Future verb methods will call this._assertOpen() first, then dispatch
  // via the worker proxy from T3.)

  /**
   * _assertOpenForTests exposes _assertOpen to test code without forcing
   * public visibility. TypeScript `private` is erased at runtime, but this
   * alias makes the intent explicit and avoids the `(client as any)` cast.
   *
   * @internal Tests only — do not call from application code.
   */
  _assertOpenForTests(): void {
    this._assertOpen();
  }
}
