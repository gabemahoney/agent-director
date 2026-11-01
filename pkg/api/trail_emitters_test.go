package api_test

// trail_emitters_test.go: which trail emitter each caller of the shared writers uses (b.7tz cw S2). A recovery
// check bounds each record by its own operation context (SR-1, SR-3), and so does the store's recovery observer
// by its call context; spawn and pause, outside recovery, keep the synchronous write. Each test holds the real
// trail writer so the two emitters behave differently. TestCursorReadsWithBlockedTrail covers the bounded status,
// get and list reads.

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// holdTrail holds the process-wide trail writer's mutex, inside a holder record's encoding, until release (or
// cleanup) lets it go: a synchronous write waits for the release, and a bounded one stays queued. It first drains
// an earlier test's bounded records, still in flight on a slow disk, and fails the test unless none is then
// pending; callers take their status baseline after it. Callers must be serial.
func holdTrail(t *testing.T) (release func()) {
	t.Helper()
	drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := trail.Emit(drain, "ad.test.trail_emitters_drained", nil); err != nil {
		t.Fatalf("FIFO drain before the hold: %v", err)
	}
	if s := trail.Default().Status(); s.Pending != 0 {
		t.Fatalf("prior audit is still pending: %+v", s)
	}
	gate := &trailEncodingGate{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release = func() { once.Do(func() { close(gate.release) }) }
	holder := make(chan error, 1)
	go func() {
		holder <- trail.Emit(context.Background(), "ad.test.trail_emitters_holder", map[string]any{"gate": gate})
	}()
	t.Cleanup(func() {
		release()
		select {
		case err := <-holder:
			if err != nil {
				t.Errorf("holder Emit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("holder did not finish after release")
		}
	})
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("holder did not acquire the real writer mutex")
	}
	return release
}

// cancelAtMark is the fake store whose mark cancels the sweep's context once the mark is made, so the mark's tick
// is written under an already-cancelled operation context (an MCP request that ended mid-sweep).
type cancelAtMark struct {
	*fakeFindMissingStore
	cancel      context.CancelFunc
	cancelledAt time.Time
}

func (s *cancelAtMark) MarkMissingIfSameLife(id string, examined store.RowSnapshot) (string, store.CondResult, error) {
	prior, res, err := s.fakeFindMissingStore.MarkMissingIfSameLife(id, examined)
	s.cancel()
	s.cancelledAt = time.Now()
	return prior, res, err
}

// TestFindMissingTickUnderCancelledContext: find-missing's records are bounded by the sweep's own context, never a
// fresh one. With the trail writer held, a tick written after that context is cancelled does not wait
// RecoveryTrailWait, and it is still accepted: queued without a failure or rejection, then on disk once the writer
// drains. A bound from a fresh context makes every sweep wait the full RecoveryTrailWait; scheduling noise can only
// slow some sweeps, so the fastest of several must beat it. A synchronous write never returns while held.
func TestFindMissingTickUnderCancelledContext(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex.
	const sweeps = 5
	writer := trail.Default()
	mark := trailMark(t)
	release := holdTrail(t)
	baseline := writer.Status()
	fastest := time.Duration(math.MaxInt64)
	var ids []string
	for i := range sweeps {
		id := fmt.Sprintf("fm-cancelled-tick-%d", i)
		ids = append(ids, id)
		ctx, cancel := context.WithCancel(context.Background())
		st := &cancelAtMark{fakeFindMissingStore: &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
			liveRow(id, withSessionStart(600+i, fmStart))}}, cancel: cancel}
		type outcome struct {
			res api.FindMissingResult
			err error
			at  time.Time
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := runFindMissing(st, procfix.New(), fmSweep{ctx: ctx})
			done <- outcome{res, err, time.Now()}
		}()
		var got outcome
		select {
		case got = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("sweep %d did not return while the trail writer was held: its tick was not bounded", i)
		}
		cancel()
		if got.err != nil {
			t.Fatalf("FindMissing: %v", got.err)
		}
		assertLists(t, got.res, []string{id}, nil)
		st.assertGuards(t)
		took := got.at.Sub(st.cancelledAt)
		t.Logf("sweep %d returned %v after its context was cancelled", i, took)
		fastest = min(fastest, took)
	}
	if fastest >= api.RecoveryTrailWait {
		t.Errorf("fastest sweep returned %v after its context was cancelled; want under RecoveryTrailWait %v "+
			"(a tick bounded by a fresh context waits the full bound)", fastest, api.RecoveryTrailWait)
	}
	if s := writer.Status(); s.Pending != baseline.Pending+sweeps || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
		t.Fatalf("held audit status = %+v; want exactly %d ticks pending without failures or rejections (baseline %+v)", s, sweeps, baseline)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := trail.Emit(ctx, "ad.test.trail_emitters_drained", nil); err != nil {
		t.Fatalf("FIFO drain: %v", err)
	}
	if s := writer.Status(); s.Pending != 0 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
		t.Fatalf("drained audit status = %+v", s)
	}
	for _, id := range ids {
		ticks := ticksSince(t, mark, id)
		if len(ticks) != 1 {
			t.Errorf("ticks on %s = %v; want exactly one", id, ticks)
			continue
		}
		assertAPITrailStr(t, ticks[0], "prior_state", store.StateWorking)
		assertAPITrailStr(t, ticks[0], "new_state", store.StateMissing)
		assertAPITrailStr(t, ticks[0], "reconciliation_reason", "proc_absent")
		assertAPITrailStr(t, ticks[0], "source", "ad_find_missing")
	}
}

// TestObserveIdleScreenUnderCancelledContext: the store's recovery observer bounds its displayed-state transition by
// the store's call context (Store.WithContext, as MCP find-missing passes its request's), never a fresh one. With the
// trail writer held, an idle_screen entry commits and its record is admitted; once the context is cancelled the
// observer returns at once, committed, with the record still queued without a failure or rejection, then on disk
// once the writer drains. A synchronous write is never admitted while held. A bound from a fresh context waits out
// the store's 50 ms observer bound, which equals RecoveryTrailWait, after the cancel; the fastest of several must
// return well inside it.
func TestObserveIdleScreenUnderCancelledContext(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex.
	const observations = 5
	sample := store.PaneSample{SHA256: "unchanged-pane"}
	envs := make([]*paneEnv, observations)
	rows := make([]store.Spawn, observations)
	for i := range envs {
		e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
		if out, err := e.s.ObserveIdleScreenOutcome(e.row(t), sample, e.now, time.Minute, store.SettleRead{}); err != nil || !out.Committed {
			t.Fatalf("baseline sample: committed %v, err %v", out.Committed, err)
		}
		e.now = e.now.Add(time.Minute)
		envs[i], rows[i] = e, e.row(t)
	}
	writer := trail.Default()
	mark := trailMark(t)
	release := holdTrail(t)
	baseline := writer.Status()
	fastest := time.Duration(math.MaxInt64)
	for i, e := range envs {
		ctx, cancel := context.WithCancel(context.Background())
		type outcome struct {
			display   store.DisplayDetail
			committed bool
			err       error
			at        time.Time
		}
		done := make(chan outcome, 1)
		go func() {
			out, err := e.s.WithContext(ctx).ObserveIdleScreenOutcome(rows[i], sample, e.now, time.Minute, store.SettleRead{})
			done <- outcome{out.Display, out.Committed, err, time.Now()}
		}()
		admitted := baseline.Pending + i + 1
		deadline := time.Now().Add(5 * time.Second)
		for writer.Status().Pending != admitted {
			select {
			case got := <-done:
				cancel()
				t.Fatalf("observation %d returned %+v before its record was admitted; want it waiting on the held writer", i, got)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("observation %d's record was not admitted while the trail writer was held (%+v): its write was not bounded", i, writer.Status())
			}
			runtime.Gosched()
		}
		cancelledAt := time.Now()
		cancel()
		var got outcome
		select {
		case got = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("observation %d did not return after its context was cancelled", i)
		}
		want := store.DisplayDetail{State: store.StateIdleScreen, SinceMillis: e.now.UnixMilli(), UnchangedMinutes: 1}
		if got.err != nil || !got.committed || got.display != want {
			t.Fatalf("observation %d = %+v committed %v, err %v; want %+v committed", i, got.display, got.committed, got.err, want)
		}
		if s := writer.Status(); s.Pending != admitted || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
			t.Fatalf("held audit status after observation %d = %+v; want %d records pending without failures or rejections (baseline %+v)", i, s, i+1, baseline)
		}
		took := got.at.Sub(cancelledAt)
		t.Logf("observation %d returned %v after its context was cancelled", i, took)
		fastest = min(fastest, took)
	}
	if fastest >= api.RecoveryTrailWait/2 {
		t.Errorf("fastest observation returned %v after its context was cancelled; want well under RecoveryTrailWait %v "+
			"(a record bounded by a fresh context waits out the observer bound)", fastest, api.RecoveryTrailWait)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := trail.Emit(ctx, "ad.test.trail_emitters_drained", nil); err != nil {
		t.Fatalf("FIFO drain: %v", err)
	}
	if s := writer.Status(); s.Pending != 0 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
		t.Fatalf("drained audit status = %+v", s)
	}
	for _, e := range envs {
		if display := e.row(t).CommittedDisplay(); display.State != store.StateIdleScreen {
			t.Errorf("%s committed display = %+v; want idle_screen", e.id, display)
		}
		events := displayEvents(t, mark, e.id)
		if len(events) != 1 {
			t.Errorf("display transitions of %s = %v; want exactly one", e.id, events)
			continue
		}
		assertDisplayEvent(t, events[0], store.StateWorking, store.StateIdleScreen, e.now)
	}
}

// TestPauseCodexEndTickSynchronous: pause is not a recovery check, so its Codex end tick keeps the synchronous
// write. With the trail writer held, pause does not return until the writer is released (a bounded write returns
// after RecoveryTrailWait with the tick still queued); once it returns, the tick is on disk and nothing is queued.
func TestPauseCodexEndTickSynchronous(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex.
	e := newKillEnv(t)
	r := e.seedRow(t, codexSpec(store.StateWorking, agentGone, true))
	mark := trailMark(t)
	release := holdTrail(t)
	baseline := trail.Default().Status()
	done := make(chan error, 1)
	go func() {
		_, err := e.pause(pauseParams(r))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Pause returned (err %v) while the trail writer was held; want it to wait for its end tick's write", err)
	case <-time.After(5 * api.RecoveryTrailWait):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Pause: %v; want the row ended", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pause did not return after the trail writer was released")
	}
	if s := trail.Default().Status(); s.Pending != 0 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
		t.Errorf("audit status on return = %+v; want nothing queued, failed or rejected", s)
	}
	assertMoveRecords(t, mark, r.ID, moveWant{prior: store.StateWorking, now: store.StateEnded,
		denyReason: store.DecisionReasonEnded, reason: "proc_absent"})
	pauAssertState(t, e, r.ID, store.StateEnded)
}

// TestScanNameHeldSynchronous: spawn is not a recovery check, so its label scan's ad.launch.name_held keeps the
// synchronous write. With the trail writer held, the refused spawn does not return until the writer is released (a
// bounded write returns after RecoveryTrailWait with the record still queued); once it returns, the one record is
// on disk and nothing is queued.
func TestScanNameHeldSynchronous(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv, and it holds the
	// process-wide trail singleton's mutex.
	e := newScanEnv(t)
	id := scanID()
	e.rec.SeedSessions(e.socket, e.leftover("old-life", "$3", id, 1790000000))
	mark := trailLen(t)
	release := holdTrail(t)
	baseline := trail.Default().Status()
	done := make(chan error, 1)
	go func() {
		_, err := e.c.Spawn(api.SpawnParams{Agent: "claude", CWD: t.TempDir(), InstanceID: id})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Spawn returned (err %v) while the trail writer was held; want it to wait for its name_held write", err)
	case <-time.After(5 * api.RecoveryTrailWait):
	}
	release()
	select {
	case err := <-done:
		assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
	case <-time.After(5 * time.Second):
		t.Fatal("Spawn did not return after the trail writer was released")
	}
	if s := trail.Default().Status(); s.Pending != 0 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
		t.Errorf("audit status on return = %+v; want nothing queued, failed or rejected", s)
	}
	if recs := ptRecords(t, mark, "ad.launch.name_held", id); len(recs) != 1 {
		t.Errorf("ad.launch.name_held records of %s on return = %v; want exactly one", id, recs)
	}
	e.assertNothingWritten(t, id)
}
