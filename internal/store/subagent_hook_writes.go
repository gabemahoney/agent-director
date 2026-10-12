package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// The store transactions of the subagent hooks (b.xjn, SR-3.2, SR-3.3, SR-12.2):
// the SubagentStart/SubagentStop write, the entry transaction that follows a
// hook's own row write, and the plain read a main-agent Stop's skip decision
// uses. The transitions they run are in subagent_writes.go. Inputs are the
// store's agent-neutral events; the hook package maps an agent's payloads onto
// them.

// errSubagentNotApplied rolls ApplySubagentLifecycle's transaction back when its
// gated row write matched no row; the caller then reads why.
var errSubagentNotApplied = errors.New("store: subagent lifecycle write not applied")

// cut returns the event with every payload-derived string cut to
// SubagentStringMaxBytes (CutSubagentString).
func (ev SubagentLifecycleEvent) cut() SubagentLifecycleEvent {
	ev.SubagentID = CutSubagentString(ev.SubagentID)
	ev.SessionID = CutSubagentString(ev.SessionID)
	ev.Type = CutSubagentString(ev.Type)
	return ev
}

func (d SubagentLaunchDetails) cut() SubagentLaunchDetails {
	d.ToolUseID = CutSubagentString(d.ToolUseID)
	d.SubagentID = CutSubagentString(d.SubagentID)
	d.LauncherID = CutSubagentString(d.LauncherID)
	d.SessionID = CutSubagentString(d.SessionID)
	d.Type = CutSubagentString(d.Type)
	d.Description = CutSubagentString(d.Description)
	d.Model = CutSubagentString(d.Model)
	return d
}

func (r SubagentLaunchRequest) cut() SubagentLaunchRequest {
	r.ToolUseID = CutSubagentString(r.ToolUseID)
	r.Type = CutSubagentString(r.Type)
	return r
}

func (f SubagentLaunchFailure) cut() SubagentLaunchFailure {
	f.ToolUseID = CutSubagentString(f.ToolUseID)
	return f
}

// ApplySubagentLifecycle is the gated write of a subagent's start or stop (the
// hook's SubagentStart and SubagentStop; SR-3.2). In ONE transaction begun with
// BEGIN IMMEDIATE (inImmediateTx, waiting the store's busy timeout for the
// write lock before its first read), both or neither:
//
//  1. The row write: the soft refresh of an ordinary hook, built on
//     gatedHookUpdate with the gate (hookGateSQL) in its own WHERE. The state
//     stays; last_seen_at is bumped; the liveness markers are cleared; the pane
//     start time is recorded when NULL; the session record follows
//     hookSessionRecordSet under gate.SessionID (a hook that carries a subagent
//     id passes none, so none is recorded); row_version advances by exactly one.
//     Unlike ApplyHookTransitionResult's soft refresh it runs inside this
//     transaction and writes no idle_since and no settle column. A gate that
//     does not hold, or no row, matches nothing: not applied.
//  2. When the row write applied and ev.SubagentID is not empty, the entry
//     writes of ev (a start with its claim, or a stop; see subagent_writes.go),
//     each statement gated with the four row conditions of SR-3.1. An empty
//     SubagentID writes no entry and claims nothing.
//
// Outcome, as ApplyHookTransitionResult's: UpsertUpdated with Applied true when
// the transaction committed; UpsertNoChange when the row write matched no row,
// with notAppliedReason's reason from one read after the rollback (no row: no
// reason); UpsertError with a zero HookApplied and a wrapped error when the
// transaction rolled back for any other reason (an entry write's failure, a
// busy store included): the row, its row_version included, is then as it was.
//
// After the commit, and only then, it emits one ad.spawn.state_transition (the
// prior state to itself, soft_refresh true, triggeringEventName), fail-open. The
// entry writes emit no trail record. The caller passes the receipt time in
// ev.ReceivedMs; the store reads no clock for the entry write.
func (s *Store) ApplySubagentLifecycle(instanceID string, gate HookGate, ev SubagentLifecycleEvent, triggeringEventName, jsonlPath string, jsonlPresent bool) (UpsertOutcome, HookApplied, error) {
	const errPrefix = "store: subagent lifecycle"
	ev = ev.cut()
	if ev.SubagentID != "" && ev.Kind != SubagentLifecycleStart && ev.Kind != SubagentLifecycleStop {
		return UpsertError, HookApplied{}, fmt.Errorf("%s: unknown event kind %q", errPrefix, ev.Kind)
	}
	q, args := gatedHookUpdate(instanceID, gate, `last_seen_at = CURRENT_TIMESTAMP`, nil, jsonlPath, jsonlPresent)
	var prior string
	err := s.inImmediateTx(func(ctx context.Context, conn *sql.Conn) error {
		err := conn.QueryRowContext(ctx, `SELECT state FROM spawns WHERE instance_id = ?`, instanceID).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) {
			return errSubagentNotApplied
		}
		if err != nil {
			return fmt.Errorf("prior state: %w", err)
		}
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("rows affected: %w", err)
		} else if n == 0 {
			return errSubagentNotApplied
		}
		if ev.SubagentID == "" {
			return nil
		}
		tx, err := newSubagentTx(ctx, conn, instanceID, gate)
		if err != nil {
			return err
		}
		if ev.Kind == SubagentLifecycleStart {
			return tx.start(ev)
		}
		return tx.stop(ev)
	})
	if errors.Is(err, errSubagentNotApplied) {
		applied, err := s.hookNotApplied(instanceID, gate, errPrefix)
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		return UpsertNoChange, applied, nil
	}
	if err != nil {
		return UpsertError, HookApplied{}, fmt.Errorf("%s: %w", errPrefix, err)
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"instance_id":           instanceID,
		"prior_state":           prior,
		"new_state":             prior,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          true,
		"source":                "ad_spawn_store",
	})
	return UpsertUpdated, HookApplied{Applied: true}, nil
}

// SubagentAfterWrite is what ApplySubagentAfterWrite applies: exactly one of its
// fields is set. Epic 2's snapshot is another field of the same transaction.
type SubagentAfterWrite struct {
	// LaunchRequest opens a claimable launch (SR-4.5, SR-7.8).
	LaunchRequest *SubagentLaunchRequest
	// LaunchDetails applies a launch's result (SR-7.3).
	LaunchDetails *SubagentLaunchDetails
	// LaunchFailure closes the claimable launch of a failed call.
	LaunchFailure *SubagentLaunchFailure
	// TurnEnd closes the current launch's stale claimable launches (SR-4.5).
	TurnEnd *SubagentTurnEnd
}

// ApplySubagentAfterWrite is the entry and claimable-launch transaction that
// follows a hook's own row write (SR-3.3): the launch request, the launch result,
// the launch failure and the main agent's turn end. It runs in its own BEGIN
// IMMEDIATE transaction (inImmediateTx), which the caller starts only after the
// hook's row write applied (a held working transition counts as applied); it
// never checks that itself. Each statement carries the hook gate and the row
// conditions of SR-3.1 in its own WHERE, so a hook of another process writes
// nothing. It emits no trail record. On any failure, an entry write's or a busy
// store's, the whole transaction rolls back, nothing is written and the error
// is returned wrapped; the caller logs it in one line and leaves the hook's
// upsert outcome as the row write's. Strings of the event are cut to
// SubagentStringMaxBytes on entry (CutSubagentString).
func (s *Store) ApplySubagentAfterWrite(instanceID string, gate HookGate, w SubagentAfterWrite) error {
	const errPrefix = "store: subagent entry transaction"
	set := 0
	for _, present := range []bool{w.LaunchRequest != nil, w.LaunchDetails != nil, w.LaunchFailure != nil, w.TurnEnd != nil} {
		if present {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("%s: exactly one write is required, got %d", errPrefix, set)
	}
	err := s.inImmediateTx(func(ctx context.Context, conn *sql.Conn) error {
		tx, err := newSubagentTx(ctx, conn, instanceID, gate)
		if err != nil {
			return err
		}
		switch {
		case w.LaunchRequest != nil:
			return tx.launchRequest(w.LaunchRequest.cut())
		case w.LaunchDetails != nil:
			return tx.launchDetails(w.LaunchDetails.cut())
		case w.LaunchFailure != nil:
			return tx.launchFailure(w.LaunchFailure.cut())
		default:
			return tx.turnEnd(*w.TurnEnd)
		}
	})
	if err != nil {
		return fmt.Errorf("%s: %w", errPrefix, err)
	}
	return nil
}

// SubagentTurnEndHasWork is the plain read behind a main-agent Stop's skip
// (SR-12.2): whether the row's current launch has a claimable launch opened
// strictly more than SubagentGraceMs before receivedMs, so the Stop's entry
// transaction (ApplySubagentAfterWrite with a TurnEnd) has something to close.
// It takes no write lock and writes nothing; it only decides whether the
// transaction runs, so a skip can defer a close to a later Stop or to liveness.
// A missing row, or one with no launch_token, has nothing. Epic 2 extends it
// with the snapshot's own conditions.
func (s *Store) SubagentTurnEndHasWork(instanceID string, receivedMs int64) (bool, error) {
	var has bool
	err := s.db.QueryRow(`SELECT EXISTS (
	    SELECT 1 FROM subagent_launches l
	      JOIN spawns sp ON sp.instance_id = l.instance_id AND sp.launch_token = l.launch_token
	     WHERE l.instance_id = ? AND l.opened_at < ?)`, instanceID, receivedMs-SubagentGraceMs).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("store: subagent turn end read: %w", err)
	}
	return has, nil
}
