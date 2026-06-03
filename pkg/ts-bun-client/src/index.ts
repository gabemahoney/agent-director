/**
 * Public entry point for agent-director.
 */
export { Client } from "./client.js";

// Version-floor constants (SR-4.5 / SR-5).  MIN_BINARY_VERSION is the
// strict-SemVer-2.0 floor every system AD install must meet; the value
// is sourced at build time from version-floor.json (the bash-readable
// single source of truth).  DEV_SENTINEL_VERSION is the dev-build
// universally-satisfies marker — consumers compare a probed AD's
// version against this literal to short-circuit the floor check for
// dev-stamped binaries.
export {
  MIN_BINARY_VERSION,
  DEV_SENTINEL_VERSION,
} from "./internal/constants.js";

// Error classes and factory.
export {
  AgentDirectorError,
  ErrClientClosed,
  ErrUnsupportedPlatform,
  ErrPlatformPackageMissing,
  ErrBunVersionTooOld,
  // Subprocess-pipeline TS-only errors (additive; SRD Epic A SR-2.3/SR-4.3/SR-5.4/SR-6.5).
  ErrCliNotExecutable,
  ErrConsumerSignal,
  ErrCallTimeout,
  ErrUnknownErrorName,
  errorFromEnvelope,
  // Catalog-derived subclasses (37 entries from pkg/api/errnames/catalog.json).
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
  ErrSpawnNotFound,
  ErrTmuxNotAvailable,
  ErrTmuxSessionCreate,
  ErrTmuxSendKeys,
  ErrTmuxCaptureFailed,
  ErrSpawnNotInteractive,
  ErrSendKeysWhileRelayed,
  ErrSpawnNotPausable,
  ErrPauseTimeout,
  ErrListInvalidLabel,
  ErrTemplateNameUnsafe,
  ErrTemplateNotFound,
  ErrTemplateMalformed,
  ErrTemplateExists,
  ErrProbeUnsupported,
  ErrSpawnNotResumable,
  ErrNoSessionId,
  ErrJsonlMissing,
  ErrRelayModeOff,
  ErrInvalidDecision,
  ErrNoOpenPermissionRequest,
  ErrAlreadyDecided,
  ErrPermissionRequestNotFound,
  ErrAmbiguousRequest,
  ErrMissingRequestToken,
  ErrInvalidFlags,
} from "./errors.js";

// Shared types and options.
export type { ClientOptions, Logger } from "./types.js";

// Shared sub-shapes.
export type { VerbSummary, PermissionRequestInfo, ListRow } from "./types.js";

// Verb Params / Result interfaces.
export type {
  SpawnParams, SpawnResult,
  StatusParams, StatusResult,
  GetParams, GetResult,
  SendKeysParams, SendKeysResult,
  ReadPaneParams, ReadPaneResult,
  KillParams, KillResult,
  DecideParams, DecideResult,
  GetPermissionParams, GetPermissionResult,
  ResumeParams, ResumeResult,
  FindMissingParams, FindMissingResult,
  ExpireParams, ExpireResult,
  DeleteParams, DeleteResult,
  MakeTemplateParams, MakeTemplateResult,
  ListParams, ListResult,
  PauseParams, PauseResult,
  VersionParams, VersionResult,
} from "./types.js";
