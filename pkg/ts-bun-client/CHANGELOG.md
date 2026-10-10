# Changelog

All notable changes to `agent-director` will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `ErrDialogMaybeOpen` (b.146 step 2c, b.8t7): a `sendKeys` without `request_token` and without `expect_pane_sha256` rejects with it, sending nothing, while a permission request of the spawn is not proven gone by Claude Code's own hooks (its tool's PostToolUse, the main agent's end of turn after it, or the agent's end). `errDetails` is the new `DialogMaybeOpenDetails`: the oldest such request with its facts, the spawn's `state` and its other `unproven_requests`. Retry later; if it stays held, have a person or an LLM read the pane and send with its `pane_sha256`. The catalog now has 55 entries.
- `proven_gone_at`, `proven_gone_how` (new type `ProvenGoneHow`: `"tool_ran"`, `"turn_end"`, `"agent_gone"`) and `unproven_since` on every `RequestDelivery` (`decide`, `getPermission`, `get` / `list` `permission_requests`, `errDetails`), and `unproven_requests` on `GetResult`: every request of the row not proven gone, including those agent-director's records read closed. A request that reads `"delivered"` or closed at the pane but stays unproven for a while is a reason to read the pane.
- Pane answers (b.146 step 2b, b.3oc): `sendKeys` gains `request_token`, `as`, `key`, `no_enter`, `expect_pane_sha256` and `n_lines`. With `request_token`, `as`, `key` and `expect_pane_sha256` it answers a fallen-back permission request at the pane with exactly one key and no Enter, recorded as `pane_answer` `"sent"`, `decision` = `as`, `decision_reason` `"pane"`. Plain calls keep Enter by default; `no_enter` drops it and `key` sends one key alone. `text` is now optional.
- `readPane`'s result gains `pane_sha256`, the SHA-256 of exactly the bytes of `pane`, which `sendKeys` and `recordPaneAnswer` take back as `expect_pane_sha256`. Never pass it from an automatic flow.
- `recordPaneAnswer` (b.3oc): records a fallen-back request answered outside agent-director (types nothing), with `RecordPaneAnswerParams` / `RecordPaneAnswerResult`; `as` is `"allow"`, `"deny"` or `"unknown"` (pass `"unknown"` unless the answer was seen). The client now has 16 callable verbs.
- `errDetails` on `AgentDirectorError` (b.3oc): the error envelope's optional `err_details` object, `null` when absent. `errorFromEnvelope` takes it as an optional fourth argument. New exported types `RelayFallenBackDetails`, `OpenRequestFacts`, `PaneAnswerInProgressDetails`, `ClaimTooSoonDetails`, `PaneChangedDetails`, `PaneKeySentDetails` and `PaneAnswer`. `PaneKeySentDetails` (`key_sent` `true`, `request_token`) is the `errDetails` of the `ErrInternal` a pane answer throws when its key was sent but not recorded: read the pane before you send anything again.
- `ErrPaneChanged`, `ErrPaneAnswerInProgress` and `ErrClaimTooSoon` (b.3oc). The catalog now has 54 entries.
- `pane_answer` and `pane_as` on every `RequestDelivery` (`decide`, `getPermission`, `get` / `list` `permission_requests`), and the `decision_reason` values `"ended"`, `"pane"`, `"pane_outside"` and `"tool_ran"` on `GetPermissionResult`. `"pane"`, `"pane_outside"` and `"tool_ran"` can come with `decision` `"allow"`. For a reason you do not know, read the recorded verdict from `decision` (`null` for a `"pane_outside"` `"unknown"` claim) and the outcome from `delivery` and `pane_answer`; never read a reason as a verdict.
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

- `sendKeys` without `request_token` (b.8t7): after a permission request, it is held (`ErrDialogMaybeOpen`) until Claude Code proves the request's dialog gone: after an allow, until its tool has run; after a deny, until the agent's turn ends. A request acked (`"delivered"`), answered at the pane or recorded with `recordPaneAnswer` no longer frees it by itself, and a retry after `ErrSendKeysWhileRelayed` can get `ErrDialogMaybeOpen`. A matching `expect_pane_sha256` passes the hold: it means a person or an LLM judged that exact screen, so never pass it from an automatic flow. A spawn with no unproven permission request is never held (one that never asked for a permission included). **Upgrade:** the upgrade proves gone (`"agent_gone"`) every request of a spawn already `ended` or `missing`; every other request recorded before this release is unproven, so a live relayed spawn that has one is held until its main agent's next Stop or idle prompt; an agent already idle at its prompt gets neither until a new turn, so its first plain `sendKeys` needs a person or an LLM to read the pane and send with its hash.
- **Breaking for TypeScript callers (b.q2i):** `GetResult` declared an optional `permission_request` object that the CLI never sent; it now declares `permission_requests: PermissionRequestInfo[]`, the array the CLI sends (`[]` unless the row is in `check_permission`). Code that read `permission_request` must read `permission_requests` instead.
- `ErrRelayFallenBack` (b.q2i): for a request recorded from this release on, `decide` rejects with it within seconds of the request's relay hook ending without an acked verdict, not after the relay window, and keeps the refused verdict as `attempted_decision`.
- `ErrRelayFallenBack` (b.3oc): `sendKeys` without `request_token` now rejects with it, sending nothing, while any permission request of the spawn has fallen back with no pane answer recorded, in every live state of the spawn; `errDetails` names the request and the spawn's other open requests. **Upgrade:** a request recorded before this release that fell back by time and was never decided rejects plain `sendKeys` to its spawn until it is closed with `recordPaneAnswer` with `as: "unknown"` (or a pane answer), or the spawn ends or is marked `missing`. `decide` does not close it, and once the spawn has moved on from it rejects with `ErrNoOpenPermissionRequest` while plain `sendKeys` still rejects with `ErrRelayFallenBack`.
- `ErrAlreadyDecided` (b.3oc): `decide` on a request already answered at the pane (`pane_answer` `"sent"`, `"outside"` or `"tool_ran"`) rejects with it, naming its `pane_answer`, as a pane answer and `recordPaneAnswer` do, and records nothing, not even `attempted_decision`.
- `ErrSendKeysWhileRelayed` (b.3oc): thrown, in any live state of the spawn, exactly while a relay hook of the spawn may still answer its request, with no time window of its own; the "request is not yet recorded" form is gone.
- `ErrNoOpenPermissionRequest` (b.3oc): `decide` also rejects with it for a request closed when its agent ended (an undecided one denied with `decision_reason` `"ended"`), even after the spawn is resumed; such a request is no longer listed and no longer holds the resumed spawn.
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
