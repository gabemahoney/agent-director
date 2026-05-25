// TS-only allow-list — imported so the drift test can reference the same
// constant. See src/internal/tsOnlyErrors.ts for the full rationale.
export { TS_ONLY_ERROR_NAMES } from "./internal/tsOnlyErrors.js";

/**
 * AgentDirectorError — base class for all typed errors surfaced by this
 * client library.
 *
 * `verb` names the callable verb that triggered the error (e.g. "spawn").
 * `errName` is the canonical error name from the Go errnames catalog.
 * `errDescription` is the human-readable description from the subprocess error envelope.
 * `message` is formatted as "${errName}: ${errDescription}".
 */
export class AgentDirectorError extends Error {
  /** Verb that triggered this error (e.g. "spawn", "status"). Empty for lifecycle ops. */
  readonly verb: string;
  /** Canonical error name, matching the Go errnames catalog (e.g. "ErrSpawnNotFound"). */
  readonly errName: string;
  /** Human-readable description forwarded from the subprocess error envelope. */
  readonly errDescription: string;

  constructor(verb: string, err_name: string, err_description: string) {
    super(`${err_name}: ${err_description}`);
    this.verb = verb;
    this.errName = err_name;
    this.errDescription = err_description;
    this.name = this.constructor.name;
    // Restore the prototype chain (required when extending built-ins in ES5 targets).
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

// ---------------------------------------------------------------------------
// TS-only error classes
//
// The following classes have no counterpart in pkg/api/errnames/catalog.json.
// Their names are centralised in src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES
// (exported from this file for convenience). The catalog-drift test imports
// that constant and filters these names out before comparing against the Go
// catalog, so CI never flags them as unexpected.
//
//   "ErrClientClosed"           — client lifecycle (src/internal/tsOnlyErrors.ts)
//   "ErrUnsupportedPlatform"    — platform detection (src/internal/tsOnlyErrors.ts)
//   "ErrPlatformPackageMissing" — missing optional sub-package (src/internal/tsOnlyErrors.ts)
//   "ErrBunVersionTooOld"       — runtime version guard (src/internal/tsOnlyErrors.ts)
//   "ErrCliNotExecutable"       — binary exists but lacks execute permission (SR-2.3)
//   "ErrConsumerSignal"         — subprocess killed by OS signal (SR-5.2/SR-5.4)
//   "ErrCallTimeout"            — per-call timeout elapsed (SR-6.2/SR-6.5)
//   "ErrUnknownErrorName"       — err_name not in catalog; TS catalog out of sync (SR-4.3)
// ---------------------------------------------------------------------------

/**
 * ErrClientClosed — thrown when a verb method (or _assertOpen) is called on a
 * Client that has already been closed.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES` so the
 * catalog-drift test never flags it as an unexpected class.
 */
export class ErrClientClosed extends AgentDirectorError {
  constructor() {
    super(
      "",
      "ErrClientClosed",
      "client is closed: call new Client() to obtain a fresh handle"
    );
    this.name = "ErrClientClosed";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrUnsupportedPlatform — thrown when `process.platform` + `process.arch`
 * produce a tuple that has no corresponding native sub-package.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 */
export class ErrUnsupportedPlatform extends AgentDirectorError {
  constructor(tuple: string) {
    super(
      "",
      "ErrUnsupportedPlatform",
      `platform/arch tuple "${tuple}" is not supported; supported: linux-x64, darwin-arm64`
    );
    this.name = "ErrUnsupportedPlatform";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrPlatformPackageMissing — thrown when the optional npm sub-package for
 * the current platform is not installed, or when its native binary file is
 * absent from the installed package directory.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 */
export class ErrPlatformPackageMissing extends AgentDirectorError {
  constructor(pkgName: string, detail?: string) {
    const desc = detail
      ? `native sub-package "${pkgName}" is not usable: ${detail}`
      : `native sub-package "${pkgName}" is not installed or its binary is absent`;
    super("", "ErrPlatformPackageMissing", desc);
    this.name = "ErrPlatformPackageMissing";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrBunVersionTooOld — thrown when Bun.version is below the declared minimum.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 */
export class ErrBunVersionTooOld extends AgentDirectorError {
  constructor(actual: string, minimum: string) {
    super(
      "",
      "ErrBunVersionTooOld",
      `Bun ${actual} is below the minimum required version ${minimum}; upgrade Bun to continue`
    );
    this.name = "ErrBunVersionTooOld";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrCliNotExecutable — thrown when the resolved CLI binary exists on disk but
 * does not have execute permission for the current process's effective uid/gid.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 * Implements SRD SR-2.3.
 */
export class ErrCliNotExecutable extends AgentDirectorError {
  constructor(path: string) {
    super(
      "",
      "ErrCliNotExecutable",
      `binary "${path}" exists but lacks execute permission; check file mode (chmod +x)`
    );
    this.name = "ErrCliNotExecutable";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrConsumerSignal — thrown when an in-flight subprocess call is killed by an
 * OS signal (e.g. SIGTERM, SIGINT delivered to the consumer process group).
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 * Implements SRD SR-5.2/SR-5.4.
 */
export class ErrConsumerSignal extends AgentDirectorError {
  /** The signal name that killed the subprocess (e.g. "SIGTERM", "SIGINT"). */
  readonly signal: string;

  constructor(verb: string, signal: string) {
    super(
      verb,
      "ErrConsumerSignal",
      `subprocess was killed by signal ${signal}`
    );
    this.signal = signal;
    this.name = "ErrConsumerSignal";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrCallTimeout — thrown when the per-call timeout (ClientOptions.callTimeoutMs,
 * default 30 s) elapses before the subprocess exits. The subprocess is sent SIGTERM
 * then SIGKILL before the error is surfaced.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 * Implements SRD SR-6.2/SR-6.5.
 */
export class ErrCallTimeout extends AgentDirectorError {
  /** Approximate elapsed time in milliseconds when the timeout fired. */
  readonly elapsedMs: number;
  /** The configured timeout threshold in milliseconds. */
  readonly timeoutMs: number;

  constructor(verb: string, elapsedMs: number, timeoutMs: number) {
    super(
      verb,
      "ErrCallTimeout",
      `call timed out after ${elapsedMs}ms (configured limit: ${timeoutMs}ms)`
    );
    this.elapsedMs = elapsedMs;
    this.timeoutMs = timeoutMs;
    this.name = "ErrCallTimeout";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * ErrUnknownErrorName — thrown when a subprocess JSON envelope contains an
 * `err_name` field that is not present in the static catalog-derived error map.
 * This indicates the Go-side catalog has a new entry that the TS catalog has
 * not yet been regenerated to include.
 *
 * Constructor: `new ErrUnknownErrorName(unknownName, envelope)`
 *   - `unknownName` — the unrecognised err_name string from the envelope.
 *   - `envelope`    — the full envelope payload for diagnostic use.
 *
 * TS-ONLY ERROR — no counterpart in pkg/api/errnames/catalog.json.
 * Listed in `src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
 * Implements SRD SR-4.3.
 */
export class ErrUnknownErrorName extends AgentDirectorError {
  /** The unrecognised err_name string from the envelope. Also in `.message`. */
  readonly unknownName: string;
  /** The full envelope payload for diagnostic use. */
  readonly envelope: unknown;

  constructor(unknownName: string, envelope: unknown) {
    super(
      "",
      "ErrUnknownErrorName",
      `unknown err_name "${unknownName}" in subprocess envelope; TS catalog may be out of sync with Go catalog`
    );
    this.unknownName = unknownName;
    this.envelope = envelope;
    this.name = "ErrUnknownErrorName";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

// ---------------------------------------------------------------------------
// Catalog-derived error subclasses
//
// One subclass per entry in pkg/api/errnames/catalog.json (33 entries).
// Bodies are empty: subclass identity is the sole value-add over the base class.
// The factory (errorFromEnvelope) at the bottom of this file maps err_name
// strings to these constructors.
// ---------------------------------------------------------------------------

/** Mirrors ErrCwdMissing (package: spawn) */
export class ErrCwdMissing extends AgentDirectorError {}
/** Mirrors ErrCwdNotAPath (package: spawn) */
export class ErrCwdNotAPath extends AgentDirectorError {}
/** Mirrors ErrCwdNotFound (package: spawn) */
export class ErrCwdNotFound extends AgentDirectorError {}
/** Mirrors ErrCwdNotADirectory (package: spawn) */
export class ErrCwdNotADirectory extends AgentDirectorError {}
/** Mirrors ErrRelayModeInvalid (package: spawn) */
export class ErrRelayModeInvalid extends AgentDirectorError {}
/** Mirrors ErrSpawnDeniedFlag (package: spawn) */
export class ErrSpawnDeniedFlag extends AgentDirectorError {}
/** Mirrors ErrReservedEnvKey (package: spawn) */
export class ErrReservedEnvKey extends AgentDirectorError {}
/** Mirrors ErrInstanceIdCollision (package: spawn) */
export class ErrInstanceIdCollision extends AgentDirectorError {}
/** Mirrors ErrTmuxSessionNameEmpty (package: spawn) */
export class ErrTmuxSessionNameEmpty extends AgentDirectorError {}
/** Mirrors ErrTmuxSessionNameInvalid (package: spawn) */
export class ErrTmuxSessionNameInvalid extends AgentDirectorError {}
/** Mirrors ErrTmuxSessionNameTooLong (package: spawn) */
export class ErrTmuxSessionNameTooLong extends AgentDirectorError {}
/** Mirrors ErrSpawnNotFound (package: store) */
export class ErrSpawnNotFound extends AgentDirectorError {}
/** Mirrors ErrTmuxNotAvailable (package: tmux) */
export class ErrTmuxNotAvailable extends AgentDirectorError {}
/** Mirrors ErrTmuxSessionCreate (package: tmux) */
export class ErrTmuxSessionCreate extends AgentDirectorError {}
/** Mirrors ErrTmuxSendKeys (package: tmux) */
export class ErrTmuxSendKeys extends AgentDirectorError {}
/** Mirrors ErrTmuxCaptureFailed (package: tmux) */
export class ErrTmuxCaptureFailed extends AgentDirectorError {}
/** Mirrors ErrSpawnNotInteractive (package: api) */
export class ErrSpawnNotInteractive extends AgentDirectorError {}
/** Mirrors ErrSendKeysWhileRelayed (package: api) */
export class ErrSendKeysWhileRelayed extends AgentDirectorError {}
/** Mirrors ErrSpawnNotPausable (package: api) */
export class ErrSpawnNotPausable extends AgentDirectorError {}
/** Mirrors ErrPauseTimeout (package: api) */
export class ErrPauseTimeout extends AgentDirectorError {}
/** Mirrors ErrListInvalidLabel (package: api) */
export class ErrListInvalidLabel extends AgentDirectorError {}
/** Mirrors ErrTemplateNameUnsafe (package: config) */
export class ErrTemplateNameUnsafe extends AgentDirectorError {}
/** Mirrors ErrTemplateNotFound (package: config) */
export class ErrTemplateNotFound extends AgentDirectorError {}
/** Mirrors ErrTemplateMalformed (package: config) */
export class ErrTemplateMalformed extends AgentDirectorError {}
/** Mirrors ErrTemplateExists (package: config) */
export class ErrTemplateExists extends AgentDirectorError {}
/** Mirrors ErrProbeUnsupported (package: probe) */
export class ErrProbeUnsupported extends AgentDirectorError {}
/** Mirrors ErrSpawnNotResumable (package: api) */
export class ErrSpawnNotResumable extends AgentDirectorError {}
/** Mirrors ErrNoSessionId (package: api) */
export class ErrNoSessionId extends AgentDirectorError {}
/** Mirrors ErrJsonlMissing (package: api) */
export class ErrJsonlMissing extends AgentDirectorError {}
/** Mirrors ErrRelayModeOff (package: api) */
export class ErrRelayModeOff extends AgentDirectorError {}
/** Mirrors ErrInvalidDecision (package: api) */
export class ErrInvalidDecision extends AgentDirectorError {}
/** Mirrors ErrNoOpenPermissionRequest (package: store) */
export class ErrNoOpenPermissionRequest extends AgentDirectorError {}
/** Mirrors ErrAlreadyDecided (package: store) */
export class ErrAlreadyDecided extends AgentDirectorError {}

// ---------------------------------------------------------------------------
// errorFromEnvelope — catalog-aware factory
// ---------------------------------------------------------------------------

type ErrConstructor = new (
  verb: string,
  err_name: string,
  err_description: string
) => AgentDirectorError;

/**
 * Lookup table from err_name strings (from the agent-director error envelope)
 * to their typed constructor. Derived from pkg/api/errnames/catalog.json — 33
 * entries.
 *
 * This is the most-grepped table in the project; keep it readable and in
 * alphabetical order within each package group.
 */
const ERROR_TABLE = {
  // spawn package
  ErrCwdMissing,
  ErrCwdNotAPath,
  ErrCwdNotFound,
  ErrCwdNotADirectory,
  ErrRelayModeInvalid,
  ErrSpawnDeniedFlag,
  ErrReservedEnvKey,
  ErrInstanceIdCollision,
  ErrTmuxSessionNameEmpty,
  ErrTmuxSessionNameInvalid,
  ErrTmuxSessionNameTooLong,
  // store package
  ErrSpawnNotFound,
  ErrNoOpenPermissionRequest,
  ErrAlreadyDecided,
  // tmux package
  ErrTmuxNotAvailable,
  ErrTmuxSessionCreate,
  ErrTmuxSendKeys,
  ErrTmuxCaptureFailed,
  // api package
  ErrSpawnNotInteractive,
  ErrSendKeysWhileRelayed,
  ErrSpawnNotPausable,
  ErrPauseTimeout,
  ErrListInvalidLabel,
  ErrSpawnNotResumable,
  ErrNoSessionId,
  ErrJsonlMissing,
  ErrRelayModeOff,
  ErrInvalidDecision,
  // config package
  ErrTemplateNameUnsafe,
  ErrTemplateNotFound,
  ErrTemplateMalformed,
  ErrTemplateExists,
  // probe package
  ErrProbeUnsupported,
} as const satisfies Readonly<Record<string, ErrConstructor>>;

/**
 * errorFromEnvelope creates a typed AgentDirectorError subclass from the
 * subprocess error envelope fields.
 *
 * @param verb            The verb name that produced the error (e.g. "spawn").
 * @param err_name        The canonical error name from the envelope.
 * @param err_description The human-readable description from the envelope.
 * @returns A typed subclass when err_name matches the catalog; a plain
 *          AgentDirectorError (with a console.warn) for unknown names.
 */
export function errorFromEnvelope(
  verb: string,
  err_name: string,
  err_description: string
): AgentDirectorError {
  const Ctor = (ERROR_TABLE as Readonly<Record<string, ErrConstructor>>)[err_name];
  if (Ctor) {
    return new Ctor(verb, err_name, err_description);
  }
  console.warn(
    `agent-director: unknown err_name "${err_name}" (verb=${verb}); returning base AgentDirectorError`
  );
  return new AgentDirectorError(verb, err_name, err_description);
}
