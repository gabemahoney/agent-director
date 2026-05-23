/**
 * AgentDirectorError — base class for all typed errors surfaced by this
 * client library.
 *
 * Fields `errName` and `errDescription` mirror the C-ABI envelope fields
 * `err_name` / `err_description`. Full population from FFI envelopes is
 * handled in T4; for now these are placeholder fields set by the constructor.
 */
export class AgentDirectorError extends Error {
  /** Canonical error name, matching the Go errnames catalog (e.g. "ErrStoreUnopenable"). */
  readonly errName: string;
  /** Human-readable description forwarded from the C-ABI envelope. */
  readonly errDescription: string;

  constructor(errName: string, errDescription: string) {
    super(errDescription);
    this.errName = errName;
    this.errDescription = errDescription;
    this.name = errName;
    // Restore the prototype chain (required when extending built-ins in ES5 targets).
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrClientClosed — thrown when a verb method (or _assertOpen) is called on a
 * Client that has already been closed.
 *
 * TS-ONLY ERROR — this error has no counterpart in the shared Go errnames
 * catalog. It must be listed in the T10 allow-list for the catalog-drift test
 * so CI does not flag it as an unexpected error class.
 *
 * Allow-list entry: "ErrClientClosed" (TS-only, not in pkg/api/errnames/catalog.json)
 */
export class ErrClientClosed extends AgentDirectorError {
  constructor() {
    super(
      "ErrClientClosed",
      "client is closed: call new Client() to obtain a fresh handle"
    );
    this.name = "ErrClientClosed";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * errorFromEnvelope creates a typed AgentDirectorError (or subclass) from a
 * C-ABI error envelope { err_name, err_description }.
 *
 * T4 will replace this stub with a full catalog-aware factory. Until then it
 * produces a plain AgentDirectorError carrying the raw envelope fields.
 */
export function errorFromEnvelope(env: {
  err_name: string;
  err_description: string;
}): AgentDirectorError {
  return new AgentDirectorError(env.err_name, env.err_description);
}
