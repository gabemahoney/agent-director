package api

import (
	"context"
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// signalPauseStore supplies bounded row reads and the deliberate end writer;
// uncovered Codex pause never enters the covered Launch/claim protocol.
type signalPauseStore interface {
	GetSpawnContext(context.Context, string, time.Duration) (Spawn, error)
	MarkSignalPauseEndedWithin(context.Context, Spawn, store.ProcessExit, time.Duration) (string, CondResult, error)
}

// signalPause stops a busy uncovered Codex agent within the original call
// budget, reusing covered pause's signal and exit judgements. No keys, relay
// check, input session or tmux call is needed, including with an open request.
func (r *pauseRun) signalPause(b keylessBudget, row Spawn) error {
	if err := r.ctxErr(); err != nil {
		return err
	}
	if b.spent() {
		return budgetExhausted(row.InstanceID, false)
	}
	st, ok := r.s.(signalPauseStore)
	if !ok || r.procs == nil {
		return agentUnverifiable(row.InstanceID)
	}
	agent := agentProcess(row, row.Identity).Identity
	if state := tmux.JudgeProcess(r.procs, agent); state != tmux.ProcAlive && state != tmux.ProcGone {
		return agentUnverifiable(row.InstanceID)
	}
	again, err := st.GetSpawnContext(b.ctx, row.InstanceID, b.wait())
	if err != nil {
		return r.storeFailed(b, row.InstanceID, false, err)
	}
	if launchOf(again) != launchOf(row) || again.Agent != row.Agent || again.BusySend != store.BusySendNotCovered ||
		agentProcess(again, again.Identity).Identity != agent {
		return launchChangedBeforeSignal(row.InstanceID)
	}
	if err := r.ctxErr(); err != nil {
		return err
	}
	if b.spent() {
		return budgetExhausted(row.InstanceID, false)
	}
	sent := false
	if tmux.JudgeProcess(r.procs, agent) != tmux.ProcGone {
		sent, err = signalOutcome(row.InstanceID, pauseTerminate(agent.PID, agent.Starttime))
		if err != nil {
			return err
		}
	}
	if err := r.awaitAgentExit(row.InstanceID, agent, b.deadline, sent); err != nil {
		return err
	}
	if err := r.established(b, row.InstanceID, sent, false); err != nil {
		return err
	}
	// The adapter keeps the shared outcome writer's closeout and tick ordering;
	// its bounded transaction guards the signalled Launch, refreshing only its
	// snapshot so hooks and process-based recovery cannot prevent deliberate end.
	end := signalPauseEnd{EndedMover: r.s, st: st, b: b, row: row}
	w := markEndedSameLife(recoveryTickTrail(recoveryTrail(b.ctx), "ad_recovery"), end, row.InstanceID, row.Snapshot,
		provenExit(agent, clockNow(r.now)), reasonProcAbsent, nil, nil)
	if errors.Is(w.Err, store.ErrBusySendLaunchChanged) || (w.Err == nil && w.Res == store.CondAbsent) {
		return notPausable(row.InstanceID, "the agent exited but its Launch was replaced before its keyless shutdown was established; nothing more was recorded; inspect with get",
			pauseCauseLaunchChanged, &sent)
	}
	if w.Err != nil {
		return r.storeFailed(b, row.InstanceID, sent, w.Err)
	}
	return r.established(b, row.InstanceID, sent, w.Res == store.CondApplied)
}

type signalPauseEnd struct {
	EndedMover
	st  signalPauseStore
	b   keylessBudget
	row Spawn
}

func (s signalPauseEnd) MarkEndedIfSameLifeFrom(_ string, _ RowSnapshot, exit store.ProcessExit) (string, CondResult, bool, error) {
	prior, res, err := s.st.MarkSignalPauseEndedWithin(s.b.ctx, s.row, exit, s.b.wait())
	return prior, res, err == nil && res == store.CondApplied, err
}
