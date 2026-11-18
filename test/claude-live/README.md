# Claude signal-change check

This explicitly invoked developer check qualifies the native signals and exact
model inputs used by AD's Claude busy-send route. It is not a daemon or an
automatic version gate, and introduces no product hook, transcript reader or
delivery proof. The [signal dependencies and order witnesses](../../docs/architecture.md#claude-signal-qualification)
explain what a changed signal breaks. Broader qualification, measured limits
and release decisions belong to the qualification runbook and release work.

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
capture. The worker uses the existing test login; it never signs in, exports
credentials or answers an unexpected Dialog/menu, including “always allow”.
An expired login or missing tool stops the phase as `not_completed`.
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
The operator must exclude all old writers before asserting the maintenance
window. The runner refuses live/uncheckable old agents, relocated stores or
unexpected layouts; stop and report a refusal rather than repairing it.

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

## Run once and collect

After a passing installation, the VM worker invokes one probe:

```sh
ssh -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test "/home/horde/ad-test/bin/ad-shell -c '/home/horde/ad-test/ci-kit-KEY/kit/bin/claude-live probe --kit /home/horde/ad-test/ci-kit-KEY/kit --home /home/horde/ad-test/home --evidence /home/horde/ad-test/ci-signals-KEY'"
```

Each phase has a nine-minute deadline, reserving its final 90 seconds for
collection and cleanup. The probe uses two owned Launches with plan
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

Cleanup stops only recorded owned agents/sessions and verifies their process
start identities have exited. It removes only this run's passive recorder
entries, scratch sibling continuation registration and private runtime
directory, preserving unrelated hooks/settings. Unverified exit leaves the
runtime for inspection. Report leftovers; do not remove them by hand. Leave
the candidate, backups and evidence in place.

Fetch results even on a nonpass. After an install failure fetch only its
evidence directory. On the development host:

```sh
mkdir -m 700 /absolute/new-local-evidence
scp -r -F /home/horde/projects/agent-director-project/.ad-test-ssh/config -o RemoteCommand=none -o RequestTTY=no ad-test:/home/horde/ad-test/ci-install-KEY ad-test:/home/horde/ad-test/ci-signals-KEY /absolute/new-local-evidence/
```

`manifest.json` binds candidate/kit/provider identities, phase deadline,
`probe_plan`, turn counts, both `owned` Launches, cases, hook ownership,
problems and artifact hashes. `commands.jsonl` retains argv, start/end, exit,
timeout and output references. Indexed `raw/` evidence holds scoped read-only
store snapshots (including read completion), passive hook inputs/receipts,
AD trail, exact transcript requests and cleanup evidence. Do not export
settings, process environments, credentials or backups. Treat transcripts and
raw evidence as private until scrubbed; do not open `raw/auth-status.json`.

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

For historical replay only: `hook-completion-order` now checks written plus
uniquely bound sighting; `wake-stop-continuation` checks the Held-steer/tool
history. `wake-stop-no-tools` / P-a is retired, and `steer-wake-order` moved to
named metadata. Neither P-a nor completion order is a current provider premise.

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
