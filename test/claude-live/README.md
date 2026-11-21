# Claude live qualification

This is the single developer runbook for the signal-change check, qualification
shakedown and final-candidate qualification. It adds no live authorization,
daemon or product delivery proof. The [signal dependencies and order witnesses](../../docs/architecture.md#claude-signal-qualification)
explain what a changed signal breaks; [qualification components](../../docs/architecture.md#claude-qualification-reusable-analysis-and-accounting)
describe the shared judgments. Versioned measurements and release decisions
belong to Task b3 and the MI.

Follow the sections in order: offline setup replay, build, transfer/verify, choose the installation
branch below, prepare evidence and authority, invoke the phases, then
collect/read the summary. Full qualification starts with `setup-launches`;
signal-only and shakedown work use the standalone candidate install.
Signal-only work uses [Run once and collect](#run-once-and-collect) and the
[signal verdict](#analyze-and-read-the-verdict). Both workflows use the same
[stop and cleanup rules](#stop-and-cleanup-rules).

## Offline phase-setup replay

Before building a kit for a live attempt, run `TestQualificationSetupReplay`
and `TestQualificationWorkloadReplay`
in `runner/qualification_replay_test.go` on the intended source in scheduler
containers, both normally and under `-race`. Run the independent real-clock
K1/K2, recorded a2 paste/long-tool and a3 refusal/progress regressions alongside
it; setup prefixes do not replace their judgments. The scheduler command is:

```sh
go test -count=1 -timeout=15m -json -run '^Test' ./test/claude-live/...
go test -race -count=1 -timeout=15m -json -run '^Test' ./test/claude-live/...
```

The six primary prefixes enter real Execute/Dispatch/preparation, checking kit,
installed product, ledger, imports, recorder registration, startup argv and
canonical cleanup/accounting. External commands and provider callbacks are
scripted; harmless child processes supply real PID/start/exit checks.

| Prefix | Bounded setup coverage |
|---|---|
| Typed | Recorded a2/a3/a4 event offsets; both hook/binding orders, receipt/path lag, late a4 submit during cleanup, and cancellation/foreign/corrupt/incomplete/extra-start controls |
| Force | Empty startup, F1 Held/refusal, readiness and force of the same pair |
| Dialogs | D1 gate/first about-to-open probe, D1–D3 startup and relay-off D4–D6 entry; no answer |
| Groups | Wholly passing sealed synthetic force prerequisite, plans/releases and G0 first live gate/member; missing or incomplete prerequisite stops before spawn/reservation/input |
| Text | Wholly passing sealed synthetic typed/force/groups prerequisites and native N1 setup; fresh N7 launch and N8 first-member variants without live ladder measurements |
| Esc | E1 startup/tool/first steer, real-clock smoke and delayed/partial receipts; no Escape or recovery input |

`runner/testdata/qualification-replay-schedules.json` retains source hashes and
original a2–a4 times, rebased to the constructor's current clock. Ownership and
prompt context, reversed ordering and a4's missing tool callbacks are explicitly
synthetic. Force, dialogs, groups, text and Esc have synthetic schedules, with
no live provider. Every positive prefix seals `not_completed`; passing replay
assertions establish no live readiness, intake, native size, Dialog safety,
recovery or product qualification. Existing K1/K2 cover a1 clock behavior;
there are no invented a1 hooks.

`runner/qualification_prerequisite_test.go` builds complete synthetic force,
typed and groups producers through the existing fixture adapter. Each must pass
sealed loading and fresh whole-phase analysis before replay use. The
`groups-incomplete-prerequisite` negative reseals a passing producer with a
workload gap and requires rejection with zero parents, reservations or inputs.
These synthetic prerequisite passes grant no live qualification authority.

Unit import coverage in `qualification/import_evidence_test.go` is separate:

| Scope | Checks |
|---|---|
| Groups/import gate | `TestQualificationGroupsRetainedForce` rejects missing/substituted force evidence. `TestQualificationProducerImportGate` rejects incomplete force, partial typed-with-cap, independent accounting failure and nested gaps at operational/offline boundaries; its wholly passing signals producer is the acceptance control. |
| Limits producer facts | `TestQualificationLimitsProducerFacts` checks cap/drain facts and each missing F1/F2/typed-cap/O1-start/O2-whole case. Partial units test selected facts without becoming passing imports. |

The 32 setup cases are complemented by 53 workload cases: actual a5 skipped-tool
replay, missing/substituted/parallel tools, partial groups/Esc schedules, Dialog
origin/plan/Stop outcomes, F3 early ordinary delivery, N7 batch correlation and
N8 live-driver/hold loss, refusals, cancellation and cleanup controls. Complete
three-gate group partitions and named partials, exact sighted/unsighted force
targets, issued-prompt/driver-exit interruption witnesses and Launch replacement
during narrow refresh are checked independently. Named
fallbacks retain gaps; independent product failures and unrelated-process
survival assertions remain required. These are synthetic offline exercises,
not additional live measurements.

Measured passing replay checks on snapshot `9ec440fb5442cc8000338bc048a068497951d061`
(Debian 12.15 linux/amd64, Go 1.27.3, tmux 3.3a, Bun 1.3.13, network off):

| Check / job | Scheduler queue / build / run / total |
|---|---:|
| Non-race, all four packages / `j20261120053113-e5b824` | 2.461 / 40.295 / 127.941 / 185.398 s |
| Race runner / `j20261120053113-8425df` | 2.406 / 42.845 / 474.860 / 535.218 s |

The non-race check passed 813 leaf tests; runner race passed 212. Both passed
all 85 replay cases, with no failures/skips/races, and finished within 15 minutes.
Setup/workload replay roots took 13.22/43.48 s non-race and 68.82/309.17 s race.
N8's slowest race cell retained all 1,000 admissions and both refusals in
142.01 s, within its unchanged 450 s exercise window. Suite walls include
the unit-kit build. These offline measurements neither establish live
qualification nor guarantee margin for the whole native/text/N8 phase; its
timeout remains a kit fault under the one-a6 rule.

## Build one identified kit

Repository scripts, compilation, tests and offline analysis run in scheduler
containers. The development host only freezes source as data, submits/waits
for jobs, verifies/transfers artifacts and reads results. Live phases run only
on the AD test VM through `ad-shell`; the packaged executable needs neither
Go nor Python there. The supplied Dockerfile builds native `linux/amd64`.
This is not the arm64 Release Check Kit and uses real Claude, not a stand-in.

The following development-network commands use the project's existing snapshot
and artifact helpers. Choose a fresh label and output directory each time;
replace `JOB_ID` with the scheduler's returned ID. On the development host:

```sh
python3 -I /home/horde/projects/agent-director-project/.scratch/b.kii/e8/prepare-ci-kit-context.py ci-kit-review-1
/home/horde/projects/sysadmin-project/box_admin_dev/box_admin_horde/job-scheduler/bin/jobsched submit --context /home/horde/projects/agent-director-project/.scratch/b.kii/e2/check-contexts/ci-kit-review-1 --name ci-kit-review-1 --channel C0B3X876XSB --memory 8g --network off
/home/horde/projects/sysadmin-project/box_admin_dev/box_admin_horde/job-scheduler/bin/jobsched wait JOB_ID
/home/horde/projects/sysadmin-project/box_admin_dev/box_admin_horde/job-scheduler/bin/jobsched results JOB_ID
python3 -I /home/horde/projects/agent-director-project/.scratch/b.kii/e8r/tools/fetch_kit.py JOB_ID /home/horde/projects/agent-director-project/.scratch/b.kii/e8r/ci-kit-review-1
```

Wait again only if `wait` times out. Keep the context unchanged until completion;
then remove its `source.tar`. Keep the job ID, wall time, Environment Record,
source identity and returned kit files. `fetch_kit.py` verifies the returned
archive's size and SHA-256 before extracting it.

Source modes:

- **Committed:** add `--revision <full-commit>` to the snapshot command. The
  selected commit must contain the harness and the wake sighting-plus-written
  implementation.
- **Precommit:** omit `--revision`. The helper freezes HEAD plus tracked and
  nonignored new working files, stages only the isolated snapshot, and records
  every selected file's digest. It does not stage the working tree. This is
  required while the harness is uncommitted. Ancillary `.scratch`, `tickets`,
  `.ad-test-ssh` and `.release-work` paths are excluded from build input.

In the scheduler, the entry point is
`bash /work/test/claude-live/prepare.sh /work /opt/snapshot/source-identity.json /opt/snapshot/source.tar /out/kit linux/amd64`.
It validates the exact inventory, hashes and Git index before building; the
toolchain must match `go.mod`. `kit.json` records `base_commit`,
`snapshot_tree`, `source_sha256`, `diff_sha256`, `dirty`, `version`,
`commit_stamp`, `platform` and each component's `sha256`/`bytes`. The runtime
and admin share the version/base stamp; the snapshot and hashes distinguish
uncommitted source. Build success proves packaging only.

Returned files are `claude-live-kit.tar.gz`, its `.sha256`, `kit.json` and
`platform-env.json`. The kit archive has no enclosing directory:

```text
bin/claude-live
payload/agent-director-linux-amd64
payload/agent-director-admin-linux-amd64
skills/install-agent-director/install.sh
skills/install-agent-director/schedule/   (cron and four service/timer assets)
docs/caller-contract.md
qualification/source.json
qualification/source-inventory.json
qualification/launch.go
qualification/spawns.go
kit.json
```

Current requalification requires a fresh kit built after the wake-order commit
`2d667e3596f98c3d877c7d21bd6ea51f0a19a263`, including the intended current
analyzer. Capture 6's [provenance](signals/testdata/claude/passing/provenance.json)
records its post-commit kit and the authorized analyzer-only repairs after
capture: the original live verdict was incomplete, and the repaired offline
replay passes. Do not restamp that old kit as a new build or a new live pass.

## Transfer, verify and install

Obtain the MI's go-ahead and an assigned Claude medium VM worker before a live
capture. The precommit shakedown is one run, with affected-phase harness repairs
only when explicitly authorized. Final-candidate qualification after b3 belongs
to the MI; only the MI decides whether unchanged shakedown behavior carries
forward. Use the current subscription login and latest Claude version. The kit
selects `claude-sonnet-5-5`, medium effort, as in E4–E6; there is no model override
flag. An MI-selected different model requires an identified kit change.
Preflight requires `claude`, `tmux`, `sqlite3`, `bash`, `cp` and `jq` and checks
the login and provider version without exporting account details.

Run each command separately on the development host. In these templates,
replace `KEY` with a fresh run key, `/absolute/kit-output` with the retrieved
directory, and `TRUSTED_SHA256` with the digest from the successful build.
Use the existing SSH alias; do not put the VM's host address in evidence.
First verify archive digest and member safety as data:

```sh
python3 -I -c 'import hashlib,pathlib,sys,tarfile; p=pathlib.Path(sys.argv[1]); assert hashlib.sha256(p.read_bytes()).hexdigest()==sys.argv[2]; seen=set()
with tarfile.open(p,"r:gz") as archive:
 for m in archive:
  n=m.name; parts=pathlib.PurePosixPath(n).parts
  assert n and not n.startswith("/") and ".." not in parts and "\\" not in n and all(ord(c)>=32 and ord(c)!=127 for c in n)
  assert (m.isfile() or m.isdir()) and not m.mode & 0o7000 and n not in seen
  seen.add(n)
print("archive digest and member safety verified")' /absolute/kit-output/claude-live-kit.tar.gz TRUSTED_SHA256
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c 'mkdir -m 700 /home/horde/ad-test/ci-kit-KEY'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c 'uname -sm'"
scp -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no /absolute/kit-output/claude-live-kit.tar.gz /absolute/kit-output/claude-live-kit.tar.gz.sha256 ad-test:/home/horde/ad-test/ci-kit-KEY/
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c 'sha256sum /home/horde/ad-test/ci-kit-KEY/claude-live-kit.tar.gz'"
```

Require `Linux x86_64` and the trusted digest before extraction. Then:

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c 'mkdir -m 700 /home/horde/ad-test/ci-kit-KEY/kit'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c 'tar --no-same-owner --no-same-permissions -xzf /home/horde/ad-test/ci-kit-KEY/claude-live-kit.tar.gz -C /home/horde/ad-test/ci-kit-KEY/kit'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live verify-kit --kit /home/horde/ad-test/ci-kit-KEY/kit'"
```

`verify-kit` checks platform, every component/installer asset, executable
identity and runtime/admin stamps before payload execution. Keep the archive
beside `kit/`; live verification records its digest too.

Follow [the install contract](../../docs/install.md#choose-the-upgrade-path)
and [operator recovery](../../docs/operator-actions.md#recovering-a-versioned-install).
Choose the preparation branch **before installing the candidate**:

- **Signal-only or shakedown:** use the standalone candidate installation below.
- **MI full qualification:** skip that installation. Prepare the latest released
  AD and a store compatible with that release in the scoped test HOME, following
  [Install from a release](../../docs/install.md#install-from-a-release) and its
  upgrade-path rules through `ad-shell`. All `~`/HOME paths in that procedure
  must resolve to `/home/horde/ad-test/home`. If a candidate installation is
  already present, use the owning
  [previous-release recovery procedure](../../docs/operator-actions.md#going-back-to-the-previous-release)
  only with its required operator authorization and complete compatible
  recovery set. Never put an old binary over a candidate store. If the released
  installation and compatible store cannot be prepared under those rules,
  stop for the MI. Then prepare full authority and run
  [setup-launches first](#full-run-first-phase); it installs the candidate itself.

Full setup requires `curl` and VM network access to GitHub's latest-release API;
release preparation also needs access to the installer and release assets.
Exclude unrelated agents, callers, `serve` processes and other writers before
asserting the maintenance window. The full setup phase deliberately creates
and tracks its own released-version Launch; its installer allowance applies
only to that owned Launch, not unrelated writers. Refused/uncheckable agents,
relocated stores or unexpected layouts stop for inspection.

### Signal-only and shakedown installation

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live install --kit /home/horde/ad-test/ci-kit-KEY/kit --home /home/horde/ad-test/home --evidence /home/horde/ad-test/ci-install-KEY --backup /home/horde/ad-test/ci-backup-KEY --maintenance-window'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c 'ls -la /home/horde/ad-test/ci-backup-KEY'"
```

Require a passing install verdict and the existing install/store backups
(`install/`, `state.db`). Only for an explicitly authorized obsolete v21 test
store, add `--recreate-v21` to that install command: it backs up the install
and SQLite database, retains original database/sidecar files in
`original-store/`, then lets the exempt installation create the current store.
A current v21 store needs no recreation. Earlier backups remain untouched;
this is not a general schema migration or downgrade procedure.

## Prepare qualification authority and evidence

Use one fresh `KEY` for this qualification; examples use first-attempt `a1`.
For the MI-authorized a6, replace `a1` with `a6` throughout evidence/collection
paths and use the prior-ledger initialization below. These are template tokens,
not shell variables.
Phase directories must be immediate children of `/home/horde/ad-test`, outside
the only permitted test HOME, `/home/horde/ad-test/home`. Keep the verified kit
at `/home/horde/ad-test/ci-kit-KEY/kit` and its archive beside it as above.
Use host destination
`/home/horde/projects/agent-director-project/.scratch/b.kii/e8/live/KEY/a1/`.

Before input, transfer the compatible sealed ci capture and scheduler-evidence
bundle from their verified private host directories:

```sh
scp -r -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no /absolute/ci-capture ad-test:/home/horde/ad-test/ci-qualified-KEY
scp -r -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no /absolute/scheduler-bundle ad-test:/home/horde/ad-test/q-KEY-scheduler
```

In commands below, replace `CI_CAPTURE` with
`/home/horde/ad-test/ci-qualified-KEY` and `SCHEDULER_JSON` with
`/home/horde/ad-test/q-KEY-scheduler/scheduler.json`. Ordinary phase
imports require identical qualification run, candidate, product, provider and
kit; prior attempts also require the explicit previous-summary link. Before
consumer dispatch, every ordinary producer must pass fresh whole-phase analysis,
with no failed verdict or incomplete judgment, including nested rows. Passing
selected facts cannot authorize importing a partial or failed producer; retained
evidence still supplies diagnosis and gaps. The ci
exception preserves its original capture/candidate/kit while requiring the
same actual product build inputs, platform/toolchain and exact Claude version.
Captures 1–5 cannot qualify the revised order point. Importing ci proves no
typed-control comparison.

`kit.json` supplies `base_commit/snapshot_tree` as `CANDIDATE`, and the trusted
archive SHA-256 is `KIT_SHA256`. `qualification/source.json` supplies `PRODUCT`:
the digest of the runtime/admin Go build graph, module pins, platform and
toolchain. `PROVIDER` is the complete `provider_version` string, including its
spaces and parenthesized suffix; `CI_RUN` comes from ci's manifest. `CI_ACTUAL` is
its observed parent starts (currently 6, never an assumed reservation of 7).
Check these records as data against the build's full inventory. Harness-only
changes can preserve product identity; matching version stamps alone cannot.

For an original ci kit without `qualification/source.json`, supply its original
inventory/build log using `source_witnesses` in [scheduler evidence](#scheduler-evidence).
Never reconstruct an old source identity from the current checkout or restamp
an old capture. Retain the source archive digest, snapshot, build job and
Environment Record with each kit.

On the development host, initialize the absent ledger only after those values
and the [shares](#phase-selection-and-turn-accounting) have been checked:

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-registry'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-init --ledger /home/horde/ad-test/q-KEY-ledger.json --qualification-id KEY --attempt a1 --candidate CANDIDATE --product PRODUCT --provider \"PROVIDER\" --kit-id KIT_SHA256 --kind shakedown --ci-capture CI_RUN --ci-turns CI_ACTUAL --dialog-cases D1,D2,D3,D4,D5,D6'"
```

For the authorized changed-kit a6, use a fresh key, ledger and backup paths.
Through the same VM `ad-shell` invocation, initialize instead with:

```sh
/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-init --ledger /home/horde/ad-test/q-KEY-ledger.json --qualification-id KEY --attempt a6 --candidate CANDIDATE --product PRODUCT --provider "PROVIDER" --kit-id KIT_SHA256 --kind shakedown --ci-capture CI_RUN --ci-turns CI_ACTUAL --dialog-cases D1,D2,D3,D4,D5,D6 --prior-ledger ORIGINAL_A5_LEDGER
```

`ORIGINAL_A5_LEDGER` is the explicit absolute path to the unchanged, settled a5
ledger, including its nested original a4/a3/a2 ledgers. Their 12 actual starts
(`PriorActual: 12`) and
original bytes/digests carry into E8 accounting; closed never-issued reservations
add no fictitious start. Preserve a1–a5 ledgers, raw evidence and kit identities.
Prior usage cannot be removed, replaced or relabelled as the new candidate's
work. This permits one authorized
a6 of at most 32 starts, including N6 if needed. **Run a6 once.** On a kit fault,
go directly to option B: finish z9 with what was measured and record every gap
for b3/release qualification. Do not build or run another retry. A product
finding still stops for the MI.

The registry exports `phases`, `coverage` and all 48 `dialogs` keys. Initialization
selects the six shakedown phases below; `--phase` accepts a comma-separated
subset. `--dialog-cases` narrows reserved starts, not mandatory coverage.
Do not narrow away a required dimension to obtain a pass.

## Phase selection and turn accounting

Every live phase has **nine minutes total, including 90 s of cleanup** and final
collection. A missed window stays `not_completed`; the deadline never renews.
The registry's required check IDs are also used by the offline finalizer.
Refusals before the phase body still write `manifest.json`, `commands.jsonl`
and a nonpass `verdict.json`, and run the shared cleanup finalizer.

| Phase | Shakedown new parent starts | MI full-run planning starts | Required checks / prerequisites |
|---|---:|---:|---|
| `setup-launches` | 0; deferred | 4 | `install-resume`, `launch-setup`, `defaults`; run-owned released Launch, backup and maintenance window |
| `signals` | 0; import ci | 7 | `signal-capture`, `p-b`, `p-c`, `turn-end-premise`, `wake-order-point`; compatible revised ci |
| `typed-controls` | 6 | 6 | `typed-long-tool`, `typed-between-tools`, `typed-idle`, `steer-cap`; all three matched AD/native pairs |
| `force-states` | 5 | 5 | `force-table`, `force-pending`, `force-group`, `wake-caps`; actual readiness/state and candidate capability agreement |
| `dialogs` | 6 | 6 | `dialog-matrix`, `pause-refusal`; D1–D6 and exact fallback for any unproducible dimension |
| `groups` | 3 | 3 | `steer-group`, `ordinary-group`, `group-gaps`, `small-backlog`, `whole-corpus`; import force F3/F4 |
| `text-limits-backlog` | 8 maximum | 8 | `native-wake-ladder`, `native-steer-ladder`, `ad-caps`, `ad-refusals`, `large-backlog`; import typed, force and groups |
| `lifecycle` | 0; deferred | 2 | `lost-reply`, `crash-resume`, `fresh-reuse`, `concurrent-delivery`, `read-only` |
| `user-hooks` | 0; deferred | 2 | `hook-block`, `hook-context`, `post-sighting-hook`, `never-sighted-hold`, `stale-unknown` |
| `esc-gap` | 4 | 4 | `esc-typed-recovery`, `esc-resume-recovery`, `esc-notification`, `esc-input-placement` |
| `uncovered` | 0; deferred | 0 Claude | `uncovered-agents`; installed/authenticated/usable kind or exact applicable scheduler case |
| summary / cleanup | 0 | 0 | all reservations reconciled and owned exits verified |
| contingency | 0 | 1, separately authorized | no implicit repair allowance |
| **Ceiling** | **32 = 6+5+6+3+8+4** | **48 (47 planned + 1 contingency)** | planning grants no run or retry |

E8's cumulative ceiling is 64 under MI decision z9 a5=A. Historical captures
used 14, ci actual 6 is charged once (allowance at most 7), and a5 plus nested
a4/a3/a2 carry 12 actual starts. Remaining authority for a6 is `64 − 14 − 6 − 12 = 32`;
its independent
shakedown ceiling is also 32. The separate full-run ceiling remains 48.
No unused slot enlarges a share, enables a deferred phase or authorizes a retry.

The durable ledger reserves before input and reconciles owned parent
`UserPromptSubmit` occurrences with case, Launch, provider prompt identity and
raw evidence. Startup/resume and failed/unexpected starts count when they
actually start a parent prompt; every forced wake reserves its own slot.
Child/background work inside an existing parent turn, mid-turn inputs,
compactions, model/API requests and other-agent usage remain separate metadata,
with no billing inference. A background notification that starts a new parent
prompt is an unexpected charged start and stops the run. Actual overshoots
remain recorded; neither failed input nor a new phase resets spending.

Claude 2.1.311 can wrap fast typed input as
`<pasted_content id="ID">` + newline + exact inner text + newline +
`</pasted_content id="ID">`, then queue it mid-turn under the running
`prompt_id`. Typed controls accept that exact form or unwrapped text, retaining
the raw wrapper/paste ID and comparing the planned inner text. Queued intake
also requires the exact recorded forward form and independent human attachment
facts; malformed wrappers are not stripped. A reused `prompt_id` does not merge
raw parent submit occurrences: the mid-turn typed submit remains charged.
Wrapped human input never supplies AD custody, admission, delivery or AD-side
whole-input/order proof, and typed timing cannot acquire AD issue/sighting facts.

### Workload and sample data

Typed long-tool input and force F2 share a request for about 180 words of
community-garden recommendations between two separate three-second foreground
tools. The workload creates an opportunity, not proof of the between-tools
window; actual events, requests and injection timing still decide it.
Owned parent PreToolUse IDs must join actual one-call PostToolBatch membership,
the exact foreground commands and request intervals. Instructions alone prove
no tool window. Skipped, substituted, extra, parallel, combined or background
calls close the branch without a replacement prompt or an answer to a question.
Cap padding/native ASCII rungs use varied numbered sample records; the
1,900-byte corpus uses varied multiline Unicode/tag records. Retain their
independent expected bodies and measured counts. Group markup is explicitly
labelled sample data and keeps literal tags. All cap/rung/member sizes and
minimal partitions remain unchanged. Owned-file waits use a compact foreground
loop bounded by 600 one-second iterations, with separate calls/paths preserved.
The corpus acknowledgment preserves scheduled tools. N7's additionalContext
rungs preserve the initial separate-tool sequence and mark padding as inert;
they do not request a five-word finish. Native wake instructions remain separate.
The full-only H2 closing-tag fixture (audit F) and signals `StartupPA` probe
retain their existing wording until full qualification; importing ci does not
rerun them.

### Physical prompt schedule

These are executable case identities, not extra manual calls. Each physical
start has one charging owner; other checks consume the same immutable evidence.
No READY/trigger prompts may be added. State-setting requests must be observed
actually executing. Every phase ends its own sessions; imports share evidence.

| Owner / IDs | Required schedule |
|---|---|
| `force-states`: F1 | Fresh Launch without initial input. Prove starting refusal, Held custody and no prior issue, then idle send-with-force at the candidate cap. Its body requests a foreground short-sleep loop followed by another tool, creating F2's long-tool window. A missed starting/idle window stops explicitly. |
| `force-states`: F2 | During that actual long tool, force cap-sized F2 plus a distinct Wait Turn tail that cannot fit. The tool outlasts the finite call: only F2 issues, the call returns pending, repeat force refuses `handover_in_progress`, and the never-issued tail returns to Held. F2's body requests a first tool, widened prose interval and second tool. Retain F2's later own sighting/intake. |
| `force-states`: F3/F4 | After F2's own sighting/order/admission, before Stop drains the tail, add one Wait Turn member and start one force in the observed between-tools interval. Minimal packing requires two whole prompts. F3 first issues the released tail; F4 issues after F3's own sighting-plus-written order and admission, within the same call. Keep release/reclaim/issue identities and both inputs; groups imports this pair. |
| `force-states`: F5 | After earlier input resolves and idle is observed, explicit `/compact` creates the compaction window for force. An extra parent start is charged and stops rather than hiding a sixth turn. Starting/ended refusal probes add no input; permission/plan forces belong to dialogs. |
| `dialogs`: D1–D3 | D1 runs relay-on main/subagent/background origins within one parent start. D2 forces at permission; D3 at plan. Both modes and confirmation choices queue behind each held Dialog. Only supported `decide` allow-once/deny answers release it. |
| `dialogs`: D4–D6 | Separate relay-off main/subagent/background Launches. Observe about-to-open and open windows, unchanged custody and options; require the deliberate `relay-off-end-D4/D5/D6` keyless ending plus verified exit. Generic cleanup alone is insufficient. |
| `groups`: G0 | One trigger requests three separately gated foreground Bash calls, with `timeout=600000` ms and `run_in_background=false`: `work-G0/release-first`, `release-second`, then `release-ordinary`. Admit the whole steer plan behind gate 1; release the first two calls for S1/S2. After both handovers are written, admit the whole ordinary plan behind gate 3 before releasing it for G0's Stop. No additional parent start. |
| `groups`: S1/S2, zero starts | Consecutive parent PostToolBatch boundaries emit two whole additionalContext groups with separate next requests. S2 precedes O1; neither crosses the first Wait Turn member. |
| `groups`: O1/O2 | G0's parent Stop records steer take evidence and ordinarily issues O1; O1's Stop issues O2. Retain separate sightings, written outputs and whole next inputs. Sighting alone never advances ordinary delivery. Text/limits imports this same small drain. |
| `text-limits-backlog`: N1–N5 | Native idle wakes outside AD: exact 1,900-byte corpus, 9,600 complete transformed bytes, then 9,999/10,000/10,001 complete-input characters. |
| `text-limits-backlog`: N6 | One bounded upward native wake exercise if N5 remains whole. A final whole rung is a lower bound, not a discovered maximum or retry allowance. |
| `text-limits-backlog`: N7 | One native trigger turn requests five separate foreground Bash calls. Distinct one-call parent batches carry the five additionalContext rungs; repeated/parallel/combined IDs cannot supply multiple measurements. `insufficient-batches` retains the measured prefix. N8 may follow only after verified native owner exit. |
| `text-limits-backlog`: N8 | One AD busy trigger with a harness-owned PreToolUse hold: accept 1,000 distinct 1,900-byte Messages, then retain over-text and `held_count` refusals. End the provider before releasing the hold; fresh reuse has no startup input. Retain original Results, identity/body/hash/target/order through custody, verified exit and final confirmed Lookups. No delivery turn. |

F1/F2 provide idle/busy AD cap proof; typed-controls supplies parent-steer cap
proof. Derive exact minimal groups from the candidate renderer/limits, complete
wrapper/escaping and decimal separator widths. Preserve mixed modes/confirmation,
whole Unicode/generic tags and once-only target/accepted order across modes and
handovers. F3/F4 and O1/O2 are four distinct starts; S1/S2 adds none.

Each G0 gate binds the exact passive foreground tool input and a live owned
driver, with its own 600 s hook/driver lifetime. A shell PreToolUse alone is
insufficient. Before/after every admission, validate the selected call, timeout,
same unreleased driver and absence of parent Stop. Every planned member must
be uniquely Held before releasing its shell path and independent gate.
`group-admission` observations retain case `G0`, mode `steer_turn|wait_turn`,
state `partial|complete` and the gate reference. Snapshots
`group-all-accepted-before-boundary` and
`group-ordinary-all-accepted-before-boundary` retain both whole plans before
release. A missing/lost gate or Stop ends a named partial with no further sends;
Stop after S2 before the third gate means zero ordinary inputs. Exact partition,
order and start accounting remain independent checks: a physically different
partition cannot inherit O1/O2 or their steer alternatives. Its start stays
unmatched and charged. Cleanup uses the same exact-driver teardown as N8.

Native sessions use temporary native-output hooks outside AD, without READY
input. Label those observations native Claude bounds, with separate text/body/
complete provider/model input layers and byte, Unicode-scalar and UTF-16 counts,
including transformation overhead. They prove no AD admission or custody.
Separately prove whole input through the unmodified candidate at its published
idle wake, busy forced wake and steer caps, with over-limit refusals. Validate
the packaged caller contract, compiled table and runtime capabilities agree;
do not bake today's numeric caps into expectations.

The backlog has two live parts: N8's admission/disposal without delivery and
groups' O1/O2 small drain over consecutive turns. Cite scheduler
`TestClaudeWakeDrainsBacklog` for the full 1,000×1,900-byte drain. Never run that
expensive drain live or add a ninth text/limits prompt.

### Named workload outcomes and reserved alternatives

The intended schedule above and its alternatives share existing reserved starts.
The ledger retains `alternatives`/`selected_case` and charges each slot once at
`max(reserved, actual)`; branch closure never refunds authority, borrows N6 or
uses the full-run contingency. Missing markers, another session, duplicate
starts or unknown input still fail accounting.

| Slot / branch | Alternative and retained gap |
|---|---|
| Typed AD idle slot | Pre-reserved for idle input **or** the first long-tool/between-tools Stop fallback. A second busy steer requires the previous handover written at a real tool boundary. Exact body/AD marker, owned Launch/session, raw submit occurrence and final stored membership reconcile the chosen start. Early passive Stop/submit evidence can close the branch before store publication, but cannot replace final accounting. `early-Stop-fallback` records **no tool boundary after injection; delivered at Stop**, preserving whole wait-turn intake separately from absent PostToolBatch proof. The idle pair and unfinished measurements stay gaps. |
| Groups O1/O2 | Each slot is its whole planned ordinary wake **or** its whole displaced steer group. Ordinary admission requires both written tool-boundary steer handovers and the third live gate. Early closure retains unmeasured gaps; a different physical partition remains unmatched/charged. |
| Esc E2/E4 | Each is its intended recovery input **or** displaced Esc-steer-1/2. A Stop fallback closes the branch and keeps unmeasured interruption/recovery gaps. |
| Dialog D2 | The selected D2 slot is intended D2 **or** the first Stop-delivered probe. All 24 probes fit one bounded prompt; the first Stop fallback closes the branch. The gate requires the owned native Bash/origin; child/background origins also require a parent Task/Agent invocation. Missing origin/plan never triggers another Dialog or an answer. D1-only selection is incomplete before probes because it lacks a fallback slot; a6 selects D1–D6. |
| Force F3 | Reserve original F3 before its first enqueue and reuse that reservation through tail/reclaim. `ordinary-early-F3` counts its ordinary start once and leaves F3/F4 incomplete. F2 handed over after its selected tool ended is a precondition deviation; valid pending follows its normal branch. |

`workload/<code>` rows are `not_completed`: `missing-next-tool`,
`missed-prose-window`, `precondition-not-established`, `different-origin`,
`plan-not-present`, `early-Stop-fallback`, `response-still-active`,
`nonterminating-model`, `ordinary-early-F3`, `insufficient-batches` and
`backlog-not-exercised`. Their `observed` binds `code`, `case`, `instance`,
`launch`, `session`, `input_sha256`, `at` and available `request`/`tool_use_id`,
plus indexed parent/tool/Stop references. `teardown_uncertain` retains unsupported
background cleanup uncertainty. Named timeouts keep their cause; independent
startup-binding faults keep their setup deadline. None is a measurement,
accepted limit, import permission or substitute for a product failure.

### N8 hold and custody proof

An independent PreToolUse driver holds the exact foreground invocation before
traffic. `raw/gates/*/input.json` binds the passive parent event;
`driver.json` records PID/start, run, instance, session, cwd and input SHA-256.
Every admission/refusal rechecks that same live driver, exact tool input,
unreleased gate and absence of parent Stop/PostToolBatch. Missing, wrong,
background or dead-driver evidence allows zero further sends. A detected
Stop/drain halts traffic and retains its charges and failing evidence.

Per-send checks use a narrow activity projection: owned spawn/Launch identity
and prompt existence, with the normal budget/Dialog/provider guards. They do
not scan retained Messages or replace custody evidence. Full snapshots remain
at every 25th admission, before/after refusals, stop/reuse and final collection.
The analyzer separately requires sealed `N8-hold-first`/`N8-hold-last`
observations of the same live PID/start spanning the first admission through
both refusals, exact driver/input/owner binding and provider exit after that
interval, alongside the ordinary backlog custody/disposal/Lookup proof.
A leftover receipt alone proves no live hold. Cleanup stops all provider owners
before releasing/reaping only exact registered driver identities; uncertain
ownership/exit retains resources for inspection.

## Invoke qualification phases once

### Full-run first phase

For the MI's separate full run, complete the released-install preparation above,
then initialize a fresh ledger with `--kind full` in place of `--kind shakedown`.
It selects every registered phase. Before any measurement phase, run this
development-host command with a fresh backup/evidence destination:

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-setup --ledger /home/horde/ad-test/q-KEY-ledger.json --phase setup-launches --backup /home/horde/ad-test/q-KEY-setup-backup --maintenance-window'"
```

Only the explicitly approved obsolete-v21-store case adds `--recreate-v21` as
described in the installation section. Setup verifies the installed released
version, owns its released Launch, installs the candidate, then checks actual
exit/same-conversation resume and fresh slash/MCP/user-hook/default setup.
Require its passing verdict before continuing. Never adopt an unrelated agent
or edit its DB; an E7 install contradiction stops.

### Selected measurement phases

The shakedown reaches these phases after its standalone candidate install;
the full run reaches them after a passing `setup-launches`.
Run each following development-host command separately, inspecting its verdict
before continuing. Only the [sealed continuation gate](#sealed-continuation-gate)
permits a nonpass phase to be followed by another phase. These six unattended
invocations batch Messages inside the
kit rather than making a host call per Message. Common build/transfer/install,
these calls and bounded collection aim at about 40 host calls in total.

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-typed --ledger /home/horde/ad-test/q-KEY-ledger.json --phase typed-controls --imports CI_CAPTURE --scheduler-evidence SCHEDULER_JSON'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-forces --ledger /home/horde/ad-test/q-KEY-ledger.json --phase force-states'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-dialogs --ledger /home/horde/ad-test/q-KEY-ledger.json --phase dialogs --scheduler-evidence SCHEDULER_JSON'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-groups --ledger /home/horde/ad-test/q-KEY-ledger.json --phase groups --imports /home/horde/ad-test/q-KEY-a1-forces'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-text --ledger /home/horde/ad-test/q-KEY-ledger.json --phase text-limits-backlog --imports /home/horde/ad-test/q-KEY-a1-forces,/home/horde/ad-test/q-KEY-a1-typed,/home/horde/ad-test/q-KEY-a1-groups --scheduler-evidence SCHEDULER_JSON'"
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-phase --kit /home/horde/ad-test/ci-kit-KEY/kit --evidence /home/horde/ad-test/q-KEY-a1-esc --ledger /home/horde/ad-test/q-KEY-ledger.json --phase esc-gap'"
```

### Remaining full-run phases

Use the same `qualification-phase` syntax and fresh phase evidence paths for
`signals`, `lifecycle`, `user-hooks` and `uncovered` as well as the six above.
Signals imports compatible ci; it never silently starts another probe.

Lifecycle uses L1/L2 for a real interrupted client reply/repeat of the same
confirmed pair, exit/resume, overlapping callers, read-only no-issue windows
and fresh reuse. User-hooks uses H1 for a genuine block and H2 for separately
named additionalContext plus the terminal ambiguous-wake hold. Neither fixture
can establish `delivered_confirmed`. Claude prompt replacement is explicitly
`claude_inapplicable`: [UserPromptSubmit cannot replace the prompt](https://code.claude.com/docs/en/hooks#decision-control).
No invented `updatedPrompt` or PreToolUse `updatedInput` substitutes for it.
If H1's actual sighting does not precede the block, post-sighting order remains
unproven; do not delay AD's hook to manufacture that ordering.

Uncovered kinds come from the packaged candidate launch source, not a fixed
list. Record installation, authentication and usability separately. Live
available kinds exercise refusal/default/resume/template/force/conditional-pause
and unchanged native send/repeat/raw-key paths. Missing kinds need their own
applicable Docker case; the registered Codex case cannot cover another kind.

## Scheduler evidence

Prepare schema-1 JSON as data beside its raw primary logs. `--scheduler-evidence`
copies it and the logs into hashed phase evidence before live input. Each
`jobs[]` entry needs `id`, exact final `candidate` and `product`, `platform`
(`linux/amd64` or `linux/arm64`), `status: succeeded`, native `environment`
(including matching `dpkg_architecture`) and `tests[]`. A test binds `coverage`,
`package`, `name`, optional `case`, relative `log`, its `sha256`, `format`
(`go-test-json` or `docker-case-json`), `reason`, `kind` and actual `source`.
It must name exactly one successful test/subcase in the hashed log, with no
failure. An unsupported case or suite heading is not a successful case.

Allowed kinds are `deterministic_seam`, `native_unavailable`, `upgrade`,
`interface_parity` and `named_premise`. Budget omissions never justify fallback.
Each Dialog citation also needs `subject` with exact `mode`, `confirmed`,
`relay`, `origin` and `state`, and its own `case`. Preserve all 48 combinations:
2 modes × 2 confirmation choices × 2 relay settings × 3 origins × 2 states.
Missing relay-off proof cannot hide under a passing parent heading.

Required exact citations include:

| Coverage key | Applicable evidence |
|---|---|
| `scheduler/full-backlog-drain` | `TestClaudeWakeDrainsBacklog` |
| `scheduler/wake-order-guards` | `TestBusySendWakeOrderEvidenceGuards`, including both fact orders and capture-5 counter-history |
| `o4/r-a` | `TestBusySendTurnEndWaitsForTakeEvidence`; b3 release disclosure remains required |
| `scheduler/never-sighted-hold` | `TestBusySendWakeOrderAcrossEnd` |
| `stale-unknown` | `TestBusySendSightingRefusals` |
| `prd/Seamless upgrades` | Docker `epic-12-install`, case `t2.75s.f7.la`, source `tickets/testplans/b.75s/t1.75s.f7/t2.75s.f7.la/t2.75s.f7.la.md` |
| `scheduler/interface-parity` | exact final-candidate CLI/MCP/Go/TypeScript cases and reasons |
| `uncovered/codex` | Docker `busy-send-uncovered`, case `t2.75s.bs.un`, source `tickets/testplans/b.75s/t1.75s.bs/t2.75s.bs.un/t2.75s.bs.un.md` |
| `uncovered/KIND/request-token` | `TestBusySendUncoveredRefusalMatrix`, exact `KIND/send-keys_request_token` subcase |

Currently no registered unavailable-kind Docker case for Droid/OpenCode/Cursor
can be borrowed from Codex; that full-qualification coverage stays incomplete
until applicable evidence exists. This does not authorize extra live input.

Optional `source_witnesses[]` binds original ci `capture`, `candidate`, `kit`,
`source_archive`, `snapshot`, `original_job`, `inventory`, `inventory_sha256`,
`build_log`, `build_log_sha256`, `toolchain` and `platform`. The referenced
original inventory must match its snapshot and current product-input digests;
the original log must contain its actual `KIT_BUILT` archive digest/platform.
These witnesses preserve the old kit's identity rather than asserting a new one.

## Required coverage map

`qualification-registry` is the machine-readable source. The rows below map
every SR-8.3 item and live PRD heading to actual phase/check evidence. A missing
required component is `not_completed`; contrary completed observations fail.
Exact scheduler fallback requires the preceding provenance and reason.

| Requirement / coverage key | Phase checks and evidence |
|---|---|
| Fresh/resumed launch setup; `sr8.3/fresh-resumed` | setup `install-resume`, `launch-setup`, `defaults`: startup slash once, MCP, appended system prompt, allowed tools and preserved user hooks |
| Dialog/about-to-open; `sr8.3/dialogs` | dialogs `dialog-matrix`, all 48 `dialog/MODE/confirmed=BOOL/relay=BOOL/ORIGIN/STATE` rows, held custody, relay requests or exact case fallback |
| Busy/idle/compaction/startup; `sr8.3/busy-idle-compaction-startup` | force `force-table`, independent state-setting observations and actual force bindings |
| Parent/subagent/background; `sr8.3/parent-subagent-background` | dialogs `dialog-matrix`, owned hook/event identities; child callbacks never stand in for parent boundaries |
| All forced states; `sr8.3/forced-states`, `sr4.4/dialog-force-states` | force `force-table`/`force-pending` plus dialogs `permission-force` and `plan-force`; measured runtime contract rows |
| Mixed choices; `sr8.3/mixed-options` | groups `whole-corpus`, per-Message Results and original confirmation choice |
| Whole Unicode/tags; `sr8.3/whole-unicode-tags` | groups `whole-corpus`, exact first eligible request/body rather than preview or later echo |
| Boundaries/minimal packing; `sr8.3/packing-boundaries` | text `ad-caps`, renderer/wrapper/decimal widths and over-limit refusals |
| Multi-prompt groups; `sr8.3/multi-prompt-groups` | groups `steer-group`, `ordinary-group`, `group-gaps`; distinct S1/S2, O1/O2 and imported F3/F4 schedules |
| Both backlog parts; `sr8.3/backlog` | text `large-backlog`, original 1,000 pairs/bodies through disposal/Lookup; groups `small-backlog` imports O1/O2; full drain exact scheduler proof |
| Crash/lost reply; `sr8.3/crash-lost-reply` | lifecycle `lost-reply`, `crash-resume`, same immutable confirmed pair and actual exit/same conversation |
| Concurrent callbacks; `sr8.3/concurrent-callbacks` | lifecycle `concurrent-delivery`, send/force/ordinary issue/order and once-only raw evidence |
| Launch replacement; `sr8.3/replacement` | lifecycle `fresh-reuse`, old custody/final Lookups, new owned Launch without replay |
| Typed comparisons/timings; `sr8.3/typed-controls` | typed all three matched pairs and `steer-cap`, observed tool interval and first eligible request independently derived |
| Hook block/context/stale/unknown; `sr8.3/hooks-stale-unknown`, `sr8.3/hook-block`, `sr8.3/hook-context` | user-hooks H1/H2, `post-sighting-hook`, `stale-unknown`, real fixture receipts and exact refusal tests; prompt replacement is `claude/prompt-replacement` inapplicability |
| Native bounds and AD caps | text `native-wake-ladder`, `native-steer-ladder`, `ad-caps`, `ad-refusals`; native lower bounds remain separate from candidate cap proof |
| Uncovered kinds; `sr8.3/uncovered-agents` | uncovered `uncovered-agents`, candidate-derived kinds and exact per-kind/operation evidence |
| Signals relied on | signals `signal-capture`: FileChanged/asyncRewake, PostToolBatch context, UserPromptSubmit/wrapping; compatible ci raw hooks, AD facts and whole requests |
| O4 P-b; `o4/p-b` | ci `p-b` plus `wake-stop-continuation`: no later-issued input overtakes the uniquely sighted wake, including sibling Stop/tool continuation |
| Reachable O4 P-c; `o4/p-c` | ci `p-c`: overlapping Held STEER/Plain lifetimes, steer context before later Plain issue and whole intake |
| Turn-end premise; `o4/turn-end-premise` | ci `turn-end-order`: steer uptake before next parent Stop; no wake release from Stop |
| Wake order point; `o4/wake-order-point` | ci mandatory `hook-completion-order` maps to `wake-order-point`: written plus unique AD sighting in either fact order, while Launch runs; ok guard regressions plus post-ok sibling continuation capture |
| R-a; `o4/r-a` | Named unmeasured premise: later wake cannot pass pending PostToolBatch context. `input_unresolved` force refusal and ordinary awaiting-take guard prevent the live race; exact guard proof plus b3 release disclosure, no live verdict |
| Never-sighted hold; `sr2.5/never-sighted-hold` | user-hooks H2 plus signals terminal Reserved case; Wait/Steer hold followers, parent boundary/Stop, no sighting/order until End, exact until-End scheduler proof |
| PRD Agents busy send does not cover | `prd/Agents busy send does not cover` → uncovered `uncovered-agents` |
| PRD Dialog safety | `prd/Dialog safety` → dialogs `dialog-matrix`, `pause-refusal` and every dimension |
| PRD Send Modes | `prd/Send Modes` → groups `whole-corpus`, plus matched typed comparisons |
| PRD Accepting | `prd/Accepting` → text `ad-refusals`, original Results/pairs and refusal custody |
| PRD Seamless upgrades | `prd/Seamless upgrades` → exact E7 scheduler Docker case, not a new live upgrade |
| PRD Order and integrity | `prd/Order and integrity` → groups `ordinary-group` plus global packing/accepted order |
| PRD Waiting and resume | `prd/Waiting and resume` → setup `install-resume`; lifecycle crash/resume evidence |
| PRD Forced delivery | `prd/Forced delivery` → force `force-table`, dialogs permission/plan rows |
| PRD Confirm From Transcripts | `prd/Confirm From Transcripts` → setup `defaults`; Claude capability `not_needed`, no confirmation from transcript reads |
| PRD Event-only delivery | `prd/Event-only delivery` → lifecycle `read-only`, no new issue from reads/sweep |
| PRD Agent Defaults | `prd/Agent Defaults` → setup `defaults`, spawn/resume/template and stored values |
| PRD Results, IDs, Lookup and recovery | matching `prd/` row → lifecycle `lost-reply` and retained pair/Result evidence |
| PRD No raw keys | `prd/No raw keys` → groups `whole-corpus`, keyless AD delivery/pause command log |
| PRD Limits and traffic | registry's `prd/Limits and CSCB traffic` → text `large-backlog` plus caps/native bounds |
| PRD Release | `prd/Release` → setup `install-resume`; b3 owns publication and final MI qualification |
| Esc gap / both recoveries | `esc-typed-recovery`, `esc-resume-recovery`, `esc-notification`, `esc-input-placement`: actual Esc, whole steer intake before it, payload or bounded Notification absence, fresh typed+Stop and independent keyless pause/resume |

P-a and hook-completion order are not current qualification premises. Historical
recordings/replays retain their identities and narrow verdicts without granting
new qualification authority.

## Stop and cleanup rules

Except for the explicit sealed continuation gate below, stop for the Main
Agent/MI on expired login, missing tool, unexpected Dialog,
exhausted share/deadline, product/O4 failure, evidence/identity mismatch or
unresolved cleanup. A completed valid force-eligible exercise with uniquely
sightable text but no required sighting, without a deliberate blocking fixture,
reopens O4/M1 G3. Never convert timeout to success or invent an accepted limit.
A missed state is incomplete without another trigger or automatic retry.
`force-sighting: fail` requires the exact unsighted target to be issued/written
and joined to an accepted forced handover on the current instance/Launch/session.
A retained sighting suppresses that missing-sighting claim even when another
workload/guard ends the wait. The original wait cause and independent force
order/contract judgments remain; unrelated exits do not become force failures.

Put duplicate-unresolved, reserved-closing-tag or deliberately unsightable wakes
last or in a dedicated owned Launch. Only explicitly named hold followers may
follow: observe both Wait Turn and Steer Turn staying Held through available
parent Stops/tool boundaries until actual Launch End. Keep absent unique
sighting/order, End/custody and exact scheduler until-End proof. End removes
the barrier; it does not prove intake. A genuine user block after AD sighting
is distinct: order/admission may hold while model intake remains unproven.

Use only supported relay-on `decide` allow-once/deny; never always-allow.
Relay-off Dialogs are observed, then deliberately ended keylessly on their own
Launch; never automate a pane answer, whether by AD native keys or direct tmux.
Only the explicit typed-control, Esc and `/compact` exercises use direct test
input. Covered AD delivery and pause remain keyless. Do not read credentials,
dump settings/environment or change unrelated hooks. No watcher, daemon or
product repair is authorized by this procedure.

The shared finalizer first refreshes and stops every recorded AD/native owned
session/process, then collects transcripts in a second pass. Same-Launch
PID/start/session facts are refreshed after pending grace and recovery, and
again before collection; replacement revokes further commands. It verifies
process start identities have exited before reuse/resume and removes
only exact owned recorder/fixture entries and private runtime resources, and
preserves unrelated settings. Unverified exit retains runtime for inspection;
report leftovers instead of removing them by hand. No owned live session may
cross a phase boundary. Leave the candidate installed, with backups/evidence.

### Named provider refusal

`provider_refusal` rows have status `not_completed`. The detector recognizes
assistant `message.stop_reason: refusal` or system
`subtype: model_refusal_no_fallback`, bound to a current owned main request in
the Launch/case interval. It does not infer refusal from response prose,
quoted tokens, generic `invalid_request`, child or stale/unbound history.

Each row's `observed` retains `instance`, `launch`, `session`, active input
`case`, `waiting_case` and `markers` (`code`, `request`, `timestamp`, `line`),
with indexed raw transcript references in `evidence`. The active input may
differ from the not-yet-sent case whose window is awaited; that waiter creates
no fictitious send/start. System-marker parent UUID ancestry supplies request
binding; native `requestId`/`refusedUserMessageUuid` are not journal aliases.
Live waits return the named refusal instead of waiting out a generic deadline;
sealed analysis scans both owners even if the first refusal stopped collection.
These facts are diagnostic only: no intake, AD delivery or confirmation proof,
no charge change and no weakening of independent failure precedence.

Checks apply to operational ready/idle/tool-window/typed-completion waits,
force sighting, group drain, Dialog gate/readiness, native startup/rungs/N7,
Esc Notification and provider-backed hook progress. Ordinary runner waits,
stop/cleanup/native process-exit verification, lifecycle client/result-file/
process waits, Dialog hook-file release and non-Claude capability waits remain
unchecked, so cleanup can finish after a refusal. There is no global guard.

### Sealed continuation gate

After each phase retain its exit, sealed capture and verdict; reanalyze the same
capture. A pass permits dependent work. For a named workload nonpass, run this
file-only command through the same VM `ad-shell` invocation, replacing `CAPTURE`
with the executed phase's absolute evidence path:

```sh
/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-continuation --evidence CAPTURE --ledger /home/horde/ad-test/q-KEY-ledger.json
```

Only exit 0 **and** `continue_independent: true` permit already selected
independent work. Output `status` remains `not_completed`; `blocked_imports`
names the incomplete phase, `b3_gaps` contains its incomplete check Results and
`outcomes` contains the bound workload records. The command loads indexed sealed
evidence and requires the current ledger to equal its exact reconciled after
ledger, valid accounting and passed `accounting`, `cleanup` and `contract`
checks (`contract` is the analyzer's canonical ID). It validates each named
outcome against frozen input SHA-256, owned Launch/session and indexed parent
events. Failed components
(including nested/independent failures), unknown returned phase problems,
unexpected starts and cleanup uncertainty block continuation. An error name or
`not_completed` status alone never opens the gate.

| Next phase | Required passing imports / dependency |
|---|---|
| `force-states`, `dialogs`, `esc-gap` | Independent of earlier measurement phases; retain installed candidate/ci prerequisites |
| `groups` | Exact passing force capture |
| `text-limits-backlog` | Exact passing typed, force and groups captures |

Skip any phase whose import is incomplete, without input/reservation; retain
every unattempted check as a b3 gap. Within a phase, closure ends that branch;
no replacement input, second Dialog attempt or retry is authorized. N7's
independent N8 continuation still requires verified native owner exit and its
own established hold. This gate grants no pass, partial import, new phase,
extra turn, cap/deadline change or waiver. Provider refusal remains subject to
its separately reviewed attempt-bound policy; it is not a workload deviation
and this gate does not authorize continuing after it.

Collection/summary name only existing capture directories and keep omitted
selected phases incomplete. Preserve diagnostic summary exit 1 and all
`b3_gaps`; later passes cannot erase failures or promote partial evidence.
The [one-a6 rule](#prepare-qualification-authority-and-evidence) still applies:
a kit fault ends with option B and every remaining gap; a product finding stops
for the MI.

## Collect, summarize and rerun

Fetch evidence even on nonpass. Never export backups, settings or process
environments. Raw captures/transcripts remain private until deliberately
scrubbed; do not open `raw/auth-status.json`. Phase `manifest.json` binds
candidate/kit/provider, deadline, cases, owned Launches, hook ownership, observed
starts, problems and artifact hashes. `commands.jsonl` records argv, start/end,
exit, timeout and output references. `raw/qualification-ledger-before.json`,
`raw/qualification-ledger-after.json`, observations/import index, scoped
read-only snapshots, exact requests, trail and cleanup retain judgment inputs.

Before classifying parent hooks, provider-progress checks refresh all active
owned Claude siblings read-only. Checked and sibling tuples use the same scoped
transcript resolver; hooks cannot establish ownership. Pending identities or
an unpublished own receipt stay within the existing bounded wait, without
accepting readiness, renewing the deadline or adding input. Once all parents
are bound, a foreign tuple reports `does not match any bound owned parent`,
including the hook's `session_id`, `event` and `invocation`.

Passive receipts publish atomically from `receipt.tmp` to `receipt.json`.
Live checks tolerate an absent or legacy empty/truncated in-flight receipt;
other integrity errors remain fatal. Sealed inventory still rejects interrupted,
incomplete, corrupt or foreign recordings.

After the selected execution path, run on the development host. For a full run, include
every current-attempt phase root in `--captures`, including
`/home/horde/ad-test/q-KEY-a1-setup`, so the sealed summary retains setup evidence.
Adapt the example to include only existing capture directories, omitting every
unrun dependent phase. Keep nonpass summaries, evidence and all selected gaps
under the [sealed continuation gate](#sealed-continuation-gate); provider refusal
remains subject to its separate attempt-bound policy.

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-summary --ledger /home/horde/ad-test/q-KEY-ledger.json --captures /home/horde/ad-test/q-KEY-a1-typed,/home/horde/ad-test/q-KEY-a1-forces,/home/horde/ad-test/q-KEY-a1-dialogs,/home/horde/ad-test/q-KEY-a1-groups,/home/horde/ad-test/q-KEY-a1-text,/home/horde/ad-test/q-KEY-a1-esc --out /home/horde/ad-test/q-KEY-a1-summary'"
mkdir -p -m 700 /home/horde/projects/agent-director-project/.scratch/b.kii/e8/live/KEY/a1
```

For shakedown collection, retain the standalone-install evidence:

```sh
scp -r -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test:/home/horde/ad-test/q-KEY-a1-summary ad-test:/home/horde/ad-test/q-KEY-ledger.json ad-test:/home/horde/ad-test/ci-install-KEY /home/horde/projects/agent-director-project/.scratch/b.kii/e8/live/KEY/a1/
```

For full-run collection, setup evidence is in the sealed summary; the skipped
standalone install creates no `ci-install-KEY` directory. Copy only:

```sh
scp -r -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test:/home/horde/ad-test/q-KEY-a1-summary ad-test:/home/horde/ad-test/q-KEY-ledger.json /home/horde/projects/agent-director-project/.scratch/b.kii/e8/live/KEY/a1/
```

`--out` names an absent directory, not a JSON file. The export contains
`summary.json`, `report.txt`, `index.json` and sealed evidence under capture IDs,
including imports/prior exports; it does not depend on an old VM path. If a
phase or summary stopped early, fetch each existing phase directory directly
with the same `scp -r` pattern. Preserve failures and unfinished raw evidence.

The report starts `PASS|FAIL|NOT_COMPLETED selected=… full=… b3=…`; JSON retains
`status`, `selected_status`, `full_status`, `b3_measurement_status`, `identity`,
`checks` (expected/observed/evidence), `coverage`, `named_premises`, `force_states`,
`route_bounds`, `timings`, `gaps`, `b3_gaps`, `cleanup`, `ledger`, `accounting` and history.
`b3_gaps` retains incomplete phase/check Results, unrun dependent measurements,
coverage-only obligations under `coverage/<id>` and incomplete children nested
under failed combined checks, sorted by phase/check. Nested entries are deduplicated
with inherited evidence; exported references retain capture prefixes. Coverage
entries retain their kind/source, so a missing R-a guard stays a premise-guard
gap. Gap export never downgrades a failed aggregate; measured timing gaps remain
separate.
The short text report lists reasons; use JSON for per-check facts and paths.
Selected shakedown completion does not mean full qualification. Deferred
setup/lifecycle/user-hooks/uncovered remain visible as incomplete. b3 readiness
requires every selected measurement, all typed pairs, both independent Esc
recoveries, Notification payload or bounded absence, interrupted-input placement
and exact wake-order/R-a guard references.

Check statuses are `pass`, `fail`, `not_completed`; `checks` displays evidenced
accepted-limit outcomes as `accepted_limit`. Workload outcomes stay incomplete.
Only `lost_wake_delay`, `wait_turn_raced`,
`user_hook_after_sighting`, `duplicate_text`, `unrecoverable_escaping`,
`handed_over_not_proven`, `esc_gap` and `never_sighted_hold` are accepted-limit
codes. They introduce no new public Result class. Missing proof blocks full
qualification, while failures remain alongside incomplete checks. Exit 0 from
phase/analyze/summary requires its applicable aggregate pass; the file-only
continuation command has the narrower exit rule above. Exit 1 denotes
nonpass or operational/evidence/output error; invalid invocation exits 2.

Timings retain send, return, physical issue, written completion, sighting,
transactional order, first eligible whole intake and first assistant text with
provider/Launch/request and original precision. First text is not semantic
acknowledgment, obedience or delivery confirmation; Claude has no
`delivered_confirmed` timing. Keep S1→S2 (G0's mid-turn contexts), O1→O2
(consecutive ordinary Stops) and F3→F4 (one force call) separate, including both
whole next inputs and completion/sighting/intake gaps. Never replace one pair
with another or infer order from the maximum of wall timestamps.

Offline scheduler analysis uses the same finalizer:

```sh
/opt/claude-live-kit/bin/claude-live qualification-analyze --evidence /opt/capture
```

Harness defects return to z9; product/expected-behavior/O4 changes go to the MI.
For an explicitly authorized affected-phase retry, first preserve the sealed
summary/raw evidence, then on the VM through the same `ad-shell` invocation:

```sh
/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live qualification-rerun --ledger /home/horde/ad-test/q-KEY-ledger.json --attempt a2 --allowance MI_ALLOWANCE --phase groups --dialog-cases D1,D2,D3,D4,D5,D6 --previous /home/horde/ad-test/q-KEY-a1-summary/summary.json
```

Use new `q-KEY-a2-*` paths. Retain the same compatible kit/candidate/product/
provider; changed identities cannot be silently imported. A groups-only retry
imports F3/F4 from a retained compatible, passing force capture; it never
silently runs force-states. Text/limits
needs the same O1/O2 small-drain and cap facts, without another force/drain.
Missing or nonpassing prerequisites stop before spending turns. Summary takes only
current-attempt roots plus `--previous` pointing to the sealed old summary. A
selected phase missing a current capture is incomplete; old results and charges
remain history. Explicit allowance still cannot exceed cumulative hard ceilings.
This is separate from [signal fixture requalification](#deliberate-requalification).

## Run once and collect

After a passing installation, the VM worker invokes one probe:

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live probe --kit /home/horde/ad-test/ci-kit-KEY/kit --home /home/horde/ad-test/home --evidence /home/horde/ad-test/ci-signals-KEY'"
```

Each phase has nine minutes total, including 90 s of cleanup (and final
collection). The probe uses two owned Launches with plan
`wake-sighting-v1` and sibling variant `held-steer-v1`:

| Launch | Scheduled/reserved turns | Exercise |
|---|---:|---|
| Primary | 4 | Startup; forced wake; later Plain Wait Turn wake; Reserved wake last. STEER and Plain are accepted while the earlier written wake is unsighted and STEER remains Held. |
| Continuation | 2 | Startup; forced wake. A one-shot sibling Stop hook continues with a real foreground tool while a later STEER remains Held, then a distinct post-sighting tool permits its one issue. |

Six turns are reserved. The hard cap is seven across the **sum** of both
Launches' monotonic observed turn maxima, never their maximum or the last
sample alone. Continuation model requests and context injections are logged
separately; any actual extra turn counts. The spare slot covers provider
scheduling, not another attempt, force or Launch. A missed unsighted/Held/tool
window is `not_completed` without retry. Never add manual prompts or sends.
Put an unsightable/Reserved wake last or in an isolated Launch: it holds every
later Message, including Steer Turn, until Launch End. This probe sends
nothing after its Reserved case.

The shared [stop and cleanup rules](#stop-and-cleanup-rules) apply to both
Launches and the scratch sibling continuation registration.

Fetch results even on a nonpass. After an install failure fetch only its
evidence directory. On the development host:

```sh
mkdir -m 700 /absolute/new-local-evidence
scp -r -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test:/home/horde/ad-test/ci-install-KEY ad-test:/home/horde/ad-test/ci-signals-KEY /absolute/new-local-evidence/
```

The [shared evidence records](#collect-summarize-and-rerun) also retain
`probe_plan`, both owned Launches and snapshot read-completion times for this
probe.

## Analyze and read the verdict

Both the live probe and offline CLI call `signals.Analyze`; its expectations
come from the contract/corpus and capability table, not from a claimed pass
in the manifest. In an offline scheduler container with the verified kit at
`/opt/claude-live-kit` and prepared evidence at `/opt/capture`, run:

```sh
/opt/claude-live-kit/bin/claude-live analyze --evidence /opt/capture --out /out/verdict.json
```

For an authorized analyzer repair, build the executable from that exact
source in the scheduler and record its identity alongside the original kit;
never relabel the captured candidate. `--out` must name a new file. The CLI
writes schema-1 JSON there and prints `signals.Format` diagnostics. `probe`
prints JSON and retains `verdict.json` in its evidence directory.

Each mandatory `results` entry has `signal_id`, `status`
(`pass`, `fail`, `not_completed`), `reason`, `expected`, `observed` and
`evidence` references. Exit 0 requires all mandatory results to pass; exit 1
covers fail/incomplete or evidence/output errors; invalid invocation exits 2.
Missing prerequisites or an unobserved overlap are incomplete. A missing or
changed signal after an independently completed exercise, or contrary observed
order, fails. An independent failure survives another incomplete result.
For example, a changed completed wake input produces a diagnostic such as:

```text
wake-stop-continuation: fail — whole wake input must precede continuation STEER input
```

`named_premises` is separate metadata (`id`, `reference`, `reason`), not a
result. It names `steer-wake-order` / R-a: a later wake never passes earlier
pending PostToolBatch context. AD's `input_unresolved` force refusal and
ordinary awaiting-take guard prevent that live race. It remains named and
unmeasured in JSON and printed output, even on early evidence errors; it has
no pass/fail status and cannot make an otherwise passing run incomplete.
Reachable P-c does not measure this direction.

Passive payloads corroborate fields. AD's own records prove sighting, order
and issue; exact whole transcript requests prove model uptake. Recorder
timestamps, requested tool behavior, assistant echoes and public
`sent_unconfirmed` Results are insufficient. These transcript reads are test
evidence only.

The continuation result's `observed.wake_input_form` identifies
`user-system-reminder` or `system-stop-continuation`. The latter is accepted
only after a verified Stop continuation, on the first eligible request after
sighting, matching the queued wake attachment's complete rendered text exactly
once with only the outer reminder removed. Generic/user intake is unchanged.
This is [observed Claude Code 2.1.311 behavior relied on by P-b](signals/testdata/claude/passing/PROVIDER-BEHAVIOUR.md).

Current mandatory check IDs include `hook-completion-order`, which checks
written plus uniquely bound sighting and maps to qualification's
`wake-order-point`, and `wake-stop-continuation`, which checks the Held-steer/tool
continuation. The former's name does not make completion order a provider premise.
Separately, `wake-stop-no-tools` / P-a is retired; `steer-wake-order` / R-a is
named, unmeasured metadata rather than a mandatory live result.

## Deliberate requalification

1. Preserve the failed/new-version run, raw originals and all identities.
   Diagnose the named contract against independent exercise completion.
   A changed version label alone is not failure; a changed provider form must
   fail, not silently pass. Send product/contract/O4 changes to the MI before
   accepting a new expectation. Replacing the baseline cannot qualify changed
   behavior, and a failed run grants no automatic rerun.
2. Obtain an authorized, genuinely passing post-wake-order capture with a fresh
   kit. Preserve the four-part [Held witness](../../docs/architecture.md#claude-signal-qualification),
   both Launch/session identities, accepted overlap, issue/written/sighting/order
   facts, exact request chronology, controlled bytes and original timing
   precision. Record provider, candidate, kit, source and any authorized offline
   analyzer drift truthfully.
3. Build an allowlisted scrubbed copy only. Replace private IDs/paths
   consistently without breaking joins; retain full relevant request messages,
   output configuration, rendering and causal hook/tool records. Keep originals
   and the private mapping outside git. Exclude original `raw/auth-status.json`
   before reading, hashing or copying inputs; create `{}` plus newline at that
   path in the copy and recompute its index. Recompute changed artifact/receipt
   digests and canonical journal hashes/references after scrubbing.
4. Keep one immutable positive baseline in `signals/testdata/claude/passing/`,
   including `provenance.json` and `PROVIDER-BEHAVIOUR.md`. Use the
   [shared fixture loader](../../docs/architecture.md#claude-signal-capture-reusable-test-fixture)
   for analyzer and exact CLI tests; mutate only temporary copies. Historical
   captures 1–5 retain their current replay verdicts, not passing-baseline status.
   Constructed or synthetic data never substitutes for a live positive.
5. On the scheduler, run the callable analyzer and exact CLI, plus the positive
   and independent per-signal negative tests. P-b and P-c need separate
   reversals preserving valid identities, hashes, overlap and issue chronology;
   an integrity error or missing exercise is not their intended negative.
   Retain the independent continuation/witness/form controls. From the frozen
   source at `/work`, the targeted test commands are:

   ```sh
   go test -race -count=1 -run '^TestClaudeLive' ./test/claude-live/signals
   go test -race -count=1 -run '^TestClaudeLive' ./test/claude-live/cmd/claude-live
   go test -race -count=1 -run '^TestClaudeLiveProbeScheduling$' ./test/claude-live/runner
   ```

Keep the per-signal verdicts, job IDs and source correspondence with the
qualification report. Baseline replacement, synthetic positives, silent
restamping and softened expectations do not establish qualification.
