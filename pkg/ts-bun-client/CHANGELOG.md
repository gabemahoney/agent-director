# Changelog

All notable changes to `agent-director` will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
