package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// The Codex turn of a row (b.x72). Codex runs no Stop hook for a turn that
// ends in an error (a model at capacity, say), but its rollout records the
// turn's end as an event_msg task_complete naming the turn's turn_id, as it
// does for every turn. Every Codex turn hook's payload (all but SessionStart
// and SessionEnd) carries that turn_id, so the row records two turns, both
// from its agent's own hooks or its reads:
//
//   - codex_turn: the turn of the latest UserPromptSubmit from the row's
//     main thread, applied or held by a permission request (NULL when that
//     hook carried none), or of a later Stop or Interrupt of the main thread
//     that carried one, when no newer turn is current (codexTurnSet);
//   - codex_turn_ended: the latest turn recorded as ended, by a Stop or
//     Interrupt of the main thread, or by a read that found the rollout's
//     task_complete for codex_turn (EndCodexTurn).
//
// Turn ids are unique, so neither is cleared: a turn recorded in an earlier
// session never matches a later session's hook or rollout line. Only
// codex_turn is ever ended by a read, and only while no later task_started
// follows its task_complete in the rollout, so a stale codex_turn (a turn
// Codex started with no UserPromptSubmit) never ends the newer turn.

// MaxCodexTurnBytes bounds a recorded turn id. A hook's longer turn_id is
// recorded as none.
const MaxCodexTurnBytes = 256

// v15Columns are the columns migrateV14toV15 adds to spawns, in order.
var v15Columns = []string{"codex_turn", "codex_turn_ended"}

// migrateV14toV15 appends the two nullable TEXT turn columns (b.x72) with no
// default and no backfill: an upgraded row records no turn until its agent's
// next UserPromptSubmit, so a turn running across the upgrade ends only by its
// Stop or Interrupt, as before. It validates first: a column of either name
// already present must have exactly that name, type TEXT, no NOT NULL, no
// default and no primary key, and its values must be NULL or non-empty TEXT
// of at most MaxCodexTurnBytes bytes; compatible reentry keeps them. Recovery
// is restore-only, as for v14.
func migrateV14toV15(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found := make(map[string]bool)
	rows, err := tx.Query("PRAGMA table_info(spawns)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, nn, pk int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &nn, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		for _, c := range v15Columns {
			if !strings.EqualFold(name, c) {
				continue
			}
			if name != c || kind != "TEXT" || nn != 0 || def.Valid || pk != 0 {
				rows.Close()
				return fmt.Errorf("%w: store: v14→v15 incompatible spawns.%s", ErrSchemaMismatch, c)
			}
			found[c] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range v15Columns {
		if !found[c] {
			continue
		}
		var invalid bool
		if err := tx.QueryRow(fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM spawns WHERE %[1]s IS NOT NULL
 AND (typeof(%[1]s) <> 'text' OR length(CAST(%[1]s AS BLOB)) NOT BETWEEN 1 AND %[2]d))`, c, MaxCodexTurnBytes)).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v14→v15 invalid spawns.%s", ErrSchemaMismatch, c)
		}
	}
	for _, c := range v15Columns {
		if found[c] {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE spawns ADD COLUMN " + c + " TEXT"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 15`); err != nil {
		return err
	}
	return tx.Commit()
}

// codexTurnSet is the SET fragment, with its arguments, by which an applied
// hook write records the row's turn (HookGate.CodexTurn): a UserPromptSubmit
// sets codex_turn, NULL when it carried no turn; a Stop or Interrupt that
// carried one sets codex_turn_ended to it, and codex_turn too when the row
// records no current turn, its current turn already ended, or the same turn.
// So a stale codex_turn (a turn Codex started with no UserPromptSubmit after
// one that ended) never lets an older turn's late tool hook count as current,
// while a late Stop or Interrupt of an older turn, landing after the next
// turn's UserPromptSubmit, keeps that newer turn current. Any other hook
// records no turn and the fragment is empty. The fragment, when not empty,
// ends with a comma.
func codexTurnSet(g HookGate) (string, []any) {
	switch {
	case g.CodexTurnStart:
		return `codex_turn = ?, `, []any{nullableStringArg(g.CodexTurn)}
	case g.CodexTurnEnd && g.CodexTurn != "":
		return `codex_turn = CASE WHEN codex_turn IS NULL OR codex_turn IS codex_turn_ended OR codex_turn IS ? THEN ? ELSE codex_turn END,
 codex_turn_ended = ?, `, []any{g.CodexTurn, g.CodexTurn, g.CodexTurn}
	}
	return "", nil
}

// codexHeldTurnSQL is the one write of a held working transition
// (reportWorkingHeld) for a main-thread UserPromptSubmit: idle_since NULLed
// and codex_turn recorded as an applied one records it (codexTurnSet), with
// the version advance, only on a row whose gate holds and that has idle_since
// set or another turn recorded. Its placeholders take the turn
// (nullableStringArg), the instance id, the turn again and hookGateArgs.
const codexHeldTurnSQL = `UPDATE spawns
    SET ` + idleClear + `, codex_turn = ?, ` + rowVersionAdvance + `
  WHERE instance_id = ? AND (idle_since IS NOT NULL OR codex_turn IS NOT ?) AND ` + hookGateSQL

// codexToolTurn reports whether g is a Codex PreToolUse or PostToolUse from
// the row's main thread carrying its turn: its move to working applies only
// while codexToolTurnCurrentSQL holds.
func codexToolTurn(g HookGate) bool {
	return g.CodexToolTurn && g.CodexTurn != ""
}

// codexToolTurnCurrentSQL is the condition, on a spawns row, under which a
// Codex tool hook of a turn may move the row to working: the turn is the row's
// current turn (codex_turn), not recorded ended. A row that records no
// current turn (upgraded to v15 mid-turn, or its UserPromptSubmit carried
// none) only requires that the turn is not the ended one. Any other tool hook
// is late: a background command of a turn that ended (by Stop, Interrupt or a
// read) or of an earlier turn, finishing after it. A late one is a soft
// refresh, so it never moves the row back to working. Both placeholders take
// HookGate.CodexTurn. ApplyHookTransitionResult's read (codexToolTurnLate)
// and the move's UPDATE (applyWorkingWrite) carry the same condition.
//
// A turn Codex starts with no UserPromptSubmit leaves codex_turn on the
// previous turn until its Stop or Interrupt records it (when the previous
// turn is recorded ended), so that turn's tool hooks keep the row as it is.
const codexToolTurnCurrentSQL = `(CASE WHEN codex_turn IS NULL THEN codex_turn_ended IS NOT ?
 ELSE codex_turn IS ? AND codex_turn IS NOT codex_turn_ended END)`

// codexToolTurnLate reads whether a Codex tool hook of turn is late on
// instanceID's row (codexToolTurnCurrentSQL does not hold). No row reads
// false.
func (s *Store) codexToolTurnLate(instanceID, turn string) (bool, error) {
	var late bool
	err := s.db.QueryRowContext(s.readContext(), `SELECT NOT `+codexToolTurnCurrentSQL+` FROM spawns WHERE instance_id = ?`,
		turn, turn, instanceID).Scan(&late)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: codex turn read: %w", err)
	}
	return late, nil
}

// CodexOpenTurn is a Codex row whose raw state is working and whose current
// turn (codex_turn) is not recorded as ended: a read may end it from the
// rollout at JSONLPath (EndCodexTurn).
type CodexOpenTurn struct {
	InstanceID string
	SessionID  string
	JSONLPath  string
	Turn       string
}

// codexOpenTurnsSQL selects every CodexOpenTurn.
const codexOpenTurnsSQL = `SELECT instance_id, COALESCE(session_id, ''), jsonl_path, codex_turn FROM spawns
 WHERE ` + codexOpenTurnSQL

// codexFamilySQL is the CTE of one row's family: the row (its one
// placeholder) and every descendant down stored parent links.
const codexFamilySQL = `WITH RECURSIVE family(id) AS (
 SELECT ?
 UNION
 SELECT child.instance_id FROM family JOIN spawns AS child ON child.parent_id = family.id
) `

// codexOpenTurnSQL is the condition, on a spawns row, of a CodexOpenTurn. The
// turn end's write repeats it in its own WHERE.
const codexOpenTurnSQL = `agent = '` + AgentCodex + `' AND state = '` + StateWorking + `'
 AND codex_turn IS NOT NULL AND codex_turn IS NOT codex_turn_ended AND COALESCE(jsonl_path, '') <> ''`

// CodexOpenTurns returns the CodexOpenTurn of instanceID's row and of each of
// its descendants, whose turns its family_state counts, or of every row when
// instanceID is "". A row that is not one is left out. It only reads.
func (s *Store) CodexOpenTurns(instanceID string) ([]CodexOpenTurn, error) {
	q, args := codexOpenTurnsSQL, []any(nil)
	if instanceID != "" {
		q, args = codexFamilySQL+q+` AND instance_id IN (SELECT id FROM family)`, []any{instanceID}
	}
	rows, err := s.db.QueryContext(s.readContext(), q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: codex open turns: %w", err)
	}
	defer rows.Close()
	var out []CodexOpenTurn
	for rows.Next() {
		var t CodexOpenTurn
		if err := rows.Scan(&t.InstanceID, &t.SessionID, &t.JSONLPath, &t.Turn); err != nil {
			return nil, fmt.Errorf("store: codex open turns: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: codex open turns: %w", err)
	}
	return out, nil
}

// CodexTurnEndEvent is the triggering_event_name of the move EndCodexTurn
// makes, and CodexTurnEndCause its cause: the rollout's task_complete.
const (
	CodexTurnEndEvent = "task_complete"
	CodexTurnEndCause = "codex_rollout_task_complete"
)

// EndCodexTurn is a read's move of t's row to waiting once the caller found
// t.Turn's task_complete in the rollout at t.JSONLPath (b.x72): the turn end a
// Stop would have written, for a turn Codex ran no Stop for. One guarded
// statement, in one transaction with the family refresh, applies only while
// the row is still t: a Codex row, raw working, whose current turn is t.Turn
// and not recorded ended, with t.JSONLPath as its rollout. It writes waiting
// as a hook's write does (hookWriteStateSet, with this store's settle time),
// records t.Turn as ended and advances row_version; it bumps no last_seen_at
// and clears no liveness note, since no hook reported. After the commit it
// emits the row's display exits and one ad.spawn.state_transition, working
// to waiting, with triggering_event_name CodexTurnEndEvent and cause
// CodexTurnEndCause.
//
// It waits at most maxWait for the write lock (0: none, as a reading verb
// waits). It reports whether the move applied. A row that is no longer t
// changes nothing and reports false; so does a commit whose outcome is
// unknown, which the read after it reports as it landed.
func (s *Store) EndCodexTurn(t CodexOpenTurn, maxWait time.Duration) (bool, error) {
	stateSet, stateArgs := s.hookWriteStateSet(StateWaiting)
	q := `UPDATE spawns SET ` + stateSet + `, codex_turn_ended = codex_turn, ` + rowVersionAdvance + `
 WHERE instance_id = ? AND codex_turn = ? AND jsonl_path = ? AND ` + codexOpenTurnSQL
	args := append(stateArgs, t.InstanceID, t.Turn, t.JSONLPath)
	var (
		moved bool
		exits []displayTransition
	)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		moved = true
		if err := s.refreshFamilies(connQuerier{ctx, conn}); err != nil {
			return err
		}
		exits, err = takeDisplayExits(ctx, conn, t.InstanceID)
		return err
	})
	if errors.Is(err, errCommitUnconfirmed) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: codex turn end: %w", err)
	}
	if !moved {
		return false, nil
	}
	emitDisplayTransitions(exits)
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"instance_id":           t.InstanceID,
		"prior_state":           StateWorking,
		"new_state":             StateWaiting,
		"triggering_event_name": CodexTurnEndEvent,
		"cause":                 CodexTurnEndCause,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	return true, nil
}
