# b.w21: unfixed unit-regression handoff

The phase-1 model gate is met: all nine scheduler results match their
predictions, including the counterexamples and passing controls. See the
[model checkpoint and receipts](../../spec/tla/codex/b.w21/README.md).
The Go regression execution status is **NOT_RUN**. This checkpoint contains
regressions against unfixed runtime code; it does not contain the product fix.

## Run on a separate disposable test host

The orchestrator will supply `dist/b.w21/unit-red.bundle`, its SHA-256 hash,
and the exact checkpoint commit. Transfer the bundle to a fresh directory on
a separate disposable test host with Git, Bash, GNU Make and a working Docker
or Podman engine. Sandbox image/dependency setup needs network access.
Do not run these commands on the development host.

The bundle includes branch `fix/b.w21`, its history, and tag `v0.11.1`.
Keep the tag: the full suite uses historical release/migration fixtures.
Clone from the bundle, rather than copying a source tree or using a shallow
checkout. The clone's runtime must match baseline `8c6998d4`; only tests,
these handoff docs, phase-1 evidence, and its `spec/tla/codex/README.md`
index may differ. The commands verify this
and record the full commit and tag IDs before execution. Compare the bundle
hash and checkpoint ID with the orchestrator's supplied values before running.

From the directory containing `unit-red.bundle`, follow
[unit-red-commands.txt](unit-red-commands.txt). The execution target is:

```sh
make -C b.w21-unit-red test-sandbox
```

The command file passes Go verbosity through the
sandbox's supported `SANDBOX_FLAGS`, so passing controls appear in the log.
It captures combined output and saves the actual Make exit status without a
pipeline, then exits with that status. A nonzero result is expected only if
the regressions execute and expose the bug.

Scheduler job containers provide no nested container engine, Docker socket
or privileged option. They cannot run this target unchanged. There is no
unit scheduler submission command in this handoff. Use the disposable host
and the literal sandbox target; do not fake an engine, set a guard bypass,
or substitute host `go test`, `bun test` or repository binaries.

## Evidence required to establish red

Return the complete `unit-red.log`, `unit-red.exitcode`,
`unit-red-checkpoint.txt`, and `unit-red-bundle.sha256`. Do not return only
the final exit code or excerpts. All rows below are static expectations;
their observed result remains **NOT_RUN** until logs are reviewed.

| Regression | Expected failure evidence on unfixed code |
|---|---|
| `TestLegacyRecoveryMigratedV6Store` | The authorized v6 migration and resumed launch succeed, then `b.w21 plain send must recover automatically` reports `ErrSendKeysWhileRelayed=true`, before SessionStart. |
| `TestLegacyRecoveryAlreadyStuckStore` | Both `pending` and `waiting` subcases fail plain send with `ErrSendKeysWhileRelayed=true` for decided legacy rows carrying `agent_gone`. |
| `TestLegacyRecoveryExpiredVerdicts` | `find_missing_deny` and `ended_deny`, at `beyond_0s` and `beyond_1s`, fail plain send with `ErrRelayFallenBack=true`, at and beyond `created_at + 86400s + 2s`. |
| `TestLegacyRecoveryCloseResumeAndSessionStart` | `b.w21 terminal close left decided legacy request unclosed`; the resumed pending plain send also exposes the relay hold. |
| `TestLegacyDecidedIntentClosesWithSpawn` | `b.w21 decided legacy intent remains unclosed` for the terminal/resume closers; verdict and unfinished pane intent must be preserved. |
| `TestCloseClosesRelayRequests` | Token `dddddddd-dddd-4ddd-addd-dddddddddddd` remains unclosed although `closed true` is expected; close-event assertions may also fail. |

Other assertions check gone-agent token refusals, preservation of verdicts
and timestamps, a stale request beside a current one, and current fallback
details. Inspect their failures too; they are not all passing controls on
unfixed code. A failure caused by an unexpected error or changed verdict
needs investigation before accepting the checkpoint.

Require explicit `--- PASS` lines for both
`TestLegacyRecoveryExpiredVerdicts/ordinary_allow_control` subcases and the
following `TestLegacyRecoveryKeepsCurrentRequestHeld` subcases:
`legacy_unproven`, `pending_legacy_unproven`, `legacy_turn_end_only`,
`legacy_undecided_agent_gone`, `modern_live_turn_end`, `modern_live_tool_ran`,
and `modern_unknown_before_settle`. These assert that valid relay holds
still refuse keys. Their parent test can fail because
`stale_legacy_plus_current` also tests recovery.

A nonzero exit alone does **not** prove red. Require a fresh disposable host
with no prior cached results for this checkpoint. Reject setup, dependency,
compile, missing-fixture/tag, sandbox-guard, timeout or unrelated failures
as substitutes for the named assertions. The sandbox prepares the TypeScript
client, then runs both Go and Bun suites; obtain the complete output even
when Go fails. Review unrelated failures separately and resolve them before
treating this as a clean regression checkpoint.

## Gates after the user returns logs

Prove the migration, already-stuck recovery, post-cutoff and closure
regressions red on this immutable checkpoint before changing runtime code.
Then implement recovery and future closure, validate the fixed model through
user-submitted scheduler jobs, and prove the unit suite green using
`make test-sandbox` on the disposable host.

The future integration handoff must cover all **15 executions**: one
`make test-docker EPIC=<slug>` for each of the 14 active entries in
[test/docker-epics.txt](../docker-epics.txt), plus the separate
`make test-docker-install-mode` target. The install-mode run is additional
to `epic-12-install`. Those jobs have not been prepared or executed here;
the user submits the scheduler jobs after the fix is ready.

Automatic recovery of existing stuck stores and the precise answer about
minutes of `ErrSendKeysWhileRelayed` before SessionStart remain pending
implementation and green verification. Update the customer contract and
architecture documentation when the final behavior is established.
