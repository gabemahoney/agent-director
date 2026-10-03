// Package tmux is the client over the tmux binary, used by internal/spawn and
// pkg/api. It carries the Phase 1 call set (SRD SR-2.1, Appendix F.1): the
// one-call Lookup, the pane listing, the pane and session kills by id, text,
// Enter, one named key (pause's C-u; b.9o4) and capture by pane id, the
// create with its chained @ad_owner label, and the label by id. Every such
// call takes the socket, passes -u first and -S <socket> next, is bounded by
// its class's timeout and the pipe-close wait handed in through New, runs
// with every AGENT_DIRECTOR_* variable removed from the client's environment,
// and reports failures as *CallError. The name-based HasSession stays
// (SR-2.1); no other name-based method remains.
//
// The package also holds the shared lookup (SR-3.3, SR-3.4, SR-3.10,
// Appendix F.2; lookup.go): Lookup and Classify turn one lookup answer and a
// row's Launch into a verdict (Ours, Leftover, Gone or Can't tell) with the
// clock-free server check over a ProcChecker, the label classes (ClassOf) and
// the name holder (StoredForms). It consumes only typed results and never
// imports internal/probe.
//
// Shared helpers for the verbs sit beside it: the unusable-recorded-name
// guard (Unusable, SR-3.2; unusable.go); agent-process selection and the one
// recorded-process judgement over the start-time reader (SelectAgentProcess,
// JudgeProcess, SR-3.8; agent_process.go), which the server check also uses;
// the pane-by-token selector over a pane listing (PaneByToken, SR-3.6,
// SR-3.7; pane_token.go); and the sweep helpers (Sweep, NewSweep, SR-3.15,
// SR-13.5; sweep.go): one lookup and at most one adoption pane listing per
// socket per run, the per-socket stop rule and the run's tmux time budget on
// an injected clock.
//
// All operations are direct exec invocations — no shell, no interpolation,
// no &&/|/$VAR (SRD §4.3, §14.3). The package never imports internal/config.
package tmux

import "errors"

// Typed errors per SRD SR-1 (SR-1.1 names and classes, SR-1.2 triggers).
// Callers should match via errors.Is. Every error a verb returns for a tmux
// cause matches exactly one catalogued sentinel below (SR-1.5): no sentinel
// wraps another, and a verb error wraps one of them only.

// ErrTmuxNotAvailable is class ENVIRONMENT: this caller cannot reach the
// agent's tmux server as launched. It does not always mean nothing was done:
// a plain spawn's row stays pending, and after a kill, send or capture call
// the call may or may not have taken effect (see below). It must never be
// read as "the agent is dead" (SR-1.1); a plain spawn that met it at the
// re-lookup after "duplicate session" has ended its new row (the description
// says if it could not be; SR-9.4). Every single-row verb that runs tmux
// (kill, read-pane, send-keys, pause, resume, spawn) returns it when its
// lookup is Can't tell with the different-server variant ("this is not the
// tmux server the agent was launched on"; SR-3.3); when the tmux binary
// cannot be run for a lookup, a pane listing or the session-creating call;
// when such a call gets tmux's socket "Permission denied" reply (the plain
// spawn's row stays pending); and, before the launch's write and with nothing
// launched, when the launch's per-user socket directory cannot be created or
// fails tmux's own check (SR-1.2, SR-3.3; a *SocketDirError). A kill, send or
// capture call that fails either way reaches this name only through its
// follow-up lookup (SR-2.5). find-missing and expire never return it. The
// text names no cause, since a description built on it may be any of these.
var ErrTmuxNotAvailable = errors.New("tmux: not available")

// ErrTmuxSessionCreate is class LAUNCH FAILURE, returned by spawn and resume
// (reuse included) when the session-creating call (`tmux new-session`) fails
// other than by timing out or by tmux being unavailable: the tmux server
// refused the create, for example with "duplicate session" whose re-lookup
// found no session holding the name, or with no server or socket to create
// it on. The wrapped call failure appears in the unwrapped chain so callers
// building error envelopes can include it. Spawn and resume also return it
// for a created session that could not be labelled, which was then ended or
// could not be (SR-1.2, SR-3.5).
var ErrTmuxSessionCreate = errors.New("tmux: new-session failed")

// ErrTmuxUnresponsive is class UNAVAILABLE (like 503) and transient: tmux did
// not answer usably, so the outcome is unknown and the caller may retry
// later. It must never be read as "the agent is dead" (SR-1.1). Plain spawn
// returns it when the session-creating call timed out, or its reply does not
// parse with a non-zero exit (the session may have been created and the row
// stays pending), and when, for a caller-supplied instance id, the label
// scan's lookup before anything is written is unreadable (SR-1.2, SR-9.3),
// and when, after "duplicate session", the re-lookup of the requested name
// is unreadable (its new row ended, or the description says it could not
// be; SR-9.4).
// kill returns it when its lookup is Can't tell with the unreadable variant
// (a call timed out, a reply was not recognised, or an answer did not
// parse), and when, after a kill was sent, the agent process cannot be
// checked and the follow-up lookup is unreadable (SR-1.2, SR-6.1). The other
// verbs that look up add their own triggers. It wraps no other sentinel
// (SR-1.5).
var ErrTmuxUnresponsive = errors.New("tmux: unresponsive")

// ErrTmuxSessionConflict is class CONFLICT (like 409): permanent until a
// human looks, since waiting does not resolve it. It must never be read as
// "the agent is dead" (SR-1.1). Plain spawn with a caller-supplied instance
// id returns it, before anything is written, when the label scan finds a
// session carrying a valid label of this store that names the id ("left over
// from an earlier life"), or when the scan's lookup finds conflicting labels
// (SR-1.2, SR-9.3). Plain spawn also returns it, after "duplicate session"
// and with its new row ended (the description says if it could not be),
// when the re-lookup finds the requested name held by a session with an old
// label of the new row's id, a foreign label, another store's label or no
// valid label, or finds conflicting labels (SR-1.2, SR-9.4). kill returns it, with no kill sent, when the lookup for a
// live row is Leftover ("not this launch's session", SR-6.1) and when it is
// Can't tell with the provenance_conflict variant ("conflicting labels":
// two sessions carry the row's current label, or an @ad_owner value is set
// at the global, server or global-window scope; SR-3.4), and, on a finished
// row with the operator-only finished-row opt-in (SR-6.5), when the lookup
// is Leftover or finds the row's own old session that never reported in to
// this row ("never reported in", SR-6.7). Every verb that looks up returns
// it for conflicting labels; the pane verbs also when the lookup is Ours but
// the agent's pane was not found, or on a Leftover session; resume and
// reuse for a session holding the name that is left over, is the row's own
// old session, or carries another row's, another store's or no valid label
// (SR-1.2, SR-3.10). It wraps no other sentinel (SR-1.5).
var ErrTmuxSessionConflict = errors.New("tmux: session conflict")

// ErrTmuxKillFailed is class UNAVAILABLE, returned by kill only, and means
// "the agent process still runs after kill" (SR-1.1, SR-1.3). Its triggers
// (SR-1.2, SR-6.1): a kill was sent to the agent's pane or to the row's
// labelled session, and the agent process, or another process of a pane of
// the labelled session, was still running after the kill exit wait
// (kill_exit_wait_ms, SR-4.1); or a kill was sent, the agent process cannot
// be checked, and the follow-up lookup still finds the labelled session; or
// no session or pane of this launch was found while the row's recorded agent
// process still runs, and no kill was sent. The row stays as it was. It must
// never be read as "the agent is dead". Its text names no command, since
// kill's descriptions wrap it (SR-1.4). It wraps no other sentinel (SR-1.5).
var ErrTmuxKillFailed = errors.New("tmux: agent process still running")

// ErrTmuxListPanesFailed marks a failed pane listing (`tmux list-panes`).
// The pane listing is called (SR-3.7), but its failures are always converted
// to a lookup outcome and never returned to callers, so this sentinel stays
// out of the error catalogue (SR-1.1).
var ErrTmuxListPanesFailed = errors.New("tmux: list-panes failed")

// ErrTmuxSendKeys is class GONE (like 410): the row's own session or pane is
// not there (SR-1.1). send-keys and pause return it when the lookup is Gone
// (nothing sent), or when a text or Enter call fails other than by timing out
// and its follow-up lookup is Gone or Leftover (the current launch's session
// is gone; SR-1.2, SR-7.3). It is distinct from the verb-layer state
// refusals (ErrSpawnNotInteractive et al.).
var ErrTmuxSendKeys = errors.New("tmux: send-keys failed")

// ErrTmuxCaptureFailed is class GONE (like 410): the row's own session or
// pane is not there (SR-1.1). read-pane returns it when the lookup is Gone
// (nothing read), or when the capture fails other than by timing out and its
// follow-up lookup is Gone or Leftover (SR-1.2). It is distinct from
// ErrSpawnNotFound, which is about a missing store row.
var ErrTmuxCaptureFailed = errors.New("tmux: capture-pane failed")
