/**
 * Public entry point for agent-director.
 */
export { Client } from "./client.js";

// Standalone discovery surface (SR-4.3).  Same SR-1 → SR-2.3 pipeline as
// Client.create(); resolves with { path, version } or rejects with the same
// four typed errors.
export { resolveSystemBinary } from "./client.js";
export type {
  ResolveSystemBinaryResult,
  ResolveSystemBinaryOptions,
} from "./client.js";

// Version-floor constants (SR-4.5 / SR-5).
export {
  MIN_BINARY_VERSION,
  DEV_SENTINEL_VERSION,
} from "./internal/constants.js";

// Error classes and factory.
export {
  AgentDirectorError,
  ErrClientClosed,
  ErrBunVersionTooOld,
  // System-install discovery errors (b.ue3 / SR-3 / SR-4.4).
  ErrSystemInstallNotFound,
  ErrSystemInstallTooOld,
  ErrSystemInstallUnreachable,
  ErrCallerCwdUnreachable, // b.cot — fail-fast when caller's cwd is unreachable
  // Subprocess-pipeline TS-only errors (SRD Epic A SR-2.3/SR-4.3/SR-5.4/SR-6.5).
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

// Companion types for the new discovery errors (SR-4.4).
export type { CheckedLocation, UnreachableReason } from "./errors.js";

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
