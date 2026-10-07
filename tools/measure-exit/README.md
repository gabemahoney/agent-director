# measure-exit: Claude Code exit-time measurement

An operator tool that measures how long real Claude Code takes to exit under
agent-director. Its results feed SRD Open Questions RN-6, RN-2 and RN-9.
Applying them is a separate change.

- **RN-6:** time from a pane kill until the agent process is gone. Sets
  `kill_exit_wait_ms`.
- **RN-2:** time from the stored `ended_at` until the session no longer
  lists. Informs `stopping_window_seconds` and the stopping window's
  minimum.
- **RN-9:** whether hooks apply as designed under an in-session `/resume`
  and agent teams, plus the oldest Claude Code that runs exec-form hooks.
  RN-7's payload-key table is recorded beside it for the record.

There are three runs, each a separate `make measure-exit` call:

| Run | `MEASURE_MODE` | What runs | Sessions | Time |
|---|---|---|---|---|
| L0 | `probe` | The exec-form version probe. No credential, `--network none`. | One idle start per probed version, about 5 to 11 | 15–25 min, mostly image builds |
| L1 | `measure` | RN-6 and RN-2 | 22 samples per case: 176 agents with vanilla settings, or 264 with `--mcp-config`. 66 are prompted. One at a time. | 1–2 h |
| L2 | `rn9` | RN-9, with RN-7's record | 5 agents plus the teammates of the two team leads, about 9 | 20–45 min |

## Before any live run

**L1 and L2 spend money.** They start real, paid Claude Code agents. L0 makes
no API call.

- **Approval.** No live run starts without explicit approval. The project
  owner approves the spend. First send a plan that names:
  - the billing: the configured gateway only;
  - the session count, from the print-only output;
  - the estimated cost. Opus-class list prices come to roughly $10 each for
    L1 and L2. Gateway billing may differ.
  - the location: a Docker container on a named host.
- **Who launches.** The operator launches L0, L1 and L2 from their own
  shell, which holds the real gateway values, once the spend is
  approved. A launch from inside a Claude Code session needs
  `MX_ORCHESTRATOR_LAUNCH=1` (see [Live runs](#live-runs)). Nobody else
  runs `make measure-image` or `make measure-exit`: both targets chain
  through `make test-image` to the host `make build`, a Go build on the
  host.
- **Docker only.** Agents run only inside a throwaway container with its own
  HOME, its own store and a private tmux server: a private `TMUX_TMPDIR`,
  with `TMUX` unset. Nothing touches the host's `~/.agent-director`,
  `~/.claude.json` or tmux server. Besides spawning its own agents, the
  harness makes only the measured exits and pane kills, the RN-9 drive's
  sends to its own agents, and read-only calls (`get`, tmux `list-*` and
  `capture-pane`).
  Nothing built runs on the host. The
  driver refuses to start outside the container.
- **The release-candidate tree.** Run from the release-candidate tree or
  later.

## Credentials

The runner forwards exactly four variables, **by name only** (`-e NAME`).
Their values are never printed, logged, written to a file or mounted:

- `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN`: the gateway the run bills
  to;
- `ANTHROPIC_MODEL`: the model pinned for the run, the Opus id that
  production agents use;
- `ANTHROPIC_SMALL_FAST_MODEL`: the background model.

Export them in your own shell. `make measure-exit` and `run.sh --run` refuse
to start, before building anything, unless all four are set. The driver also
refuses real mode without `ANTHROPIC_MODEL`.

The run bills to the gateway only. Real mode refuses, with rule
`real-gateway-only` and before anything is written, when any of these is set,
even empty: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`,
`CLAUDE_CODE_USE_BEDROCK`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
`AWS_SESSION_TOKEN`, `AWS_BEARER_TOKEN_BEDROCK`, `AWS_REGION`, `AWS_PROFILE`
or `AWS_DEFAULT_REGION`. The refusal names the variable, never its value.
These variables are also dropped from every child's environment.

The same rule covers the settings and MCP layer files the agents load. The
driver's preflight reads the managed layer
(`/etc/claude-code/managed-settings.json`), the user layer
(`$HOME/.claude/settings.json`) and any project, local or MCP layer it is
given. It refuses real mode with `real-gateway-only` when one of them
would be refused by the runner (see
[Settings layers and MCP servers](#settings-layers-and-mcp-servers)): it
is, or links to, a `.claude.json` or a `.credentials.json`, it cannot be
read or is not JSON, a key in it looks like a credential or is a
credential-producing setting, or an `env` object in it breaks the layer
env rule. A missing file is skipped, but a dangling link is refused,
because its target could appear later in the run. The runner never stages
a dangling link. The refusal names the layer, its path and the key, never
a value. Dry and probe mode do not run this check.

Real mode also refuses, with `real-gateway-only`, a container that has
anything at one of three paths: a file, a directory or a link, dangling or
not.

- `$HOME/.claude/.credentials.json`, a Claude Code login.
- `/etc/claude-code/managed-mcp.json`, Claude Code's managed MCP file.
- `/etc/claude-code/managed-settings.d`, Claude Code's managed settings
  drop-in directory. It is refused even when empty or unreadable.

A path it cannot check is refused too. Nothing at these paths is opened or
listed. The refusal names the file or directory and its path, and why it is
refused (or, for a path it cannot check, the OS error), never its content.
The runner never mounts any of these paths, and its image creates only
`/etc/claude-code` itself, so only a container started by hand meets this.

The managed MCP file and the drop-in directory are refused outright, not
checked as layers, because nothing legitimate puts them there:

- The runner has no way to stage the managed MCP file, and a deployment's
  MCP servers come through `--mcp-config`. While that file is present,
  Claude Code refuses `--mcp-config` servers, so the MCP cases could not
  run as measured.
- Claude Code merges every `*.json` file in the drop-in directory into the
  managed settings, which override every other settings level. A drop-in
  could set `env` or `apiKeyHelper` past the gateway. A deployment's
  managed settings come through the managed layer
  (`/etc/claude-code/managed-settings.json`), which is checked as above.

Dry and probe mode do not run this check.

L1 and L2 bill only to the InferenceHub gateway: `ANTHROPIC_BASE_URL` and
`ANTHROPIC_AUTH_TOKEN` point at it, and the runner forwards them by name. No
run uses a seat login or credentials from `~/.claude`. The runner never
forwards `ANTHROPIC_API_KEY`, `TMUX`, `CLAUDE_CONFIG_DIR`,
`CLAUDECODE` or any other host Claude Code session variable. L0 forwards
nothing: its container has no network and gets a dummy token literal that is
not a secret.

## Claude Code version

L1 and L2 measure Claude Code **2.1.280** by default, the deployed version.
`MEASURE_CLAUDE_CODE_VERSION` sets the version. It becomes the
Dockerfile's `CLAUDE_CODE_VERSION` build argument and the image tag
`agent-director-measure:cc-<version>`. Match it to the deployment. The
driver's real mode refuses anything older than 2.1.280.

L0 probes the deployed version first (`--deployed`, default 2.1.280), then
bisects between 2.1.120 (which ignores exec-form `args`) and 2.1.285. Its
verdict for the deployed version is the `deployed` line of
`probe-summary.txt` and `deployed-verdict.txt`:

- `deployed 2.1.280: RUNS exec-form hooks`: L1 and L2 may run at 2.1.280.
- `deployed 2.1.280: does NOT run exec-form hooks`: **STOP and tell the user
  at once.** agent-director's hooks are ignored by the deployed version, and
  L1 and L2 must not run at that version. L0 ends right after the guard
  verify, with no bisection and no further build. `probe-summary.txt` gets
  `bisect skipped: the deployed 2.1.280 does NOT run exec-form hooks (STOP)`,
  and `run.sh` exits 3.
- `UNDECIDED`: `run.sh` exits 1. Find out why before going on.

**Candidate versions.** By default the bisection's candidates come from
npm's listing. `run.sh` fetches it with `npm view @anthropic-ai/claude-code
versions --json` in a credential-free container of the base image, on the
host network, the same network the image builds use. Print-only shows it as
step 2b. If the listing fails or lists nothing, the bisection stops with
"pin the candidates with --versions". The deployed version has been probed
by then. To skip npm, pin the candidates with `--versions "V ..."` or
`--versions-file F`. Each entry must be `X.Y.Z`, or the runner exits 2.
Both options are for probe mode only. Print-only then shows the pinned list,
sorted.

**Native binary.** The measured image must hold Claude Code's native binary.
A JS launcher left by a failed postinstall would make every hook a
`pid_mismatch`. The Dockerfile's `REQUIRE_NATIVE_CLAUDE` build argument
controls the check, which runs after `npm install`. `claude` must resolve to
the package's `bin/claude.exe`, which must be an ELF file over 1 MiB. The
image records `native` or `not native: <why>` in
`/opt/measure-exit/claude-launcher.txt`. `make measure-image` passes
`REQUIRE_NATIVE_CLAUDE=1`, so a non-native install fails the build and says
why. The probe's per-version builds pass `0`, because older versions ship
JS and the probe reads only hook `args`.

A Claude Code that does not run exec-form hooks writes `no_exec_form` to the
trail, and its agents never report in. The fix is to upgrade Claude Code. The
main README's [Prerequisites](../../README.md#prerequisites) state the
supported minimum.

## Host guard

`guard.sh` wraps every live run. It takes a snapshot before the container
starts and verifies afterwards, **even when the container run fails**. It
reads only `state.db` and `ad-trail.jsonl` under the real home. That home
comes from the passwd entry, not `$HOME`: the host's own agent-director state
lives under the home your `HOME` normally points to, which is the passwd-entry
home, and a redirected `HOME` cannot point the guard elsewhere. It never
opens the database and never runs agent-director or tmux.

- **Busy-host mode** (default, `--guard-mode busy`). Use it on a host whose
  own agent-director sessions keep writing the store. Verify scans only the
  bytes appended to the trail since the snapshot, looking for this run's
  identifiers (its store id, instance ids, private socket and run id). It
  prints only the identifiers it found and their counts. `state.db` shows as
  `not checked on a busy host`. A trail that shrank or was replaced fails.
- **Quiet-host mode** (`--guard-mode quiet`). Use it on a dedicated host
  where agent-director is installed but idle: no session, hook or scheduled
  job writes the store during the run. The snapshot takes two readings a few
  seconds apart and refuses (exit 3) if the files already differ, so it
  confirms that the host is quiet. Verify prints each file's SHA-256 before
  and after, side by side, and fails on any change.

A pass ends with `guard passed`. The runner writes `pass` or `fail <status>`
to `guard-status.txt`. **On `GUARD FAILED`:** treat it as a possible
isolation break. Do not apply the results, and report it.

## Settings layers and MCP servers

By default no deployment layer is staged, and the results are labelled
"vanilla settings". To measure under a deployment's settings, pass copies:

```bash
make measure-exit-print MEASURE_MODE=measure MEASURE_ARGS="--user-settings /path/settings.json --managed-settings /path/managed-settings.json --project-settings /path/project.json --mcp-config /path/mcp.json"
```

- Each copy is staged and mounted read-only. The originals and the copies are
  never edited. A missing file, a dangling link included, is reported and
  not staged.
- A layer that carries credentials is refused with exit 2, in print-only
  too, so you see it before launching. The refusal names the key or file,
  never a value. A layer is refused when:
  - it is, or links to, a `.claude.json` or a `.credentials.json`;
  - it is not JSON, or `jq` is missing;
  - any key anywhere in it looks like a credential (`KEY`, `TOKEN`,
    `SECRET`, `PASSWORD`, `PASSWD`, `CREDENTIAL`, `OAUTH`, `AUTHORIZATION`,
    `COOKIE`). That covers `env` blocks, MCP server `env` and `headers`, and
    helpers such as `apiKeyHelper`;
  - any key anywhere in it is a credential-producing setting: its name is,
    in any case, `awsAuthRefresh`, `gcpAuthRefresh`, `otelHeadersHelper`,
    `policyHelper` or `headersHelper` (an MCP server's). Each runs a command
    whose output Claude Code uses as a credential, request headers or
    settings, which no check can see;
  - an `env` object at any depth (the settings `env`, an MCP server's
    `env`) breaks the **layer env rule**: it sets, in any case,
    `ANTHROPIC_BASE_URL`, `ANTHROPIC_CUSTOM_HEADERS`, any `CLAUDE_CODE_USE_*`
    name or any name real mode refuses in the environment (see
    [Credentials](#credentials)), or it sets any name to a value that
    looks like a URL or an authorization header (`://`, `Bearer `,
    `Authorization:`). Any of these would take the agents off the gateway.
    The value test runs inside `jq`, so values never reach the shell.
    `run.sh`'s `LAYER_REFUSED_ENV` list and the driver's `layerenv.go`
    hold the same names and must stay in step with `realModeRefusedEnv`.
- In real mode the driver repeats all of these refusals in its preflight,
  except the missing-`jq` one, since it does not use `jq` (see
  [Credentials](#credentials)). A container started by hand is held to
  them too.
- The driver reports every hook program missing from the container.
- `--local-settings` is refused in real mode when a selected case needs the
  harness's generated layer.
- `~/.claude.json` is never copied. MCP servers come only through
  `--mcp-config`. Without it, the MCP cases (`rn6.mcp*`, `rn2.mcp`) read
  "not run".
- **Warnings:** each MCP case really starts the supplied servers, about 22
  times per case, so they may contact their own services. An MCP
  configuration may hold secrets, and its copy is mounted into the
  container.

## Budgets

Cases without a suffix run under Claude Code's default SessionEnd budget.
Each RN-6 case has two raised-budget variants. Both use a generated
`.claude/settings.local.json` in the agent's own working directory, holding
one slow SessionEnd hook (`-slow-hook-ms`):

- `.raised-hook`: a per-hook `timeout` (`-raised-hook-timeout`, seconds);
- `.raised-env`: `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS`
  (`-raised-env-timeout-ms`).

The slow hook must run longer than the default budget and less than both
raised budgets. The driver refuses values that break this. Raised-budget
times are reported, never used for a default. Each case lists the budgets in
force and the layer they came from.

## Review: print-only

The runner prints by default and executes nothing:

```bash
make measure-exit-print MEASURE_MODE=probe
make measure-exit-print MEASURE_MODE=measure MEASURE_ARGS="--run-id mx-l1-measure --host-network"
make measure-exit-print MEASURE_MODE=rn9 MEASURE_ARGS="--run-id mx-l2-rn9 --host-network"
```

Check:

- the `-e` list: the marker, `DISABLE_AUTOUPDATER` and the four names, with
  no `=value`;
- the mounts: only staged layer copies (read-only) and one results
  directory;
- the image tag, the `cases:` line and the session count;
- the guard snapshot and verify lines;
- `note: --run would refuse` when a variable is unset.

**Cases.** The driver refuses real mode without `-cases`, so the container
line always names its cases. Without `--cases`, the runner passes all of the
mode's ids: the 12 RN-6 and RN-2 ids for `measure`, MCP cases included, and
the 4 RN-9 scenarios for `rn9`. The MCP cases read "not run" without
`--mcp-config`. A `--cases` id that is not one of the mode's ids exits 2, so
L1 never runs L2's scenarios and L2 never runs L1's cases. `probe` takes no
`--cases`.

`--host-network` is for hosts where the bridge network breaks large TLS
transfers. Pass the same `--run-id` to the live run.

## Dry run

```bash
make measure-exit-dryrun
```

It runs in the repo's Docker sandbox with the stub `claude`, a fixture home,
no credential and no network. It covers every case, the RN-9 scenarios, the
probe stubs, the guard in both modes, and print-only. It also runs
`rn9.team-splitpane` alone with two problem leads: `stub/lead-dialog`, whose
dialog never yields its input ("input never ready", with the panes captured
and no prompt sent), and `stub/lead-late`, whose prompt shows late (a pass,
because the prompt waits). It checks that a line naming a credential is
withheld from the kept debug logs, and it runs decide's `-supersede` on
copies of those two runs. A pass prints
`DRY RUN PASSED`, the guard's identical before-and-after checksums, and
tables headed with the dry-run banner. **Dry-run numbers are not
measurements.** `decide` refuses them.

## Live runs

After approval, from your own shell with the four variables exported (L0
needs none of them):

```bash
make measure-exit MEASURE_MODE=probe MEASURE_ARGS="--run-id mx-l0-probe"
make measure-exit MEASURE_MODE=measure MEASURE_ARGS="--run-id mx-l1-measure --host-network"
make measure-exit MEASURE_MODE=rn9 MEASURE_ARGS="--run-id mx-l2-rn9 --host-network"
```

Each call builds its image(s), takes the guard snapshot, runs the container
and verifies.

**Order.** Run L0, then L1, then L2, one after the other and never at the
same time. L1 and L2 run only when L0's `deployed-verdict.txt` reads exactly
`deployed 2.1.280: RUNS exec-form hooks (args_received)`.

**The launch gates are not in this repo.** They live in each run's operator
command file, the reviewed launch script the operator runs.
`run.sh` and `make measure-exit` do not enforce them. Each command file:

- refuses unless L0's verdict file reads exactly that line, and, for a later
  run, unless the earlier runs' `guard-status.txt` files read `pass`;
- takes one shared lock, `.live-run.lock` under the results root, and
  refuses while another run holds it;
- refuses when its results directory already exists, so a stale verdict is
  never re-read;
- refuses inside a Claude Code session unless the operator sets
  `MX_ORCHESTRATOR_LAUNCH=1` for an approved launch.

A launch that skips the command file skips these gates too.

**Re-running one scenario.** A run may take a single RN-9 scenario under a
new run id, for example after an inconclusive team result:
`MEASURE_MODE=rn9 MEASURE_ARGS="--run-id mx-l2b-splitpane --cases rn9.team-splitpane --host-network"`.
Its command file keeps the same gates. decide then needs `-supersede` (see
[Deciding](#deciding-decide)).

**Read the verdict file, not make's exit status.** make reports every runner
failure as its own exit 2, the probe's STOP (exit 3) included. L0's success
or STOP signal is `<results>/deployed-verdict.txt`.

**Samples.** L1 takes 22 samples per case by default: the driver's floor of
20 plus a buffer of 2, so one flaky sample does not force a full re-run.
The driver's own `-samples` default is also 22. Real mode refuses fewer than
20.

Results go to `$HOME/measure-exit-results/<run id>/` (`--results-root DIR`
moves them; the directory may not be inside the worktree). The runner prints
the path and removes its staging directory.

| File | Holds |
|---|---|
| `results.json` | every sample and section (the raw samples) |
| `results-table.txt` | the tables below |
| `run-log.jsonl` | every agent-director and tmux call: argv only, credentials scrubbed (see [Scrubbing](#scrubbing)) |
| `harness-ids.txt` | the identifiers the busy-host guard scans for |
| `rn9.*-hooks.jsonl` | the RN-9 recorder's keys-only lines |
| `<case>-pane-*.txt`, `<case>-claude-debug-*` | agent team scenarios only: pane captures and Claude debug-log copies (see [Team-scenario evidence](#team-scenario-evidence)) |
| `guard-verify.txt`, `guard-status.txt`, `container-status.txt` | the guard's output and verdict, and the container's exit status |
| `probe-summary.txt`, `deployed-verdict.txt`, `<version>/` | L0 only: the per-version lines, the deployed verdict, one directory per probed version holding its `results.json` and a copy of the run's `guard-status.txt` |

## Reading the results

**RN-6 and RN-2 table** (one row per case):

| Column | Meaning |
|---|---|
| `case`, `family` | the case id and RN-6 or RN-2 |
| `attempted`, `completed` | samples started, and samples whose exit was observed |
| `did not exit` | the exit was not seen within the sample ceiling |
| `no ended_at` | RN-2: the row never recorded an end; the trail's ignored-hook reasons are listed |
| `failed` | setup failures, such as an agent that never reached its prompt |
| `largest` | the largest completed time; empty when there is no data |
| `SessionEnd budgets` | the budgets in force; per-case detail follows the table |

The presence check runs every 100 ms, so a time can be up to 100 ms late.
RN-2 times start at `ended_at`, which has whole-second resolution, so they
can read up to 1 s long. Explain every failed, did-not-exit or not-run
sample before you use the table.

**RN-9 table** (one row per scenario: `rn9.drive`, `rn9.resume`,
`rn9.team-inprocess`, `rn9.team-splitpane`): each verdict is `pass`,
`fail`, `inconclusive` or `not_run`. Under it, each hook is shown with its
event, `source`, the recorder's parent pid against the row's pane pid,
`agent_id`, `agent_type`, and the outcome read from the container's trail
(`applied`, `ignored` with its reason, `no_change`, `no_record`). STOP flags:

- `resume-not-applied`: an in-session `/resume` did not move the row to the
  new session;
- `inprocess-teammate-lifecycle-without-agent-id`: an in-process teammate's
  SessionStart or SessionEnd came without `agent_id`;
- `split-pane-teammate-hook-applied`: a hook from a split-pane teammate's
  own process was applied.

The drive sends only to its own agents on the private server, and it
answers permission prompts with `decide`. Each answer is scoped to the
scenario. It reads only the requested tool's name, never the tool input. It
allows the tools the scenario's prompts call for and denies everything else,
leaving a note for each answer:

- `rn9.drive`: `Bash`;
- `rn9.resume`: none;
- both team scenarios: `Agent`, `SendMessage`, `TaskCreate`, `TaskUpdate`,
  `TaskList` and `TaskGet`.

A denial is safe. At worst it cuts the scenario short, and the scenario reads
inconclusive. The recorder keeps key names and
id-shaped values only, never prompt text, tool input or other payload
content.

### Team-scenario evidence

The two agent team scenarios (`rn9.team-inprocess`, `rn9.team-splitpane`)
keep evidence, so a team that does not settle says why.

**The input-ready wait.** The team prompt is sent only once the lead's pane
shows its input:

- Every 500 ms the driver reads the lead's visible screen
  (`capture-pane -p -J`, no history).
- **Ready:** a prompt line (`❯`, or `>` in older versions, then a space or
  the end of the line, optionally inside a box edge), or the idle footer
  `? for shortcuts`.
- **Not ready:** a `❯` on a numbered option, or a dialog footer ("Enter to
  confirm", "Esc to cancel", "Esc to exit", "Press Enter to continue").
- When ready, the driver waits 2 s more and then sends. The scenario's note
  says `input ready after Xs (<signal>); team prompt sent 2s later`.
- If the input is not ready within `-input-ready-timeout`, the prompt is
  never sent, so nothing is spent on it. The reason reads `input never
  ready: …` and quotes the pane's last 6 non-empty lines.
- After the send, the driver waits up to `-prompt-accept-wait` for the
  lead's own `UserPromptSubmit`. Without it the reason reads `the team prompt
  was not accepted: …`, instead of a stall until the step timeout.

| Flag | Real-mode default | Dry-mode default |
|---|---|---|
| `-input-ready-timeout` | 60 s | 10 s |
| `-prompt-accept-wait` | 60 s | 10 s |

Both must be positive.

**Evidence files**, all in the results directory and listed in the
scenario's `artifacts` in `results.json`. The table prints each as
`evidence file: <name>`, and `run.sh` prints an `evidence: <path>` line for
each after an `rn9` run.

- **Pane captures**, `<case>-pane-<label>.txt`. They are written when a team
  scenario is cut short (input never ready, prompt not accepted, team not
  settled), before `pause`. Each holds a pane's whole history
  (`capture-pane -p -J -S -`). The labels:
  - `lead`: the lead's pane;
  - `teammate-pN`: any other pane in the lead's session;
  - `claude-swarm-<pid>-pN`: each pane on a `claude-swarm-*` tmux server
    beside the private socket, which Claude Code starts for split-pane
    teammates when it is not inside tmux.

  If `pause` then fails, every pane is captured again with the suffix
  `-after-pause`. The reason ends `pane text in <case>-pane-lead.txt`.
- **Debug-log copies**, `<case>-claude-debug-*`. Each team lead runs with
  `--debug-file` under the run's HOME, never under the results directory.
  After the scenario, whatever its result, the driver copies that log to
  `<case>-claude-debug-lead.txt`. It also copies every file in
  `$HOME/.claude/debug/` that is new or changed during the scenario, such as
  a split-pane teammate's own log, to `<case>-claude-debug-<name>`. A copy
  over 32 MiB keeps its tail, starting at a whole line.

**These files hold Claude's own content:** screen text, prompts and log
lines. They are unlike the recorder's keys-only lines. Every one is
scrubbed as evidence before it is written (below). So are the pane lines a
reason or note quotes.

### Scrubbing

The driver builds its scrub list from the credential variables set in its
environment. Only parts of 6 or more characters are used, matched without
regard to case:

- every credential value, exactly;
- for `ANTHROPIC_BASE_URL`: the URL without its trailing slash, the host
  (user info dropped) with and without its port, and every path or query
  part of 12 or more characters;
- for every other credential variable: each 12-character piece of its
  value.

Each match becomes `<redacted>`, and overlapping matches merge into one.

| Output | Parts replaced | Keyword lines withheld |
|---|---|---|
| pane captures, debug-log copies, the pane excerpt in a reason or note | yes | yes |
| `results.json`, `results-table.txt` and its stdout copy, `run-log.jsonl` argv | yes | no |

A **keyword line** is any line that matches `auth`, `token`, `bearer`,
`api key` (also `api_key`, `api-key`, `apikey`), `cookie`, `secret` or
`passw`, in any case. In evidence it is replaced whole by
`[line withheld: it names a credential]`. `results.json` and the table only
get the substring replacement, which keeps the JSON valid and keeps
legitimate notes.

The input-ready check reads pane text with only the exact values replaced,
so a prompt line is still seen. Scrubbing removes what the driver knows to
look for. It cannot prove that no other secret Claude shows or logs is
gone. That is why a live run's command file checks every result file for
leaks before it prints anything.

**RN-7 table:** per event, `transcript_path` presence and basename,
`session_id` and the payload's top-level keys. It is a record only and
blocks nothing.

**Probe table:** per version, `args_received`, `args_not_received` or
`did_not_start`.

## Deciding: `decide`

`decide` applies the decision rules to the results directories and prints a
markdown decision record that shows the arithmetic. It writes nothing. It
runs in the measurement image, never on the host:

```bash
docker run --rm --network none -v "$HOME/measure-exit-results:/in:ro" agent-director-measure:cc-2.1.280 measure-exit decide -in /in/mx-l1-measure -in /in/mx-l2-rn9 -in /in/mx-l0-probe/2.1.120 -in /in/mx-l0-probe/2.1.200 -in /in/mx-l0-probe/2.1.280
```

Give one `-in` per directory that holds a `results.json` and the runner's
`guard-status.txt`: the L1 and L2 directories, plus one per probed version
under the L0 directory. Pass every version that `probe-summary.txt` lists;
the versions above are examples. decide needs at least one version that
ignores args and one that runs them, so a single L0 version is invalid
("not bracketed"). The L0 runner copies the run's `guard-status.txt` into
each probed version's directory, so each one can be passed on its own. The
current values come from the agent-director tree the image was built from.

**`-supersede ID`** (repeatable) lets a re-run of one RN-9 scenario replace
an earlier inconclusive result of it. Add the re-run's directory as another
`-in`, for example `-in /in/mx-l2b-splitpane -supersede rn9.team-splitpane`.

- Only RN-9 scenario ids are accepted. Any other id is a usage error
  (exit 2).
- Without the flag, a scenario found in two inputs is invalid, and the
  message names `-supersede`.
- With it, the scenario's results are ordered by their run's `finished_at`
  and the latest is used. Every earlier result must be `inconclusive`. An
  earlier `pass` or `fail` is refused ("a measured result is never
  dropped"), and two runs that finished at the same instant, or without a
  `finished_at`, "cannot order". Both leave the input invalid.
- The record gains a `## Superseded` section after its inputs. It names the
  earlier and later directories, runs, finish times and results, or says
  "NOT superseded" or "nothing superseded".

**Expect exit 3 from a complete run.** The main README's
[Prerequisites](../../README.md#prerequisites) state the minimum Claude Code
version (2.1.280). With complete L0, L1 and L2 inputs, decide exits 3 (STOP
for the user) whenever the measured minimum is below that stated minimum.
L0 measured 2.1.139, so the probe's STOP fires on every complete record.
The stated minimum is never lowered without the user.

| Exit | Meaning |
|---|---|
| 0 | decided: apply the record |
| 2 | invalid input; nothing in it may be applied. Causes include a dry run, an unfinished run, a missing or failed guard, a missing case or scenario, an RN-9 scenario in two inputs that `-supersede` does not resolve, fewer than 20 usable samples, a `no ended_at` sample, a case whose counts disagree with its samples, a did-not-exit or non-default budgets under the default budget, an incomplete probe, or an inconclusive scenario |
| 3 | STOP for the user. Causes include a kill ceiling past its limit, a default that would go lower, a probed minimum below the stated one, or any RN-9 STOP flag or failed scenario |

Invalid input wins over STOP.

**Which samples count.** Each RN-6 and RN-2 case uses every usable sample,
in any position, once there are at least 20. Its largest time is the
largest over all its completed samples, so the buffer past 20 can only add
evidence, never hide a slower exit. The record shows each case as recorded,
used and dropped, with the dropped samples counted by outcome, for example
`22 recorded (…); used all 21 completed, largest 2.5 s; dropped 1 (failed 1)`.

- **Default budget.** A usable sample is a completed one. Fewer than 20
  completed is invalid. A "did not exit" is invalid in any position. It is
  a real, unbounded time, not a flaky sample, and dropping it would
  understate the largest time.
- **Raised budget.** A usable sample is completed or "did not exit", so 20
  measured samples are needed. A "did not exit" is allowed and gets its own
  record line, reported for the README.
- **`no ended_at`.** A sample whose row never recorded an end makes its
  case invalid, under any budget. No time can be taken from it.
- **Failed samples** (setup failures) are dropped and counted.
- **Counts check.** decide reads the samples, not the stored counts. A case
  whose stored counts or largest time disagree with its samples is invalid.

**Kill ceiling.** decide derives kill's SR-13.2 ceiling from the
`internal/config` constants Q (query timeout), A (action timeout) and W
(pipe-close wait), never from a literal. The record shows both paths and
their max: path (i) 2Q + 2A + E + 4W and path (ii) 3Q + 2A + 5W. A ceiling
past its limit, or a required E at the SRD's STOP threshold, is a STOP.

The rules themselves are in the SRD's RN-6,
RN-2 and RN-9 entries. The keys are described in the main README's
[timing settings](../../README.md#timing-settings-tmux). For RN-6, the new
`kill_exit_wait_ms` is at least
twice the largest default-budget time, rounded up to a whole second. RN-2
may raise `stopping_window_seconds` or the stopping window's minimum. A
default is never lowered without the user. RN-7 is reported only. The
record is applied in a separate change, never straight into the
configuration.
