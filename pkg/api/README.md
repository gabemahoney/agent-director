# pkg/api — Go client library for agent-director

Typed Go client for agent-director. Shared behavior and recovery rules are in
the [caller contract](../../docs/caller-contract.md).

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Verb examples](#verb-examples)
  - [Spawn](#spawn)
  - [Status](#status)
  - [GetMany](#getmany)
  - [List](#list)
  - [ListSubagents](#listsubagents)
  - [SendKeys](#sendkeys)
  - [ReadPane](#readpane)
  - [Kill](#kill)
  - [Sweep](#sweep)
  - [Pause](#pause)
  - [ForceDelivery](#forcedelivery)
  - [GetForce and GetDelivery](#getforce-and-getdelivery)
- [Permission relay](#permission-relay)
- [Version mapping](#version-mapping)
- [Errors](#errors)
- [See also](#see-also)

---

## Install

Requires **Go 1.27.3** or later.

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

`Version` reads build metadata without store or tmux access. Use `List` to
check store access.

`Close` releases this client's store. Audit reporting belongs to the host
process lifetime; closing a client does not drain accepted process audit
events or certify their durability.

---

## Verb examples

### Spawn

Launch a tracked agent inside a new tmux session. `Spawn` returns immediately
with the `instance_id`; use `Status` or `Get` to observe progress.

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

Set `SpawnParams.Agent` to `"claude"`, `"codex"` or `"cursor"` (case-sensitive),
or use a template that names its `Agent`. `MakeTemplateParams.Agent` sets the
template's agent. Argument merging is defined in
[Settings](../../docs/settings.md#templates-and-per-call-merge).

Go applications launching or resuming Cursor require an installed
`$HOME/.config/agent-director/bin/agent-director` whose resolved target is a
regular executable. See [Installation](../../docs/install.md). Cursor uses
`Options.StorePath` too; no separate gateway or store parameter is needed.

`SpawnParams.ReuseFinished` starts a fresh conversation on a finished id;
`Resume(api.ResumeParams{InstanceID: id})` continues the latest recorded
session. See [life rules](../../docs/usage.md#naming-the-agent) and
[Pause and resume](../../docs/usage.md#pause-and-resume). Cursor startup and
asynchronous failures are covered by
[Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries).

`SpawnParams.IdleScreenTimeoutSeconds` gives the row its own `idle_screen`
timeout in whole seconds (0 for none); its range and lifecycle are in
[Configuration](../../docs/configuration.md#idle-screen-timeout-state).

Claude's defaults are Wait Turn and unconfirmed delivery; resume carries Held
Messages without replaying startup input.

`SpawnParams`, `ResumeParams` and `MakeTemplateParams` take the Agent Defaults
`SendMode` and `ConfirmedDelivery` and the experimental
`ConfirmFromTranscripts` ([Confirm From Transcripts](../../docs/caller-contract.md#confirm-from-transcripts)).
Each is a pointer: nil leaves it unset, and a pointer
to any value, `false` or `"wait_turn"` included, sets it. They apply only to
Covered Agents; see
[Busy-send options](../../docs/caller-contract.md#busy-send-options).

Returns `SpawnResult`:

- `InstanceID`: the tracked id.
- `DroppedArgs`: removed original argument tokens in argv order on a switching
  reuse; omitted from JSON when empty. See
  [input refusals and reporting](../../docs/caller-contract.md#spawn-and-template-input-refusals).
- `PreTrust`: `"ok"`, `"skipped"` or `"failed"`. This does not prove readiness.
  See [Codex pre-trust](../../docs/settings.md#pre-trust-for-codex) and
  [Cursor launch settings](../../docs/settings.md#cursor-launch-settings).

Input errors include `ErrAgentMissing`, `ErrAgentInvalid`,
`ErrAgentArgsMismatch`, `ErrTemplateAgentMissing`, `ErrPermissionsUnsupported`,
`ErrSpawnDeniedFlag` and `ErrCantSend`; meanings and handling are in
[Spawn and template input refusals](../../docs/caller-contract.md#spawn-and-template-input-refusals).
Other common errors: `ErrCwdNotFound`, `ErrCwdNotADirectory`,
`ErrRelayModeInvalid`, `ErrTmuxNotAvailable`, `ErrTmuxSessionCreate`,
`ErrTmuxUnresponsive` and `ErrTmuxSessionConflict`.
See `(*Client).Spawn` godoc for the full enumeration.

---

### Status

Return the current lifecycle state of a tracked Spawn. State values:
`pending`, `waiting`, `working`, `ask_user`, `check_permission`, `ended`,
`missing`.
For launch readiness and startup prompts, see the
[caller contract](../../docs/caller-contract.md#pending-rows-and-startup-screens).

Client `Status`, `Get`, `GetMany`, `List` and `Sweep` (also `FindMissing`) share
[bounded recovery](../../docs/caller-contract.md#bounded-recovery-on-state-reads).
Package-level helpers and custom stores run only checks their supplied
capabilities support.

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

`Get`, each `GetMany` row and `Status` expose `FailureReason *string`
(JSON `failure_reason`, nil encodes as `null`); `List` omits it. Claude Code
and Codex always return nil. See [Failure reasons](../../docs/caller-contract.md#failure-reasons)
for recording support and behaviour.

`StatusResult` and `Get`'s `SpawnRow` embed `StateDetail`:
`DisplayedState` (`api.StateIdleScreen` or the defined-but-unused
`api.StateNeedsIntelligence`), `Since string` and `UnchangedMinutes *int64`.
`StatusResult.TmuxSessionName` identifies the session; `ListRow` carries
`DisplayedState` without detail. Non-applicable display fields are empty/nil
and omitted from JSON. Test `DisplayedState`, never `State`, for a display.

`StatusResult`, `SpawnRow` and `ListRow` also carry:

- `IdleScreenAvailability` (JSON `idle_screen_availability`):
  `api.IdleScreenAvailable` for Claude/Codex, `api.IdleScreenUnavailable` for
  Cursor. Agentless custom-store rows report available but cannot be sampled.
- `FamilyState string` (JSON `family_state`): `"working"` or `"waiting"`;
  [Usage](../../docs/usage.md#family-state) defines aggregation and
  [Family reporting limits](../../docs/caller-contract.md#family-reporting-limits)
  defines freshness.
- `ProcessLiveness string` (JSON `process_liveness`): `"alive"`, `"exited"`
  or `"unknown"`.
- `Confirmations api.Confirmations` (JSON `confirmations`): exactly `State`,
  `ProcessLiveness` and `DisplayedState`, each an `api.Confirmation` with
  `Status` and nullable `*string` fields `ConfirmedAt`, `Source`, `CheckedAt`
  and `Reason`. Nil fields encode as `null`, never omitted.

Display fields use JSON names `displayed_state`, `since`, `unchanged_minutes`
and `tmux_session_name`. Their meanings are in
[Displayed states](../../docs/caller-contract.md#needs-intelligence-and-idle-screen-rows)
and [Confirmations](../../docs/caller-contract.md#confirmations).
Stores without evidence report unknown records. On error, the zero result's
`ProcessLiveness` and confirmation `Status` fields are empty strings.

`SpawnRow` and `ListRow`, but not `StatusResult`, expose
`StateChangedAt time.Time` (JSON `state_changed_at`); see
[State change time](../../docs/caller-contract.md#state-change-time).

`Client.Status`, `Client.Get`, `Client.GetMany` and `Client.List` use the config
loaded at construction for [state and family settling](../../docs/configuration.md#settle-time-state).
The package-level `api.Status`, `api.Get` and `api.List` functions use no
settle time and sample no pane. Package-level `Sweep` (and `FindMissing`) can
sample when its tmux provider supports capture. Gateway liveness checks need
the store capabilities and `RelayView.Procs`; read-versus-write guarantees are
in [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries).

---

### GetMany

`Get` and each found `GetMany` row expose `BusySend`; covered rows also have
`AgentDefaults`, `Delivery` and `DeliveryCapabilities`. `Status` has defaults
and delivery facts; `List` has defaults. Reads deliver nothing. See
[Busy-send coverage](../../docs/caller-contract.md#busy-send-coverage).
`DeliveryCapabilities.Force` is a `*DeliveryForceCapability` (nil when none)
whose `States` are `DeliveryForceState` rows
([Claude Code force capability](../../docs/caller-contract.md#claude-code-force-capability)).
`ConfirmFromTranscriptsCapability` is one of the `api.ConfirmFromTranscripts*`
constants, with `ConfirmFromTranscriptsReason`
([Confirm From Transcripts](../../docs/caller-contract.md#confirm-from-transcripts)).

```go
	result, err := c.GetMany([]string{"agent-b", "unknown", "agent-a"})
	if err != nil {
		log.Fatal(err)
	}
	for _, row := range result.Spawns {
		fmt.Println(row.InstanceID, row.State)
	}
	fmt.Println("Not found:", result.NotFound)
```

Returns `GetManyResult`: each `Spawns` element is exactly `Get`'s `SpawnRow`,
with the fields and guarantees described here. `Spawns` and `NotFound` are
never nil, and both are empty on every failure.

Input refusals are `ErrInvalidFlags` and `ErrTooManyIds`; for the latter,
`api.ErrDetails(err)` returns `TooManyIdsDetails` with the `IdCap` loaded
when the Client was built. Ordering, read failures and polling are defined in
the [caller contract](../../docs/caller-contract.md#reading-many-agents).

---

### List

Enumerate Spawn rows matching a filter set. All filters AND together;
state values OR together. Returned order is unspecified.

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
Selection and recovery follow the
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

Returns `ListSubagentsResult` with `Tracked` and `Subagents []SubagentRecord`
(never nil). Unknown fields are nil pointers encoded as JSON `null`; times
are UTC `*time.Time`. `ErrSpawnNotFound` and other errors return `Tracked`
false and an empty slice. Agent support, fields and retention are defined in
[Read an agent's subagent records](../../docs/usage.md#read-an-agents-subagent-records).
The package-level `api.ListSubagents(ListSubagentsStore, id)` takes a store,
not a `RelayView`. See `(*Client).ListSubagents` godoc.

---

### SendKeys

Behavior follows `Get`/`GetMany`'s `BusySend`. Covered Claude stores an ordinary
Message byte for byte; CR is refused, empty text and raw-key parameters are
malformed, and `AllowPending` changes nothing. Busy, permission and dialog
boundaries hold accepted Messages instead of refusing or typing them.
A successful send returns `SendKeysResult.Delivery *DeliveryResult`, never
`ErrSubmissionUnconfirmed`; shared fields such as `Class` and `RequestID`
repeat the Result for Go callers. See [Covered sends](../../docs/caller-contract.md#covered-sends).

This send uses the target's defaults:

```go
_, err := c.SendKeys(api.SendKeysParams{
    InstanceID: "claude_2026-05-22T18-23-15",
    Text:       "what is 2+2?",
})
if err != nil {
    log.Fatal(err)
}
```

For a confirmed Wait Turn Message, keep your pair for later reads or a repeat:

```go
mode, confirmed := "wait_turn", true
caller := "7af00f9e-f42d-4a24-9cc1-c9697d17c261"
sent, err := c.SendKeys(api.SendKeysParams{
    InstanceID: id, Text: "Review the project entry points",
    SendMode: &mode, ConfirmedDelivery: &confirmed, CallerUUID: &caller,
    DeliveryRequestID: "review-1",
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(sent.Delivery.Class, sent.Delivery.Final)
lookup, err := c.GetDelivery(api.GetDeliveryParams{
    CallerUUID: caller, DeliveryRequestID: "review-1",
})
if err != nil {
    log.Fatal(err)
}
if lookup.Result != nil {
    fmt.Println(lookup.Lookup, lookup.Result.Class, lookup.Result.Final)
}
```

The default is unconfirmed: no pair and no lookup. Result semantics and safe
repeat rules live in the [caller contract](../../docs/caller-contract.md#message-results).

Uncovered Codex, Cursor and pane-only agents keep their native/manual paths.
For those agents CR is stripped, LF preserved and one Enter appended unless
`NoEnter`; empty `Text` sends Enter only and `Key` sends one key alone.
`ExpectPaneSHA256`, with the inspected `ReadPane`'s `NLines`, guards that
exact screen. A supported native Codex send returns `Outcome: "submitted"`
and receipt fields; fallback and manual success return an empty result.
Their errors include `ErrSubmissionUnconfirmed`, state, relay and dialog
refusals. See [Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission),
[Manual input ownership](../../docs/caller-contract.md#manual-input-ownership)
and [Permission relay](../../docs/caller-contract.md#permission-relay).

`SendMode`, `ConfirmedDelivery`, `CallerUUID` and `Force` are pointers with
the same rule as Spawn's Agent Defaults: nil omits one, and any value names
it. They apply only to Covered Agents; when the target's current launch is not
covered, a call naming one
returns `ErrCantSend` (see [Errors](#errors)) and sends nothing. See
[Busy-send options](../../docs/caller-contract.md#busy-send-options).

With `Force: &force` (`force := true`), an accepted send also forces the
agent's Held Messages, as [ForceDelivery](#forcedelivery) does, and blocks for
up to about 15 seconds: `SendKeysResult.Force` is that attempt's
`*ForceResult`, nil for any other send, and its JSON is `force_result`. Such
a send can fail after its Message was accepted; see
[Stored force attempts](../../docs/caller-contract.md#stored-force-attempts).

`SendKeysContext(ctx, params)` uses the caller's context; `SendKeys` uses a
background context. A cleanup error can accompany a confirmed result;
`api.ErrDetails(err)` retains that result. See `(*Client).SendKeys` godoc and
the [caller contract](../../docs/caller-contract.md#ordinary-text-submission)
for cancellation and retry rules.

#### `AllowPending` — reaching a `pending` launch

For covered Claude, `AllowPending` is ignored. For uncovered agents, set
`AllowPending: true` to allow input to a pending launch, for example a
startup choice with `Key: "Enter"`. It defaults to false and does not bypass
ownership, readiness, relay or dialog guards. See
[Pending rows and startup screens](../../docs/caller-contract.md#pending-rows-and-startup-screens).
`ErrSubmissionUnconfirmed` carries `SubmissionUnconfirmedDetails` through
`api.ErrDetails(err)`: `RequestID`, `Outcome`, `Phase`, `BytesAttempted`,
`EnterMayHaveBeenSent`, optional `LastQueue`, `Reason`, `KeysSent`,
`SubmissionConfirmed` and `RowState`. A failed fallback hook observation has
an empty `Outcome` and serializes only `keys_sent: true`,
`submission_confirmed: false` and `row_state`. See
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission)
for their meaning and recovery.

#### Pure `SendKeys` function

The package-level function accepts explicit store, tmux and environment
interfaces for callers that supply their own dependencies:

```go
func SendKeys(s SendKeysStore, t SendKeysTmux, env SendKeysEnv, params SendKeysParams) (SendKeysResult, error)
```

`SendKeysEnv` supplies `RelayView`, the sender's `Self` identity, `IntentHold`,
optional `Context`, optional `Input` (`InputDeliveryBackend`, nil uses native
delivery), and optional `Sleep`. Nil `Sleep` uses context-cancellable timers,
or `time.Sleep` without `Context`. The Client method supplies these.
Custom `SendKeysStore` implementations must provide `GetSpawnWithin`,
bounding connection and lock waits by the supplied duration.
Custom tmux adapters supplied to this function, to `ReadPane` or
`RecordPaneAnswer`, or through `Options.TmuxClient` must return from
`CapturePaneID` the last `nLines` rows of the pane, counted up from the last
row with content: blank rows below it are left out, blank rows between content
rows count, and a row is blank when it is whitespace only once ANSI escape
sequences are stripped. `read-pane` returns that text and the
`expect_pane_sha256` checks hash it, so passing tmux's `capture-pane -S -<n>`
output through unchanged (history plus the whole screen) breaks `n_lines`.
They also need these optional capabilities for the corresponding fallback
paths:

- `PasteKeysTmux` supports ordinary Codex fallback:
  `PasteKeysPane(socket, paneID, text string) error`. It preserves text and LF in
  one paste, sends one Enter only after paste succeeds, and never retries input.
  Failures use `*TmuxCallError` with the failed text or Enter call.
- `GuardedPasteKeysTmux` is also required for journal-backed Codex fallback:
  `PasteKeysPaneGuarded(socket, paneID, text string, beforeWrite func(enter bool) error) error`.
  It invokes the guard immediately before paste and Enter and stops on refusal,
  with the same framing and error rules as `PasteKeysTmux`.
Adapters without a needed paste capability receive a pre-input `not_started`
refusal on that fallback path. The default client provides both.
See their godoc for each field's contract.

```go
_, err := c.SendKeys(api.SendKeysParams{
    InstanceID:   "codex_example",
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
the hash of an `ANSI: true` read never matches).
Common errors: `ErrInvalidFlags` (negative `NLines`), `ErrSpawnNotFound`,
`ErrTmuxCaptureFailed`, `ErrTmuxSessionConflict`, `ErrTmuxUnresponsive`,
`ErrTmuxNotAvailable` and `ErrInternal`. See `(*Client).ReadPane` godoc and
the [caller contract](../../docs/caller-contract.md).

#### `AllowPending` — surface symmetry with `send-keys`

`ReadPane` has no state guard: it can read `pending`, `ended` or `missing`
rows if their pane is still present. `AllowPending` is accepted but has no
effect.

---

### Kill

End the agent of a live row's current launch (`pending` included).
Finished rows (`ended`, `missing`) are a no-op success, not exit verification.
`Kill` does not change row state. The
[caller contract](../../docs/caller-contract.md#caller-contract) defines exit
checks and the live-row sequence.

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

| Sentinel | Meaning |
|---|---|
| `ErrSpawnNotFound` | No row has this id |
| `ErrTmuxKillFailed` | Exit could not be confirmed |
| `ErrTmuxUnresponsive` | tmux did not answer usably |
| `ErrTmuxSessionConflict` | Session ownership conflicts |
| `ErrTmuxNotAvailable` | The tmux environment is unavailable |
| `ErrInternal` | The recorded session name cannot be used |

Each tmux error's class is in the
[caller contract](../../docs/caller-contract.md#caller-contract).

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

---

### Pause

`c.Pause(ctx, api.PauseParams{InstanceID: id})` is keyless for covered Claude
in any live state. It preserves Held Messages for resume and returns
`ErrSpawnNotPausable` with `NotPausableDetails` on refusal; see
[keyless pause](../../docs/caller-contract.md#keyless-pause-of-a-covered-agent).
For uncovered agents it types exit only while waiting and can return
`ErrPauseTimeout` (see [Errors](#errors)). A finished row is a no-op.

`PauseParams.OnlyIfWaiting: true` currently returns `ErrInternal` on a Covered
Agent, ending nothing; see [Busy-send options](../../docs/caller-contract.md#busy-send-options).
When the row's current launch is not covered, `true` returns `ErrSpawnNotPausable` in any row
state, with
`api.ErrDetails(err)` a `NotPausableDetails` whose `Cause` is
`"agent_not_covered"`; nothing is ended. `false` is the default pause.

---

### ForceDelivery

`res, err := c.ForceDelivery(api.ForceDeliveryParams{InstanceID: id})` forces
a Covered Agent's Held Messages and blocks for up to about 15 seconds. It
returns `ForceDeliveryResult`, whose `ForceResult` has `ForceID`, `Outcome`
(`api.ForceOutcomeHandedOver`, `ForceOutcomePending`, `ForceOutcomeRefused` or
`ForceOutcomeFailed`), `Reason`, `StartedAt`, `EndedAt` (`*time.Time`, nil
while pending), `CarriedPairs` and `CarriedResults` (`[]DeliveryResult`; both
never nil). Keep `res.ForceResult.ForceID` for `GetForce`. Outcomes, reasons
and errors are in
[Stored force attempts](../../docs/caller-contract.md#stored-force-attempts).

---

### GetForce and GetDelivery

`c.GetForce(api.GetForceParams{ForceID: id})` returns `GetForceResult`
(`Lookup`, `ForceResult *ForceResult`). A read's `Outcome` may also be
`api.ForceOutcomeRunning`, with `Reason` `""`, while the attempt's call has
not returned; so may `Delivery.Force` on `Get`, `GetMany` and `Status`.
`c.GetDelivery(api.GetDeliveryParams{CallerUUID: caller, DeliveryRequestID: request})`
returns `GetDeliveryResult` (`Lookup`, `Result *DeliveryResult`). `Lookup` is
`"found"`, `"unknown"` or `"retention_expired"`; the pointer is nil unless
`"found"`. `DeliveryResult` carries the Result's `Class`, `Final`, `Reason`,
`ResendSafe`, `RetryWith`, `Wait` (nil when final), target, hand-over binding,
receipt identity and `Outcome`, never the Message text. See
[Durable lookups](../../docs/caller-contract.md#durable-lookups).

## Permission relay

`(*Client).Decide` returns a `DecideResult` embedding `RequestDelivery`.
Read `Delivery` (`delivered` or `not_confirmed`), not the recorded decision.
`GetPermission` reads subsequent
delivery and `ConfirmBy`; confirmation handling is in the
[caller contract](../../docs/caller-contract.md#permission-relay).

`DecideParams.MaxWaitMs` bounds the whole call and has no default. Pass at
most your deadline minus 1 s. Timing, commit uncertainty and recovery follow
the linked contract.

Cursor uses the same methods and fields for permissions and blocking plans.
Its `HookAlive` is nil. Agent-specific rules are in
[Cursor permissions](../../docs/permissions.md#cursor-spawns).
`ErrNoOpenPermissionRequest` means no eligible request has that token.
Closed requests stay closed after `Resume`; `GetPermission` can still read
their delivery. Requests with no recorded settle instant follow
[time-derived delivery](../../docs/permissions.md#requests-with-no-recorded-settle-instant).
`ErrRelayFallenBack` carries `RelayFallenBackDetails` from `api.ErrDetails`.
For uncovered agents, `SendKeys` answers a pane request using `RequestToken`, `As`, `Key` and
`ExpectPaneSHA256`. Covered Claude uses [direct tmux](../../docs/permissions.md#answering-at-the-pane).
`RecordPaneAnswer` records an outside answer using
`RequestToken`, `As`, `ExpectPaneSHA256` and `NLines`, without typing.
`PaneAnswer` is `sent`, `outside` or `tool_ran`; `PaneAs` is the caller's claim.
`Pause` accepts no pane hash. Handling and dialog proof are defined in
[Permission relay](../../docs/caller-contract.md#permission-relay).
`api.DetailedError` is the error type that carries such facts: its `Err`
holds the catalogued sentinel (`errors.Is` sees through it) and `Details`
the object; `api.ErrDetails(err)` returns the `Details` of the first one in
`err`'s chain, or nil. `ErrPaneChanged` (`PaneChangedDetails`),
`ErrPaneAnswerInProgress` (`PaneAnswerInProgressDetails`),
`ErrClaimTooSoon` (`ClaimTooSoonDetails`), `ErrDialogMaybeOpen`
(`DialogMaybeOpenDetails`) and `ErrTooManyIds` (`TooManyIdsDetails`)
carry it too, and so does a
pane answer's `ErrInternal` after its key was sent (`PaneKeySentDetails`).

The exported functions `api.Decide`, `api.Status`, `api.Get`, `api.List` and
`api.GetPermission` take a `RelayView`, and `api.SendKeys`,
`api.RecordPaneAnswer` and `api.Pause` (`PauseEnv`) an env holding one (the
`Client` methods pass their own); `DecideResult` embeds `RequestDelivery`.

---

## Version mapping

Use the same release tag for `pkg/api` and the installed agent-director
binary: `pkg/api@v0.X.Y` requires binary `v0.X.Y`.

---

## Errors

Named errors can wrap sentinel values and structured details. Use `errors.Is`
to inspect them — never compare error strings.

For uncovered agents, `ErrPauseTimeout` matches with `errors.Is`; `api.ErrDetails(err)`
returns an `api.PauseTimeoutDetails` value after exit keys were sent but
`ended` was not observed. Its fields are `InspectionRequired`, `ExitKeysSent`,
`ExitConfirmed` and `RowState`, with the shared meanings and recovery in
[When pause cannot confirm exit](../../docs/caller-contract.md#when-pause-cannot-confirm-exit).

`ErrCantSend` matches with `errors.Is`; `api.ErrDetails(err)` returns an
`api.CantSendDetails` with `Class`, `Reason`, `ResendSafe` and `RetryWith`.
`Limit` describes a capacity refusal; `Result` carries the owner of a
conflicting pair. Both are nil for `"agent_not_covered"`. Meanings are in
[Busy-send refusals](../../docs/caller-contract.md#busy-send-refusals).

```go
res, err := c.SendKeys(api.SendKeysParams{
    InstanceID: id,
    Text:       "hello",
})
if errors.Is(err, api.ErrSpawnNotInteractive) {
    // Spawn not yet ready — wait for state == "waiting"
}
```

Each method's doc comment lists its sentinels under "Errors:".
`go doc github.com/gabemahoney/agent-director/pkg/api` lists every exported
sentinel. Shared handling is in the [caller contract](../../docs/caller-contract.md);
human repairs are in [Operator actions](../../docs/operator-actions.md).
Common sentinels across verbs:

| Sentinel | Meaning |
|---|---|
| `ErrSpawnNotFound` | No row for the given `instance_id` |
| `ErrClientClosed` | Called after `Close()` |
| `ErrStoreNotInitialized` | Store file absent and `CreateIfMissing` is false |
| `ErrSchemaMismatch` | Incompatible schema, store id or input authority; see [Operator actions](../../docs/operator-actions.md#upgrading-the-native-input-journal) |
| `ErrOldLayout` | Default store still uses the old layout; see [Operator actions](../../docs/operator-actions.md#a-verb-refuses-with-erroldlayout) |
| `ErrSpawnNotInteractive` | The row cannot accept input under the current launch guards |
| `ErrSendKeysWhileRelayed` | A permission relay may still answer |
| `ErrRelayFallenBack` | The relay settled without acknowledgement or a recorded pane answer |
| `ErrDialogMaybeOpen` | A permission dialog is not proven gone |
| `ErrStoreBusy` | A bounded store operation did not finish in time |
| `ErrAlreadyDecided` | The request already has a verdict or completed pane answer |
| `ErrListInvalidLabel` | Label filter not in `key=value` form |

---

## See also

- [Caller contract](../../docs/caller-contract.md)
