/**
 * tsOnlyErrors.ts — centralized allow-list for TS-only AgentDirectorError
 * subclasses that have no counterpart in pkg/api/errnames/catalog.json.
 *
 * The catalog-drift regression test (T10) imports this constant so it never
 * flags these classes as unexpected. Each name must also have a corresponding
 * subclass in src/errors.ts with a comment cross-referencing this module.
 */

/**
 * TS_ONLY_ERROR_NAMES lists every AgentDirectorError subclass that is defined
 * in this package but intentionally absent from the shared Go err_name catalog
 * at pkg/api/errnames/catalog.json.
 *
 * Membership criteria:
 *  - ErrClientClosed              — client lifecycle error, no Go equivalent.
 *  - ErrBunVersionTooOld          — runtime version guard; no Go equivalent.
 *  - ErrConsumerSignal            — subprocess killed by OS signal (SR-5.4).
 *  - ErrCallTimeout               — per-call timeout elapsed (SR-6.5).
 *  - ErrUnknownErrorName          — unrecognised err_name in envelope (SR-4.3).
 *  - ErrSystemInstallNotFound     — discovery found no AD binary (b.ue3 / SR-3.1).
 *  - ErrSystemInstallTooOld       — discovered binary is below floor (b.ue3 / SR-3.2).
 *  - ErrSystemInstallUnreachable  — discovered binary failed probe (b.ue3 / SR-3.3).
 *  - ErrCallerCwdUnreachable      — caller's process.cwd() is gone at construction (b.cot).
 *  - ErrSystemInstallDisappeared  — binary gone at verb-dispatch time, after valid construction (b.xht).
 *
 * Removed in b.ue3 (vendored-binary surface dropped):
 *  - ErrUnsupportedPlatform
 *  - ErrPlatformPackageMissing
 *  - ErrCliNotExecutable
 *
 * When adding a new TS-only subclass: add its name here AND add a comment in
 * src/errors.ts near the subclass cross-referencing this allow-list.
 */
export const TS_ONLY_ERROR_NAMES = [
  "ErrClientClosed",
  "ErrBunVersionTooOld",
  "ErrConsumerSignal",
  "ErrCallTimeout",
  "ErrUnknownErrorName",
  "ErrSystemInstallNotFound",
  "ErrSystemInstallTooOld",
  "ErrSystemInstallUnreachable",
  "ErrCallerCwdUnreachable",
  /** b.xht — binary gone at verb-dispatch time, after valid construction. */
  "ErrSystemInstallDisappeared",
] as const;

/** Type alias for the individual allow-listed names. */
export type TsOnlyErrorName = (typeof TS_ONLY_ERROR_NAMES)[number];
