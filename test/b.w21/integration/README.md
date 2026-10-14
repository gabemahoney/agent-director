# b.w21 Docker integration scheduler handoff

Execution is reserved to the User. No image build, integration run or scheduler
submission has occurred here. Every job in `manifest.json` is expected to PASS
and currently has observed result `NOT_RUN`. This is 14 canonical EPICs from
`test/docker-epics.txt` plus the separate `make test-docker-install-mode` suite:
15 executions, not a fifteenth EPIC.

The runtime source is pinned to commit
`9d025f8f2caf176f3a60421f52ba94b293021bdb`. The generator uses `git archive` at
that commit; ignored/untracked files cannot enter it. Its explicit source
selection includes all product, test, skill, tool and documentation directories
and `tickets/testplans`, excluding `.claude`, `.git`, unrelated ticket hives and
the large TLA contexts. Frozen model contexts and receipts are untouched.
`source-files.json` records every archived file's SHA256, size and mode.

## Submit the prepared jobs

Run these commands from the worktree root:

```sh
cd /home/horde/projects/agent-director-project/agent-director-w21-tla
python3 test/b.w21/integration/verify.py
```

Then run the 15 exact submission lines in `scheduler-commands.txt`, individually,
or submit that file as a batch:

```sh
bash test/b.w21/integration/scheduler-commands.txt
```

Only the User runs those submissions. Record each printed job ID against its
job name/slug. The batch contains independent commands and does not retry or
stop subsequent submissions if one fails; inspect all 15 submission results.
The prepared shared context is
`dist/b.w21/integration/context-9d025f8f`. Keep **every file in that context
immutable until all 15 jobs finish**: the scheduler reads it again after queueing.
Do not regenerate it, edit it or delete it while queued. The generator refuses
to overwrite an existing context, even if its contents would be unchanged.

The manifest pins every context file and its aggregate payload hash. The static
verifier reconstructs the archive and Dockerfile from the pinned Git tree and
reviewed templates, compares all bytes, rejects unexpected/non-regular entries,
and checks Python/Bash syntax. It never builds, executes repository binaries,
runs suites, contacts a model or submits jobs. To regenerate after moving this
handoff to another checkout, retain the pinned source commit and run
`python3 test/b.w21/integration/prepare.py` only when its destination does not
exist. Different revised contexts and manifests must use new paths via
`--context` and `--handoff-dir`, and get reviewed before submission; there is
no force-overwrite option. Those options also apply to `verify.py`.

## How the scheduler image follows the canonical harness

The Dockerfile retains the canonical `test/Dockerfile` prefix through its pinned
previous-release asset checks. That includes Claude Code 2.1.120, Go 1.22.10 with
both architecture checksums, Bun 1.3.13, and v0.11.1's install script and both
binary assets with the original checksums. Debian bookworm, Node 20 setup and
GitHub CLI repositories retain their canonical network behavior. Product source
is never downloaded from mutable HEAD. These external repositories and the
base-image tag remain mutable dependencies, so static checks do not establish
that the image will build.

`Dockerfile.tail` adapts the host-built binary copies into an in-container
`make build`, using the explicit pinned `COMMIT_SHA` and canonical dev version
`0.0.0-dev` for **both** product binaries. It puts source at `/work/source`,
testplans at `/work/tickets/testplans`, the main binary on PATH, the admin
binary at `/opt/bin/agent-director-admin`, and the original driver, skills and
install-mode suite at their canonical `/opt` paths. Source and testplans are
root-owned and not writable by tester, as in the canonical read-only
mounts; `/opt/driver` and `/opt/skills` retain the canonical tester ownership and
permissions. No host paths, binaries, credentials, Git pointers or caches are
needed inside the image. No Docker socket, nested engine, privileged option or
guard bypass is added.

The original `USER tester`, `HOME=/home/tester` and
`AGENT_DIRECTOR_TEST_HARNESS=1` remain. Python 3 is explicitly installed because
the existing `bootstrap-7-installsh-arch-probe` testplan requires it, and the
receipt checker also uses it. The selected shell scripts require no source
Git history; the previous release is installed from the canonical baked assets.
An archive has no source-tree Git HEAD, so install.sh's Git-checkout-specific
stamp check does not apply; the handoff independently verifies source checksums
and both binary stamps before running the suite.

Every submitted EPIC sets `DRIVER_MODE=shell`, matching canonical CI, and the
wrapper calls the actual `/opt/driver/run-testplan.sh`. The final job calls
the original `/opt/install-mode/run.sh` entrypoint. None requires an API/model
credential. The wrapper refuses credentialed driver environment variables.
Jobs use network on to preserve canonical build/runtime behavior, 8 GiB memory,
4 CPUs and a 1-hour runtime limit; the scheduler's separate build limit is
30 minutes. The scheduler host must be Linux amd64: the existing preflight
case explicitly asserts `Linux/x86_64`. Image building and runtime capability
remain unverified until the User returns scheduler results.

## Required receipts and non-empty success

Return, for each of the 15 jobs:

- Slug, scheduler job ID and final scheduler state/exit code (including build
  failures or timeouts).
- Complete build and run logs, with the emitted `source_identity` JSON showing
  the pinned commit, both binary stamps and `context_payload_sha256`.
- The driver's ordered case JSON and its summary, or the install-mode PASS/FAIL
  assertions and summary; preserve failure details rather than only a verdict.
- `HANDOFF-RECEIPT`, `VERDICT` and `JOB-VERDICT` lines. Also return the scheduler's
  `result.json` if available. Missing wrapper output, build failure, timeout or
  a generic exit zero without the required cases is not a passing receipt.

The wrapper rejects a nominal zero exit unless each EPIC reports exactly its
pinned ordered case IDs, all pass statuses and one matching summary. There are
152 EPIC cases in total. The install-mode receipt must include all 22 expected
assertions in order, a `22 passed, 0 failed` summary, and no FAIL lines; the
suite covers five scenarios. The manifest lists every expected case ID and
assertion name, so an empty or partial success cannot satisfy this handoff.

| Job slug | Expected cases/assertions |
| --- | ---: |
| epic-03-spawn | 22 |
| epic-04-interact | 11 |
| epic-05-stop | 15 |
| epic-06-discover | 12 |
| epic-07-templates | 8 |
| epic-08-recovery | 8 |
| epic-09-resume | 11 |
| epic-10-relay | 8 |
| epic-11-mcp | 9 |
| epic-12-install | 13 |
| epic-13-agents | 24 |
| harness-smoke | 5 |
| installsh-preflight-gates | 2 |
| template-overwrite | 4 |
| test-docker-install-mode | 22 assertions / 5 scenarios |

Current integration result: `could not run: execution reserved to user scheduler; 15 jobs prepared`.
