// Package tmux is the client over the tmux binary, used by internal/spawn and
// pkg/api. It carries the Phase 1 call set (SRD SR-2.1, Appendix F.1): the
// one-call Lookup, the pane listing, the pane and session kills by id, text,
// Enter and capture by pane id, the create with its chained @ad_owner label,
// and the label by id. Every such call takes the socket, passes -u first and
// -S <socket> next, is bounded by its class's timeout and the pipe-close wait
// handed in through New, runs with every AGENT_DIRECTOR_* variable removed
// from the client's environment, and reports failures as *CallError. The
// pre-Phase-1 name-based methods remain until their last user moves.
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

// Typed errors per SRD §13.1. Callers should match via errors.Is.

// ErrTmuxNotAvailable is returned when the tmux binary cannot be located on
// PATH or refuses to execute (e.g. wrong arch). It is distinct from "tmux ran
// but reported an error" — which surfaces as a verb-specific error below.
var ErrTmuxNotAvailable = errors.New("tmux: binary not available on PATH")

// ErrTmuxSessionCreate is returned when `tmux new-session` exits non-zero.
// Common causes: name collision, invalid cwd, the user-set default-shell is
// missing. The wrapped tmux stderr (when present) appears in the unwrapped
// chain so callers building error envelopes can include it.
var ErrTmuxSessionCreate = errors.New("tmux: new-session failed")

// ErrTmuxKillFailed is returned when `tmux kill-session` exits non-zero for
// any reason other than the canonical "session not found" (which is mapped to
// a quiet no-op success by callers via HasSession-before-kill).
var ErrTmuxKillFailed = errors.New("tmux: kill-session failed")

// ErrTmuxListPanesFailed is returned when `tmux list-panes` exits non-zero —
// either the session doesn't exist or tmux refused to talk. Kept distinct
// from ErrTmuxSessionCreate / ErrTmuxKillFailed so error envelopes are
// specific to the operation that produced them.
var ErrTmuxListPanesFailed = errors.New("tmux: list-panes failed")

// ErrTmuxSendKeys is returned when `tmux send-keys` exits non-zero — most
// commonly because the named session has no live pane. Callers use this
// to distinguish a transport-layer tmux failure from the verb-layer
// state-precondition errors (ErrSpawnNotInteractive et al.).
var ErrTmuxSendKeys = errors.New("tmux: send-keys failed")

// ErrTmuxCaptureFailed is returned when `tmux capture-pane` exits non-zero —
// the session vanished mid-call, the pane disappeared, etc. Distinct from
// ErrSpawnNotFound (which is a store-layer concept) so the verb surface
// can report a missing tmux session differently from a missing DB row.
var ErrTmuxCaptureFailed = errors.New("tmux: capture-pane failed")
