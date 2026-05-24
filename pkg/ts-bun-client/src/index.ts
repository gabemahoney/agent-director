/**
 * Public entry point for agent-director.
 *
 * ffi.ts and platform.ts are internal — they are NOT re-exported here.
 */
export { Client } from "./client.js";

// Error classes and factory.
export {
  AgentDirectorError,
  ErrClientClosed,
  ErrUnsupportedPlatform,
  ErrPlatformPackageMissing,
  ErrBunVersionTooOld,
  errorFromEnvelope,
  // Catalog-derived subclasses (34 entries from pkg/api/errnames/catalog.json).
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
  ErrUnknownHandle,
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
  ResumeParams, ResumeResult,
  FindMissingParams, FindMissingResult,
  ExpireParams, ExpireResult,
  DeleteParams, DeleteResult,
  MakeTemplateParams, MakeTemplateResult,
  ListParams, ListResult,
  PauseParams, PauseResult,
  VersionParams, VersionResult,
} from "./types.js";
