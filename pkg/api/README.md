# pkg/api — Go client library for agent-director

Typed Go interface to all agent-director verbs, with the same semantics as the
CLI and MCP server. Verb calls run in process; agent launches use their own
processes.

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Verb examples](#verb-examples)
  - [Spawn](#spawn)
  - [Status](#status)
  - [List](#list)
  - [ListSubagents](#listsubagents)
  - [SendKeys](#sendkeys)
  - [ReadPane](#readpane)
  - [Kill](#kill)
  - [Sweep](#sweep)
- [Permission relay](#permission-relay)
- [Version mapping](#version-mapping)
- [Errors](#errors)
- [See also](#see-also)

---

## Install

Requires **Go 1.22** or later.

```sh
go get github.com/gabemahoney/agent-director@<tag>
```

Import the package:

```go
import "github.com/gabemahoney/agent-director/pkg/api"
```

`pkg/api` is a sub-package of the `github.com/gabemahoney/agent-director`
module. Do not treat it as a standalone module or add it as a separate
`go.mod` entry.

---

## Quick start

```go
package main

import (
	"fmt"
	"log"

	"github.com/gabemahoney/agent-director/pkg/api"
)

func main() {
	c, err := api.New(api.Options{
		CreateIfMissing: true, // create state.db on first run
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	v, err := c.Version()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("agent-director %s (%s)\n", v.Version, v.Commit)
}
```

`Version` reads build-time metadata only — no store I/O, no tmux — so it
is the canonical safe first call for verifying connectivity. `List` is an
acceptable alternative when you want to confirm store access as well.

`Close` releases this client's store. Audit reporting belongs to the host
process lifetime; closing a client does not drain accepted process audit
events or certify their durability.

---

## Verb examples

### Spawn

Launch a tracked agent inside a new tmux session.
`Spawn` returns immediately with the `instance_id`; the row is
`pending` from its insert until the agent reports in (its
`SessionStart` hook), then `waiting`. Use `Status` or `Get` to observe
progress. Shared CLI commands are in [Usage](../../docs/usage.md).

```go
result, err := c.Spawn(api.SpawnParams{
    Agent:      "claude",
    CWD:        "/tmp",
    RelayMode:  "on",
    InstanceID: "claude_example",
    AgentDirectorLabels: map[string]string{
        "project": "widget",
    },
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(result.InstanceID)
```

Cursor uses the existing `SpawnResult` and row fields; `Status` keeps its
existing shape. Its fresh workflow is in [Usage](../../docs/usage.md#fresh-cursor-use).
`Resume(api.ResumeParams{InstanceID: id})` launches load of only its latest saved session;
the row can remain `pending` until a first real prompt. After inspecting the
pane, set `SendKeysParams.AllowPending` for that prompt; current binding and
dialog guards still apply. Saved configuration and initial-prompt replay are
defined in [Pause and resume](../../docs/usage.md#pause-and-resume).
`SpawnParams.ReuseFinished` supports Cursor and agent switches; see the
[life rules](../../docs/usage.md#naming-the-agent). Argument and
asynchronous failure handling are in the
[caller contract](../../docs/caller-contract.md#cursor-operation-boundaries).

Go applications launching or resuming Cursor require an installed
`$HOME/.config/agent-director/bin/agent-director`. Its resolved symlink target
must be a regular executable; an unavailable gateway fails before row mutation.
The application itself does not need to implement the gateway. The Client's
`Options.StorePath` selection is also used by fresh, reused and resumed Cursor
processes; no separate gateway path or store parameter is needed.

A spawn names its agent: `SpawnParams.Agent`, `"claude"`, `"codex"` or `"cursor"`
(case-sensitive), or the `Agent` its template names. `MakeTemplateParams.Agent`
bakes one into a template; a template with `AgentArgs` needs it. How a call's
`AgentArgs` combine with a template's is in
[docs/settings.md](../../docs/settings.md#templates-and-per-call-merge).

Five sentinels refuse the agent input, each before anything is written;
`pkg/api` re-exports them and callers match them with `errors.Is`:
`ErrAgentMissing` (no `Agent`, and the template names none), `ErrAgentInvalid`
(not `"claude"`, `"codex"` or `"cursor"`), `ErrAgentArgsMismatch` (the template's
`AgentArgs` are for another agent: spawn that agent, or pass this call's own
non-empty `AgentArgs`), `ErrTemplateAgentMissing` (the template has `AgentArgs`
and no agent: give it one; `MakeTemplate` returns it too) and
`ErrPermissionsUnsupported` (a `"codex"` or `"cursor"` spawn or template carrying an allow,
deny or ask entry; `MakeTemplate` returns it too).

`ErrSpawnDeniedFlag` refuses an unsupported `AgentArgs` entry before anything
is written; `MakeTemplate` returns it for unsupported Cursor arguments too.
Cursor's supported model/mode/prompt forms and asynchronous configuration
failures are in [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries).
Claude always refuses `--settings`,
`--output-format` and print (`--print`, `-p`, parsed clusters included).
Session selectors and their owned values are refused except on a verified
finished-agent switch, where they are removed from launch and storage and the
remainder is checked as for a plain spawn. Exact denied long names owned as
option values always refuse. Codex also refuses other command-position
subcommands, `--remote`, `--remote-auth-token-env`, and config keys or features
that override its hooks, typing setting or, while `disable_ask_user` is on,
question-tool setting. The complete selector lists, recognition rules and
switch policy are in
[docs/caller-contract.md](../../docs/caller-contract.md#spawn-and-template-input-refusals).
`Resume` filters stored `AgentArgs` without applying spawn's argument refusals;
see [Pause and resume](../../docs/usage.md#pause-and-resume) for replay rules and
[Caller contract](../../docs/caller-contract.md#spawn-and-template-input-refusals)
for argument recognition limits.

For all agents, `SpawnParams.ReuseFinished` on a finished id starts a
fresh conversation. Same-agent reuse retains current-life session history and subagent records;
switching agents starts another life, including when switching back.
`Resume` accepts only the id and continues its recorded agent's latest
reported session. It never selects an archived session, even when its
transcript exists. See [Transcript recovery](../../docs/caller-contract.md#transcript-recovery)
for classification and recovery, and [Usage](../../docs/usage.md#naming-the-agent)
for restoration and children.

`SpawnParams.IdleScreenTimeoutSeconds` gives the row its own `idle_screen`
timeout in whole seconds (0 for none); its range and lifecycle are in
[Configuration](../../docs/configuration.md#idle-screen-timeout-state).

Returns `SpawnResult` (`.InstanceID`, `.PreTrust`, `.DroppedArgs`).
`.DroppedArgs` lists removed original argument tokens in argv order on a
switching reuse, which starts fresh and resumes nothing. JSON omits
`dropped_args` when empty. Reporting details are in the
[caller contract](../../docs/caller-contract.md#spawn-and-template-input-refusals).
`.PreTrust` is
`"ok"`, `"skipped"` or `"failed"`: what the folder-trust pre-trust did, for
the selected agent (a failed pre-trust never fails the spawn; see the
`SpawnResult.PreTrust` godoc,
[Codex pre-trust](../../docs/settings.md#pre-trust-for-codex) and
[Cursor launch settings](../../docs/settings.md#cursor-launch-settings)).
Cursor reports `failed` for unsupported pre-trust or `skipped` for the saved
opt-out, never a validated `ok`. Neither outcome proves readiness. Most-likely sentinel errors: `ErrCwdNotFound`, `ErrCwdNotADirectory`,
`ErrRelayModeInvalid`, `ErrTmuxNotAvailable`, `ErrTmuxSessionCreate`,
`ErrTmuxUnresponsive` and `ErrTmuxSessionConflict`. Timeout and held-name
recovery are owned by the [caller contract](../../docs/caller-contract.md#caller-contract)
and [Operator actions](../../docs/operator-actions.md).
See `(*Client).Spawn` godoc for the full enumeration.

---

### Status

Return the current lifecycle state of a tracked Spawn. State values:
`pending`, `waiting`, `working`, `ask_user`, `check_permission`, `ended`,
`missing`.
`pending` means a launch (spawn, reuse or resume) is in progress and the
agent has not reported in yet (its `SessionStart` hook); it may be
loading or waiting at a startup prompt. A resumed `pending` row keeps its
session id and history; a caller tells it from a fresh one by its non-empty
`session_id`, shown by `Get`.

Client `Status`, `Get`, `List` and `Sweep` (also `FindMissing`) share
[bounded recovery](../../docs/caller-contract.md#bounded-recovery-on-state-reads).
Single-row reads check only that row. Package-level helpers and custom stores
run only checks their supplied capabilities support; they do not create
missing process or tmux providers. Read success does not confirm every write.

```go
res, err := c.Status("claude_2026-05-22T18-23-15")
if err != nil {
    log.Fatal(err)
}
fmt.Println(res.State) // e.g. "waiting"
```

Returns `StatusResult` (`.State`, `.FamilyState`, and `.LaunchStartedAt`, the start of
the launch in progress, set only while the row is `pending`; JSON key
`launch_started_at`, omitted otherwise). Most-likely sentinel error:
`ErrSpawnNotFound`. See `(*Client).Status` godoc.

`State` is never a Displayed State. While a row shows one, `StatusResult`
and `Get`'s `SpawnRow` carry it in the embedded `StateDetail`:
`.DisplayedState` (`api.StateIdleScreen`; `api.StateNeedsIntelligence` is
defined, but nothing shows it yet), `.Since`, a string, and for
`idle_screen` `.UnchangedMinutes`, an `*int64` (JSON `displayed_state`,
`since`, `unchanged_minutes`); `StatusResult.TmuxSessionName` (JSON
`tmux_session_name`) is set. `ListRow.DisplayedState` carries it with no
detail. A field that does not apply is empty (nil), and absent from the
JSON. Test `DisplayedState`, never `State`, for `api.StateIdleScreen` or
`api.StateNeedsIntelligence`. `StatusResult`,
`SpawnRow` and `ListRow` also carry `IdleScreenAvailability` (JSON
`idle_screen_availability`): `api.IdleScreenAvailable` for Claude and Codex,
`api.IdleScreenUnavailable` for Cursor. The
[caller contract](../../docs/caller-contract.md#needs-intelligence-and-idle-screen-rows)
defines the states, fields, omissions and detection.

For Go custom stores, successful status/get/list rows with no agent report
`api.IdleScreenAvailable` for compatibility; this does not permit pane
sampling. Only Claude and Codex are eligible for sampling.

`StatusResult`, `Get`'s `SpawnRow` and each `ListRow` expose
`FamilyState string` (JSON `family_state`), either `"working"` or `"waiting"`.
[Usage](../../docs/usage.md#family-state) defines membership and
aggregation; the [caller contract](../../docs/caller-contract.md#family-reporting-limits)
defines reporting limits.

They also expose `ProcessLiveness string` (JSON `process_liveness`:
`"alive"`, `"exited"` or `"unknown"`) and `Confirmations` (JSON
`confirmations`), an `api.Confirmations` holding exactly `State`,
`ProcessLiveness` and `DisplayedState`. Each is an `api.Confirmation`: `Status`,
never null, and the nullable `*string` fields `ConfirmedAt`, `Source`,
`CheckedAt` and `Reason` (nil is JSON `null`, never omitted). The
[caller contract](../../docs/caller-contract.md#confirmations) defines the
values and when each is `null`. `Confirmations.DisplayedState` describes the
returned display, including an omitted `DisplayedState`; Cursor's record is
`unknown`/`unsupported`. A store that supplies no evidence, such as a custom
store or a narrow status fallback, gets `"unknown"` and unknown records. With a non-nil error,
the zero result's `ProcessLiveness` and each `Status` are empty strings, outside
their value sets.

`Get`'s `SpawnRow` and each `ListRow` expose `StateChangedAt time.Time` (JSON
`state_changed_at`), when the raw state last changed; `StatusResult` does
not. The [caller contract](../../docs/caller-contract.md#state-change-time)
defines it and how it differs from `LastSeenAt`, the settled `State` and
`Since`.

`Client.Status`, `Client.Get` and `Client.List` use the config loaded when
the client was built for [independent state and family
settling](../../docs/configuration.md#settle-time-state). While a
Displayed State's exit settles, `Status`, `Get` and `List` retain its
`DisplayedState` and applicable confirmation, and `Status` and `Get` its
committed `Since` and detail.
`StateChangedAt` and permission-request lists follow the live state;
commands keep their raw-state guards. The package-level
`api.Status`, `api.Get` and `api.List` functions use no settle time and
sample no pane: they report stored display evidence without advancing either
confirmation time. A committed non-sampling exit clears that evidence.
Package-level `Sweep` (and its `FindMissing` alias) can sample panes when its
supplied tmux provider supports capture.

For Cursor, `Client.Status`, `Client.Get` and `Client.List` can report a
launch-guarded `missing` read result even when the separate terminal write is
busy or reaches its deadline. This does not confirm durable closure;
`ended_at` and `state_changed_at` remain the stored values read, and
`ended_at` may be absent. The
[caller contract](../../docs/caller-contract.md#cursor-operation-boundaries)
defines caller-budget errors, request proof and recovery. Package-level
`api.Status`, `api.Get` and `api.List` can check gateway liveness when their
store and `RelayView.Procs` supply the required capabilities.

---

### List

Enumerate Spawn rows matching a filter set. All filters AND together;
state values OR together. Returned order is unspecified — sort with
`jq` or in Go as needed.

```go
res, err := c.List(api.ListParams{
    State:  []string{"waiting", "working"},
    Labels: []string{"project=widget"},
})
if err != nil {
    log.Fatal(err)
}
for _, row := range res.Spawns {
    fmt.Println(row.InstanceID, row.State)
}
```

Returns `ListResult` (`.Spawns []ListRow`; each row's `Agent` is `"claude"`,
`"codex"` or `"cursor"`, as is `Get`'s `SpawnRow.Agent`). `Spawns` is never nil —
encodes as `[]` when empty. The `State` filter keeps a row whose `State` or
`DisplayedState` (see Status) is one of its values, so `"working"` keeps a
working row that shows `"idle_screen"`; it is applied before `Limit`.
Filters and limit select stored IDs before recovery checks. The result keeps
those IDs even when their refreshed values no longer match, without refilling
slots or adding newly matching rows. Recovery and family freshness follow the
[caller contract](../../docs/caller-contract.md#bounded-recovery-on-state-reads).
`Limit` caps the rows returned; 0 means no cap. Sentinel errors, each returned before the store is
read: `ErrListInvalidLabel` (label not in `key=value` form) and
`ErrInvalidFlags` (`Limit` is negative). See `(*Client).List` godoc.

---

### ListSubagents

Return the subagent records across launches of one agent's current life and
whether the agent is tracked. `SubagentID` is unique among the returned
records; earlier-life records remain stored but are hidden. See
[visibility and retention](../../docs/usage.md#read-an-agents-subagent-records).

```go
res, err := c.ListSubagents("claude_2026-05-22T18-23-15")
if err != nil {
    log.Fatal(err)
}
fmt.Println(res.Tracked)
for _, sub := range res.Subagents {
    fmt.Println(sub.SubagentID, sub.State)
}
```

Returns `ListSubagentsResult` (`.Tracked`, and `.Subagents []SubagentRecord`,
never nil: `[]` when the agent has none). A `SubagentRecord` field with no
known value is a nil pointer and encodes as JSON `null`; its times are UTC
`*time.Time`, encoded like `launch_started_at`. `Tracked` is false, with no
records, for a `"cursor"` agent, and for a `"codex"` agent (from its spawn, and after every `Resume` or
reuse of its id) and for a `"claude"` agent that predates subagent tracking
(it turns true when that agent is next resumed or its id is reused). Sentinel error:
`ErrSpawnNotFound`; every error returns `Tracked` false and an empty slice.
The package-level `api.ListSubagents(ListSubagentsStore, id)` reads a store
you pass in and takes no `RelayView`. The records, their fields and their
values are described in
[Read an agent's subagent records](../../docs/usage.md#read-an-agents-subagent-records).
See `(*Client).ListSubagents` godoc.

---

### SendKeys

Send text into the agent's own pane. CR bytes (`\r`, `0x0D`) are
stripped automatically before delivery to prevent premature buffer
submission; LF bytes (`\n`, `0x0A`) are preserved as composed newlines
in the agent's input box. A single Enter is appended to submit the buffer
unless `NoEnter`; an empty `Text` sends that Enter only, submitting what
is already typed. `Key` sends one key alone instead (a named key `Escape`,
`Enter`, `Up`, `Down`, `Tab`, or one character), with no Enter. With
`ExpectPaneSHA256` (the `PaneSHA256` of the `ReadPane` you looked at, with
its `NLines`) the pane is checked first and nothing is sent if it changed
(`ErrPaneChanged`); on a relayed Spawn a match also passes the API's dialog hold
(`ErrDialogMaybeOpen`, below). Set it only for a pane a person or an LLM
judged, never from an automatic flow. There is no flag to suppress CR
stripping; it applies to every call.

```go
_, err := c.SendKeys(api.SendKeysParams{
    InstanceID: "claude_2026-05-22T18-23-15",
    Text:       "what is 2+2?",
})
if err != nil {
    log.Fatal(err)
}
```

On a supported native launch, ordinary nonempty text with Enter and no pane
hash returns `SendKeysResult`
with `Outcome: "submitted"`, `RequestID`, `SessionID`, `RecordSHA256` and
`RecordID` when the native record supplies it. The result confirms the exact
normalized text in the launch's native transcript. `DeliveryRequestID` owns
one attempt: the same ID and text retrieve confirmation or reconcile without
replay; a new ID allows an intentional identical turn. Launches without native
capability use fallback input, return an empty result on success and make no
exact transcript-confirmation claim. Manual operations
(`NoEnter`, `Key`, empty text, pane hashes and permission answers) return an
empty result on success; input-ownership refusals, including those from
`Pause`, are in [Manual input ownership](../../docs/caller-contract.md#manual-input-ownership).
Supported launches, fallback readiness and input restrictions, omitted-ID
behavior, structured outcomes and recovery are in
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission).
Cursor sends with Enter can return `ErrSubmissionUnconfirmed` with
`Outcome: "gateway_refused"`; see
[Cursor input outcomes](../../docs/caller-contract.md#cursor-input-outcomes).

Most-likely sentinel errors: `ErrSpawnNotFound`, `ErrSpawnNotInteractive`,
`ErrSubmissionUnconfirmed`, `ErrInvalidFlags`, `ErrSendKeysWhileRelayed`,
`ErrRelayFallenBack`, `ErrDialogMaybeOpen`, `ErrPaneChanged`, `ErrTmuxSendKeys`,
`ErrTmuxSessionConflict`, `ErrTmuxUnresponsive` and `ErrTmuxNotAvailable`.
`api.ErrDetails(err)` gives structured submission, permission and pane facts.
Permission holds, retry rules and timeout recovery are in the
[caller contract](../../docs/caller-contract.md#permission-relay).

After the state and relay refusals, a row whose recorded tmux session name
cannot be used (empty, with a control character, or with a character tmux
stores differently) returns an error classified `ErrInternal`, with no tmux
call; nothing was sent.

`SendKeysContext(ctx, params)` uses the caller's context; `SendKeys` uses a
background context. Cancellation cannot undo attempted input. A cleanup error
can accompany a confirmed result: `api.ErrDetails(err)` retains that exact
result. Follow the [caller contract](../../docs/caller-contract.md#ordinary-text-submission)
before deciding whether another call is safe. See `(*Client).SendKeys` godoc.

#### `AllowPending` — reaching a `pending` launch

By default `SendKeys` rejects new input to a `pending` row with
`ErrSpawnNotInteractive`. `AllowPending: true` also allows a `pending`
row: a launch (spawn, reuse or resume) whose agent has not reported in
yet, for example to answer a startup choice with `Key` (including `Key: "Enter"`).
See [pending startup screens](../../docs/caller-contract.md#pending-rows-and-startup-screens)
for text answers and Enter-only sends. Keys are delivered only to a session
started by the row's current launch. `ended` and `missing` rows are rejected,
with no tmux call, including retries of an existing delivery request. State,
relay and dialog guards precede native capability selection and request
reconciliation.

A `pending` row is refused with `ErrSpawnNotInteractive` and nothing sent
when its launch start or launch token is not recorded (before any tmux
call), or when the only session found is one an earlier launch left
behind (not this launch's session). The keys reach the agent's pane, but
the caller cannot be sure the prompt it saw is still showing when they
arrive.

`AllowPending` preserves native readiness and transcript requirements on
supported launches; it does not make a startup dialog an ordinary composer.
Ordinary pending Claude fallback has a readiness wait, and pending Claude and
Codex fallback have a hook check; see
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission).
`ErrSubmissionUnconfirmed` carries `SubmissionUnconfirmedDetails` through
`api.ErrDetails(err)`: `RequestID`, `Outcome`, `Phase`, `BytesAttempted`,
`EnterMayHaveBeenSent`, optional `LastQueue`, `Reason`, `KeysSent`,
`SubmissionConfirmed` and `RowState`. A failed fallback hook observation has
an empty `Outcome` and serializes only `keys_sent: true`,
`submission_confirmed: false` and `row_state`. See
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission)
for their meaning and recovery.

`LivenessNote` `unreported` means an alive agent has not reported since
launch. For action on pending rows see the
[caller contract](../../docs/caller-contract.md#pending-rows-and-startup-screens).

#### Pure `SendKeys` function

The package-level function accepts explicit store, tmux and environment
interfaces for callers that supply their own dependencies:

```go
func SendKeys(s SendKeysStore, t SendKeysTmux, env SendKeysEnv, params SendKeysParams) (SendKeysResult, error)
```

`SendKeysEnv` supplies a `RelayView`, the pane sender's `Self` identity,
`IntentHold`, optional `Input` (`InputDeliveryBackend`, nil uses native
delivery), and optional `Sleep` for native readiness, drain and submission
checks, pending Claude readiness and settle, pending Codex and Claude hook
checks, and Cursor gateway verdict reads (nil uses `time.Sleep`). Most callers
use the Client method, which supplies these.
Custom `SendKeysStore` implementations must provide `GetSpawnWithin`,
bounding connection and lock waits by the supplied duration.
Custom tmux adapters supplied to this function or through `Options.TmuxClient`
need these optional capabilities for the corresponding fallback paths:

- `PasteKeysTmux` supports ordinary Codex fallback:
  `PasteKeysPane(socket, paneID, text string) error`. It preserves text and LF in
  one paste, sends one Enter only after paste succeeds, and never retries input.
  Failures use `*TmuxCallError` with the failed text or Enter call.
- `GuardedPasteKeysTmux` is also required for journal-backed Codex fallback:
  `PasteKeysPaneGuarded(socket, paneID, text string, beforeWrite func(enter bool) error) error`.
  It invokes the guard immediately before paste and Enter and stops on refusal,
  with the same framing and error rules as `PasteKeysTmux`.
- `PaneTerminalTmux` supports ordinary pending Claude fallback:
  `PaneTerminalRaw(socket, paneID string, maxWait time.Duration) (bool, error)`.
  It reports whether canonical mode, echo and CR-to-NL translation are all off,
  without writing to the terminal. `maxWait` bounds the tmux read and pipe-close
  wait together; a nonpositive allowance refuses without invoking tmux. tmux
  failures use `*TmuxCallError` with `TmuxCallPaneTerminal`; terminal-device
  failures retain their OS error.

Adapters without the needed capability remain source-compatible but receive a
pre-input `not_started` refusal on that path. The default client provides all three.
See their godoc for each field's contract.

```go
_, err := c.SendKeys(api.SendKeysParams{
    InstanceID:   "claude_2026-05-22T18-23-15",
    Text:         "",       // press Enter to dismiss the prompt
    AllowPending: true,
})
if err != nil {
    log.Fatal(err)
}
```

---

### ReadPane

Capture the last N lines of the agent's own pane. Default 25 lines
(`NLines` 0); no upper cap. ANSI escape codes are stripped by default
(pass `ANSI: true` to get raw bytes).

Call `c.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 50})`.
Returns `ReadPaneResult` (`.Pane` string, and `.PaneSHA256`, the SHA-256 in
lowercase hex of exactly the bytes of `.Pane`, which `SendKeys` and
`RecordPaneAnswer` take back as `ExpectPaneSHA256` with the same `NLines`;
the hash of an `ANSI: true` read never matches). `ReadPane` reads only this
agent's pane (or, with no session of the current launch, the pane of the
one session an earlier launch of this row left behind) and changes
nothing. Most-likely sentinel errors:

- `ErrInvalidFlags`: `NLines` is negative; refused before the row is read,
  with no tmux call.
- `ErrSpawnNotFound`: no row has this id.
- `ErrTmuxCaptureFailed`: no session of this agent is there.
- `ErrTmuxSessionConflict`: the agent's pane was not found, more than one
  session an earlier launch left behind is there, or tmux holds
  conflicting labels.
- `ErrTmuxUnresponsive`, `ErrTmuxNotAvailable`: no information about the
  pane; nothing was read.

A row in any state whose recorded tmux session name cannot be used (empty,
with a control character, or with a character tmux stores differently)
returns an error classified `ErrInternal`, with no tmux call. Its
description quotes the recorded name exactly, every control character and
invalid byte written as an escape.

See `(*Client).ReadPane` godoc.

#### `AllowPending` — surface symmetry with `send-keys`

`ReadPane` has **no state guard** — it can read the pane of a `pending`,
`ended`, or `missing` row just as easily as a live one (provided tmux still
holds the agent's session). Passing `AllowPending: true` is accepted but has no
behavioral effect. The flag exists only so callers that pair `readPane` +
`sendKeys` with `allow_pending: true` can set the same option on both calls
without special-casing.

---

### Kill

End the agent of a live row's current launch (`pending` included). Kill
finds the tmux session that carries the launch's label on the row's
recorded socket, ends the agent's pane and that session by their tmux ids,
and succeeds only once the agent process and every other process found in
the session's panes are gone, waiting up to `kill_exit_wait_ms`. When no
session of the launch is found, the agent process decides: gone, or none
recorded, is success with nothing sent.
Kill never signals a process itself and never ends a session an earlier
launch left behind.

A finished row (`ended`, `missing`) is a no-op success with no tmux call;
that is not verification that the agent exited. Kill never changes the
row's state: a state read or `sweep` marks it once its agent process is gone.

```go
res, err := c.Kill(api.KillParams{
    InstanceID: "claude_2026-05-22T18-23-15",
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(res.KillSent) // true: a kill was sent to the agent's session
```

Returns `KillResult`. `KillSent` (`kill_sent`) is true exactly when a pane
kill or a session kill was sent, including one whose call failed; false
when nothing was sent, for example for a finished row.

Errors (check them with `errors.Is` against the exported sentinels):

| Sentinel | Class | Meaning |
|---|---|---|
| `ErrSpawnNotFound` | — | No row has this id |
| `ErrTmuxKillFailed` | UNAVAILABLE | The agent process, or another process of the session's panes, still ran after the kill exit wait; or it cannot be checked and its labelled session is still there; or no session or pane of this launch was found while it runs. See [Caller contract](../../docs/caller-contract.md) for retry and refusal handling |
| `ErrTmuxUnresponsive` | UNAVAILABLE | tmux did not answer usably, before or after a kill was sent. See [Caller contract](../../docs/caller-contract.md) for retry handling |
| `ErrTmuxSessionConflict` | CONFLICT | The session found is not this launch's session, or tmux holds conflicting labels; no kill was sent. A human must look: see "Operator actions" in [docs/operator-actions.md](../../docs/operator-actions.md) |
| `ErrTmuxNotAvailable` | ENVIRONMENT | tmux could not be run, its socket is not accessible to this user, or this is not the tmux server the agent was launched on |

A live row whose recorded tmux session name cannot be used (empty, with a
control character, or with a character tmux stores differently) returns an
error classified `ErrInternal`, with no tmux call. None of these errors
means that the agent is dead.

A repeated kill right after the last session on its tmux server ends can
get `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable` while the server exits.
See [Caller contract](../../docs/caller-contract.md)
for handling.

```go
_, err := c.Kill(api.KillParams{InstanceID: id})
switch {
case err == nil:
    // live-row success verifies exit; finished-row success is a no-op
    // follow the caller contract before relaunching the id
case errors.Is(err, api.ErrTmuxKillFailed), errors.Is(err, api.ErrTmuxUnresponsive):
    // retry later with backoff; never delete the row
case errors.Is(err, api.ErrTmuxSessionConflict):
    // stop and surface it to a human
}
```

The live-row sequence and finished-row no-op limits are owned
in
[Caller contract](../../docs/caller-contract.md#caller-contract). See `(*Client).Kill`
godoc.

---

### Sweep

`Client.Sweep(ctx)` returns `(SweepResult, error)` for one bounded all-agent
pass. `SweepResult.Checks` contains a `SweepCheck` per selected identity, with
values, three confirmation records and independent `Completion`/`Reason` fields.
The result also retains `Count`, `IDs`, `Unverified` and `UnverifiedIDs`. See
[Sweep results](../../docs/caller-contract.md#sweep-results) for membership,
coverage, counters and failure handling.

The exported `Sweep` function accepts the same store, tmux, process checker,
grace, budget, clock and logger arguments as `FindMissing`. `FindMissing` and
`Client.FindMissing` remain supported delegating aliases; `SweepResult` aliases
`FindMissingResult`. Existing `FindMissingStore`, `FindMissingTmux` and
`FindMissingLogger` interfaces remain accepted. A narrow store can supply only
live identities and its available checks, so it cannot promise an all-agent
snapshot and reports unsupported coverage explicitly.

## Permission relay

`(*Client).Decide` records a verdict on a relayed permission request, waits
at most 1 s for relay acknowledgement, and returns a
`DecideResult`, which embeds `RequestDelivery`: read `Delivery`
(`delivered` or `not_confirmed`), not the recorded decision.
`GetPermission` exposes subsequent delivery and `ConfirmBy`; see the
[caller contract](../../docs/caller-contract.md#permission-relay) for
confirmation handling.

Cursor uses these same methods and fields for permissions and blocking plans.
Its `HookAlive` is nil; `ConfirmBy` is finite and unchanged by recreating a
client. `Delivery` `delivered` means durable relay acknowledgement, not Cursor
receipt, execution or dialog closure.
Cursor's once-only decisions, native choices, question denial and conservative
unknown-origin holds are defined in
[Cursor permissions](../../docs/permissions.md#cursor-spawns); uncertain
answers and fallback follow the
[caller contract](../../docs/caller-contract.md#permission-relay).

`DecideParams.MaxWaitMs` bounds the whole call, including SQL, cleanup and
reporting, and has no default; pass at most your deadline minus 1 s. Already
entered database work can retain its connection while the caller returns.
Precommit exhaustion returns `ErrStoreBusy`; an unconfirmed commit returns
an internal error. Once the verdict is confirmed committed, `Decide` never
returns `ErrStoreBusy`. Zero-wait semantics and recovery are defined in the
[caller contract](../../docs/caller-contract.md#permission-relay).

`ErrNoOpenPermissionRequest` means no eligible request has that token,
including a closed, proven-gone or obsolete request; see the
[caller contract](../../docs/caller-contract.md#permission-relay) for handling.
A request still awaiting
an answer when its Spawn ended (its agent's terminal SessionEnd, or the move
of a Codex Spawn to `ended` by shared recovery or `Pause` once its agent is
gone with an end report) or when shared recovery marked it `missing` is closed in the same write, an
undecided one denied with `DecisionReason` `ended` or `find_missing`, and
stays closed after `Resume`; `Get` and `List` no longer show it. The same
close includes decided requests without a confirmation or completed pane
answer, including requests recorded by an older binary.
`GetPermission` still reads its delivery. For a request with a settle
instant, `delivered` retains an acknowledgement from before closure; closure
cannot authorize a later response. Requests
recorded by an older binary retain the
[legacy delivery rules](../../docs/permissions.md#requests-recorded-by-an-older-binary),
including derived delivery after expiry even when closed.

`ErrRelayFallenBack` carries `RelayFallenBackDetails` from `api.ErrDetails`: the
request, spawn state and other open requests. `SendKeys` can answer a pane
request using `RequestToken`, `As`, `Key` and `ExpectPaneSHA256`.
`RecordPaneAnswer` records an answer made outside agent-director, using
`RequestToken`, `As`, `ExpectPaneSHA256` and `NLines`, without typing.
`PaneAnswer` is `sent`, `outside` or `tool_ran`; `PaneAs` is the caller's claim.
`Pause` accepts no pane hash and is held by the same permission checks as
a plain `SendKeys`. These calls and `Decide` can return structured refusals;
handling, dialog proof, agent differences and stopping a held agent are in
[Permissions](../../docs/permissions.md) and the
[caller contract](../../docs/caller-contract.md#permission-relay).

`api.DetailedError` is the error type that carries such facts: its `Err`
holds the catalogued sentinel (`errors.Is` sees through it) and `Details`
the object; `api.ErrDetails(err)` returns the `Details` of the first one in
`err`'s chain, or nil. `ErrPaneChanged` (`PaneChangedDetails`),
`ErrPaneAnswerInProgress` (`PaneAnswerInProgressDetails`),
`ErrClaimTooSoon` (`ClaimTooSoonDetails`) and `ErrDialogMaybeOpen`
(`DialogMaybeOpenDetails`) carry it too, and so does a
pane answer's `ErrInternal` after its key was sent (`PaneKeySentDetails`).

`Decide` on a request already answered at the pane (`PaneAnswer`
`"sent"`, `"outside"` or `"tool_ran"`) returns `ErrAlreadyDecided`
naming its `PaneAnswer`, as `SendKeys` and `RecordPaneAnswer` do, and
records nothing.

The exported functions `api.Decide`, `api.Status`, `api.Get`, `api.List` and
`api.GetPermission` take a `RelayView`, and `api.SendKeys`,
`api.RecordPaneAnswer` and `api.Pause` (`PauseEnv`) an env holding one (the
`Client` methods pass their own); `DecideResult` embeds `RequestDelivery`.

---

## Version mapping

`pkg/api` and the CLI binary use a **1:1 lock**: `pkg/api@v0.X.Y`
requires CLI binary `v0.X.Y`. The two components share an envelope shape
— the JSON structures the library reads and writes are identical to those
the binary produces. Mixing versions risks silent field mismatches that
are not detectable at compile time.

---

## Errors

Named errors can wrap sentinel values and structured details. Use `errors.Is`
to inspect them — never compare error strings.

`ErrPauseTimeout` matches with `errors.Is`; `api.ErrDetails(err)`
returns an `api.PauseTimeoutDetails` value after exit keys were sent but
`ended` was not observed. Its fields are `InspectionRequired`, `ExitKeysSent`,
`ExitConfirmed` and `RowState`, with the shared meanings and recovery in
[When pause cannot confirm exit](../../docs/caller-contract.md#when-pause-cannot-confirm-exit).

```go
res, err := c.SendKeys(api.SendKeysParams{
    InstanceID: id,
    Text:       "hello",
})
if errors.Is(err, api.ErrSpawnNotInteractive) {
    // Spawn not yet ready — wait for state == "waiting"
}
```

A verb method's godoc has an "Errors:" block listing its verb's
manifest error names: the sentinels catalogued for that verb on every
surface (CLI, MCP, TypeScript client and Go). "Errors: none" means the
verb has none. Sentinels common to all methods, such as
`ErrClientClosed`, are not repeated there, and a refusal only a Go
caller can reach is stated in the method's prose instead. The canonical
list of all sentinels, with their descriptions, lives at the godoc index:

<https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api>

Common sentinels across verbs:

| Sentinel | Meaning |
|---|---|
| `ErrSpawnNotFound` | No row for the given `instance_id` |
| `ErrClientClosed` | Called after `Close()` |
| `ErrStoreNotInitialized` | Store file absent and `CreateIfMissing` is false |
| `ErrSchemaMismatch` | DB schema is newer than the binary, the store has no valid store id, or its current-schema input authority is incompatible or uncommitted; see [Operator actions](../../docs/operator-actions.md#upgrading-the-native-input-journal) |
| `ErrOldLayout` | From `New`, when it is about to open the default store (no `StorePath`, no `[store] db_path` in `config.toml`), no store exists in `~/.config/agent-director` and the old directory exists. Nothing was created; take no action, a human runs this release's `install.sh` (see [Operator actions](../../docs/operator-actions.md#a-verb-refuses-with-erroldlayout)) |
| `ErrSpawnNotInteractive` | New input requires a live conversational state; with `AllowPending`, a `pending` row is refused when its launch start or token is not recorded or only a session of an earlier launch is found. `ended` and `missing` rows are refused even when observing an existing delivery request; see [Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission) |
| `ErrSendKeysWhileRelayed` | The Spawn's request relay may still answer (from `SendKeys` and `Pause`) — the message distinguishes an undecided request from a recorded verdict still being delivered; see [Permission relay](../../docs/caller-contract.md#permission-relay) for handling |
| `ErrRelayFallenBack` | A permission request fell back (relay settled without acknowledgement, no pane answer recorded; from `Decide`, a plain `SendKeys` and `Pause`) — `ErrDetails` gives its facts; the relevant API methods are `SendKeys` with `RequestToken` and `RecordPaneAnswer`; shared pane handling is in [Permission relay](../../docs/caller-contract.md#permission-relay) |
| `ErrDialogMaybeOpen` | A plain `SendKeys` without `ExpectPaneSHA256`, or a `Pause`, while a permission request of the Spawn is not proven gone by valid agent evidence and is not a Cursor request of an earlier launch; nothing was sent — `ErrDetails` gives its facts; shared retry, escalation and pane handling is in [Permission relay](../../docs/caller-contract.md#permission-relay) |
| `ErrPauseTimeout` | Exit keys were sent but `ended` was not observed; `ErrDetails` gives `PauseTimeoutDetails`. See [When pause cannot confirm exit](../../docs/caller-contract.md#when-pause-cannot-confirm-exit). |
| `ErrListInvalidLabel` | Label filter not in `key=value` form |

---

## See also

- [`../../docs/architecture.md`](../../docs/architecture.md) — internal architecture and topology
- [`../../docs/cli-reference.md`](../../docs/cli-reference.md) — manifest-derived CLI reference
- <https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api> — godoc index
