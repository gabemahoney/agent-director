# b.w21: local unit evidence and remaining validation

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
| Final aggregate full suite | **PASS**, exit 0. Go: 67 passing packages (37 cached), zero failures, 12 without tests. Bun: 420 pass, zero fail, 57 files. |

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
remain frozen. The [fixed-model handoff](../../spec/tla/codex/b.w21/fixed/README.md)
has fourteen prepared jobs, all **NOT_RUN**. Return their complete logs,
job IDs, verdict lines and counterexamples for review.
Its [exact commands](../../spec/tla/codex/b.w21/fixed/scheduler-commands.txt)
predict eleven PASS results, one reachability-probe failure and two
repair-mutant failures; no fixed-model pass is established yet.

Docker integration needs **15 executions**: one `make test-docker EPIC=<slug>`
for each of the 14 active entries in [docker-epics.txt](../docker-epics.txt),
plus `make test-docker-install-mode`, additional to `epic-12-install`.
The [integration handoff](integration/README.md) is **READY for user submission**.
Run its [15 exact scheduler commands](integration/scheduler-commands.txt)
from the worktree root. All integration observations remain **NOT_RUN**,
with PASS predicted for every job: 152 EPIC cases plus 22 install-mode
assertions covering five scenarios. The shared context at
`dist/b.w21/integration/context-9d025f8f` contains 10 regular files,
4,806,313 bytes, with payload SHA-256
`ee259f45e8ae667ae8f083b03128ebeb2d46526903aa9d72fa2b1165822a49fe`.
Keep every context file immutable until all jobs finish. Image build and
runtime capability remain unverified; the scheduler needs Linux amd64
and the canonical network dependencies. The user submits all model and
integration jobs and returns the full receipts specified in each handoff.

## Review status and remaining work

Runtime code and unit tests passed review. The integration handoff's static
code and test reviews found no issues; static checks establish preparation,
not an image build or execution result. Relevant documentation contract
findings are resolved and focused review confirmed them. The nine observed
phase-1 outcomes, local red proof and final unit PASS are established; the
fourteen fixed-model and fifteen integration observations remain **NOT_RUN**.
The bug stays active pending review of those scheduler receipts.

Broad pre-existing documentation style work is deferred: internal/PRD
references, the old unsupported-configuration note, duplicate recovery
procedures and API examples against the generic documentation guide.
An exhaustive style audit of the roughly 21,000-line architecture document
was not completed. These limits do not reopen the resolved repair-contract
findings.
