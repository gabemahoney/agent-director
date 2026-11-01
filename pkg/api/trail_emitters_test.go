package api_test

// trail_emitters_test.go: which trail emitter each caller of the shared writers uses (b.7tz cw S2). A recovery
// check bounds each record by its own operation context (SR-1, SR-3), and so does the store's recovery observer
// by its call context; pause also bounds reporting, while spawn keeps the synchronous write. Each test holds the real
// trail writer so the two emitters behave differently. TestCursorReadsWithBlockedTrail covers the bounded status,
// get and list reads.

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// trailEncodingGate blocks JSON encoding while the actual singleton owns its mutex.
type trailEncodingGate struct {
	entered chan struct{}
	release chan struct{}
}

func (g *trailEncodingGate) MarshalJSON() ([]byte, error) {
	close(g.entered)
	<-g.release
	return []byte(`"released"`), nil
}

// drainTrail acknowledges the existing worker's FIFO with one bounded sentinel.
// Tests reading accepted bounded records must reuse this barrier before disk assertions.
func drainTrail(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := trail.Emit(ctx, "ad.test.trail_emitters_drained", nil); err != nil {
		t.Fatalf("FIFO drain: %v", err)
	}
}

// holdTrail holds the process-wide trail writer's mutex, inside a holder record's encoding, until release (or
// cleanup) lets it go: a synchronous write waits for the release, and a bounded one stays queued. It first drains
// an earlier test's bounded records, still in flight on a slow disk, and fails the test unless none is then
// pending; callers take their status baseline after it. Callers must be serial.
// Serial trail-blocking tests must reuse this holder instead of creating inline copies.
func holdTrail(t *testing.T) (release func()) {
	t.Helper()
	drainTrail(t)
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
	drainTrail(t)
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
	drainTrail(t)
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

// TestPauseCodexEndTickBounded (b.x9o): a committed Codex end returns while
// reporting is blocked; its accepted recovery tick survives caller cancellation.
func TestPauseCodexEndTickBounded(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex.
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%v", cancelled), func(t *testing.T) {
			attempts := 1
			if cancelled {
				attempts = 5 // As for cancelled sweeps, tolerate scheduler noise.
			}
			fastest := time.Duration(math.MaxInt64)
			for i := range attempts {
				t.Run(fmt.Sprintf("sample=%d", i), func(t *testing.T) {
					e := newKillEnv(t)
					r := e.seedRow(t, codexSpec(store.StateWorking, agentGone, true))
					mark := trailMark(t)
					release := holdTrail(t)
					baseline := trail.Default().Status()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var cancelledAt time.Time
					if cancelled {
						e.store.beforeEnded = func() {
							cancelledAt = time.Now()
							cancel()
						}
					}
					type outcome struct {
						err error
						at  time.Time
					}
					done := make(chan outcome, 1)
					go func() {
						_, err := e.pauseWithin(ctx, pauseTimeoutSeconds, pauseParams(r))
						done <- outcome{err, time.Now()}
					}()
					select {
					case got := <-done:
						if got.err != nil {
							t.Fatalf("Pause: %v; want the row ended", got.err)
						}
						if cancelled {
							took := got.at.Sub(cancelledAt)
							t.Logf("API tick returned %v after cancellation at end-write entry", took)
							fastest = min(fastest, took)
						}
					case <-time.After(time.Second):
						t.Fatal("Pause did not return while the trail writer was held")
					}
					if s := trail.Default().Status(); s.Pending != baseline.Pending+1 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
						t.Fatalf("held audit status = %+v; want exactly one accepted tick (baseline %+v)", s, baseline)
					}
					pauAssertState(t, e, r.ID, store.StateEnded)
					release()
					drainTrail(t)
					if s := trail.Default().Status(); s.Pending != 0 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
						t.Errorf("drained audit status = %+v", s)
					}
					assertMoveRecords(t, mark, r.ID, moveWant{source: "ad_recovery", prior: store.StateWorking, now: store.StateEnded,
						denyReason: store.DecisionReasonEnded, reason: "proc_absent"})
				})
			}
			if cancelled && fastest >= api.RecoveryTrailWait/2 {
				t.Errorf("TB1 API tick: fastest cancelled reporting took %v; want under %v", fastest, api.RecoveryTrailWait/2)
			}
		})
	}
}

// TestPauseCodexTerminalReportingBounded (b.x9o, a9 review B1) reaches the
// concrete store's display exit and request closeout before the API tick.
func TestPauseCodexTerminalReportingBounded(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex.
	for _, mode := range []string{"finite", "cancelled", "earlier-deadline"} {
		t.Run(mode, func(t *testing.T) {
			attempts := 1
			if mode != "finite" {
				attempts = 5
			}
			fastest := time.Duration(math.MaxInt64)
			for i := range attempts {
				t.Run(fmt.Sprintf("sample=%d", i), func(t *testing.T) {
					e := newKillEnv(t)
					spec := codexSpec(store.StateWorking, agentAlive, false)
					spec.RelayOn = true
					r := e.seedRow(t, spec)
					// Enter a real display episode, then let the hook retain it while
					// moving the raw state into check_permission.
					for _, at := range []time.Time{e.clock.Now(), e.clock.Now().Add(time.Minute)} {
						row, err := e.st.GetSpawn(r.ID)
						if err != nil {
							t.Fatal(err)
						}
						out, err := e.st.ObserveIdleScreenOutcome(row, store.PaneSample{SHA256: "same-pane"}, at, time.Minute, store.SettleRead{})
						if err != nil || !out.Committed {
							t.Fatalf("display setup: %+v, %v", out, err)
						}
					}
					if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "PermissionRequest", r.Spawn.SessionID, apitest.HookAgent(store.AgentCodex)); !a.Applied {
						t.Fatalf("permission hook: %+v", a)
					}
					tokens := pcxSeedRequests(t, e, r.ID, 1)
					e.codexExits(t, r, nil)
					before, err := e.st.GetSpawn(r.ID)
					if err != nil || before.EndReportedAtMillis <= 0 || before.State != store.StateCheckPermission || before.CommittedDisplay().State != store.StateIdleScreen {
						t.Fatalf("end report must retain the live display: %+v, %v", before, err)
					}
					if pr, err := e.st.GetPermissionRequest(r.ID, tokens[0]); err != nil || pr.Closed() || pr.Decision != "" {
						t.Fatalf("end report must leave request open and undecided: %+v, %v", pr, err)
					}
					mark := trailMark(t)
					release := holdTrail(t)
					baseline := trail.Default().Status()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					type outcome struct {
						err error
						at  time.Time
					}
					done := make(chan outcome, 1)
					started := time.Now()
					go func() {
						callCtx := ctx
						if mode == "earlier-deadline" {
							// Start immediately before entry, not before scheduling the caller.
							var stop context.CancelFunc
							callCtx, stop = context.WithTimeout(ctx, api.RecoveryTrailWait/5)
							defer stop()
						}
						// Pass the concrete store: the narrow killStore wrapper bypasses
						// Pause's production reporting view and cannot cover B1.
						_, err := api.Pause(callCtx, e.st, e.rec, api.PauseEnv{Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now}}, pauseTimeoutSeconds, pauseParams(r))
						done <- outcome{err, time.Now()}
					}()
					// Read through a second connection: the terminal writer may still
					// own its connection while reporting the already committed change.
					limit := time.Now().Add(5 * time.Second)
					var lastLiveRead time.Time
					for {
						readStarted := time.Now()
						if e.columns(t, r.ID).State == store.StateEnded {
							break
						}
						// Timestamp BEFORE a read that still sees the live row: this
						// precedes COMMIT and therefore any fresh reporting allowance.
						lastLiveRead = readStarted
						if time.Now().After(limit) {
							t.Fatal("terminal transaction did not commit")
						}
						runtime.Gosched()
					}
					if mode == "cancelled" {
						cancel() // after entry and COMMIT, never a pre-cancelled initial read
					}
					returned := false
					select {
					case got := <-done:
						returned = true
						if got.err != nil {
							t.Errorf("Pause after committed end: %v", got.err)
						}
						// Ignore samples with no live observation. A delayed COMMIT
						// observer must never move the baseline past reporting entry:
						// its delay only enlarges this conservative commit/report tail.
						if !lastLiveRead.IsZero() {
							took := got.at.Sub(lastLiveRead)
							if took < 0 {
								t.Error("return preceded the last live read")
							} else {
								fastest = min(fastest, took)
							}
							t.Logf("terminal reporting from last live read: %v; mode=%s", took, mode)
						} else {
							t.Log("no live observation; sample excluded from timing assertion")
						}
					case <-time.After(time.Second):
						t.Error("B1: Pause committed ended but did not return while store reporting was held")
					}
					t.Logf("Pause returned=%v after %v; mode=%s; held status=%+v", returned, time.Since(started), mode, trail.Default().Status())
					if returned {
						if s := trail.Default().Status(); s.Pending != baseline.Pending+4 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
							t.Errorf("held audit status = %+v; want exactly four accepted records (baseline %+v)", s, baseline)
						}
					}
					release()
					if !returned {
						select {
						case got := <-done:
							if got.err != nil {
								t.Errorf("Pause after release: %v", got.err)
							}
						case <-time.After(5 * time.Second):
							t.Fatal("Pause did not finish after releasing the writer")
						}
					}
					drainTrail(t)
					if s := trail.Default().Status(); s.Pending != 0 || s.Failed != baseline.Failed || s.Rejected != baseline.Rejected {
						t.Errorf("drained audit status = %+v", s)
					}
					after, err := e.st.GetSpawn(r.ID)
					if err != nil || after.State != store.StateEnded || after.EndedAt == nil || after.DisplayObservation != "" || after.Snapshot.RowVersion != before.Snapshot.RowVersion+1 || after.Identity != before.Identity || after.LifeNumber != before.LifeNumber {
						t.Fatalf("guarded terminal result: %+v, %v; before %+v", after, err, before)
					}
					pr, err := e.st.GetPermissionRequest(r.ID, tokens[0])
					if err != nil || !pr.Closed() || pr.Decision != "deny" || pr.DecisionReason != store.DecisionReasonEnded || pr.DecisionOrigin != store.DecisionOriginObservation {
						t.Fatalf("committed request closure: %+v, %v", pr, err)
					}
					e.assertKillCalls(t) // early terminal recovery never looks up or types into tmux
					if calls := e.pc.StartTimeCalls(); !slices.Equal(calls, []int{r.AgentPID}) {
						t.Errorf("process checks = %v; want only recorded agent %d", calls, r.AgentPID)
					}
					events := displayEvents(t, mark, r.ID)
					if len(events) != 1 {
						t.Fatalf("display exits = %v; want exactly one", events)
					}
					// The first row event is the display exit; the existing exact oracle
					// checks all remaining mutation/orphan/terminal fields and ordering.
					for i, line := range readAPITrailLines(t)[mark:] {
						if line["instance_id"] != r.ID {
							continue
						}
						if line["event"] != "ad.spawn.displayed_state_transition" {
							t.Fatalf("first terminal record = %v; want display exit", line)
						}
						at, err := time.Parse(displayTS, line["ts"].(string))
						if err != nil || at.Before(started.Truncate(time.Millisecond)) || at.After(time.Now()) {
							t.Fatalf("display exit timestamp = %v, %v", at, err)
						}
						assertDisplayEvent(t, events[0], store.StateIdleScreen, store.StateEnded, at)
						assertMoveRecords(t, mark+i+1, r.ID, moveWant{source: "ad_recovery", prior: before.State, now: store.StateEnded,
							denyReason: store.DecisionReasonEnded, reason: "proc_absent", tokens: tokens})
						break
					}
				})
			}
			// A fresh allowance starts after the last live read, so even a
			// late observer cannot make that control beat the bound. Use the
			// fastest sample, as the existing cancellation tests do, so slow
			// scheduling cannot make an individual noisy sample fail the test.
			if mode != "finite" && fastest >= api.RecoveryTrailWait/2 {
				t.Errorf("TB1 terminal reporting: fastest %s reporting took %v; want under %v", mode, fastest, api.RecoveryTrailWait/2)
			}
		})
	}
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
