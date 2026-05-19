// Package tmux is a thin client over the tmux binary, used by internal/spawn
// to launch Spawn sessions and by future Epics to read panes / kill sessions.
// All operations are direct exec.Command invocations — no shell, no
// interpolation, no &&/|/$VAR (SRD §4.3, §14.3).
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
