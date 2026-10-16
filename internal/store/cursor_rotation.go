package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// RotateCursorSession is only called by an independently verified direct
// gateway hook. Startup binding and generic SessionStart history retain their
// separate contracts. All three publication writes share one transaction.
func (s *Store) RotateCursorSession(examined Spawn, gate HookGate, next, path string, until time.Time, check CursorCheck) (bool, error) {
	if gate.Cursor == nil || gate.Cursor.Terminated || gate.SessionID != gate.Cursor.SessionID || next == "" || strings.TrimSpace(next) != next || len(next) > 512 || strings.ContainsAny(next, "\x00\r\n") || next == gate.SessionID || path == "" || !time.Now().Before(until) {
		return false, fmt.Errorf("store: invalid Cursor rotation")
	}
	b := *gate.Cursor
	applied := false
	prior, state := "", ""
	err := s.inWriteTx(time.Until(until), func(ctx context.Context, conn *sql.Conn) error {
		ctx, cancel := context.WithDeadline(ctx, until)
		defer cancel()
		row, err := s.cursorAuthority(ctx, conn, b, until, check)
		if err != nil {
			return err
		}
		if row.Snapshot != examined.Snapshot || row.Identity != examined.Identity || row.LifeNumber != examined.LifeNumber || row.JSONLPath != examined.JSONLPath || row.JSONLPath != path || gate.ParentPID != b.GatewayPID || gate.ParentStart != b.GatewayStart {
			return fmt.Errorf("store: Cursor rotation snapshot changed")
		}
		var reused int
		if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_history WHERE instance_id=? AND session_id=?`, b.InstanceID, next).Scan(&reused); err != nil {
			return err
		}
		if reused != 0 {
			return fmt.Errorf("store: Cursor session identity reused")
		}
		// Session creation supplies no proof about independent dialogs.
		state = StateWaiting
		var held bool
		if err = conn.QueryRowContext(ctx, `SELECT NOT (`+noRequestHoldsRowSQL+`) FROM spawns WHERE instance_id=?`, b.InstanceID).Scan(&held); err != nil {
			return err
		}
		if held {
			state = StateCheckPermission
		}
		set, args := s.hookWriteStateSet(state)
		args = append(args, next, path, b.InstanceID)
		args = append(args, hookGateArgs(gate)...)
		args = append(args, snapshotMatchArgs(examined.Snapshot)...)
		result, err := conn.ExecContext(ctx, `UPDATE spawns SET `+set+`, session_id=?,jsonl_path=?,last_seen_at=CURRENT_TIMESTAMP,ended_at=NULL,end_reported_at=NULL,liveness_unverified_since=NULL,liveness_note=NULL,`+idleClear+`,`+launchStartClear+`,`+rowVersionAdvance+` WHERE instance_id=? AND `+hookGateSQL+` AND `+snapshotMatchSQL, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("store: Cursor rotation gate changed")
		}
		if err = upsertSessionHistoryEntry(connQuerier{ctx: ctx, conn: conn}, b.InstanceID, b.SessionID, row.JSONLPath, b.Life); err != nil {
			return fmt.Errorf("store: required Cursor archive: %w", err)
		}
		args = append([]any{next}, cursorBindingArgs(b)...)
		args = append(args, b.SessionID)
		result, err = conn.ExecContext(ctx, `UPDATE cursor_runtimes SET bound_session_id=? WHERE `+cursorBindingMatchSQL+` AND bound_session_id=? AND bound_terminated=0`, args...)
		if err != nil {
			return err
		}
		n, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("store: Cursor runtime rotation changed")
		}
		if !time.Now().Before(until) {
			return fmt.Errorf("store: Cursor rotation publication expired")
		}
		prior = row.State
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if applied {
		_ = trail.Emit(context.Background(), "ad.session.archived", map[string]any{"instance_id": b.InstanceID, "prior_session_id": b.SessionID, "new_session_id": next, "source": "ad_spawn_store"})
		_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{"instance_id": b.InstanceID, "prior_state": prior, "new_state": state, "triggering_event_name": "SessionStart", "soft_refresh": false, "source": "ad_spawn_store"})
	}
	return applied, nil
}

// ObserveCursorRotation reads committed publication evidence in one snapshot.
// It grants no runtime authority and never waits beyond the caller's deadline.
func (s *Store) ObserveCursorRotation(ctx context.Context, previous CursorRuntime, examined Spawn, next, path string) (bool, error) {
	committed, _, err := s.ReconcileCursorRotation(ctx, previous, examined, next, path)
	return committed, err
}

// ReconcileCursorRotation distinguishes a committed fresh binding from the
// unchanged outgoing snapshot. Neither result grants runtime authority. Both
// false means the current binding/history is unknown, not an outgoing proof.
func (s *Store) ReconcileCursorRotation(ctx context.Context, previous CursorRuntime, examined Spawn, next, path string) (committed, unchanged bool, err error) {
	ctx = context.WithValue(ctx, connectionInitializationContextKey{}, ctx)
	args := cursorBindingArgs(previous)
	args = append(args, path, examined.Identity.Token, examined.LifeNumber, examined.Identity.Socket, examined.Identity.ServerPID, examined.Identity.ServerStart, examined.Identity.ServerStarttime, examined.Identity.PaneID, examined.Identity.PanePID, examined.Identity.PaneStarttime)
	args = append(args, next, next, previous.SessionID, previous.Life, path, previous.SessionID, previous.SessionID)
	args = append(args, snapshotMatchArgs(examined.Snapshot)...)
	args = append(args, previous.SessionID, previous.Life)
	err = s.db.QueryRowContext(ctx, `WITH binding AS (
 SELECT bound_instance_id,bound_session_id,session_id,row_version,started_at,pid,proc_starttime,tmux_session_name
 FROM cursor_runtimes JOIN spawns ON instance_id=bound_instance_id
 WHERE `+cursorBindingMatchSQL+` AND bound_terminated=0
 AND agent='cursor' AND jsonl_path=? AND launch_token=? AND life_number=?
 AND tmux_socket IS ? AND tmux_server_pid IS ? AND tmux_server_started IS ? AND tmux_server_starttime IS ?
 AND pane_id IS ? AND pane_pid IS ? AND pane_starttime IS ?)
 SELECT EXISTS (SELECT 1 FROM binding WHERE bound_session_id=? AND session_id=?
 AND EXISTS (SELECT 1 FROM session_history WHERE instance_id=bound_instance_id AND session_id=? AND life_number=? AND jsonl_path=?)),
 EXISTS (SELECT 1 FROM binding WHERE bound_session_id=? AND session_id=? AND `+snapshotMatchSQL+`
 AND NOT EXISTS (SELECT 1 FROM session_history WHERE instance_id=bound_instance_id AND session_id=? AND life_number=?))`, args...).Scan(&committed, &unchanged)
	return committed, unchanged, err
}
