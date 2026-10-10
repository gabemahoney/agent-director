package store

// settle.go — the settle time of the state status, get and list report
// (b.3ie; model: spec/tla/settle/B3ieSettle.tla). Each spawns row keeps three
// columns:
//   - state: the raw state, as every guard reads it;
//   - state_changed_ms: when the raw value last changed (milliseconds);
//   - settled_state: the last settled value as of the last raw change.
//
// The fragments below are the only code that writes the two settle columns,
// and SettleRead.stateExpr is the only code that derives the reported state
// from them. Every write of state sets all three in its own statement, never
// from a Go-side read of the row (a duplicate hook would otherwise restart the
// clock: the model's red_stalebase), with the time taken inside the statement,
// under the write lock (red_nowoutside).

// nowMsSQL is SQLite's own clock in whole milliseconds since the epoch, read
// inside the statement and so under its write lock. SQLite gives every date
// function of one sqlite3_step the same 'now', so two uses in one statement
// agree. unixepoch('subsec') is the clock's whole milliseconds as a float of
// seconds; ROUND before the CAST keeps the float's error from truncating a
// millisecond away. 'subsec' needs SQLite 3.42 or later; modernc.org/sqlite
// v1.34.0 bundles 3.46.0.
const nowMsSQL = `CAST(ROUND(unixepoch('subsec') * 1000) AS INTEGER)`

// hookStateSet is the SET fragment of a hook's write of a non-terminal value
// V, written by the hook process whose settle time is Tw (Store.settleMs). Its
// placeholders are V, Tw, Tw, V, V (hookStateArgs). SET sees the row as it was
// before the statement, so the same-value test and the base are decided in
// this one statement:
//   - V equal to the raw state: no column changes, so a same-value write
//     never restarts the clock;
//   - otherwise the old raw value becomes the base when it had settled (Tw
//     0, or it held at least Tw), else the base is the value the read
//     reports for an unsettled row: the kept base, or the old raw value when
//     there is none (the empty string, as a hand-inserted row has), the same
//     fallback as SettleRead.stateExpr, so the stored base is never empty;
//     state becomes V and state_changed_ms now.
const hookStateSet = `settled_state = CASE WHEN state = ? THEN settled_state
                            WHEN ? = 0 OR ` + nowMsSQL + ` - state_changed_ms >= ? THEN state
                            ELSE COALESCE(NULLIF(settled_state, ''), state) END,
       state_changed_ms = CASE WHEN state = ? THEN state_changed_ms ELSE ` + nowMsSQL + ` END,
       state = ?`

// immediateSettleSet is the settle-column SET fragment of an immediate write
// of V, for a statement whose own SET writes state = V elsewhere (the reuse
// restore's generated list): settled_state V, so V reports at once, and
// state_changed_ms now when V is not the raw state already (kept when it is).
// Its placeholders are V, V.
const immediateSettleSet = `settled_state = ?,
       state_changed_ms = CASE WHEN state = ? THEN state_changed_ms ELSE ` + nowMsSQL + ` END`

// immediateStateSet is the SET fragment of an immediate write of V: any
// terminal value (ended, missing), a hook's included, and every write that is
// not a hook's (find-missing's mark, EndHeldLaunch, MoveToPending,
// ResetForReuse, resume's and reuse's restores). V reports at once and becomes
// the last settled value. Its placeholders are V, V, V (immediateStateArgs).
const immediateStateSet = `state = ?, ` + immediateSettleSet

// immediateStateArgs returns immediateStateSet's arguments for the value v.
func immediateStateArgs(v string) []any {
	return []any{v, v, v}
}

// isTerminalState reports whether v is a terminal state (ended, missing),
// which every write, a hook's included, writes immediately.
func isTerminalState(v string) bool {
	return v == StateEnded || v == StateMissing
}

// hookWriteStateSet returns the SET fragment and its arguments for a hook's
// write of v: hookStateSet with this process's settle time when v is not
// terminal, immediateStateSet when it is (a dead agent never reads as alive
// for the settle time).
func (s *Store) hookWriteStateSet(v string) (string, []any) {
	if isTerminalState(v) {
		return immediateStateSet, immediateStateArgs(v)
	}
	return hookStateSet, []any{v, s.settleMs, s.settleMs, v, v}
}

// SetSettleMs sets the settle time, in whole milliseconds, with which this
// store's hook writes judge whether the raw value they replace had settled
// ([state] settle_ms, config.State.EffectiveSettleMs; b.3ie). 0, the value a
// store opens with, is no settle time; a negative value is taken as 0. Every
// opener that loads the config (the hook handler, pkg/api.New) calls it once,
// right after the open and before the store is shared. Only hook writes read
// it; reads take their settle time from a SettleRead.
func (s *Store) SetSettleMs(ms int64) {
	s.settleMs = max(ms, 0)
}

// SettleRead is a reader's settle time and its clock, both in milliseconds
// (b.3ie). Take NowMs before the read that uses it, never after (a clock
// taken after the row read can report a value that never settled: the
// model's red_readafter). The zero SettleRead is no settle time: the read
// reports the raw state and its statement is exactly the read without
// settling.
type SettleRead struct {
	// SettleMs is the reader's [state] settle_ms; 0 or less is off.
	SettleMs int64
	// NowMs is the reader's clock, in milliseconds since the epoch; read only
	// when SettleMs is positive.
	NowMs int64
}

// on reports whether r settles: a positive settle time.
func (r SettleRead) on() bool {
	return r.SettleMs > 0
}

// stateExpr is THE derivation of the state status, get and list report (and
// list --state filters on), as a SQL expression over a spawns row, with its
// arguments. With no settle time it is the bare state column and no argument,
// so every read statement is exactly the one without settling. With settle
// time T it is the raw state when it has held at least T (age NowMs minus
// state_changed_ms), and the last settled value otherwise; a negative age (a
// clock behind the stamp) is not settled. A row with no settled value (the
// empty string) reports its raw state. It never waits: it is a pure expression over the row
// and the reader's clock.
func (r SettleRead) stateExpr() (string, []any) {
	if !r.on() {
		return "state", nil
	}
	return "CASE WHEN ? - state_changed_ms >= ? THEN state ELSE COALESCE(NULLIF(settled_state, ''), state) END",
		[]any{r.NowMs, r.SettleMs}
}
