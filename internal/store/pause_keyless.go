package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// MarkKeylessPauseEndedWithin records keyless pause's deliberate end after
// its caller verified the captured agent's exit and established shared Launch
// exit evidence. Keyless pause must use this bounded terminal writer so row,
// request/subagent cleanup, family state and process evidence commit together.
// It preserves the fresh row's session_id and never changes a replacement Launch.
//
// The write lock protects a fresh row snapshot: a same-Launch hook or sweep
// may have advanced the captured snapshot's version. A live row is ended;
// missing is corrected only when its current state and exited-process records
// attribute it to the same agent's proven exit (recovery's proc_absent).
// Other missing causes and an already-ended row are unchanged, CondChanged.
// Another Launch or agent identity is ErrBusySendLaunchChanged; an absent row
// is CondAbsent. The caller owns the applied move's find_missing tick.
func (s *Store) MarkKeylessPauseEndedWithin(ctx context.Context, captured Spawn, launch BusySendPublishedLaunch, exit ProcessExit, maxWait time.Duration) (string, CondResult, error) {
	// Bounded SQL can finish unwinding after a refused call returns. Keep
	// callback results private until the transaction confirms completion.
	var prior string
	var res CondResult
	var closed []closedRequest
	var exits []displayTransition
	// Selection is pure: no tmux call or process probe. SessionStart can move
	// the same process from pane-only metadata to both identity columns.
	capturedAgent := tmux.SelectAgentProcess(
		tmux.ProcIdentity{PID: captured.PID, Starttime: captured.ProcStarttime},
		tmux.ProcIdentity{PID: captured.Identity.PanePID, Starttime: captured.Identity.PaneStarttime}).Identity
	err := s.durableWriteWithin(ctx, maxWait, func(ctx context.Context, conn *sql.Conn) error {
		row, err := scanSpawn(conn.QueryRowContext(ctx, getSpawnSQL, captured.InstanceID), getSpawnErrs)
		if errors.Is(err, sql.ErrNoRows) {
			res = CondAbsent
			return nil
		}
		if err != nil {
			return err
		}
		p, err := readExaminedBusySendLaunch(ctx, conn, captured.InstanceID)
		if err != nil {
			return err
		}
		agent := tmux.SelectAgentProcess(
			tmux.ProcIdentity{PID: row.PID, Starttime: row.ProcStarttime},
			tmux.ProcIdentity{PID: row.Identity.PanePID, Starttime: row.Identity.PaneStarttime}).Identity
		if launch.LaunchToken == "" || captured.Identity.Token != launch.LaunchToken ||
			p.launch != launch.LaunchID || p.token != launch.LaunchToken || p.provider != launch.Provider ||
			p.conversation != launch.ConversationID || p.launchCoverage != BusySendCovered ||
			row.Identity.Token != launch.LaunchToken || row.LifeNumber != captured.LifeNumber ||
			row.Agent != captured.Agent || agent != capturedAgent {
			return ErrBusySendLaunchChanged
		}
		res = CondChanged
		if !IsLiveState(row.State) && !keylessProcessMissing(row, exit) {
			return nil
		}
		// The snapshot is read under this same write lock, while the fixed
		// Launch checks above keep a refreshed snapshot from adopting a resume.
		statement := markEndedSetSQL + ` WHERE instance_id = ? AND state = ? AND ` + snapshotMatchSQL
		args := append(markEndedArgs(exit), row.InstanceID, row.State)
		args = append(args, snapshotMatchArgs(row.Snapshot)...)
		prior, res, closed, err = terminalMarkOnConn(ctx, conn, s.StoreID(), "store: end keyless pause", statement, args,
			row.InstanceID, DecisionReasonEnded, nil)
		if err != nil || res != CondApplied {
			return err
		}
		exits, err = takeDisplayExits(ctx, conn, row.InstanceID)
		if err != nil {
			return err
		}
		return s.refreshFamiliesAfterStateChange(connQuerier{ctx, conn}, prior, StateEnded, exits...)
	})
	if err != nil {
		return "", 0, fmt.Errorf("store: end keyless pause: %w", err)
	}
	if res == CondApplied {
		reportCtx, cancel := context.WithTimeout(ctx, observationTrailWait)
		defer cancel()
		emitDisplayTransitionsContext(reportCtx, exits)
		emitTerminalCloseout(reportCtx, closed, captured.InstanceID, DecisionReasonEnded, "ad_recovery")
	}
	return prior, res, nil
}

// keylessProcessMissing distinguishes a same-Launch proc_absent mark from a
// missing mark on tmux evidence, an unattributed mark or stale records.
func keylessProcessMissing(row Spawn, exit ProcessExit) bool {
	if row.State != StateMissing {
		return false
	}
	state, ok := row.rawStateRecord()
	if !ok || state.Source != EvidenceProcess {
		return false
	}
	process, ok := row.applicableProcessEvidence()
	return ok && process.Value == ProcessExited && process.Source == EvidenceProcess && process.Reason == "" &&
		process.PID == exit.PID && process.Start == exit.Start
}
