# Self-hosted darwin/arm64 GitHub Actions runner — setup runbook

This runbook is the operator-facing procedure for bringing the
`darwin-arm64` leg of `.github/workflows/cabi-matrix.yml` online. The
audience is an operator with no prior self-hosted-runner experience.
The goal is the matrix leg going green on every commit, with no
secrets leaking to the runner and the public-repo attack surface
explicitly bounded.

The `cabi-matrix.yml` workflow targets one self-hosted runner via the
full label tuple `[self-hosted, macOS, ARM64, darwin-arm64]`. The
`darwin-arm64` label is the workflow-side handle — without it the
workflow cannot route to your machine, and registering any other
self-hosted runner without that label cannot accidentally pick up the
job. Hardware target: Apple Silicon Mac (M1 / M2 / M3 / M4).

Estimated wall-clock for a first-time operator: ~25 minutes once the
prerequisites are met.

## 0. Prerequisites

Before downloading the runner, confirm the host has a working C
toolchain. CGO inside the workflow needs `clang`; `actions/setup-go`
installs the Go toolchain but does not install the C toolchain.

Run these three commands on the Mac. All three must succeed before
proceeding:

```sh
xcode-select -p           # prints an active developer directory path
clang --version           # prints a working compiler version
sw_vers                   # confirms macOS version
```

If `xcode-select -p` errors with "no developer tools were found",
install one of the following:

- **Full Xcode** (recommended). Download from the Mac App Store or
  <https://developer.apple.com/download>. Provides `xcodebuild`, the
  Simulator, and the full SDK set. Required if you want the workflow
  to record a precise Xcode version into `build-info-xcode.txt`.
- **Xcode Command Line Tools (CLT)** only. Install with
  `xcode-select --install`. Sufficient for this project's CGO needs
  (just `clang`), but `xcodebuild` is not present, so the workflow's
  capture step falls back to `clang --version`.

This project's `.github/workflows/cabi-matrix.yml` records the active
toolchain into a build-info artifact per leg (see Task `wz.i6`). On
the self-hosted leg the capture step uses `xcodebuild -version` when
available and falls back to `clang --version` when only CLT is
installed. Either choice keeps the leg green; full Xcode produces a
more precise toolchain attribution in release notes.

**Host requirements summary**

| Requirement | Why |
| --- | --- |
| Apple Silicon Mac (ARM64) | The leg is `darwin-arm64`; an Intel host cannot serve it. |
| macOS 13 (Ventura) or newer | Conservative floor — matches the historical darwin-amd64 leg's minimum (the leg itself was dropped 2026-05-24). |
| Xcode or Xcode Command Line Tools installed | CGO requires `clang`. |
| `xcode-select -p` resolves to a developer directory | Required for `clang` invocations. |
| Network egress to `github.com` and `objects.githubusercontent.com` | The runner polls GitHub for jobs. |

## 1. Download

Fetch the latest `actions-runner-osx-arm64-<version>.tar.gz` from the
[GitHub Actions runner releases page](https://github.com/actions/runner/releases).
Copy the published SHA-256 from the same release page — it appears
next to the tarball under "SHA-256 checksums".

The release page is the source of truth for `<version>` and the
checksum. The substituted commands below are the exact sequence to
run on the host, with placeholders the operator fills in.

```sh
# Pick a stable directory the launchd service will run from.
mkdir -p ~/actions-runner && cd ~/actions-runner

# 1. Download. Replace <version> with the value from the release page.
RUNNER_VERSION=<version>
curl -fsSL -O \
  "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-osx-arm64-${RUNNER_VERSION}.tar.gz"

# 2. Verify SHA-256 against the checksum copied from the release page.
#    Replace <sha256> with the published value. shasum returns "OK" on
#    a match and non-zero exit on a mismatch.
echo "<sha256>  actions-runner-osx-arm64-${RUNNER_VERSION}.tar.gz" \
  | shasum -a 256 -c -

# 3. Extract into the current directory.
tar xzf "./actions-runner-osx-arm64-${RUNNER_VERSION}.tar.gz"
```

**Do not skip step 2.** A mismatched SHA-256 indicates either the
download was corrupted in flight or the release page's tarball does
not match the published checksum — in either case, stop and
re-download from a known network. The verification is the only thing
catching a tampered-in-flight tarball before you execute it.

## 2. Register the runner

Registration binds this Mac to your repository and records the
labels the workflow targets. You will need a one-shot registration
token from the repository's Runners page.

1. Open <https://github.com/gabemahoney/agent-director> in a browser.
2. Navigate to **Settings → Actions → Runners**.
3. Click **New self-hosted runner**.
4. Select **macOS** + **ARM64**.
5. Copy the token shown in the **Configure** section's
   `./config.sh --token <TOKEN>` example. **Registration tokens are
   short-lived** (≈1 hour) — if you wait too long before running
   `./config.sh` the token will be rejected and you will need to
   regenerate.

Then on the Mac:

```sh
cd ~/actions-runner

# Replace <TOKEN> with the token copied above.
# Replace <HOSTNAME> with this Mac's hostname (e.g. "mini-m2").
./config.sh \
  --url https://github.com/gabemahoney/agent-director \
  --token <TOKEN> \
  --name <HOSTNAME>-darwin-arm64 \
  --labels self-hosted,macOS,ARM64,darwin-arm64 \
  --work _work \
  --unattended \
  --replace
```

Why each flag is there:

- `--url` points at this repository's runner registration endpoint.
- `--token` is the short-lived registration token from the Runners
  page. **It is not a PAT and is not reusable** — once `config.sh`
  consumes it, it is spent.
- `--name` is the identifier that appears in the Actions UI under
  Settings → Runners. The `<HOSTNAME>-darwin-arm64` pattern makes it
  obvious which physical machine is serving the job.
- `--labels` is the full four-label set. The first three
  (`self-hosted`, `macOS`, `ARM64`) are also auto-applied by the
  runner, but spelling them out here lets you confirm them in the
  Settings → Runners UI. The **fourth label, `darwin-arm64`, is the
  workflow-target label**. The `cabi-matrix.yml` workflow's
  `runs-on: [self-hosted, macOS, ARM64, darwin-arm64]` matches all
  four labels conjunctively, so omitting `darwin-arm64` means the
  job will never route to this runner (no error, just queued
  forever). Conversely, never apply `darwin-arm64` to a runner that
  is not the operator-owned Apple Silicon Mac for this project — the
  conjunctive label match is the only routing guard.
- `--work _work` is the default working folder for job checkouts.
- `--unattended` skips interactive confirmation prompts so the
  command is scriptable.
- `--replace` allows re-running registration on the same machine
  (e.g. after re-installing the OS) without manually un-registering
  first.

`config.sh` writes `.runner` and `.credentials` into
`~/actions-runner/` and prints "√ Settings Saved" on success. After
this step the runner exists but is not running.

## 3. Install as a launchd service

The runner ships a wrapper that installs it as a launchd-managed
LaunchAgent so it survives reboots and starts unattended.

```sh
cd ~/actions-runner

# Install the launchd plist under ~/Library/LaunchAgents/.
./svc.sh install

# Start the service immediately (it also auto-starts on next login).
./svc.sh start

# Verify state. Expect output like "actions.runner.<repo>.<name>" with
# a "Started" status.
./svc.sh status
```

**Logs.** The runner writes to:

- `~/Library/Logs/com.github.actions.runner.<owner>.<repo>.<name>/`

Each job run gets a fresh log file under that directory. Tail the
most recent one when debugging:

```sh
ls -t ~/Library/Logs/com.github.actions.runner.* | head -1 | xargs ls -t | head -3
```

**Unattended-start expectation.** Once `./svc.sh install` + `start`
has run, the launchd agent persists across reboots and across the
operator logging out and back in. No manual restart is required after
a power cycle. If a job is in flight when the Mac powers off, GitHub
re-queues the job for the next available runner with matching labels
once this runner re-registers on boot.

## 4. Security configuration (CRITICAL)

**Read this section before exposing the runner to the public repo.**
`agent-director` is a public repository on github.com. Attaching a
self-hosted runner to a public repo means that *any code in any pull
request from any contributor* can, by default, execute on your Mac
under the runner service account. That is the documented attack
surface. The settings below bound it.

### 4.1 Require approval for fork PR workflow runs

Workflows triggered by a pull request from a fork must require
explicit approval before they execute on this self-hosted runner.

1. Open <https://github.com/gabemahoney/agent-director> Settings →
   Actions → General.
2. Under **Fork pull request workflows from outside collaborators**,
   select **Require approval for all outside collaborators** (or
   stricter: **Require approval for first-time contributors who are
   new to GitHub**).
3. Under **Fork pull request workflows**, additionally enable
   **Require approval for all outside collaborators**.

GitHub's docs:
<https://docs.github.com/en/actions/managing-workflow-runs/approving-workflow-runs-from-public-forks>

The effect: a fork PR cannot dispatch a job to this runner until a
repository contributor manually approves the run from the Actions
UI. Without this setting, the default behaviour is to run first-time
contributor PRs after a one-time approval, which is too permissive
for a self-hosted-runner-attached public repo.

### 4.2 Allowlist actions and reusable workflows

Restrict which third-party actions may run inside any workflow on
this runner. The `cabi-matrix.yml` workflow uses `actions/checkout`
and `actions/setup-go`; nothing else from the broader marketplace
should be reachable.

1. Open Settings → Actions → General.
2. Under **Actions permissions**, select **Allow `<owner>`, and
   select non-`<owner>`, actions and reusable workflows**.
3. Under **Allow specified actions and reusable workflows**, enter
   the allowlist below verbatim:

   ```
   actions/checkout@*,
   actions/setup-go@*,
   ```

4. Click **Save**.

GitHub's docs:
<https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository#allowing-specific-actions-and-reusable-workflows-to-run>

If a future workflow change introduces a new third-party action,
expand the allowlist deliberately — the audit trail is a settings
change recorded against the repository.

### 4.3 Secret and environment-variable surface

The `cabi-matrix.yml` workflow does **not** read any repository
secrets. It uses the workflow's ephemeral `GITHUB_TOKEN` only for
artifact uploads (`actions/upload-artifact`'s implicit token); it
does not need an `ANTHROPIC_API_KEY`, a `CLAUDE_CODE_OAUTH_TOKEN`,
or any other secret to do its job. (`integration.yml` uses those
secrets, but it runs on GitHub-hosted runners, not on this
self-hosted machine.)

Do not grant the self-hosted runner access to operator credentials
beyond what the workflow needs. Specifically:

- Do not set Mac-side environment variables that name secrets the
  workflow would inherit (e.g. `ANTHROPIC_API_KEY` in your shell
  profile would leak into job environments).
- Do not run the launchd service as a privileged user. The default
  install (`./svc.sh install`) registers the service under your own
  user account, which is correct.
- Do not store SSH keys or git credentials on the runner that have
  push access to repositories beyond this one.

### 4.4 Residual risk acknowledgement

Even with sections 4.1, 4.2, and 4.3 in place, attaching a
self-hosted runner to a public repository retains a documented
attack surface. Read GitHub's own guidance before proceeding:

<https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/about-self-hosted-runners#self-hosted-runner-security>

The operator running this runbook accepts that residual risk by
enabling the architecture. If the project later moves the
`darwin-arm64` leg to a GitHub-hosted ARM macOS runner (currently
not available as a generally-hosted SKU at the time of writing),
this runbook becomes vestigial and the runner should be retired
following the Operations section below.

## 5. Verify end-to-end

After steps 0–4, the runner should be visible in the GitHub UI and
ready to pick up a workflow run.

1. **Confirm registration.** Open
   <https://github.com/gabemahoney/agent-director> Settings → Actions
   → Runners. The runner appears as
   `<HOSTNAME>-darwin-arm64` with status **Idle** and labels
   `self-hosted, macOS, ARM64, darwin-arm64`. If status is not
   **Idle**, return to step 3 and re-check `./svc.sh status`.

2. **Trigger a workflow run.** Either:
   - push a commit to a branch that opens a PR, or
   - push a commit to `main`, or
   - open the workflow file in the Actions UI and use
     `workflow_dispatch` if/when the workflow exposes one.

3. **Watch dispatch.** Open the Actions tab for the resulting run.
   The `cabi-matrix.yml` workflow lists two jobs: `linux-amd64` and
   `darwin-arm64`. The second job should reach the **In progress**
   state within ~30 seconds and the job sidebar should display:

   - **Runner**: `<HOSTNAME>-darwin-arm64`
   - **Labels**: `self-hosted`, `macOS`, `ARM64`, `darwin-arm64`

   If the `darwin-arm64` job stays **Queued** indefinitely while the
   other leg progresses, the most common causes are:
   - the runner is offline (`./svc.sh status` on the Mac);
   - the registered labels do not include `darwin-arm64` (Settings →
     Runners → click the runner → check labels);
   - the workflow is paused pending fork-PR approval (Section 4.1).

4. **Confirm green completion.** When the `darwin-arm64` job
   finishes, the **Artifacts** tab on the run summary lists two
   `pkg/cabi`-* artifacts (one per leg) — including the
   `darwin-arm64` artifact, which carries the
   `libagent_director.dylib`, `libagent_director.h`, and
   `build-info-xcode.txt` files produced on your Mac.

## 6. Operations

### Pause

```sh
cd ~/actions-runner && ./svc.sh stop
```

Stops the launchd agent. The Mac stays registered with GitHub and
appears as **Offline** in Settings → Runners until you restart with
`./svc.sh start`. Use this when you need to take the Mac offline
briefly (reboots are fine — launchd restarts automatically; this
flag is for deliberate maintenance windows).

### Uninstall

To fully retire the runner from this Mac:

```sh
cd ~/actions-runner

# 1. Stop and remove the launchd agent.
./svc.sh stop
./svc.sh uninstall

# 2. Un-register from GitHub. Obtain a REMOVAL token from
#    Settings → Actions → Runners → click the runner → Remove.
#    This is a different token from the registration token; both
#    are short-lived.
./config.sh remove --token <REMOVAL-TOKEN>
```

After `config.sh remove` succeeds the runner disappears from
Settings → Runners and the `~/actions-runner/.runner` and
`.credentials` files are deleted. The download tarball and
`~/actions-runner/` directory itself can then be removed manually
if you no longer need the binaries.

### Upgrade

GitHub publishes new runner releases periodically. To upgrade:

1. Stop the service: `./svc.sh stop`.
2. Re-download and verify the new tarball per Section 1.
3. Extract over `~/actions-runner/` (the runner tarball is designed
   to overlay-extract; `.runner` and `.credentials` are preserved).
4. Restart: `./svc.sh start`.

No re-registration is required for an in-place version upgrade — the
`.runner` / `.credentials` files carry the identity forward.

### Rotate the registration token

Registration tokens are short-lived; you generally do not need to
rotate them after the initial `config.sh`. However, if you suspect a
token was leaked while still valid:

1. Open Settings → Actions → Runners and click **New self-hosted
   runner**. This regenerates the registration token, invalidating
   any previously-issued one.
2. Re-run `./config.sh --replace` with the new token (Section 2).
   The `--replace` flag re-registers in place without going through
   `config.sh remove` first.

### Limit per-runner job concurrency

The `cabi-matrix.yml` workflow declares **workflow-level**
concurrency (`cabi-matrix-${{ github.ref }}` with
`cancel-in-progress: true`) which cancels superseded PR runs. That
setting does NOT bound the queue depth on the single self-hosted
Mac: each distinct main-branch commit still queues its own job, and
two simultaneous PR branches can produce two queued darwin/arm64
jobs back-to-back.

If you want to cap how many jobs run on this Mac at once
(particularly if you ever add a second self-hosted workflow), the
runner respects the `RUNNER_MAX_PARALLEL` configuration only at
registration time, OR you can run multiple runner instances and let
GitHub's matchmaker route them. For most setups the default of one
runner, one job at a time, is correct — back-to-back darwin/arm64
jobs serialize naturally without operator intervention.
