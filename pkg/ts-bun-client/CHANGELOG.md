# Changelog

All notable changes to `agent-director` will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Relay delivery facts (b.146 step 2, b.q2i): `decide` now resolves with the request's `delivery` (`"delivered"` or `"not_confirmed"`) and the rest of its delivery facts (`confirm_by`, `hook_alive`, `hook_gone_at`, `attempted_decision`, `attempted_at`, `tool_use_id`) instead of an empty object. `getPermission`, `get`'s `permission_requests` and each `list` row's new `permission_requests` carry the same facts (`delivery` may also be `"fallen_back"`), plus `decision` and `decision_reason` on each `get` / `list` request. New exported types `Delivery` and `RequestDelivery`. A recorded `decision` is not the outcome: read `delivery`.
- `max_wait_ms` on `DecideParams` (b.q2i): an optional bound on the whole `decide` call, its reads and its write, with no default; pass at most your deadline minus 1 s.
- `ErrStoreBusy` (b.q2i): `decide`'s `max_wait_ms` ran out, during its reads or its write, before its verdict was recorded; nothing was recorded, so retry. The catalog now has 51 entries.
- `ErrSystemInstallDisappeared` + runtime cwd detection (b.xht): `SubprocessClient#doCall` now catches `ENOENT` from `posix_spawn` at verb-dispatch time and diagnoses the true cause before rethrowing. Binary missing → new `ErrSystemInstallDisappeared` (carrying `binaryPath` and `verb`). Cwd missing → `ErrCallerCwdUnreachable` (same class introduced by b.cot, now also thrown at runtime when the working directory disappears after construction). All other ENOENT failures fall through to the existing `ErrSubprocessCrash` path unchanged.
- `ErrCallerCwdUnreachable` (b.cot): `Client.create()` and `resolveSystemBinary()` now stat `process.cwd()` after the binary probe. If the working directory is missing or is not a directory they throw `ErrCallerCwdUnreachable` (carrying the offending path and the underlying cause) instead of letting the first verb call fail with a misleading `ENOENT` on the binary. Restart your service from a valid directory to resolve.
- `allow_pending` parameter on `sendKeys` (b.dnn): opt-in to send text into a
  `pending` Spawn before `SessionStart` fires. Primary use case is dismissing
  pre-SessionStart interactive prompts such as the
  `--dangerously-load-development-channels` safety warning. `ended`/`missing`
  Spawns are still rejected regardless of the flag.
- `allow_pending` parameter on `readPane` (b.dnn): accepted for surface
  symmetry with `sendKeys`; `readPane` has no state guard and the flag has no
  behavioral effect.
- `readPane` verb documented in README verb examples.

- Initial TypeScript/Bun client implementation (`pkg/ts-bun-client/`).
- Bun FFI boundary over `pkg/cabi` C-ABI (`src/ffi.ts`, `src/internal/bindingSpec.ts`).
- Public `Client` class with full verb surface: `spawn`, `status`, `list`,
  `stop`, `send`, `read`, `resume`, `hooks` (`src/client.ts`).
- Typed error hierarchy mirroring the Go catalog (`src/errors.ts`).
- Platform resolver with optional-dependency sub-packages for linux-x64
  and darwin-arm64 (`src/platform.ts`).
- Off-main-thread worker for FFI calls (`src/worker.ts`).
- `prepublishOnly` guard (`scripts/check-not-placeholder.ts`) that aborts
  publish if the package name still contains the `CHANGEME-H3` placeholder
  (kept as a forward-going tripwire against future placeholder pollution).
- Full bun:test suite (163 tests) covering FFI binding, envelope-diff
  invariants, error catalog drift, platform resolution, and smoke tests.

### Changed

- **Breaking for TypeScript callers (b.q2i):** `GetResult` declared an optional `permission_request` object that the CLI never sent; it now declares `permission_requests: PermissionRequestInfo[]`, the array the CLI sends (`[]` unless the row is in `check_permission`). Code that read `permission_request` must read `permission_requests` instead.
- `ErrRelayFallenBack` (b.q2i): for a request recorded from this release on, `decide` rejects with it within seconds of the request's relay hook ending without an acked verdict, not after the relay window, and keeps the refused verdict as `attempted_decision`.
- `ErrNoOpenPermissionRequest` (b.q2i): `decide` also rejects with it, recording nothing, for a request `find-missing` closed when it marked the spawn `missing` before the request's relay hook confirmed the verdict recorded on it, even after the spawn is resumed (do not answer it at the pane). A closed request is no longer listed in `get`'s or `list`'s `permission_requests`; `getPermission` still reads it, and its `delivery` is `"delivered"` only if its relay hook confirmed.
- **H3 resolved (2026-05-24).** The placeholder scope `@CHANGEME-H3/` has been
  replaced with the resolved names: umbrella package `agent-director`
  (unscoped); per-platform sub-packages `@agent-director/linux-x64` and
  `@agent-director/darwin-arm64` (esbuild-style layout). The publish-guard
  sentinel and `release.sh` H3 regex remain in place as tripwires against
  re-introduction.
- **darwin/amd64 dropped from v1 (2026-05-24).** Supported platforms shrink
  to `{linux/amd64, darwin/arm64}`. Operator runs both legs on self-hosted
  runners (Reno + M1 Mac); no Intel Mac users to serve and macOS-13
  GH-hosted minutes carry a 10x billing multiplier.

[Unreleased]: https://github.com/gabemahoney/agent-director/compare/HEAD...HEAD
