package config

import (
	"fmt"
	"math"
	"time"

	"github.com/BurntSushi/toml"
)

// Defaults of the nine [tmux] settings (SR-4.1, SR-13.4, Appendix A). Each is
// the value Default() seeds into its Tmux field AND the fallback its accessor
// returns for a missing, zero or negative configured value, so the two never
// drift. No other package defines these values.
const (
	// DefaultStartingSessionSeconds is the default starting-session bound,
	// in whole seconds (300). Safe minimum: MinStartingSessionSeconds (60).
	// Bounds how long a finished row's own young session counts as "still
	// starting" rather than a conflict (SR-4.1, SR-4.2).
	DefaultStartingSessionSeconds = 300

	// DefaultStoppingWindowSeconds is the default stopping window, in whole
	// seconds (90). Safe minimum: MinStoppingWindowSeconds (30). Bounds how
	// long an ended agent counts as "still stopping" (SR-4.1, SR-4.2).
	DefaultStoppingWindowSeconds = 90

	// DefaultPendingGraceSeconds is the default pending grace period, in
	// whole seconds (60). Safe minimum: the derived
	// PendingGraceMinimumSeconds (30 at the default create timeout and
	// pipe-close wait). Bounds how long find-missing leaves a pending row
	// untouched after its launch start, and how long a SessionStart hook
	// waits for its launch's identity write, a wait the hook also caps at
	// 540 s from its start whatever this value (SR-4.1, SR-11.2, SR-13.4,
	// SR-22.9; WD 2026-09-30c).
	DefaultPendingGraceSeconds = 60

	// DefaultQueryTimeoutMs is the default timeout of each tmux lookup and
	// pane listing, in milliseconds (1500). No safe minimum: a value too low
	// fails closed (SR-4.1, SR-2.4).
	DefaultQueryTimeoutMs = 1500

	// DefaultActionTimeoutMs is the default timeout of each tmux kill, text
	// send, Enter send, key send and capture, in milliseconds (2000). No safe
	// minimum: a value too low fails closed (SR-4.1, SR-2.4).
	DefaultActionTimeoutMs = 2000

	// DefaultCreateTimeoutMs is the default timeout of the session-creating
	// tmux call of spawn, resume and reuse, in milliseconds (5000). No safe
	// minimum: a value too low fails closed; it raises the pending grace
	// period's minimum (SR-4.1).
	DefaultCreateTimeoutMs = 5000

	// DefaultPipeCloseWaitMs is the default pipe-close wait, in milliseconds
	// (100): how long a tmux call waits for its output pipes after its
	// process exits or is terminated. No safe minimum: a value too low fails
	// closed; it raises the pending grace period's minimum (SR-4.1, SR-2.4).
	DefaultPipeCloseWaitMs = 100

	// DefaultSweepBudgetSeconds is the default per-run tmux time budget of
	// find-missing and expire, in whole seconds (15). No safe minimum: a
	// value too low fails closed (SR-4.1, SR-13.5).
	DefaultSweepBudgetSeconds = 15

	// DefaultKillExitWaitMs is the default kill exit wait, in milliseconds
	// (5000): how long kill, after killing the agent's pane, waits for the
	// agent process to exit before it returns ErrTmuxKillFailed. The value
	// rests on RN-6's measurement of Claude's exit after a pane kill (Epic
	// 21): it is at least 2x the largest exit time under Claude Code's
	// default SessionEnd budget, rounded up to a whole second. An agent with
	// a raised SessionEnd budget takes longer to exit, and the operator
	// raises the setting for it. No safe minimum: a value too low fails
	// closed with ErrTmuxKillFailed (SR-4.1, SR-6.1).
	DefaultKillExitWaitMs = 5000
)

// Safe minimums and the pending grace period's rule constants (SR-4.1,
// Appendix A; PO 2026-09-26 MIN). A positive configured value below its key's
// safe minimum is refused by Load, never raised to the minimum.
const (
	// MinStartingSessionSeconds is the safe minimum of
	// starting_session_seconds, in whole seconds (60) (SR-4.1).
	MinStartingSessionSeconds = 60

	// MinStoppingWindowSeconds is the safe minimum of
	// stopping_window_seconds, in whole seconds (30) (SR-4.1).
	MinStoppingWindowSeconds = 30

	// PendingGraceFloorSeconds is the lowest safe minimum the pending grace
	// period can have, in whole seconds (30): the floor of
	// PendingGraceMinimumSeconds (SR-4.1).
	PendingGraceFloorSeconds = 30

	// PendingGraceMarginSeconds is the margin, in whole seconds (20), that
	// PendingGraceMinimumSeconds adds to the create timeout plus the
	// pipe-close wait (SR-4.1, SR-13.4).
	PendingGraceMarginSeconds = 20
)

// Tmux holds the [tmux] table: the nine timing settings of agent-director's
// use of tmux (SR-4.1). In a config Load returns, a field holds the file's
// TOML integer when the key is set (0 included), otherwise the Default()
// value. Only a Tmux built in Go has 0 for an unset field. The accessors
// treat 0 as "use the default"; read the values only through them. A Tmux
// built in Go with zero fields yields every default. Load refuses a negative
// value and a positive value below a key's safe minimum, and, for
// pending_grace_seconds, a missing or 0 key whose default is below the derived
// minimum.
type Tmux struct {
	// StartingSessionSeconds is the starting-session bound, in whole seconds.
	// Default DefaultStartingSessionSeconds (300); safe minimum
	// MinStartingSessionSeconds (60) (SR-4.1, SR-4.2).
	StartingSessionSeconds int64 `toml:"starting_session_seconds"`

	// StoppingWindowSeconds is the stopping window, in whole seconds.
	// Default DefaultStoppingWindowSeconds (90); safe minimum
	// MinStoppingWindowSeconds (30) (SR-4.1, SR-4.2).
	StoppingWindowSeconds int64 `toml:"stopping_window_seconds"`

	// PendingGraceSeconds is the pending grace period, in whole seconds:
	// find-missing's, and also a bound of a SessionStart hook's wait for its
	// launch's identity write, both measured from the launch start; the hook
	// also ends that wait 540 s after it began, whichever comes first (WD
	// 2026-09-30c). Default DefaultPendingGraceSeconds (60); safe minimum
	// PendingGraceMinimumSeconds of the effective create timeout and
	// pipe-close wait (30 at their defaults); no maximum (SR-4.1, SR-11.2,
	// SR-13.4, SR-22.9).
	PendingGraceSeconds int64 `toml:"pending_grace_seconds"`

	// QueryTimeoutMs is the timeout of each tmux lookup and pane listing, in
	// milliseconds. Default DefaultQueryTimeoutMs (1500); minimum none:
	// fails closed (SR-4.1, SR-2.4).
	QueryTimeoutMs int64 `toml:"query_timeout_ms"`

	// ActionTimeoutMs is the timeout of each tmux kill, text send, Enter send,
	// key send and capture, in milliseconds. Default DefaultActionTimeoutMs
	// (2000); minimum none: fails closed (SR-4.1, SR-2.4).
	ActionTimeoutMs int64 `toml:"action_timeout_ms"`

	// CreateTimeoutMs is the timeout of the session-creating tmux call, in
	// milliseconds. Default DefaultCreateTimeoutMs (5000); minimum none:
	// fails closed; it raises the pending grace period's minimum (SR-4.1).
	CreateTimeoutMs int64 `toml:"create_timeout_ms"`

	// PipeCloseWaitMs is the pipe-close wait, in milliseconds. Default
	// DefaultPipeCloseWaitMs (100); minimum none: fails closed; it raises the
	// pending grace period's minimum (SR-4.1, SR-2.4).
	PipeCloseWaitMs int64 `toml:"pipe_close_wait_ms"`

	// SweepBudgetSeconds is the per-run tmux time budget of find-missing and
	// expire, in whole seconds. Default DefaultSweepBudgetSeconds (15);
	// minimum none: fails closed (SR-4.1, SR-13.5).
	SweepBudgetSeconds int64 `toml:"sweep_budget_seconds"`

	// KillExitWaitMs is how long kill waits for the agent process to exit
	// after killing its pane, in milliseconds. Default DefaultKillExitWaitMs
	// (5000, from RN-6's measurement); minimum none: fails closed (SR-4.1,
	// SR-6.1).
	KillExitWaitMs int64 `toml:"kill_exit_wait_ms"`
}

// TmuxKey identifies one [tmux] setting. It is the single definition of each
// key's TOML name, unit, default and safe minimum (SR-4.1), so the refusal
// description, the apitest config writer and tests take key names from here
// and never spell them.
type TmuxKey int

// The nine [tmux] settings, in table order (SR-4.1). TmuxKeys lists them in
// this order.
const (
	TmuxStartingSessionSeconds TmuxKey = iota
	TmuxStoppingWindowSeconds
	TmuxPendingGraceSeconds
	TmuxQueryTimeoutMs
	TmuxActionTimeoutMs
	TmuxCreateTimeoutMs
	TmuxPipeCloseWaitMs
	TmuxSweepBudgetSeconds
	TmuxKillExitWaitMs
)

// TmuxUnit is the unit of a [tmux] setting's integer value.
type TmuxUnit int

const (
	// TmuxUnitSeconds is whole seconds.
	TmuxUnitSeconds TmuxUnit = iota + 1
	// TmuxUnitMilliseconds is milliseconds.
	TmuxUnitMilliseconds
)

// String returns the unit's symbol as used in the refusal description: "s"
// or "ms".
func (u TmuxUnit) String() string {
	if u == TmuxUnitMilliseconds {
		return "ms"
	}
	return "s"
}

// duration returns one unit as a time.Duration.
func (u TmuxUnit) duration() time.Duration {
	if u == TmuxUnitMilliseconds {
		return time.Millisecond
	}
	return time.Second
}

// TmuxMinimumKind says what kind of safe minimum a [tmux] setting has.
type TmuxMinimumKind int

const (
	// TmuxMinimumNone: the key has no safe minimum; a value too low fails
	// closed (SR-4.1).
	TmuxMinimumNone TmuxMinimumKind = iota
	// TmuxMinimumFixed: the key's safe minimum is a constant.
	TmuxMinimumFixed
	// TmuxMinimumDerived: the key's safe minimum is computed from other keys
	// by PendingGraceMinimumSeconds (the pending grace period only).
	TmuxMinimumDerived
)

// tmuxKeyDef is one row of the [tmux] key table.
type tmuxKeyDef struct {
	name         string
	unit         TmuxUnit
	defaultValue int64
	minimumKind  TmuxMinimumKind
	fixedMinimum int64
	field        func(*Tmux) *int64
}

// tmuxKeyDefs is the [tmux] key table, indexed by TmuxKey, in table order.
// Each name must equal its field's toml struct tag.
var tmuxKeyDefs = [...]tmuxKeyDef{
	TmuxStartingSessionSeconds: {"starting_session_seconds", TmuxUnitSeconds, DefaultStartingSessionSeconds,
		TmuxMinimumFixed, MinStartingSessionSeconds, func(t *Tmux) *int64 { return &t.StartingSessionSeconds }},
	TmuxStoppingWindowSeconds: {"stopping_window_seconds", TmuxUnitSeconds, DefaultStoppingWindowSeconds,
		TmuxMinimumFixed, MinStoppingWindowSeconds, func(t *Tmux) *int64 { return &t.StoppingWindowSeconds }},
	TmuxPendingGraceSeconds: {"pending_grace_seconds", TmuxUnitSeconds, DefaultPendingGraceSeconds,
		TmuxMinimumDerived, 0, func(t *Tmux) *int64 { return &t.PendingGraceSeconds }},
	TmuxQueryTimeoutMs: {"query_timeout_ms", TmuxUnitMilliseconds, DefaultQueryTimeoutMs,
		TmuxMinimumNone, 0, func(t *Tmux) *int64 { return &t.QueryTimeoutMs }},
	TmuxActionTimeoutMs: {"action_timeout_ms", TmuxUnitMilliseconds, DefaultActionTimeoutMs,
		TmuxMinimumNone, 0, func(t *Tmux) *int64 { return &t.ActionTimeoutMs }},
	TmuxCreateTimeoutMs: {"create_timeout_ms", TmuxUnitMilliseconds, DefaultCreateTimeoutMs,
		TmuxMinimumNone, 0, func(t *Tmux) *int64 { return &t.CreateTimeoutMs }},
	TmuxPipeCloseWaitMs: {"pipe_close_wait_ms", TmuxUnitMilliseconds, DefaultPipeCloseWaitMs,
		TmuxMinimumNone, 0, func(t *Tmux) *int64 { return &t.PipeCloseWaitMs }},
	TmuxSweepBudgetSeconds: {"sweep_budget_seconds", TmuxUnitSeconds, DefaultSweepBudgetSeconds,
		TmuxMinimumNone, 0, func(t *Tmux) *int64 { return &t.SweepBudgetSeconds }},
	TmuxKillExitWaitMs: {"kill_exit_wait_ms", TmuxUnitMilliseconds, DefaultKillExitWaitMs,
		TmuxMinimumNone, 0, func(t *Tmux) *int64 { return &t.KillExitWaitMs }},
}

// TmuxKeys returns the nine [tmux] settings in table order (SR-4.1). The
// slice is a fresh copy on each call.
func TmuxKeys() []TmuxKey {
	keys := make([]TmuxKey, len(tmuxKeyDefs))
	for i := range keys {
		keys[i] = TmuxKey(i)
	}
	return keys
}

// Name returns the key's TOML name in the [tmux] table, e.g.
// "stopping_window_seconds".
func (k TmuxKey) Name() string { return tmuxKeyDefs[k].name }

// Unit returns the unit of the key's integer value.
func (k TmuxKey) Unit() TmuxUnit { return tmuxKeyDefs[k].unit }

// DefaultValue returns the key's default in its unit (its Default* constant).
func (k TmuxKey) DefaultValue() int64 { return tmuxKeyDefs[k].defaultValue }

// MinimumKind returns whether the key's safe minimum is none, fixed or
// derived. Tmux.Minimum returns the minimum's value.
func (k TmuxKey) MinimumKind() TmuxMinimumKind { return tmuxKeyDefs[k].minimumKind }

// Value returns the field value of key k, with no default applied for 0. In
// a config Load returns, that is the file's value when the key is set (0
// included), otherwise the Default() value; only a Tmux built in Go has 0
// for an unset field.
func (t Tmux) Value(k TmuxKey) int64 { return *tmuxKeyDefs[k].field(&t) }

// Effective returns the effective value of key k as a duration: the
// configured value when positive, otherwise the key's default. A value too
// large to express as a duration gives the largest duration. It performs no
// minimum check; Load refuses values below a minimum (SR-4.1).
func (t Tmux) Effective(k TmuxKey) time.Duration {
	v := t.effectiveValue(k)
	unit := k.Unit().duration()
	if v > math.MaxInt64/int64(unit) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(v) * unit
}

// Minimum returns key k's safe minimum in its unit and true, or 0 and false
// for a key without one. The pending grace period's minimum is derived from
// t's create timeout and pipe-close wait by PendingGraceMinimumSeconds.
func (t Tmux) Minimum(k TmuxKey) (int64, bool) {
	switch k.MinimumKind() {
	case TmuxMinimumFixed:
		return tmuxKeyDefs[k].fixedMinimum, true
	case TmuxMinimumDerived:
		return PendingGraceMinimumSeconds(t.CreateTimeoutMs, t.PipeCloseWaitMs), true
	default:
		return 0, false
	}
}

// PendingGraceMinimumSeconds is the one rule for the pending grace period's
// safe minimum, in whole seconds (SR-4.1, SR-13.4; PO 2026-09-26 MIN):
//
//	max(PendingGraceFloorSeconds, ⌈(C + W) / 1000⌉ + PendingGraceMarginSeconds)
//
// where C and W are the effective create_timeout_ms and pipe_close_wait_ms:
// a configured value that is not positive (missing, 0, or a refused negative)
// counts as its key's default. It cannot overflow for any int64 C and W. At
// the defaults it is 30.
func PendingGraceMinimumSeconds(createTimeoutMs, pipeCloseWaitMs int64) int64 {
	c := positiveOr(createTimeoutMs, TmuxCreateTimeoutMs.DefaultValue())
	w := positiveOr(pipeCloseWaitMs, TmuxPipeCloseWaitMs.DefaultValue())
	// ⌈(c + w) / 1000⌉ computed from the quotients and remainders so c + w is
	// never formed: each quotient is below MaxInt64/1000, and the remainders
	// add at most 2.
	seconds := c/1000 + w/1000 + (c%1000+w%1000+999)/1000
	return max(PendingGraceFloorSeconds, seconds+PendingGraceMarginSeconds)
}

// pendingGraceTotalLimitMs returns the largest effective create_timeout_ms
// plus pipe_close_wait_ms, in milliseconds, at which
// PendingGraceMinimumSeconds is at most graceSeconds, and true; or 0 and false
// when no total gives so low a minimum (graceSeconds below
// PendingGraceFloorSeconds) or the total does not fit an int64. It is the
// rule's inverse, used only to word a refusal (b.n4q): for
// graceSeconds >= PendingGraceFloorSeconds, ⌈T / 1000⌉ + margin <= graceSeconds
// holds exactly when T <= (graceSeconds - margin) × 1000.
func pendingGraceTotalLimitMs(graceSeconds int64) (int64, bool) {
	if graceSeconds < PendingGraceFloorSeconds || graceSeconds-PendingGraceMarginSeconds > math.MaxInt64/1000 {
		return 0, false
	}
	return (graceSeconds - PendingGraceMarginSeconds) * 1000, true
}

// positiveOr returns v when positive, otherwise def.
func positiveOr(v, def int64) int64 {
	if v > 0 {
		return v
	}
	return def
}

// EffectiveStartingSession returns the starting-session bound
// (starting_session_seconds, whole seconds): the configured value when
// positive, otherwise DefaultStartingSessionSeconds (300 s); the largest
// duration when too large. Safe minimum MinStartingSessionSeconds (60 s),
// enforced by Load, not here (SR-4.1, SR-4.2).
func (t Tmux) EffectiveStartingSession() time.Duration {
	return t.Effective(TmuxStartingSessionSeconds)
}

// EffectiveStoppingWindow returns the stopping window
// (stopping_window_seconds, whole seconds): the configured value when
// positive, otherwise DefaultStoppingWindowSeconds (90 s); the largest
// duration when too large. Safe minimum MinStoppingWindowSeconds (30 s),
// enforced by Load, not here (SR-4.1, SR-4.2).
func (t Tmux) EffectiveStoppingWindow() time.Duration {
	return t.Effective(TmuxStoppingWindowSeconds)
}

// EffectivePendingGrace returns the pending grace period
// (pending_grace_seconds, whole seconds): the configured value when positive,
// otherwise DefaultPendingGraceSeconds (60 s); the largest duration when too
// large. Safe minimum PendingGraceMinimumSeconds (30 s at the defaults),
// enforced by Load, not here; no maximum. find-missing uses it, and the hook
// passes it to hook.HandleConfig.PendingGrace as one bound of SessionStart's
// wait for its launch's identity write, which the hook also ends 540 s after
// it began, whichever comes first (SR-4.1, SR-11.2, SR-13.4, SR-22.9; WD
// 2026-09-30c).
func (t Tmux) EffectivePendingGrace() time.Duration {
	return t.Effective(TmuxPendingGraceSeconds)
}

// EffectiveQueryTimeout returns the timeout of each tmux lookup and pane
// listing (query_timeout_ms, milliseconds): the configured value when
// positive, otherwise DefaultQueryTimeoutMs (1500 ms); the largest duration
// when too large. Minimum none: fails closed (SR-4.1, SR-2.4).
func (t Tmux) EffectiveQueryTimeout() time.Duration {
	return t.Effective(TmuxQueryTimeoutMs)
}

// EffectiveActionTimeout returns the timeout of each tmux kill, text send,
// Enter send, key send and capture (action_timeout_ms, milliseconds): the
// configured value when positive, otherwise DefaultActionTimeoutMs (2000
// ms); the largest duration when too large. Minimum none: fails closed
// (SR-4.1, SR-2.4).
func (t Tmux) EffectiveActionTimeout() time.Duration {
	return t.Effective(TmuxActionTimeoutMs)
}

// EffectiveCreateTimeout returns the timeout of the session-creating tmux
// call (create_timeout_ms, milliseconds): the configured value when positive,
// otherwise DefaultCreateTimeoutMs (5000 ms); the largest duration when too
// large. Minimum none: fails closed; it raises the pending grace period's
// minimum (SR-4.1).
func (t Tmux) EffectiveCreateTimeout() time.Duration {
	return t.Effective(TmuxCreateTimeoutMs)
}

// EffectivePipeCloseWait returns the pipe-close wait (pipe_close_wait_ms,
// milliseconds): the configured value when positive, otherwise
// DefaultPipeCloseWaitMs (100 ms); the largest duration when too large.
// Minimum none: fails closed; it raises the pending grace period's minimum
// (SR-4.1, SR-2.4).
func (t Tmux) EffectivePipeCloseWait() time.Duration {
	return t.Effective(TmuxPipeCloseWaitMs)
}

// EffectiveSweepBudget returns the per-run tmux time budget of find-missing
// and expire (sweep_budget_seconds, whole seconds): the configured value when
// positive, otherwise DefaultSweepBudgetSeconds (15 s); the largest duration
// when too large. Minimum none: fails closed (SR-4.1, SR-13.5).
func (t Tmux) EffectiveSweepBudget() time.Duration {
	return t.Effective(TmuxSweepBudgetSeconds)
}

// EffectiveKillExitWait returns how long kill waits for the agent process to
// exit after killing its pane (kill_exit_wait_ms, milliseconds): the
// configured value when positive, otherwise DefaultKillExitWaitMs (5000 ms,
// from RN-6's measurement); the largest duration when too large. Minimum
// none: fails closed (SR-4.1, SR-6.1).
func (t Tmux) EffectiveKillExitWait() time.Duration {
	return t.Effective(TmuxKillExitWaitMs)
}

// tmuxRefusal is one refused [tmux] key: its SR-4.1 description, and whether
// the key's default is itself below its safe minimum (defaultRefused). For
// such a key a missing key or 0 is refused too, so its description ends with
// a change that loads, and the refusal's closing sentence that a missing key,
// or 0, gives the default must not cover it (b.n4q).
type tmuxRefusal struct {
	key            TmuxKey
	text           string
	defaultRefused bool
}

// tmuxRefusals applies the SR-4.1 refusal rules to the [tmux] table decoded
// into t, whose metadata meta says which keys the file sets. It returns every
// refused key in table order, or nil when every value loads; Load's validate
// words the refusal from them. Values are never changed.
func tmuxRefusals(t Tmux, meta toml.MetaData) []tmuxRefusal {
	configured := t
	for _, k := range TmuxKeys() {
		if !meta.IsDefined("tmux", k.Name()) {
			*tmuxKeyDefs[k].field(&configured) = 0
		}
	}
	var refused []tmuxRefusal
	for _, k := range TmuxKeys() {
		if r, ok := configured.refusal(k); ok {
			refused = append(refused, r)
		}
	}
	return refused
}

// refusal returns key k's refusal and true, or a zero tmuxRefusal and false
// when the value loads. t holds the configured values, 0 standing for a
// missing key. A negative value is refused; so is a value, or for a missing
// or 0 key the default, below the key's safe minimum. When the default is
// below the minimum, the description ends with defaultRefusedFix.
func (t Tmux) refusal(k TmuxKey) (tmuxRefusal, bool) {
	v := t.Value(k)
	minimum, hasMinimum := t.Minimum(k)
	if !hasMinimum {
		if v < 0 {
			return tmuxRefusal{key: k, text: fmt.Sprintf("[tmux] %s = %d, which must be positive", k.Name(), v)}, true
		}
		return tmuxRefusal{}, false
	}
	defaultRefused := k.DefaultValue() < minimum
	var msg string
	switch {
	case v == 0 && defaultRefused:
		msg = fmt.Sprintf("[tmux] %s is missing or 0, and its default, %d, is below its safe minimum %d %s",
			k.Name(), k.DefaultValue(), minimum, k.Unit())
	case v != 0 && v < minimum:
		msg = fmt.Sprintf("[tmux] %s = %d, below its safe minimum %d %s", k.Name(), v, minimum, k.Unit())
	default:
		return tmuxRefusal{}, false
	}
	if k.MinimumKind() == TmuxMinimumDerived {
		msg += fmt.Sprintf(" (computed from the effective %s %d and %s %d)",
			TmuxCreateTimeoutMs.Name(), t.effectiveValue(TmuxCreateTimeoutMs),
			TmuxPipeCloseWaitMs.Name(), t.effectiveValue(TmuxPipeCloseWaitMs))
	}
	if defaultRefused {
		msg += defaultRefusedFix(k, v, minimum)
	}
	return tmuxRefusal{key: k, text: msg, defaultRefused: defaultRefused}, true
}

// defaultRefusedFix words the change that loads for key k, whose configured
// value v (0 for a missing key) is refused and whose default is below its safe
// minimum, so neither dropping the key nor setting it to 0 loads (b.n4q): set
// the key to at least the minimum, or, for the derived minimum, lower the
// effective create_timeout_ms and pipe_close_wait_ms to the largest total
// whose minimum is at most v (the default for a missing or 0 key), keeping
// the key as it is. The wording says the total is of the effective values, a
// missing or 0 key counting as its default, so a follower who counts only the
// keys in the file is not refused again. The second change is left out when v
// is negative (still refused) or pendingGraceTotalLimitMs has no total for it.
func defaultRefusedFix(k TmuxKey, v, minimum int64) string {
	fix := fmt.Sprintf(", so set it to at least %d", minimum)
	if k.MinimumKind() != TmuxMinimumDerived || v < 0 {
		return fix
	}
	if total, ok := pendingGraceTotalLimitMs(positiveOr(v, k.DefaultValue())); ok {
		fix += fmt.Sprintf(", or lower the effective %s and %s to a total of %d ms or less"+
			" (a missing or 0 key counts as its default)",
			TmuxCreateTimeoutMs.Name(), TmuxPipeCloseWaitMs.Name(), total)
	}
	return fix
}

// effectiveValue returns key k's effective value in its unit: the configured
// value when positive, otherwise the default.
func (t Tmux) effectiveValue(k TmuxKey) int64 {
	return positiveOr(t.Value(k), k.DefaultValue())
}
