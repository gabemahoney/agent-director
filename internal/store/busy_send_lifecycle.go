package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Lifecycle custody (SR-6.2, SR-6.3; M1's EndLaunch, Exit, Resume,
// ReusePublish and Delete under G4; M2 NothingTakenAfterExit; sf-answer-2).
// Two facts stay distinct here:
//
//   - Launch End (M1's EndLaunch) is logical: AD learned the Launch ended. The
//     transaction that records it applies the inbox's custody disposition
//     (applyBusySendLaunchEnd): never-issued claims return to Held, every
//     intent-bearing or output-bearing non-Final Message becomes Final
//     sent_unconfirmed, and every hand-over's revision advances, so no old job
//     persists intent, passes revalidation or changes custody afterwards.
//   - Exit (M1's Exit, R6 and R8) is positive exit evidence: identity-checked
//     exit of every process the Launch owns that can emit (its agent and every
//     recorded issuer), with its captured destinations fenced. Only it lifts the
//     fences of intent-bearing custody, and only under it may resume, reuse
//     publication or deletion go on, or not_started be written.
//
// The store never probes a process. A caller reads the scope
// (ReadBusySendExitScope), judges every process in it by identity, and passes
// the scope back as evidence (BusySendExitEvidence); the lifecycle
// transaction reads the scope again under the write lock and applies the
// evidence only when it is unchanged, so a registration or identity write
// after the judgement voids it (ErrBusySendExitNotEstablished). No elapsed
// time, worker death, missing proof, ended or missing row, logical end cause
// (agent_missing, record_deleted, candidate_replaced, creation_failed,
// launch_published) or
// successful kill no-op ever stands in for Exit.

// ErrBusySendExitNotEstablished is a lifecycle write refused because positive
// exit evidence for the instance's Launches was not supplied, or no longer
// matches the Launches' owned processes. Nothing was written.
var ErrBusySendExitNotEstablished = errors.New("store: busy-send exit evidence not established")

// BusySendExitLaunch is one Launch an instance's exit evidence must cover:
// a Launch of its published conversation that is or was its current Launch.
type BusySendExitLaunch struct {
	LaunchID string
	// Current is whether this is the published current Launch.
	Current bool
	// Ended is whether its Launch End is recorded.
	Ended bool
	// Processes are every owned process identity the evidence must show gone:
	// the agent's identities the spawns row records while this is the row's
	// own Launch (role agent, no namespace recorded), then the Launch's
	// busy_send_launch_processes rows (an agent row recorded by earlier exit
	// evidence, and every issuer), in role order.
	Processes []BusySendLaunchProcess
	// Routes are the route kinds of its hand-overs still open, whose
	// captured destinations the evidence must fence.
	Routes []BusySendRouteKind
	// Issued is whether any prompt of any of its hand-overs ever persisted
	// emission intent: whether AD ever issued input for it.
	Issued bool
}

// BusySendExitScope is what positive exit evidence for an instance must
// cover (SR-6.2; M1 NotStartedAfterExit): every Launch of its published
// conversation that is or was current, with its owned processes and open
// routes. A candidate Launch staged meanwhile is not in it: it never owned
// a hand-over, so nothing issued can reach it.
type BusySendExitScope struct {
	InstanceID     string
	ConversationID string
	// Coverage is the published conversation's coverage. Only a covered
	// conversation holds custody, so only it needs exit evidence.
	Coverage BusySendCoverage
	Launches []BusySendExitLaunch
}

// BusySendExitEvidence is a caller's positive exit evidence: Scope exactly as
// ReadBusySendExitScope returned it, after the caller judged every process in
// it gone by its identity (process start time, and pid namespace where one is
// recorded) and found every route fenceable. A lifecycle write applies it
// only while the scope read under its write lock still has the same Launches
// and processes.
type BusySendExitEvidence struct {
	Scope BusySendExitScope
}

// sameProcesses reports whether a and b name the same conversation, the
// same Launches in the same order with the same current Launch, the same
// owned processes and the same issued fact, which the judgement read. Launch
// End may have been recorded in between, which the evidence does not depend
// on.
func (a BusySendExitScope) sameProcesses(b BusySendExitScope) bool {
	return a.InstanceID == b.InstanceID && a.ConversationID == b.ConversationID &&
		slices.EqualFunc(a.Launches, b.Launches, func(x, y BusySendExitLaunch) bool {
			return x.LaunchID == y.LaunchID && x.Current == y.Current && x.Issued == y.Issued &&
				slices.Equal(x.Processes, y.Processes)
		})
}

// ReadBusySendExitScope reads instanceID's exit scope in one read
// transaction. No row is ErrSpawnNotFound; a row with no consistent
// published binding is an error. It writes nothing and probes nothing.
func (s *Store) ReadBusySendExitScope(instanceID string) (BusySendExitScope, error) {
	var out BusySendExitScope
	err := s.readWithin(DefaultLockWait, "store: read busy-send exit scope", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		var err error
		out, err = readBusySendExitScope(ctx, conn, s.StoreID(), instanceID)
		return err
	})
	return out, err
}

// ReadBusySendExitScopeOf is ReadBusySendExitScope for a caller that acted on
// one Launch, the one whose launch token is token (kill's or keyless pause's
// verified exit): current reports whether that Launch is still the published
// current Launch, in the same read. Such a caller judges and applies the scope
// only when current, and the lifecycle write's revalidation of the same
// Launches then binds its evidence to that Launch, never to one that became
// current later.
func (s *Store) ReadBusySendExitScopeOf(instanceID, token string) (scope BusySendExitScope, current bool, err error) {
	return s.ReadBusySendExitScopeOfWithin(context.Background(), instanceID, token, DefaultLockWait)
}

// ReadBusySendExitScopeOfWithin is ReadBusySendExitScopeOf within ctx, its
// waits bounded by maxWait (readWithin; a wait cut short is ErrStoreBusy):
// keyless pause passes its one call budget. Its results are read only once
// the read completed: a read the budget cuts short returns its error alone,
// while its abandoned operation may still be running.
func (s *Store) ReadBusySendExitScopeOfWithin(ctx context.Context, instanceID, token string, maxWait time.Duration) (BusySendExitScope, bool, error) {
	var scope BusySendExitScope
	var current bool
	err := s.WithContext(ctx).readWithin(maxWait, "store: read busy-send exit scope", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		var currentToken string
		read, err := readBusySendExitScopeWith(ctx, conn, s.StoreID(), instanceID, &currentToken)
		scope, current = read, err == nil && token != "" && currentToken == token
		return err
	})
	if err != nil {
		return BusySendExitScope{}, false, err
	}
	return scope, current, nil
}

func readBusySendExitScope(ctx context.Context, conn *sql.Conn, storeID, instanceID string) (BusySendExitScope, error) {
	var currentToken string
	return readBusySendExitScopeWith(ctx, conn, storeID, instanceID, &currentToken)
}

// readBusySendExitScopeWith is readBusySendExitScope that also sets
// *currentToken to the published current Launch's launch token ("" for none).
func readBusySendExitScopeWith(ctx context.Context, conn *sql.Conn, storeID, instanceID string, currentToken *string) (BusySendExitScope, error) {
	out := BusySendExitScope{InstanceID: instanceID}
	p, err := readExaminedBusySendLaunch(ctx, conn, instanceID)
	if err != nil {
		return out, err
	}
	out.ConversationID, out.Coverage = p.conversation, p.coverage
	var rowToken sql.NullString
	var pid, panePID sql.NullInt64
	var start, paneStart sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT launch_token, pid, proc_starttime, pane_pid, pane_starttime
	    FROM spawns WHERE instance_id = ?`, instanceID).Scan(&rowToken, &pid, &start, &panePID, &paneStart); err != nil {
		return out, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT launch_id, launch_token, ended_at_ms IS NOT NULL FROM busy_send_launches
	    WHERE conversation_id = ? AND store_id = ? AND phase IN ('current', 'superseded')
	        AND (phase = 'current' OR end_cause IS NOT 'candidate_replaced')
	    ORDER BY recorded_at_ms, launch_id`, p.conversation, storeID)
	if err != nil {
		return out, err
	}
	type launchRow struct {
		BusySendExitLaunch
		token sql.NullString
	}
	var launches []launchRow
	for rows.Next() {
		var l launchRow
		if err := rows.Scan(&l.LaunchID, &l.token, &l.Ended); err != nil {
			rows.Close()
			return out, err
		}
		l.Current = l.LaunchID == p.launch
		launches = append(launches, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	for _, l := range launches {
		if l.Current {
			*currentToken = l.token.String
		}
		if l.Current && l.token.Valid && rowToken.Valid && l.token.String == rowToken.String {
			l.Processes = busySendRowAgentIdentities(pid, start, panePID, paneStart)
		}
		// The Launch's own exit facts (readBusySendLaunchFacts), the one
		// production reading of its processes, open routes and issue.
		facts, err := readBusySendLaunchFacts(ctx, conn, l.LaunchID)
		if err != nil {
			return out, err
		}
		l.Processes = append(l.Processes, facts.processes...)
		l.Routes, l.Issued = facts.routes, facts.issued
		out.Launches = append(out.Launches, l.BusySendExitLaunch)
	}
	return out, nil
}

// busySendRowAgentIdentities is the spawns row's recorded agent identities
// (its SessionStart identity and its pane identity, each when its pid is
// recorded), as role agent processes, the same identity listed once. Both are
// in scope: evidence must show every identity the row records gone, so no
// selection between them is needed.
func busySendRowAgentIdentities(pid sql.NullInt64, start sql.NullString, panePID sql.NullInt64, paneStart sql.NullString) []BusySendLaunchProcess {
	var out []BusySendLaunchProcess
	for _, id := range []struct {
		pid   sql.NullInt64
		start sql.NullString
	}{{pid, start}, {panePID, paneStart}} {
		if !id.pid.Valid || id.pid.Int64 <= 0 {
			continue
		}
		p := BusySendLaunchProcess{Role: BusySendProcessAgent, PID: int(id.pid.Int64), StartTime: id.start.String}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// validateBusySendExitEvidence reads instanceID's exit scope again under the
// caller's write lock and returns it when ev still describes it:
// ErrBusySendExitNotEstablished for no evidence, evidence for another
// instance, or a scope whose Launches or owned processes changed since the
// judgement (a late issuer registration or identity write).
func validateBusySendExitEvidence(ctx context.Context, conn *sql.Conn, storeID, instanceID string, ev *BusySendExitEvidence) (BusySendExitScope, error) {
	if ev == nil || ev.Scope.InstanceID != instanceID {
		return BusySendExitScope{}, fmt.Errorf("%w: no exit evidence for %s", ErrBusySendExitNotEstablished, instanceID)
	}
	scope, err := readBusySendExitScope(ctx, conn, storeID, instanceID)
	if err != nil {
		return scope, err
	}
	if !scope.sameProcesses(ev.Scope) {
		return scope, fmt.Errorf("%w: the owned processes of %s changed since they were judged", ErrBusySendExitNotEstablished, instanceID)
	}
	return scope, nil
}

// prepareBusySendExit is the exit gate of a lifecycle write that starts or
// publishes a new Launch of instanceID, or retires its conversation: run
// under the write lock before the write changes the row's recorded
// identities. An uncovered published conversation holds no custody and needs
// no evidence (nil scope). A covered one needs ev, validated.
func prepareBusySendExit(ctx context.Context, conn *sql.Conn, storeID, instanceID string, ev *BusySendExitEvidence) (*BusySendExitScope, error) {
	p, err := readBusySendPublished(ctx, conn, instanceID)
	if err != nil {
		return nil, err
	}
	if p.coverage != BusySendCovered {
		return nil, nil
	}
	scope, err := validateBusySendExitEvidence(ctx, conn, storeID, instanceID, ev)
	if err != nil {
		return nil, err
	}
	return &scope, nil
}

// prepareBusySendExitIfFinished is prepareBusySendExit for a lifecycle write
// guarded on a finished row holding examined: it validates only when that
// guard holds now, so a changed or removed row stays the write's own
// not-applied outcome, never an exit refusal.
func (s *Store) prepareBusySendExitIfFinished(ctx context.Context, conn *sql.Conn, instanceID string, examined RowSnapshot, ev *BusySendExitEvidence) (*BusySendExitScope, error) {
	var holds bool
	args := append([]any{instanceID}, finishedStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id = ? AND `+
		finishedStateGuardSQL+` AND `+snapshotMatchSQL+`)`, args...).Scan(&holds); err != nil || !holds {
		return nil, err
	}
	return prepareBusySendExit(ctx, conn, s.StoreID(), instanceID, ev)
}

// recordBusySendLaunchEnd records launchID's Launch End with cause and
// applies its custody disposition (applyBusySendLaunchEnd) in the caller's
// transaction (M1's EndLaunch). Only a current Launch whose End is not yet
// recorded changes; any other is unchanged and reported false, so a repeat
// or a stale Launch changes nothing.
func recordBusySendLaunchEnd(ctx context.Context, conn *sql.Conn, storeID, launchID string, cause BusySendLaunchEnd, now int64) (bool, error) {
	r, err := conn.ExecContext(ctx, `UPDATE busy_send_launches SET ended_at_ms = ?, end_cause = ?, revision = revision + 1
	    WHERE launch_id = ? AND store_id = ? AND phase = 'current' AND ended_at_ms IS NULL`, now, string(cause), launchID, storeID)
	if err != nil {
		return false, err
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	return true, applyBusySendLaunchEnd(ctx, conn, storeID, launchID, now)
}

// busySendRowLaunch is instanceID's row's own published current Launch, the
// one whose launch token is the row's, and whether its End is recorded; ""
// for a row with a staged candidate (whose token is the candidate's) or no
// consistent binding.
func busySendRowLaunch(ctx context.Context, q busySendLaunchSQL, storeID, instanceID string) (string, bool, error) {
	var launch string
	var ended bool
	err := q.QueryRowContext(ctx, `SELECT l.launch_id, l.ended_at_ms IS NOT NULL FROM spawns s
	    JOIN busy_send_launches l ON l.instance_id = s.instance_id AND l.conversation_id = s.busy_conversation_id
	        AND l.phase = 'current' AND l.launch_token IS s.launch_token AND s.launch_token IS NOT NULL
	    WHERE s.instance_id = ? AND l.store_id = ?`, instanceID, storeID).Scan(&launch, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: busy-send Launch End: %w", err)
	}
	return launch, ended, nil
}

// endBusySendRowLaunch records the Launch End of instanceID's row's own
// published current Launch (busySendRowLaunch) with the supplied cause, in
// the caller's transaction. A row with a staged candidate, an already ended
// Launch or no consistent binding changes nothing. It is logical End only:
// it never ends a pause claim, which needs exit evidence.
func endBusySendRowLaunch(ctx context.Context, conn *sql.Conn, storeID, instanceID string, cause BusySendLaunchEnd, now int64) (bool, error) {
	launch, ended, err := busySendRowLaunch(ctx, conn, storeID, instanceID)
	if err != nil || launch == "" || ended {
		return false, err
	}
	return recordBusySendLaunchEnd(ctx, conn, storeID, launch, cause, now)
}

// endBusySendOnTerminalMark is the Launch End a terminal row write records in
// its own transaction, once it applied: on a move to ended (the agent's own
// end report, or a proven exit with one), and on any mark whose statement
// recorded the agent process's proven exit (process_evidence). A covered
// Launch also ends on a missing mark without proven exit (agent_missing).
// That logical end neither establishes exit nor lifts physical fences, even
// if the row's own agent reports in and revives its lifecycle state later.
//
// When the mark's statement stored positive exit evidence for the row's
// recorded agent (Spawn.provenAgentExit: its exited record, for the row's
// life, launch token and identity), the same transaction also ends the
// standing pause claim of the row's own current Launch, its End recorded now
// or earlier (endBusySendRowPauseClaim). An ended state, a missing mark or
// any other process record ends none.
func endBusySendOnTerminalMark(ctx context.Context, conn *sql.Conn, storeID, instanceID string) error {
	var state string
	var proven, covered bool
	if err := conn.QueryRowContext(ctx, `SELECT state, process_evidence IS NOT NULL, busy_send = 'covered' FROM spawns WHERE instance_id = ?`,
		instanceID).Scan(&state, &proven, &covered); err != nil {
		return fmt.Errorf("busy-send Launch End: %w", err)
	}
	cause := BusySendProcessExited
	if state != StateEnded && !proven {
		if state != StateMissing || !covered {
			return nil
		}
		cause = busySendAgentMissing
	}
	now := storeNowMillis()
	if _, err := endBusySendRowLaunch(ctx, conn, storeID, instanceID, cause, now); err != nil || !proven {
		return err
	}
	row, err := scanSpawn(conn.QueryRowContext(ctx, getSpawnSQL, instanceID), getSpawnErrs)
	if err != nil {
		return fmt.Errorf("busy-send pause claim end: %w", err)
	}
	if _, exited := row.provenAgentExit(); !exited {
		return nil
	}
	return endBusySendRowPauseClaim(ctx, conn, storeID, instanceID, now)
}

// EndBusySendLaunch records the Launch End of instanceID's published current
// Launch on a caller's verified agent exit (kill's process wait, keyless
// pause): only while that Launch is the row's own and its launch token is
// token, the Launch the caller judged. It reports whether it recorded one;
// a repeat, another Launch or a deleted row changes nothing. It is Launch
// End only, never exit evidence for its delivery fences. The caller's
// verified agent exit does end that Launch's standing pause claim
// (launch_end), in the same transaction, also when its End was already
// recorded (endBusySendRowPauseClaim).
func (s *Store) EndBusySendLaunch(instanceID, token string) (bool, error) {
	return s.EndBusySendLaunchWithin(context.Background(), instanceID, token, DefaultLockWait)
}

// durableWriteWithin runs fn in one durable write transaction within ctx, all
// its waits (the store's connection and the write lock) sharing the one
// deadline maxWait gives (DefaultLockWait: the store's own waits), so a
// caller's budget is never a fresh wait for each phase.
func (s *Store) durableWriteWithin(ctx context.Context, maxWait time.Duration, fn func(context.Context, *sql.Conn) error) error {
	ctx, cancel := s.WithContext(ctx).operationContext(maxWait)
	defer cancel()
	return s.inDurableWriteTxContext(ctx, maxWait, fn)
}

// EndBusySendLaunchWithin is EndBusySendLaunch within ctx, its waits bounded
// by maxWait (durableWriteWithin): keyless pause passes its one call budget.
func (s *Store) EndBusySendLaunchWithin(ctx context.Context, instanceID, token string, maxWait time.Duration) (bool, error) {
	var ended bool
	err := s.durableWriteWithin(ctx, maxWait, func(ctx context.Context, conn *sql.Conn) error {
		var current bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id = ? AND launch_token IS ?)`,
			instanceID, nullableStringArg(token)).Scan(&current); err != nil || !current || token == "" {
			return err
		}
		now := storeNowMillis()
		var err error
		if ended, err = endBusySendRowLaunch(ctx, conn, s.StoreID(), instanceID, BusySendProcessExited, now); err != nil {
			return err
		}
		return endBusySendRowPauseClaim(ctx, conn, s.StoreID(), instanceID, now)
	})
	if err != nil {
		return false, fmt.Errorf("store: end busy-send Launch: %w", err)
	}
	return ended, nil
}

// EstablishBusySendExit applies ev (M1's Exit) in one durable transaction: it
// validates the scope again, records the current Launch's End (process_exited)
// when it is not yet recorded, and lifts every in-scope Launch's fences
// (exitBusySendLaunches). It reports false, with nothing written, when the
// scope changed since the judgement (ErrBusySendExitNotEstablished is not
// returned for that: the caller judges again or reports exclusion not
// established). It never deletes, resumes or publishes anything.
func (s *Store) EstablishBusySendExit(ev BusySendExitEvidence) (bool, error) {
	return s.EstablishBusySendExitWithin(context.Background(), ev, DefaultLockWait)
}

// EstablishBusySendExitWithin is EstablishBusySendExit within ctx, its waits
// bounded by maxWait (durableWriteWithin): keyless pause passes its one call
// budget. A wait cut short is an error holding ErrStoreBusy, nothing written.
func (s *Store) EstablishBusySendExitWithin(ctx context.Context, ev BusySendExitEvidence, maxWait time.Duration) (bool, error) {
	err := s.durableWriteWithin(ctx, maxWait, func(ctx context.Context, conn *sql.Conn) error {
		scope, err := validateBusySendExitEvidence(ctx, conn, s.StoreID(), ev.Scope.InstanceID, &ev)
		if err != nil {
			return err
		}
		now := storeNowMillis()
		for _, l := range scope.Launches {
			if l.Current && !l.Ended {
				if _, err := recordBusySendLaunchEnd(ctx, conn, s.StoreID(), l.LaunchID, BusySendProcessExited, now); err != nil {
					return err
				}
			}
		}
		return exitBusySendLaunches(ctx, conn, s.StoreID(), scope, now)
	})
	if errors.Is(err, ErrBusySendExitNotEstablished) || errors.Is(err, ErrSpawnNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: establish busy-send exit: %w", err)
	}
	return true, nil
}

// exitBusySendLaunches applies validated exit evidence for scope in the
// caller's transaction: every in-scope Launch's End must already be recorded;
// its intent-bearing custody leaves its fences (applyBusySendLaunchExit), its
// standing pause claim ends (launch_end: the evidence covers its recorded
// agent; endBusySendPauseClaim), and the current Launch's judged agent
// identity is kept as its agent process row, so a later retirement judges it
// again after the spawns row moves on.
func exitBusySendLaunches(ctx context.Context, conn *sql.Conn, storeID string, scope BusySendExitScope, now int64) error {
	for _, l := range scope.Launches {
		if err := applyBusySendLaunchExit(ctx, conn, storeID, l.LaunchID, now); err != nil {
			return err
		}
		if err := endBusySendPauseClaim(ctx, conn, l.LaunchID, BusySendPauseLaunchEnd, now); err != nil {
			return err
		}
		if !l.Current {
			continue
		}
		for _, p := range l.Processes {
			if p.Role != BusySendProcessAgent {
				continue
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_launch_processes (launch_id, role, store_id, pid, start_time, recorded_at_ms)
			    VALUES (?, 'agent', ?, ?, ?, ?) ON CONFLICT(launch_id, role) DO NOTHING`,
				l.LaunchID, storeID, p.PID, nonEmptyStartTime(p.StartTime), now); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// nonEmptyStartTime is a start time the process table accepts: a pid-only
// identity proven gone is kept with an explicit "unrecorded" start, which no
// live process ever reports, so a later judgement finds it gone or unknown,
// never alive.
func nonEmptyStartTime(start string) string {
	if start == "" {
		return "unrecorded"
	}
	return start
}

// busySendLaunchExited reports whether launchID's End is recorded and none of
// its hand-overs still holds fenced custody: the durable trace of applied exit
// evidence (or of a Launch End that left nothing to fence). Publication
// checks it before it supersedes the Launch.
func busySendLaunchExited(ctx context.Context, conn busySendLaunchSQL, launchID string) (bool, error) {
	var exited bool
	err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_launches WHERE launch_id = ? AND ended_at_ms IS NOT NULL)
	    AND NOT EXISTS(SELECT 1 FROM busy_send_handovers WHERE launch_id = ? AND state = 'open')`, launchID, launchID).Scan(&exited)
	return exited, err
}

// retireBusySendCustody finalizes a retiring conversation's never-emitted
// custody (SR-6.3; M1's Retire), in the transaction that retires it, after
// exit evidence for every Launch of it: each Held non-Final Message becomes
// Final not_started with reason (record_deleted or conversation_replaced)
// and leaves custody, its reservation freed; a Final Message keeps its
// Result. Claimed or issued custody left means Launch End or exit evidence
// did not run, so it refuses and the caller's transaction rolls back.
func retireBusySendCustody(ctx context.Context, conn busySendLaunchSQL, conversation string, reason BusySendResultReason, now int64) error {
	var inFlight bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_messages
	    WHERE conversation_id = ? AND place IN ('claimed', 'issued'))`, conversation).Scan(&inFlight); err != nil {
		return err
	}
	if inFlight {
		return fmt.Errorf("%w: conversation %s still has custody in a hand-over", ErrBusySendExitNotEstablished, conversation)
	}
	if err := freeBusySendReservations(ctx, conn, conversation, `place = 'held'`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'closed', result_class = 'not_started',
	        result_final = 1, result_reason = ?, final_at_ms = ?, wait_reason = NULL, wait_since_ms = NULL,
	        wait_earlier_message_id = NULL, revision = revision + 1
	    WHERE conversation_id = ? AND place = 'held' AND result_final = 0`, string(reason), now, conversation); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'closed', revision = revision + 1
	    WHERE conversation_id = ? AND place = 'held'`, conversation)
	return err
}

// readBusySendCustodyHeld reports whether instanceID's published conversation
// still holds custody: a Message Held, or claimed or issued in a hand-over,
// that a resume could still carry or finalize (SR-5.5). A row with no
// consistent binding holds none.
func readBusySendCustodyHeld(ctx context.Context, q busySendLaunchSQL, instanceID string) (bool, error) {
	var held bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns s JOIN busy_send_messages m
	    ON m.conversation_id = s.busy_conversation_id AND m.instance_id = s.instance_id
	    WHERE s.instance_id = ? AND m.place IN ('held', 'claimed', 'issued'))`, instanceID).Scan(&held)
	return held, err
}

// refreshBusySendLaunchWaits records the published conversation's Held
// Messages' waits for its current Launch after a lifecycle publication: the
// first waits as admission would derive it (initialBusySendWait's Launch
// rules: agent_starting, launch_ended, route_unavailable while no route is
// recorded, else readiness_unknown), later ones behind it. A resume's new
// Launch thus clears route_unavailable once its route is recorded, and keeps
// agent_starting while it is pending; a staging shows agent_starting and a
// failed creation's restoration launch_ended. A row with no consistent
// published binding holds no Messages and changes nothing.
func refreshBusySendLaunchWaits(ctx context.Context, conn busySendLaunchSQL, instanceID string, now int64) error {
	p, err := readBusySendPublished(ctx, conn, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	first := BusySendWaitReadinessUnknown
	switch {
	case p.state == StatePending:
		first = BusySendWaitAgentStarting
	case p.launchEnded || p.state == StateEnded || p.state == StateMissing:
		first = BusySendWaitLaunchEnded
	case p.routeKind == "" || p.wakePath == "":
		first = BusySendWaitRouteUnavailable
	}
	return refreshBusySendHeldWaits(ctx, conn, p.conversation, first, now)
}
