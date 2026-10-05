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
without it the spawn collides with the id's finished row) and `ErrTmuxSessionConflict`
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
in Claude's input box. A single Enter is always appended to submit the
buffer; an empty `Text` sends that Enter only, submitting what is already
typed. There is no flag to suppress CR stripping — the behavior is
unconditional by design (SRD §4.3).

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
- `ErrSendKeysWhileRelayed`: relay_mode=on and state is `check_permission`
  **and** at least one of the row's permission requests is still within its
  relay window plus 1 s (`RelayKillSafetyMargin`) — or the row has zero
  request rows; the relay still owns the answer. This guard is
  **time-bounded**: it releases 1 s after every request row's window has
  elapsed, when the delivering hook is dead, letting the caller recover the
  wedged row through this sanctioned, audited surface. `Decide`'s
  `ErrRelayFallenBack` points here; on the refused request's account the
  guard can still hold for up to 2 s after that refusal.
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

#### Pure `SendKeys` function

`(*Client).SendKeys` is a thin wrapper over a pure package-level function
that takes the relay window and the clock as explicit inputs so the
guard-release verdict is deterministic and testable:

```go
func SendKeys(s SendKeysStore, t SendKeysTmux, pc ProcChecker, effectiveWindow time.Duration, now time.Time, params SendKeysParams) (SendKeysResult, error)
```

`SendKeysStore` is the row read, the permission-request read, the
conditional adoption write and the store id; `SendKeysTmux` is the lookup,
the pane listing and the send by pane id; `ProcChecker` checks the agent
process. The `Client` method passes its store, tmux client and process
checker, resolves `effectiveWindow` via
`cfg.Relay.EffectiveTimeoutSeconds()` (the single source for the window: a
missing or 0 `relay.timeout_seconds` gives the default, and `New` refuses a
config whose value is negative or above 2147483) and passes its own clock as
`now`, then
records the call on the `ad.send_keys.called` trail event. Most callers use
the `Client` method; the pure function is for tests and callers that need to
control the window and clock.

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
Returns `ReadPaneResult` (`.Pane` string). `ReadPane` reads only this
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

Every `Client` method's godoc enumerates the sentinel errors that
method may return. The canonical list of all sentinels, with their
descriptions, lives at the godoc index:

<https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api>

Common sentinels across verbs:

| Sentinel | Meaning |
|---|---|
| `ErrSpawnNotFound` | No row for the given `claude_instance_id` |
| `ErrClientClosed` | Called after `Close()` |
| `ErrStoreNotInitialized` | Store file absent and `CreateIfMissing` is false |
| `ErrSchemaMismatch` | DB schema is newer than the binary, or the store has no valid store id — install the matching binary for a newer store; restore the pre-install copy of `state.db` for a store with no valid id. Never delete `state.db` |
| `ErrSpawnNotInteractive` | State is not a live conversational state; with `AllowPending`, a `pending` row is refused when its launch start or token is not recorded or only a session of an earlier launch is found |
| `ErrSendKeysWhileRelayed` | Relay path still owns the `check_permission` answer — refused until 1 s (`RelayKillSafetyMargin`) after every request window has elapsed |
| `ErrListInvalidLabel` | Label filter not in `key=value` form |

---

## See also

- [`../../docs/architecture.md`](../../docs/architecture.md) — internal architecture and topology
- [`../../docs/cli-reference.md`](../../docs/cli-reference.md) — manifest-derived CLI reference
- Plan bee `b.qe2` — the plan governing this library's public API surface (bee tickets are not on GitHub)
- [`../../docs/test-writing-guide.md`](../../docs/test-writing-guide.md) — for contributors writing tests against this package
- <https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api> — godoc index
