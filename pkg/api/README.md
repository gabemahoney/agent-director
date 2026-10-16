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

`ErrSpawnDeniedFlag` refuses an `AgentArgs` entry the supervisor must own,
before anything is written. Claude always refuses `--settings`,
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

Returns `SpawnResult` (`.InstanceID`, `.PreTrust`, `.DroppedArgs`).
`.DroppedArgs` lists removed original argument tokens in argv order on a
switching reuse, which starts fresh and resumes nothing. JSON omits
`dropped_args` when empty. Reporting details are in the
[caller contract](../../docs/caller-contract.md#spawn-and-template-input-refusals).
`.PreTrust` is
`"ok"`, `"skipped"` or `"failed"`: what the folder-trust pre-trust did, for
the selected agent (a failed pre-trust never fails the spawn; see the
`SpawnResult.PreTrust` godoc and
[docs/settings.md](../../docs/settings.md#pre-trust-for-codex)). Most-likely sentinel errors: `ErrCwdNotFound`, `ErrCwdNotADirectory`,
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

```go
res, err := c.Status("claude_2026-05-22T18-23-15")
if err != nil {
    log.Fatal(err)
}
fmt.Println(res.State) // e.g. "waiting"
```

Returns `StatusResult` (`.State`, and `.LaunchStartedAt`, the start of
the launch in progress, set only while the row is `pending`; JSON key
`launch_started_at`, omitted otherwise). Most-likely sentinel error:
`ErrSpawnNotFound`. See `(*Client).Status` godoc.

With `[state] settle_ms` set in the config the `Client` loaded when it was
built, `Status`, `Get` and `List` report the settled state: a state a hook
wrote shows only once it has held that long, the last settled state until
then; `ended`, `missing` and a state a verb wrote show at once. The other
fields, and every verb that acts on a row, follow its live state. The
package-level `api.Status`, `api.Get` and `api.List` functions use no
settle time.

For Cursor, `Client.Status`, `Client.Get` and `Client.List` can report a
launch-guarded `missing` read result even when the separate terminal write is
busy or reaches its deadline. This does not confirm durable closure;
`ended_at` remains the stored timestamp read and may be absent. The
[caller contract](../../docs/caller-contract.md#cursor-operation-boundaries)
defines caller-budget errors, request proof and recovery. Package-level
`api.Status`, `api.Get` and `api.List` do not check gateway liveness.

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
encodes as `[]` when empty. With `[state] settle_ms` set, `State` filters
on the settled state each row reports (see Status). `Limit` caps the rows
returned; 0 means no cap. Sentinel errors, each returned before the store is
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
(`ErrPaneChanged`); on a relayed Spawn a match also passes the dialog hold
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

Returns `SendKeysResult` (empty struct, reserved for future fields).
Most-likely sentinel errors: `ErrSpawnNotFound`, `ErrSpawnNotInteractive`,
`ErrInvalidFlags`, `ErrSendKeysWhileRelayed`, `ErrRelayFallenBack`,
`ErrDialogMaybeOpen`, `ErrPaneChanged`, `ErrTmuxSendKeys`,
`ErrTmuxSessionConflict`, `ErrTmuxUnresponsive` and `ErrTmuxNotAvailable`.
`api.ErrDetails(err)` gives structured permission and pane refusal facts.
Permission holds, retry rules and timeout recovery are in the
[caller contract](../../docs/caller-contract.md#permission-relay).

After the state and relay refusals, a row whose recorded tmux session name
cannot be used (empty, with a control character, or with a character tmux
stores differently) returns an error classified `ErrInternal`, with no tmux
call; nothing was sent.

See `(*Client).SendKeys` godoc.

#### `AllowPending` — reaching a `pending` launch

By default `SendKeys` rejects a `pending` row with
`ErrSpawnNotInteractive`. `AllowPending: true` also allows a `pending`
row: a launch (spawn, reuse or resume) whose agent has not reported in
yet, for example to dismiss a prompt the agent shows before it reports
in. Keys are delivered only to a session started by the row's current
launch. `ended` and `missing` rows are still rejected, with no tmux call.

A `pending` row is refused with `ErrSpawnNotInteractive` and nothing sent
when its launch start or launch token is not recorded (before any tmux
call), or when the only session found is one an earlier launch left
behind (not this launch's session). The keys reach the agent's pane, but
the caller cannot be sure the prompt it saw is still showing when they
arrive.

`LivenessNote` `unreported` means an alive agent has not reported since
launch. For action on pending rows see the
[caller contract](../../docs/caller-contract.md#caller-contract).

#### Pure `SendKeys` function

The package-level function accepts explicit store, tmux and environment
interfaces for callers that supply their own dependencies:

```go
func SendKeys(s SendKeysStore, t SendKeysTmux, env SendKeysEnv, params SendKeysParams) (SendKeysResult, error)
```

`SendKeysEnv` supplies a `RelayView`, the pane sender's `Self` identity and
`IntentHold`. Most callers use the Client method, which supplies these.
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
row's state: `find-missing` marks the row once its agent process is gone.

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

`ErrNoOpenPermissionRequest` means no open request has that token, or the
request is closed; see the
[caller contract](../../docs/caller-contract.md#permission-relay) for handling.
A request still awaiting
an answer when its Spawn ended (its agent's terminal SessionEnd, or the move
of a Codex Spawn to `ended` by `FindMissing` or by `Pause` once its agent is
gone with an end report) or when `find-missing` marked it `missing` is closed in the same write, an
undecided one denied with `DecisionReason` `ended` or `find_missing`, and
stays closed after `Resume`; `Get` and `List` no longer show it.
`GetPermission` still reads its delivery, which is `delivered` only if its
relay acknowledged a verdict.

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

The exported functions `api.Decide`, `api.Get`, `api.List` and
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
| `ErrSchemaMismatch` | DB schema is newer than the binary, or the store has no valid store id; see [Operator actions](../../docs/operator-actions.md#a-migration-write-failed-after-the-binary-swap) |
| `ErrOldLayout` | From `New`, when it is about to open the default store (no `StorePath`, no `[store] db_path` in `config.toml`), no store exists in `~/.config/agent-director` and the old directory exists. Nothing was created; take no action, a human runs this release's `install.sh` (see [Operator actions](../../docs/operator-actions.md#a-verb-refuses-with-erroldlayout)) |
| `ErrSpawnNotInteractive` | State is not a live conversational state; with `AllowPending`, a `pending` row is refused when its launch start or token is not recorded or only a session of an earlier launch is found |
| `ErrSendKeysWhileRelayed` | The Spawn's request relay may still answer (from `SendKeys` and `Pause`) — the message distinguishes an undecided request from a recorded verdict still being delivered; see [Permission relay](../../docs/caller-contract.md#permission-relay) for handling |
| `ErrRelayFallenBack` | A permission request fell back (relay settled without acknowledgement, no pane answer recorded; from `Decide`, a plain `SendKeys` and `Pause`) — `ErrDetails` gives its facts; the relevant API methods are `SendKeys` with `RequestToken` and `RecordPaneAnswer`; shared pane handling is in [Permission relay](../../docs/caller-contract.md#permission-relay) |
| `ErrDialogMaybeOpen` | A plain `SendKeys` without `ExpectPaneSHA256`, or a `Pause`, while a permission request of the Spawn is not proven gone by valid agent evidence; nothing was sent — `ErrDetails` gives its facts; shared retry, escalation and pane handling is in [Permission relay](../../docs/caller-contract.md#permission-relay) |
| `ErrListInvalidLabel` | Label filter not in `key=value` form |

---

## See also

- [`../../docs/architecture.md`](../../docs/architecture.md) — internal architecture and topology
- [`../../docs/cli-reference.md`](../../docs/cli-reference.md) — manifest-derived CLI reference
- <https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api> — godoc index
