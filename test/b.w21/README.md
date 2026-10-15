# b.w21: validation evidence and merge handoff

The user approved local `make sandbox CMD=...` and `make test-sandbox` on
gmahoney-dev, with `/usr/local/go/bin` in `PATH`. A separate host is not
required. Execution stays inside the real sandbox; host Go/Bun tests and
repository binaries are prohibited. TLC and Docker integration groups are
scheduler-only, submitted by the user.

## Observed evidence

[unit-evidence.json](unit-evidence.json) preserves concise receipts, exact
commands, log hashes and the source identity of each unit stage. Complete
local logs and original summaries remain under `dist/b.w21/`.

| Stage | Observed result |
|---|---|
| Unfixed targeted red at `eafd10f5851e5ca39db83feda3f1bc6f8aa0ecc3` | 31 failing, 10 passing leaf cases; no skips. Runtime matched `8c6998d4`. |
| One-line `SetCapture` fixture correction, rerun before runtime edits | All three gone-agent token cases still failed; the correct pane hash exposed token refusals. |
| Repaired targeted regressions | 41 passing leaf cases, zero failures/skips. |
| First full `make test-sandbox` | Go: 64 passing packages (5 cached), 3 failing packages, 12 without tests. Bun: 420 pass, zero fail, 57 files. |
| Lifecycle expectation correction | All 3 cases passed in a focused rerun. |
| Advice expectation correction | All 9 cases passed in a focused rerun. |
| Historical aggregate full suite before round 2 | **PASS**, exit 0. Go: 67 passing packages (37 cached), zero failures, 12 without tests. Bun: 420 pass, zero fail, 57 files. |
| Historical round-2 full `make test-sandbox` | **PASS**, exit 0. Go: 67 passing packages (39 cached), zero failures, 12 without tests. Bun: 420 pass, zero fail, 57 files, 1,626 expect calls. |
| Post-merge full `make test-sandbox` | **PASS**, exit 0. Go: 67 passing packages (39 cached), zero failures, 12 without tests. Bun: 420 pass, zero fail, 57 files, 1,626 expect calls. |

Red established the actual v6 migration and all eight already-stuck
pending/waiting cases refusing with `ErrSendKeysWhileRelayed`. Four close-deny
cases at and beyond `created_at + 86400s + 2s` refused with
`ErrRelayFallenBack`. Eight current-request safety controls and two ordinary
expired-allow controls passed. The remaining red failures covered stale-token
selection, early token refusals and lifecycle closure persistence.

The first full-suite failures were two outdated behavioral expectations and
the TLA artifact checks. The two behavioral expectations were corrected and
rerun green. The artifact contract now permits only manifest-declared b.w21
contexts with canonical jar bytes/hashes and canonical runners; provenance
pins cover the modified source and new artifacts. The final full suite
passed after these corrections; the first failed full-suite receipt and
the final pass are both retained in `unit-evidence.json`.
The green runs used uncommitted changes on the red checkpoint: their source
hashes, rather than the unchanged HEAD alone, identify the repair tested.
The tested runtime is now committed at
`9d025f8f2caf176f3a60421f52ba94b293021bdb`; its hashes match the
[final full-suite receipt](../../dist/b.w21/unit-green-final/summary.json).
The integration context remains pinned to this source revision even if
handoff documentation receives a later commit.

## Current round: mixed-store model correction

The returned fourteen-job fixed-model evidence has **13 prediction matches**:
ten PASS, three expected failures and the unexpected initial `NoRace` failure
(3,835 distinct states, depth 11). The [model diagnosis](../../spec/tla/codex/b.w21/fixed-r2/README.md)
identifies a model ACK shortcut that production does not use. All 269 production
Go files remain identical to `9d025f8f`, aggregate SHA-256
`2550ab71219eb05813efd2741a99696926af97f05398a077e8d6777e936a9f79`.

The new `TestLegacyRecoveryMixedModernDelivery` covers 32 public API cases:
pending/check_permission × ACKed live, unACKed unknown before confirmation,
ACKed unknown, ACKed gone × plain, valid hash, stale hash, obsolete legacy token.
A current live or unconfirmed hook remains held before any pane access;
ACKed unknown/gone controls retain the dialog, hash and obsolete-token refusals.
[Targeted sandbox evidence](../../dist/b.w21/model-r2-unit/summary.json) records
118 passing API/store cases and eight artifact cases, zero failures/skips;
all 32 new cases pass. Two prior attempts corrected fixture/assertion mistakes
and remain archived; they were not runtime failures. `TestCodexJobArtifacts`
now also requires the round-2 manifest and its declared contexts.

The historical round-2 [full-suite receipt](../../dist/b.w21/model-r2-unit-full/summary.json)
records `env PATH="/usr/local/go/bin:$PATH" make test-sandbox`, exit 0,
from 23:25:35 to 23:29:12 UTC on 2026-10-14. Its SHA-256 is
`f08b2931a4dcf238dac59d190bc7aa911a2fc6a165374af888a8d475528d4050`.
All 269 production Go files matched `9d025f8f` before and after; all 713
hashed test files were unchanged during the run. Nonverbose Go output gives
package results, so individual test and skip counts are unavailable. Bun
emitted no skip lines; this is not an independent count of all skipped cases.
The receipt predates this documentation-only receipt and pin refresh; current
pins identify current file bytes, without attributing them to that run.

The fresh [post-merge receipt](../../dist/b.w21/merge-main-unit/summary.json)
records the same exact command, exit 0, in 216.812 seconds on 2026-10-15
(01:13:54–01:17:31 UTC), with merge parents `b9f73c45` and `728180eb`.
Its SHA-256 is
`864ba033b6dbc6d951bcfa2c03dded22e43d3b5b5f036fe8b5f330d765619b65`.
All 269 production Go files still match `9d025f8f`; all 714 test-file hashes
were unchanged during the run. Main’s imported README tests match its bytes,
and the b.w21 regressions remain intact. Individual Go test/skip counts are
unavailable from nonverbose output; Bun emitted no skip lines. This receipt
precedes the final documentation-only receipt and provenance refresh; its
source hashes identify the tested bytes.

## Recovery contract and limit

A decided pre-v7 request with durable `agent_gone` proof is logically closed
at read time, releasing relay and fallen-back holds automatically in an
already-upgraded store. It preserves decision, reason, timestamps and
historical delivery facts. Lifecycle closes also persist `closed_at` on
decided unacked requests with `pane_answer` `none` or `intent`, preserving
prior proofs; this closure survives resume even with a prior `turn_end`.

Pending-before-SessionStart holds can still last minutes for undecided
legacy requests, legacy requests without `agent_gone` (including already-stuck
earlier-launch `turn_end`-only rows with no `closed_at`), and modern
live/unknown hooks. The default legacy window is 24 h + 2 s. A decided
legacy `agent_gone` row contributes no hold; another current request can.
See the [caller contract](../../docs/caller-contract.md#permission-relay).

## Commands and scheduler gates

[unit-red-commands.txt](unit-red-commands.txt) records the approved local
method and historical red command. Do not reset this active worktree to
reproduce red. The existing red logs already establish the prerequisite;
a historical reproduction belongs in a distinct disposable checkout with
the release tag `v0.11.1` retained.

The nine [phase-1 model receipts](../../spec/tla/codex/b.w21/README.md)
remain frozen. The [submitted fixed results](../../spec/tla/codex/b.w21/fixed/README.md)
preserve the unexpected failure; original predictions, cfgs and contexts
remain intact. The [round-2 evidence](../../spec/tla/codex/b.w21/fixed-r2/README.md)
records all six cases matching: four PASS, one intended reachability failure
and one intended `NoRace` mutant failure. Seven attempts are archived with
full logs and allowlisted scheduler metadata. The initial full-Spec attempt
was incomplete at 2,000,377 distinct states; a separate resource-only rerun
completed at 6,289,976 states, depth 43, with all 22 checks including temporal
properties. Original contexts and commands remain frozen. No new model job
is needed; the resource overlay and identity limits are documented there.

The [integration evidence](integration/README.md) records **15 PASS** at the
unchanged source: 152 EPIC cases and 22 install-mode assertions/five scenarios.
Every raw run log attests source commit `9d025f8f` and context payload SHA-256
`ee259f45e8ae667ae8f083b03128ebeb2d46526903aa9d72fa2b1165822a49fe`.
Actual `--network off` is the User's statement; historical prepared network-on
commands and immutable context `dist/b.w21/integration/context-9d025f8f`
remain untouched. No integration repeat is required for this model-only round.
The separate [observational archive](integration/integration-observed-receipts.json)
contains all fifteen full run logs and user receipt. It does not include build
logs or independently verify scheduler final state/network policy.

## Review status and remaining work

The prior runtime/code/test and handoff reviews found no actionable issues;
those reviews and the old full-unit PASS predate the current model/test round.
All nine red model outcomes, unit-red proof, fifteen integration passes
and the historical round-2 full unit PASS are established. All six corrected
round-2 model outcomes now match predictions. Main `728180eb` was merged
without conflicts, retaining its minimum-version and idle-hook documentation
and tests alongside the legacy recovery contract. The fresh post-merge
`make test-sandbox` result is **PASS**, as recorded above. Final review and
commit are owned by the calling agent. No push has been performed.

Broad pre-existing documentation style work is deferred: internal/PRD
references, the old unsupported-configuration note, duplicate recovery
procedures and API examples against the generic documentation guide.
An exhaustive style audit of the roughly 21,000-line architecture document
was not completed. These limits do not reopen the resolved repair-contract
findings.
