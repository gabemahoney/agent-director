# pkg/api — Go client library for agent-director

Typed Go interface to all agent-director verbs. Same semantics as the
CLI and MCP server; no subprocess or network hop required.

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Verb examples](#verb-examples)
  - [Spawn](#spawn)
  - [Status](#status)
  - [List](#list)
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

---

## Verb examples

### Spawn

Launch a tracked Claude Code instance inside a new tmux session.
`Spawn` returns immediately with the `claude_instance_id`; the row is
`pending` from its insert until the agent reports in (Claude Code's
`SessionStart`), then `waiting`. Use `Status` or `Get` to observe progress.

```bash
# CLI: spawn in current directory, tagged for later filtering
agent-director spawn \
  --cwd "$PWD" \
  --label project=widget \
  --relay-mode on
```

```go
result, err := c.Spawn(api.SpawnParams{
    CWD:              "/tmp",
    RelayMode:        "on",
    ClaudeInstanceID: "claude_example",
    AgentDirectorLabels: map[string]string{
        "project": "widget",
    },
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(result.ClaudeInstanceID)
```

Returns `SpawnResult` (`.ClaudeInstanceID`, `.PreTrust`). `.PreTrust` is
`"ok"`, `"skipped"` or `"failed"`: what the folder-trust pre-trust did (a
failed pre-trust never fails the spawn; see the `SpawnResult.PreTrust`
godoc). Most-likely sentinel errors:
`ErrCwdNotFound`, `ErrCwdNotADirectory`, `ErrRelayModeInvalid`,
`ErrTmuxNotAvailable`, `ErrTmuxSessionCreate`, `ErrTmuxUnresponsive` (the
session-creating call timed out, here or in `Resume`: the row stays
`pending`; do not retry until `Get` shows it `ended` or `missing`; then
`Spawn` an explicit `ClaudeInstanceID` again with `ReuseFinished`, since
without it the spawn collides with the id's finished row. When the launch
did start, the row may instead stay `pending` with `LivenessNote`
`unreported`, because its agent runs: use that agent (`ReadPane`, then
`SendKeys` with `AllowPending`; only a caller that looked should type) or
end it with `Kill` and the live-row sequence and retry once `Get` shows it
`ended` or `missing`) and `ErrTmuxSessionConflict`
(an explicit id with no row whose labelled session from an earlier life
still runs, or conflicting labels; nothing is written. Also a held name:
tmux answered "duplicate session" and the requested name is held by a
session left over from an earlier life of this id, another row's session,
a session of another agent-director store, or one with no valid instance
id, or the labels conflict; the new row is ended and the error names the
blocking session. Another row's or another store's session is another
agent and is never ended; the other cases need a human, see "Operator
actions" in the [top-level README](../../README.md#operator-actions)).
After "duplicate session" the new row is ended too when the holding
session is gone by the re-lookup (`ErrTmuxSessionCreate`), the re-lookup
cannot be read (`ErrTmuxUnresponsive`), or tmux cannot be run or the
re-lookup found a different tmux server (`ErrTmuxNotAvailable`); in every
case the error says if the new row was not ended. The
`ErrTmuxSessionCreate` and `ErrTmuxUnresponsive` errors also name the
instance id (a minted one included) and the retry: that id with
`ReuseFinished` once the name is free (and, if the row was not ended,
once `Get` shows it `ended` or `missing`).
The holding session is never ended, read or typed into.
See `(*Client).Spawn` godoc for the full enumeration.

---

### Status

Return the current lifecycle state of a tracked Spawn. State values:
`pending`, `waiting`, `working`, `ask_user`, `check_permission`, `ended`,
`missing`.
`pending` means a launch (spawn, reuse or resume) is in progress and the
agent has not reported in yet (Claude Code's `SessionStart`); it may be
loading or waiting at a startup prompt. A resumed `pending` row keeps its
session id and history; a caller tells it from a fresh one by its non-empty
`claude_session_id`, shown by `Get`.

```bash
agent-director status \
  --claude-instance-id claude_2026-05-22T18-23-15
```

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

---

### List

Enumerate Spawn rows matching a filter set. All filters AND together;
state values OR together. Returned order is unspecified — sort with
`jq` or in Go as needed.

```bash
# CLI: running spawns tagged project=widget
agent-director list \
  --state waiting,working \
  --label project=widget
```

```go
res, err := c.List(api.ListParams{
    State:  []string{"waiting", "working"},
    Labels: []string{"project=widget"},
})
if err != nil {
    log.Fatal(err)
}
for _, row := range res.Spawns {
    fmt.Println(row.ClaudeInstanceID, row.State)
}
```

Returns `ListResult` (`.Spawns []ListRow`). `Spawns` is never nil —
encodes as `[]` when empty. Most-likely sentinel error:
`ErrListInvalidLabel` (label not in `key=value` form). See
`(*Client).List` godoc.

---

### SendKeys

Send text into the agent's own pane. CR bytes (`\r`, `0x0D`) are
stripped automatically before delivery to prevent premature buffer
submission; LF bytes (`\n`, `0x0A`) are preserved as composed newlines
in Claude's input box. A single Enter is appended to submit the buffer
unless `NoEnter`; an empty `Text` sends that Enter only, submitting what
is already typed. `Key` sends one key alone instead (a named key `Escape`,
`Enter`, `Up`, `Down`, `Tab`, or one character), with no Enter. With
`ExpectPaneSHA256` (the `PaneSHA256` of the `ReadPane` you looked at, with
its `NLines`) the pane is checked first and nothing is sent if it changed
(`ErrPaneChanged`); on a relayed Spawn a match also passes the dialog hold
(`ErrDialogMaybeOpen`, below). Set it only for a pane a person or an LLM
judged, never from an automatic flow. There is no flag to suppress CR
stripping — the behavior is unconditional by design (SRD §4.3).

```bash
agent-director send-keys \
  --claude-instance-id claude_2026-05-22T18-23-15 \
  --text "what is 2+2?"
```

```go
_, err := c.SendKeys(api.SendKeysParams{
    ClaudeInstanceID: "claude_2026-05-22T18-23-15",
    Text:             "what is 2+2?",
})
if err != nil {
    log.Fatal(err)
}
```

Returns `SendKeysResult` (empty struct, reserved for future fields).
Most-likely sentinel errors:

- `ErrSpawnNotFound`: no row has this id.
- `ErrSpawnNotInteractive`: the state is not `waiting/working/ask_user/
  check_permission` (a `pending` row needs `AllowPending`, below); nothing
  was sent.
- `ErrInvalidFlags`: the params' shape is refused (for example `Key` with
  a non-empty `Text`, `NoEnter` with neither, `As` without
  `RequestToken`, a malformed hash); nothing was read or sent.
- `ErrSendKeysWhileRelayed`: relay_mode=on, in any live state, and a
  relay hook of the row may still answer its request (its process runs,
  or it cannot be checked and the request's `confirm_by` has not passed);
  the relay still owns the answer and nothing was sent. The message names
  that request and states no release time. If it is undecided, it says to
  answer it with decide (`… on request <request_token>; answer it with
  decide`), and `Decide` answers it. If its verdict is already recorded,
  it says to retry later (`… the relayed permission verdict on request
  <request_token> is recorded and its relay hook may still be delivering
  it; retry send-keys later`); `Decide` on it would return
  `ErrAlreadyDecided`. Once that hook has ended, a plain retry can get
  `ErrDialogMaybeOpen` until Claude Code proves the request gone.
- `ErrRelayFallenBack`: a plain call (no `RequestToken`) while a request
  of the row has fallen back with no pane answer recorded (its relay hook
  is gone and acked no verdict); nothing was sent. `api.ErrDetails(err)`
  is a `RelayFallenBackDetails` naming that request (the oldest) and the
  row's other open requests. Close it with a pane answer or with
  `RecordPaneAnswer` (see [Permission relay](#permission-relay)).
- `ErrDialogMaybeOpen`: a plain call without `ExpectPaneSHA256` while a
  permission request of the row is not proven gone by Claude Code's hooks
  (its tool's PostToolUse, the main agent's end of turn after it, or the
  agent's end), whatever agent-director's records say of it; nothing was
  sent. `api.ErrDetails(err)` is a `DialogMaybeOpenDetails` naming that
  request (the oldest), the row's `State` and its other
  `UnprovenRequests`. Retry later; if it stays held, have a person or an
  LLM look at the pane and send with its hash (see
  [Permission relay](#permission-relay)).
- `ErrPaneChanged`: `ExpectPaneSHA256` no longer matches the pane;
  nothing was sent. Read the pane again: the error does not carry the new
  hash.
- `ErrTmuxSendKeys`: the row's session or pane is not there.
- `ErrTmuxSessionConflict`: the agent's pane was not found, a session an
  earlier launch left behind is there on a live row, or tmux holds
  conflicting labels; nothing was sent.
- `ErrTmuxUnresponsive`: tmux did not answer usably; after a timed-out
  send the keys may have been delivered, and after a failed Enter the
  text may be typed but not submitted. Then the error names the next step
  instead of "retry later", because the same call would type the text a
  second time: after a timed-out text call, `ReadPane` and, if the text
  is typed, `SendKeys` with empty `Text`, otherwise the same `SendKeys`;
  after a failed or timed-out Enter, `SendKeys` with empty `Text`, which
  submits it.
- `ErrTmuxNotAvailable`: tmux could not be run, its socket is not
  accessible to this user, or this is not the tmux server the agent was
  launched on.

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

A `pending` row whose `LivenessNote` is `unreported` (written by
`FindMissing`) has a running agent that has not reported in since its
launch: it may show a Claude Code startup screen or sit idle at its
prompt. Read it with `ReadPane` first and send with `AllowPending` only
having looked; without it the row is refused like any `pending` row. A
caller that cannot judge the pane ends the launch with `Kill` and the
live-row sequence (see `Kill` below) or hands it to a human. A later
`FindMissing` that finds the agent alive keeps the note; the agent's next
hook clears it.

#### Pure `SendKeys` function

`(*Client).SendKeys` is a thin wrapper over a pure package-level function
that takes how it judges the relay as an explicit `SendKeysEnv`, so the
relay guard's verdict is deterministic and testable:

```go
func SendKeys(s SendKeysStore, t SendKeysTmux, env SendKeysEnv, params SendKeysParams) (SendKeysResult, error)
```

`SendKeysStore` is the row read, the permission-request read, the
conditional adoption write, the `hook_gone_at` write, a pane answer's three
writes (`RecordPaneIntent`, `RecordPaneSent`, `ReleasePaneIntent`) and the
store id; `SendKeysTmux` is the lookup, the pane listing, the text and the
named-key sends by pane id and the capture `ExpectPaneSHA256` is checked
against. `SendKeysEnv` holds a `RelayView` (the start-time reader, which
also checks the agent process, the reader's own pid namespace, the clock
and the relay window), `Self` (the identity a pane answer records as its
sender) and `IntentHold` (how long an intent whose sender cannot be
checked counts as in progress). The `Client` method passes its store, tmux
client and its own env: its start-time reader, pid namespace and clock,
the window from `cfg.Relay.EffectiveTimeoutSeconds()` (the single source
for the window: a missing or 0 `relay.timeout_seconds` gives the default,
and `New` refuses a config whose value is negative or above 2147483), its
own process as sender, and the `[tmux]` action timeout plus the pipe-close
wait plus 2 s as `IntentHold`; then it records the call on the
`ad.send_keys.called` trail event. Most callers use the `Client` method;
the pure function is for tests and callers that need to control the relay
judgement.

```go
_, err := c.SendKeys(api.SendKeysParams{
    ClaudeInstanceID: "claude_2026-05-22T18-23-15",
    Text:             "",       // press Enter to dismiss the prompt
    AllowPending:     true,
})
if err != nil {
    log.Fatal(err)
}
```

---

### ReadPane

Capture the last N lines of the agent's own pane. Default 25 lines;
no upper cap. ANSI escape codes are stripped by default (pass `ANSI: true`
to get raw bytes).

```bash
agent-director read-pane \
  --claude-instance-id claude_2026-05-22T18-23-15 \
  --n-lines 50
```

Call `c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: id, NLines: 50})`.
Returns `ReadPaneResult` (`.Pane` string, and `.PaneSHA256`, the SHA-256 in
lowercase hex of exactly the bytes of `.Pane`, which `SendKeys` and
`RecordPaneAnswer` take back as `ExpectPaneSHA256` with the same `NLines`;
the hash of an `ANSI: true` read never matches). `ReadPane` reads only this
agent's pane (or, with no session of the current launch, the pane of the
one session an earlier launch of this row left behind) and changes
nothing. Most-likely sentinel errors:

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
and succeeds only once the agent process is gone, waiting up to
`kill_exit_wait_ms`. When no session of the launch is found, the agent
process decides: gone, or none recorded, is success with nothing sent.
Kill never signals a process itself and never ends a session an earlier
launch left behind.

A finished row (`ended`, `missing`) is a no-op success with no tmux call;
that is not verification that the agent exited. Kill never changes the
row's state: `find-missing` marks the row once its agent process is gone.

```bash
agent-director kill \
  --claude-instance-id claude_2026-05-22T18-23-15
```

```go
res, err := c.Kill(api.KillParams{
    ClaudeInstanceID: "claude_2026-05-22T18-23-15",
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
| `ErrTmuxKillFailed` | UNAVAILABLE | The agent process, or another process of the session's panes, still ran after the kill exit wait; or it cannot be checked and its labelled session is still there; or no session or pane of this launch was found while it runs. Retry later; never delete the row |
| `ErrTmuxUnresponsive` | UNAVAILABLE | tmux did not answer usably, before or after a kill was sent. Retry later with backoff |
| `ErrTmuxSessionConflict` | CONFLICT | The session found is not this launch's session, or tmux holds conflicting labels; no kill was sent. A human must look: see "Operator actions" in the [top-level README](../../README.md#operator-actions) |
| `ErrTmuxNotAvailable` | ENVIRONMENT | tmux could not be run, its socket is not accessible to this user, or this is not the tmux server the agent was launched on |

A live row whose recorded tmux session name cannot be used (empty, with a
control character, or with a character tmux stores differently) returns an
error classified `ErrInternal`, with no tmux call. None of these errors
means that the agent is dead.

A repeated kill right after the last session on its tmux server ends can
get `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable` while the server exits;
the caller waits and checks again.

```go
_, err := c.Kill(api.KillParams{ClaudeInstanceID: id})
switch {
case err == nil:
    // the agent process is gone
case errors.Is(err, api.ErrTmuxKillFailed), errors.Is(err, api.ErrTmuxUnresponsive):
    // retry later with backoff; never delete the row
case errors.Is(err, api.ErrTmuxSessionConflict):
    // stop and surface it to a human
}
```

The caller must run as the same user and in the same tmux environment as
the agents. What to do next with a stuck live row is the live-row sequence
in the top-level README's
[Caller contract](../../README.md#caller-contract). See `(*Client).Kill`
godoc.

---

## Permission relay

`(*Client).Decide` records a verdict on a relayed permission request, waits
at most 1 s for the agent's relay hook to confirm it, and returns a
`DecideResult`, which embeds `RequestDelivery`: read `Delivery`
(`delivered` or `not_confirmed`), not the recorded decision. On
`not_confirmed`, poll `GetPermission` until `Delivery` reads `delivered` or
`fallen_back`, which it does by `ConfirmBy`.

`DecideParams.MaxWaitMs` bounds the whole call, its reads and its write,
and has no default; pass at most your deadline minus 1 s. A bound reached
before the verdict is recorded returns `ErrStoreBusy` with nothing
recorded, whether the store was held by another process or by another
call on the same `Client` (a `Client` uses one store connection).
Once the verdict is recorded, `Decide` never returns `ErrStoreBusy`.

`ErrNoOpenPermissionRequest` means no open request has that token, or the
request is closed: do not answer it at the pane. A request still awaiting
an answer when its Spawn ended (its agent's terminal SessionEnd) or when
`find-missing` marked it `missing` is closed in the same write, an
undecided one denied with `DecisionReason` `ended` or `find_missing`, and
stays closed after `Resume`; `Get` and `List` no longer show it.
`GetPermission` still reads its delivery, which is `delivered` only if its
relay hook confirmed a verdict.

`ErrRelayFallenBack` means the request's relay hook is gone and acked no
verdict, and no pane answer is recorded on it through agent-director.
`Decide` returns it for that request, and a plain `SendKeys` for the
Spawn's oldest such request. `api.ErrDetails(err)` returns its facts as a
`RelayFallenBackDetails`: the request's `PermissionRequestInfo`, the
Spawn's `State`, and `OpenRequests`, the Spawn's other open requests. It
is closed in one of three ways, each recorded in `PaneAnswer` (`PaneAs`
holds the caller's claim, never checked):

- **A pane answer**: `SendKeys` with `RequestToken`, `As` (`"allow"` or
  `"deny"`), `Key` (the one key, sent with no Enter) and
  `ExpectPaneSHA256` (the `PaneSHA256` of the `ReadPane` you looked at;
  never set it from an automatic flow). It records its intent before the
  key and `PaneAnswer` `"sent"`, `Decision` `As` and `DecisionReason`
  `"pane"` after it. `ErrPaneAnswerInProgress`: another pane answer to the
  request is still being sent (do nothing). A retry after a sender that
  died needs a fresh hash. An error classified `ErrInternal` whose
  `ErrDetails` is a `PaneKeySentDetails` (`KeySent` true) means the key
  was sent but not recorded: read the pane before you send anything
  again, and close the request with `RecordPaneAnswer` if it shows the
  request answered.
- **`RecordPaneAnswer`**, when it was answered outside agent-director (it
  types nothing): `RecordPaneAnswerParams{RequestToken, As, ExpectPaneSHA256,
  NLines}`, `As` `"allow"`, `"deny"` or `"unknown"` (pass `"unknown"`
  unless you saw the answer). It records `PaneAnswer` `"outside"`,
  `Decision` the claim (nil for `"unknown"`) and `DecisionReason`
  `"pane_outside"`. `ErrClaimTooSoon`: the relay hook may still answer or
  has not been gone 2 s yet; retry at the `NotBefore` of its
  `ClaimTooSoonDetails` (nil only while the hook is seen running).
- **The agent's PostToolUse or PostToolUseFailure** for the request's
  tool, which records
  `PaneAnswer` `"tool_ran"`, `Decision` `"allow"`, `DecisionReason`
  `"tool_ran"`.

None of these, nor the relay hook's ack (`Delivery` `delivered`), proves
that Claude Code's dialog for the request is gone. Only Claude Code's own
hooks do, recorded on the request as `ProvenGoneAt` and `ProvenGoneHow`
(`"tool_ran"`: its tool's PostToolUse; `"turn_end"`: the turn of the agent
that asked ended after it was written, which the main agent's Stop or idle
prompt proves for a request with no agent id; `"agent_gone"`: the Spawn's
end). Until then a plain `SendKeys` without `ExpectPaneSHA256`
returns `ErrDialogMaybeOpen`: after an allow until the tool has run, after
a deny until the main agent's turn ends. `Get`'s `SpawnRow.UnprovenRequests`
lists every request not proven gone, each with `UnprovenSince` (when
agent-director's records stopped awaiting an answer); one that stays
unproven a while is a reason to read the pane. A plain `SendKeys` whose
`ExpectPaneSHA256` matches the pane passes the hold: it means a person or
an LLM judged that screen, so never set it from an automatic flow. A
subagent's denied request is proven only by the Spawn's end.

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
`api.GetPermission` take a `RelayView`, and `api.SendKeys` and
`api.RecordPaneAnswer` an env holding one (the `Client` methods pass their
own); `DecideResult` embeds `RequestDelivery`.

---

## Version mapping

`pkg/api` and the CLI binary use a **1:1 lock**: `pkg/api@v0.X.Y`
requires CLI binary `v0.X.Y`. The two components share an envelope shape
— the JSON structures the library reads and writes are identical to those
the binary produces. Mixing versions risks silent field mismatches that
are not detectable at compile time.

> **Note (recommended-defer):** the version-coupling policy is marked
> "recommended-defer" in the SRD Open Questions for Plan bee `b.qe2`.
> This section may need updating if the policy changes. See Plan bee
> `b.qe2` for the current status.

---

## Errors

All errors returned by `pkg/api` are sentinel values. Use `errors.Is`
to inspect them — never compare error strings.

```go
res, err := c.SendKeys(api.SendKeysParams{
    ClaudeInstanceID: id,
    Text:             "hello",
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
| `ErrSpawnNotFound` | No row for the given `claude_instance_id` |
| `ErrClientClosed` | Called after `Close()` |
| `ErrStoreNotInitialized` | Store file absent and `CreateIfMissing` is false |
| `ErrSchemaMismatch` | DB schema is newer than the binary, or the store has no valid store id — install the matching binary for a newer store; restore the pre-install copy of `state.db` for a store with no valid id. Never delete `state.db` |
| `ErrSpawnNotInteractive` | State is not a live conversational state; with `AllowPending`, a `pending` row is refused when its launch start or token is not recorded or only a session of an earlier launch is found |
| `ErrSendKeysWhileRelayed` | A relay hook of the Spawn may still answer its request — answer the undecided request the message names with `Decide`, or, when the message says its verdict is recorded, retry `SendKeys` later |
| `ErrRelayFallenBack` | A permission request fell back (its relay hook is gone, no pane answer recorded) — answer it at the pane with `SendKeys` and its `RequestToken`, or close it with `RecordPaneAnswer`; `ErrDetails` gives its facts |
| `ErrDialogMaybeOpen` | A plain `SendKeys` without `ExpectPaneSHA256` while a permission request of the Spawn is not proven gone by Claude Code's hooks; nothing was sent — retry later, or have a person or an LLM look and send with the pane's hash; `ErrDetails` gives its facts |
| `ErrListInvalidLabel` | Label filter not in `key=value` form |

---

## See also

- [`../../docs/architecture.md`](../../docs/architecture.md) — internal architecture and topology
- [`../../docs/cli-reference.md`](../../docs/cli-reference.md) — manifest-derived CLI reference
- Plan bee `b.qe2` — the plan governing this library's public API surface (bee tickets are not on GitHub)
- [`../../docs/test-writing-guide.md`](../../docs/test-writing-guide.md) — for contributors writing tests against this package
- <https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api> — godoc index
