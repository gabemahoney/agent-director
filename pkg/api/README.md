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
`pending`; do not retry until `Get` shows it `ended` or `missing`) and `ErrTmuxSessionConflict`
(an explicit id with no row whose labelled session from an earlier life
still runs, or conflicting labels; nothing is written).
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

Send text into a tracked Spawn's tmux pane. CR bytes (`\r`, `0x0D`) are
stripped automatically before delivery to prevent premature buffer
submission; LF bytes (`\n`, `0x0A`) are preserved as composed newlines
in Claude's input box. A single Enter is always appended to submit the
buffer. There is no flag to suppress CR stripping — the behavior is
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
Most-likely sentinel errors: `ErrSpawnNotFound`,
`ErrSpawnNotInteractive` (state is not `waiting/working/ask_user/
check_permission`), `ErrSendKeysWhileRelayed` (relay_mode=on and state
is `check_permission` **and** at least one of the Spawn's permission
requests is still within its relay window — or the Spawn has zero request
rows; the relay still owns the answer). This guard is **time-bounded**:
once every request row's window has elapsed the delivering hook is dead
and the guard releases, letting the operator recover the wedged Spawn
through this sanctioned, audited surface. See `(*Client).SendKeys` godoc.

#### `AllowPending` — pre-SessionStart opt-in

By default `SendKeys` rejects a `pending` Spawn with
`ErrSpawnNotInteractive`. Set `AllowPending: true` to bypass that check
and send text directly into the tmux pane while the Spawn is still in
`pending` state.

**Use case:** Claude Code renders some interactive prompts *before* its
`SessionStart` hook fires — for example the
`--dangerously-load-development-channels` safety warning. The Spawn stays
`pending` until the user (or orchestrator) dismisses the prompt, so the
hook never fires and the state never advances. `AllowPending: true` lets
a caller detect and dismiss such prompts without deadlocking.

`ended` and `missing` Spawns are still rejected regardless of
`AllowPending` — there is no pane to write to.

#### Pure `SendKeys` function

`(*Client).SendKeys` is a thin wrapper over a pure package-level function
that takes the relay window and the clock as explicit inputs so the
guard-release verdict is deterministic and testable:

```go
func SendKeys(s SendKeysStore, tmux SendKeysTmux, effectiveWindow time.Duration, now time.Time, params SendKeysParams) (SendKeysResult, error)
```

The `Client` method resolves `effectiveWindow` via
`cfg.Relay.EffectiveTimeoutSeconds()` (the single source for the
non-positive → default fallback) and injects `time.Now()`, then records the
guard evaluation on the `ad.send_keys.called` trail event. Most callers use
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

Capture the last N lines of a tracked Spawn's tmux pane. Default 25 lines;
no upper cap. ANSI escape codes are stripped by default (pass `ANSI: true`
to get raw bytes).

```bash
agent-director read-pane \
  --claude-instance-id claude_2026-05-22T18-23-15 \
  --n-lines 50
```

Call `c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: id, NLines: 50})`.
Returns `ReadPaneResult` (`.Pane` string). Most-likely sentinel errors:
`ErrSpawnNotFound`, `ErrTmuxCaptureFailed`. See `(*Client).ReadPane` godoc.

#### `AllowPending` — surface symmetry with `send-keys`

`ReadPane` has **no state guard** — it can read the pane of a `pending`,
`ended`, or `missing` Spawn just as easily as a live one (provided tmux still
holds the session). Passing `AllowPending: true` is accepted but has no
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
| `ErrSpawnNotInteractive` | State is not a live conversational state |
| `ErrSendKeysWhileRelayed` | Relay path still owns the `check_permission` answer — refused while any request window is live; releases once every window has elapsed |
| `ErrListInvalidLabel` | Label filter not in `key=value` form |

---

## See also

- [`../../docs/architecture.md`](../../docs/architecture.md) — internal architecture and topology
- [`../../docs/cli-reference.md`](../../docs/cli-reference.md) — manifest-derived CLI reference
- Plan bee `b.qe2` — the plan governing this library's public API surface (bee tickets are not on GitHub)
- [`../../docs/test-writing-guide.md`](../../docs/test-writing-guide.md) — for contributors writing tests against this package
- <https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api> — godoc index
