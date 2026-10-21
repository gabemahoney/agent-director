# b.w21 Docker integration results and preserved handoff

All **15 user-submitted jobs PASS** at source `9d025f8f`: 14 canonical EPICs
with 152 ordered cases, plus standalone install-mode with 22 assertions across
five scenarios. [integration-observed-receipts.json](integration-observed-receipts.json)
preserves the user receipt, all complete run logs and hashes, source identity,
`HANDOFF-RECEIPT`, `VERDICT` and `JOB-VERDICT` lines. Each run log confirms both
binary stamps, the pinned commit and context payload hash; all harness exit
codes are zero. Build logs and independent scheduler final-state/exit-code
receipts are not included in this archive.

The User reports actual submission with **`--network off`**, using network
only for image builds. Run logs do not independently attest that scheduler
setting. The original manifest and command file still say `--network on`;
they are preserved historical preparation artifacts, with observed `NOT_RUN`
unchanged there. Observations live in the separate archive. Do not resubmit
these integrations for the model-only round-2 correction: runtime is unchanged.

The runtime source is pinned to commit
`9d025f8f2caf176f3a60421f52ba94b293021bdb`. The generator uses `git archive` at
that commit; ignored/untracked files cannot enter it. Its explicit source
selection includes all product, test, skill, tool and documentation directories
and `tickets/testplans`, excluding `.claude`, `.git`, unrelated ticket hives and
the large TLA contexts. Original submitted model contexts remain untouched.
`source-files.json` records every archived file's SHA256, size and mode.

## Preserved submission handoff

All fifteen jobs have returned; no further submission is pending. Static
verification runs from the repo root of any clone that contains commit
`9d025f8f`. The shared context `dist/b.w21/integration/context-9d025f8f` is
untracked, so prepare it first; `prepare.py` runs only when that destination
does not exist:

```sh
python3 test/b.w21/integration/prepare.py
python3 test/b.w21/integration/verify.py
```

The rest of this section describes the original handoff. It submitted the 15
lines in `scheduler-commands.txt` individually or as one batch. That file is a
historical record, not a command to run: its `--context` is repo-relative, but
every submission line starts with the original host's `jobsched` path and
requests `--network on`. The User ran the submissions and recorded each job
ID against its slug. The batch's commands were independent, with no retry or
stop on failure. The scheduler reads the context again after queueing, so it
stayed immutable until all 15 jobs finished.

The manifest pins every context file and its aggregate payload hash. The static
verifier reconstructs the archive and Dockerfile from the pinned Git tree and
reviewed templates, compares all bytes, rejects unexpected/non-regular entries,
and checks Python/Bash syntax. It never builds, executes repository binaries,
runs suites, contacts a model or submits jobs. The generator refuses to
overwrite an existing context, even with unchanged contents; there is no
force-overwrite option. Revised contexts and manifests must use new paths via
`--context` and `--handoff-dir` (which `verify.py` also takes) and get reviewed
before submission.

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
The prepared commands requested network on, 8 GiB memory, 4 CPUs and a
1-hour runtime limit; the scheduler's separate build limit is 30 minutes.
The User actually submitted with network off, as recorded above. The scheduler
host must be Linux amd64: the existing preflight case explicitly asserts
`Linux/x86_64`. Returned run logs establish successful image execution and
the required cases;
this archive does not preserve the separate build logs.

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

## Observed results

All rows below have `HANDOFF-RECEIPT` verdict PASS, no errors and zero harness
exit; the exact ordered case IDs/assertion names match the frozen manifest.

| Job slug | Scheduler job | Observed count |
|---|---|---:|
| `epic-03-spawn` | `j20261014225817-11ebc5` | 22 |
| `epic-04-interact` | `j20261014225817-fd5e65` | 11 |
| `epic-05-stop` | `j20261014225817-9d0ceb` | 15 |
| `epic-06-discover` | `j20261014225817-ca9241` | 12 |
| `epic-07-templates` | `j20261014225818-e44612` | 8 |
| `epic-08-recovery` | `j20261014225818-920cb0` | 8 |
| `epic-09-resume` | `j20261014225818-71292a` | 11 |
| `epic-10-relay` | `j20261014225818-c76c21` | 8 |
| `epic-11-mcp` | `j20261014225818-880868` | 9 |
| `epic-12-install` | `j20261014225818-fa6f73` | 13 |
| `epic-13-agents` | `j20261014225818-ba41ae` | 24 |
| `harness-smoke` | `j20261014225819-15e19b` | 5 |
| `installsh-preflight-gates` | `j20261014225819-4af4e0` | 2 |
| `template-overwrite` | `j20261014225819-ebf575` | 4 |
| `test-docker-install-mode` | `j20261014225819-80fdfc` | 22 |

Current integration result: **PASS**. The five install-mode scenarios are the
frozen suite's scenario inventory; the run log independently lists its 22
passing assertions. The context remains byte-identical at payload SHA-256
`ee259f45e8ae667ae8f083b03128ebeb2d46526903aa9d72fa2b1165822a49fe`.
