# Experimental agents

Internal, unsupported opt-ins. Nothing here is part of the public contract;
it can change or go away in any release.

Droid is the one experimental agent. It runs in the pane-only lane:
agent-director launches, tracks and ends it, but receives no reports from it,
so a supervisor reads everything from its pane.

## Setup

- `droid` 1.5.0 with auto-update disabled, signed in through a Factory
  subscription (no API key or BYOK), on the agents' tmux server `PATH` or named
  by `droid_path`. Repeat the live startup, model, prompt, input and quit
  checks before using another version.
- `install.sh` neither discovers nor counts Droid; the install still needs one
  of the agents in [Requirements](install.md#requirements).
- Enable the opt-in in `~/.config/agent-director/config.toml`:

```toml
[experimental]
droid = true
```

## Keys

| Key | Range | Default | Effect |
|---|---|---|---|
| `[launch] droid_path` | executable name, absolute path or `~/` path | unset | The program a Droid spawn or finished-ID reuse runs. Empty or unset runs `droid` from the tmux server's `PATH`; a bare name also uses that `PATH`. Paths follow the other launch keys' expansion and refusals. A set value is one executable, without shell parsing, fallback or an advance check. Applies at the next launch. |
| `[experimental] droid` | `true`, `false` | `false` | Enables Droid spawn, finished-ID reuse into Droid and Droid template creation. Reads, `kill`, `sweep`/`find-missing` and `expire` never consult it. |

A non-boolean `droid` is refused with `ErrConfigMalformed`. The next call uses
the flag ([reload rules](configuration.md#when-a-change-takes-effect)). With it
off, `droid` launches and templates are refused with
`ErrExperimentalAgentDisabled`, and refusal advice lists no `droid` agent.

## Supervising Droid

Launch with exactly one model ID and no task. The pane shows a model display
name, not the ID: `droid exec --help` maps the current canonical IDs to their
display names.

```sh
model='<ID>'
id=$(agent-director spawn --cwd "$PWD" --agent droid -- --model "$model" | jq -r .instance_id)
agent-director read-pane --instance-id "$id"
```

A Droid template may leave the model for the spawn to supply. The row stays
`pending` through startup, work, prompts and idle, so `pending` never means
still starting; tell these apart from the pane, at a cost in supervision
tokens.

1. **Model.** Before any work, check that the pane's model display is the
   name help gives for `$model`. Droid can silently run another model (a
   legacy ID started a newer model on 1.5.0). On any difference, stop and
   `kill`. The requested value, a catalog entry and an accepted launch do not
   confirm the running model.
2. **Folder trust.** `pre_trust` is always `skipped`. Droid 1.5.0 showed no
   trust prompt in a fresh directory or git repository. If one appears,
   inspect and answer it; after declining, kill and reconcile without
   assigning work.
3. **Permission prompts.** Inspect each one and choose the one-time allow or
   cancel; never a persistent "always allow" choice. agent-director answers
   nothing. Droid's default autonomy ran commands without asking, and can edit
   files, so no prompt is no protection. When plain text hides the selected
   choice, read with `read-pane --ansi` or
   [attach to the pane](usage.md#attach-to-a-spawns-pane). Answer with the
   menu's displayed key through `send-keys --allow-pending --key`; for a hash
   guard, take `pane_sha256` from a plain (non-ANSI) read and pass it as
   `--expect-pane-sha256` with the same `--n-lines`.
4. **Input.** Send only when the pane shows idle: Enter during a turn steers
   that turn. A send appends to any draft already in the editor, so before an
   independent task read the pane and clear unwanted text on purpose, then
   read again. Ctrl+U cleared typed text on 1.5.0:

   ```sh
   agent-director send-keys --instance-id "$id" --allow-pending --no-enter --text "$(printf '\025')"
   agent-director read-pane --instance-id "$id"
   agent-director send-keys --instance-id "$id" --allow-pending --text 'Describe the entry points.
   Then summarize the relevant files.'
   agent-director read-pane --instance-id "$id"
   ```

5. **Outcome.** A successful send means typing was attempted. Judge
   completion and failures (API, credit, sign-in) from the pane; an API
   connection failure shows there while the row stays `pending`. An idle
   editor or exit status zero is not task success.
6. **End.** Use Droid's own `/exit` after inspecting it, or `kill`; then
   reconcile and inspect:

   ```sh
   agent-director kill --instance-id "$id"
   agent-director find-missing
   agent-director get --instance-id "$id"
   ```

For a fresh conversation on the same ID, follow the
[live-row safeguards](caller-contract.md#caller-contract), then launch again
and repeat the model check. There is no `pause` or `resume`.

```sh
agent-director spawn --instance-id "$id" --reuse-finished --cwd "$PWD" --agent droid -- --model "$model"
```

Unavailable: lifecycle states, delivery and task-success proof, permission
relay, structured questions, context, subagent and spend readings.
`[defaults] relay_mode`, `inject_help_hook` and `disable_ask_user` have no
effect. The primary model does not pin native side calls, compaction or
subagents, whose model and spend stay outside accounting. The operator remains
responsible for the development network's approved-code, data and spending
rules; the lane does not isolate Droid or prevent reads or disclosure.

## Pane-only rows

`get`, `get-many`, `list` and `status` carry `integration_mode: "pane_only"`
(omitted for other agents; Go `IntegrationMode`). A live row stays `pending`; it never
reports work, idle, questions or permission waits. Context fields are all
`null`, `idle_screen_availability` is `unavailable`, and `list-subagents`
returns `tracked: false` with an empty list. Hook reports change none of these.
Past the pending grace period, a recovery check that finds the agent alive
sets `liveness_note: unreported`; this is expected, and the hook diagnoses in
[Operator actions](operator-actions.md) do not apply.

`pause`, `resume`, `decide`, `record-pane-answer` and `send-keys` with
`delivery_request_id` refuse with `ErrAgentCapabilityUnsupported` before any
change or input. The lane creates no permission requests, so `send-keys`
with a `request_token` gives `ErrNoOpenPermissionRequest`, and `get-permission`
or `record-pane-answer` with one gives `ErrPermissionRequestNotFound`.

Plain `send-keys` needs `allow_pending` and the owned pane. An optional
`expect_pane_sha256` keeps its guard (a plain read's hash, same `n_lines`).
It strips CR, keeps LF as newlines and adds one Enter unless `no_enter`; `key`
sends one key without Enter. Success returns `{}`, with no delivery,
deduplication or task-success proof. After a failed or uncertain send, read the
pane; never resend text or Enter blindly.

Recovery is a fresh `spawn --reuse-finished` under the
[live-row, kill and reuse safeguards](caller-contract.md#caller-contract).
`kill` alone does not finish the row: run `find-missing` and inspect `get`
before reuse. `missing` alone is not proof of exit.

## Refusals

Each is refused before any row, session, trust entry, generated launch file or
template is written.

- `ErrExperimentalAgentDisabled`: the opt-in is off.
- `ErrModelMissing`: a spawn or reuse resolves no model. Supply one split
  `--model ID`; a template may defer it to the spawn.
- `ErrSpawnDeniedFlag`: anything but one split `--model ID`: duplicate
  directives, a missing, empty or option-like value, `--model=ID`, a `custom:`
  selector, other options and positional tasks, in launches and templates.
  Model IDs pass unchanged, without catalog lookup.
- `ErrAgentCapabilityUnsupported`: explicit or template-inherited
  `relay_mode: "on"`. Omitted relay is `off` whatever the global default.
- `ErrPermissionsUnsupported`: any `allow`, `deny` or `ask` entry.
- `ErrReservedEnvKey`: `extra_env` holds `FACTORY_API_KEY`,
  `AWS_BEARER_TOKEN_BEDROCK` or `FACTORY_HOME_OVERRIDE`, even empty. The usual
  `HOME`, `AGENT_DIRECTOR_*` and malformed-key refusals also apply. Diagnostics
  never echo rejected values.

The Go client exports these sentinels; the TypeScript client throws the
matching error classes.

## Retained launch files after failed reuse

`~/.config/agent-director/launch/<store_id>/<launch_token>/` holds a launch's
private `droid-settings.json` overlay: only the requested model ID, no
credentials. The warning `retained launch files may be unreachable after
failed reuse` means a failed attempt's files were kept because its launch may
still run; its `store_id`, `launch_token`, `row_state` and `owning_path`
identify it, and ordinary cleanup may never reach it.

Before removal, match the warning to [this store](operator-actions.md#this-stores-id)
and the exact launch. Establish that it never started, or use
[session inspection](operator-actions.md#inspect-and-end-a-session-by-hand)
to verify that both its session and agent process are gone. A finished or
restored row and a successful finished-row `kill` are not enough. If ownership
or exit cannot be established, keep the files.

Then check that the path components are real directories, not symlinks, and
the overlay is a regular file owned by the agents' user. As that user, remove
only the overlay, then the token directory if empty; never recursively:

```sh
rm -- "$HOME/.config/agent-director/launch/<store_id>/<launch_token>/droid-settings.json"
rmdir -- "$HOME/.config/agent-director/launch/<store_id>/<launch_token>"
```
