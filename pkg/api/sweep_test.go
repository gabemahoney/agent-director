package api_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.x9o cb: fresh SQLite stores and independent clocks prevent the first
// alias from performing the second alias's recovery on its behalf.
func TestSweepAliases(t *testing.T) {
	t.Parallel()
	var want api.SweepResult
	for _, entry := range []string{"Sweep", "FindMissing", "Client.Sweep", "Client.FindMissing"} {
		t.Run(entry, func(t *testing.T) {
			e := newKillEnv(t)
			for _, row := range []struct {
				id, state string
				pid       int
			}{
				{"a-live", store.StateWaiting, 9201}, {"b-gone", store.StateMissing, 9202},
				{"c-ended", store.StateEnded, 9203}, {"d-pending", store.StatePending, 9204},
			} {
				_, err := apitest.SeedSpawn(e.dbPath, row.id, row.state, "/tmp", "off", "", false,
					apitest.WithNoPane(), apitest.WithPID(row.pid), apitest.WithProcStarttime(fmStart),
					apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli()))
				if err != nil {
					t.Fatal(err)
				}
			}
			e.pc.Set(9201, procfix.Alive(fmStart))
			e.pc.Set(9202, procfix.Gone())
			var got api.SweepResult
			var err error
			switch entry {
			case "Sweep", "FindMissing":
				got, err = runFindMissing(e.st, e.pc, fmSweep{canonical: entry == "Sweep", tmux: e.rec, clock: e.clock})
			default:
				c, _ := e.client(t)
				if entry == "Client.Sweep" {
					got, err = c.Sweep(context.Background())
				} else {
					got, err = c.FindMissing(context.Background())
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			assertLists(t, got, nil, nil)
			apitest.AssertConfirmationsShape(t, jsonOf(t, got))
			if len(got.Checks) != 4 {
				t.Fatalf("checks = %+v; want every selected row", got.Checks)
			}
			for i, state := range []string{store.StateWaiting, store.StateMissing, store.StateEnded, store.StatePending} {
				check := got.Checks[i]
				if check.State != state || check.Completion != "complete" || check.Reason != nil {
					t.Errorf("check %d = %+v; want %s, complete/null", i, check, state)
				}
			}
			if got.Checks[2].Confirmations.ProcessLiveness.ConfirmedAt != nil || got.Checks[3].Confirmations.ProcessLiveness.Status != "pending" {
				t.Error("complete finished/pending checks must not invent successful process evidence")
			}
			if entry == "Sweep" {
				want = got
			} else if !reflect.DeepEqual(got, want) {
				t.Errorf("%s = %s; Sweep = %s", entry, jsonOf(t, got), jsonOf(t, want))
			}
			if calls := e.pc.StartTimeCalls(); !slices.Equal(calls, []int{9201}) {
				t.Errorf("process calls %v; want one per eligible selected row", calls)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// An incomplete row must keep its place even if cancellation prevents every
// attempt. Time passing is not evidence and cannot refresh these records.
func TestSweepCancellationRetainsSelectedEvidence(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	a := e.seedRow(t, killRowSpec{State: store.StateWorking, Opts: []apitest.SpawnOption{apitest.WithIdleScreenTimeout(60)}})
	b := e.seedRow(t, killRowSpec{State: store.StateEnded})
	if hook := apitest.ApplyAgentHook(t, e.dbPath, a.ID, "PreToolUse", ""); !hook.Applied {
		t.Fatalf("PreToolUse = %+v", hook)
	}
	e.rec.SetCapture(a.Socket, a.Spawn.Identity.PaneID, "unchanged pane")
	// The hook owns lifecycle evidence; two real checks establish the process
	// record and an applicable idle-screen pane record on the injected clock.
	fgcSetClock(e.clock, time.Now())
	mustSweep(t, e.st, e.pc, fmSweep{canonical: true, clock: e.clock, tmux: e.rec})
	e.clock.Advance(time.Minute)
	before := mustSweep(t, e.st, e.pc, fmSweep{canonical: true, clock: e.clock, tmux: e.rec})
	wantIDs := []string{a.ID, b.ID}
	slices.Sort(wantIDs)
	var ids []string
	for _, row := range before.Checks {
		ids = append(ids, row.InstanceID)
		if row.Completion != "complete" || row.Reason != nil {
			t.Fatalf("fixture check incomplete: %s", jsonOf(t, row))
		}
		if row.InstanceID == a.ID {
			if row.State != store.StateWorking || row.DisplayedState != api.StateIdleScreen || row.ProcessLiveness != "alive" {
				t.Fatalf("fixture lacks applicable values: %s", jsonOf(t, row))
			}
			for source, record := range map[string]api.Confirmation{
				"agent_event": row.Confirmations.State, "process": row.Confirmations.ProcessLiveness, "pane": row.Confirmations.DisplayedState,
			} {
				if record.Status != "confirmed" || record.ConfirmedAt == nil || record.CheckedAt == nil || record.Source == nil || *record.Source != source || record.Reason != nil {
					t.Fatalf("fixture lacks populated %s evidence: %s", source, conf(record))
				}
			}
		} else {
			for _, record := range []api.Confirmation{row.Confirmations.State, row.Confirmations.ProcessLiveness, row.Confirmations.DisplayedState} {
				if conf(record) != unknownRecord("no_evidence") {
					t.Fatalf("finished fixture gained evidence: %s", jsonOf(t, row))
				}
			}
		}
	}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("fixture IDs = %v; want %v", ids, wantIDs)
	}
	probes, envReads := e.pc.StartTimeCalls(), e.pc.EnvReads()
	e.rec.Reset()
	e.clock.Advance(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := interleavedStore{Store: e.st, between: cancel}
	got, err := runFindMissing(s, e.pc, fmSweep{canonical: true, ctx: ctx, clock: e.clock, tmux: e.rec})
	if err != nil {
		t.Fatal(err)
	}
	assertLists(t, got, nil, nil)
	if len(got.Checks) != len(before.Checks) {
		t.Fatalf("checks = %+v; want all selected rows", got.Checks)
	}
	for i, row := range got.Checks {
		want := before.Checks[i]
		reason := "deadline"
		want.Completion, want.Reason = "incomplete", &reason
		if !reflect.DeepEqual(row, want) {
			t.Errorf("cancelled row = %s; want unchanged values and evidence %s", jsonOf(t, row), jsonOf(t, want))
		}
	}
	if !slices.Equal(e.pc.StartTimeCalls(), probes) || e.pc.EnvReads() != envReads {
		t.Error("cancelled rows were probed")
	}
	e.assertNoTmuxCall(t)
}

// The permission read is a real per-agent adapter boundary. A failed read
// leaves later eligible adapters unattempted; other rows still make progress.
type sweepPermissionFailure struct {
	*store.Store
	id  string
	err error
}

func (s sweepPermissionFailure) PermissionRequestsForSpawnWithin(id string, wait time.Duration) ([]store.PermissionRow, error) {
	if id == s.id {
		return nil, s.err
	}
	return s.Store.PermissionRequestsForSpawnWithin(id, wait)
}

func TestSweepIncompleteReasonsAndIndependentProgress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"busy", store.ErrStoreBusy}, {"unreadable", errors.New("disk read")},
		{"stale_observation", inputdelivery.ErrStale}, {"commit_unconfirmed", inputdelivery.ErrUnconfirmed},
		{"no_evidence", store.ErrSpawnNotFound}, {"deadline", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			a := e.seedRow(t, killRowSpec{State: store.StateWorking})
			b := e.seedRow(t, killRowSpec{State: store.StateWaiting})
			s := sweepPermissionFailure{Store: e.st, id: a.ID, err: tc.err}
			got, err := runFindMissing(s, e.pc, fmSweep{canonical: true, clock: e.clock, tmux: e.rec})
			if err != nil {
				t.Fatal(err)
			}
			assertLists(t, got, nil, nil)
			if len(got.Checks) != 2 {
				t.Fatalf("checks = %+v", got.Checks)
			}
			for _, row := range got.Checks {
				if row.InstanceID == a.ID {
					if row.Completion != "incomplete" || row.Reason == nil || *row.Reason != tc.name {
						t.Errorf("failed row = %+v; want incomplete/%s", row, tc.name)
					}
					if row.Confirmations.DisplayedState.CheckedAt != nil {
						t.Error("unattempted display gained a timestamp")
					}
				} else if row.InstanceID != b.ID || row.Completion != "complete" || row.Reason != nil {
					t.Errorf("independent row = %+v; want complete/null", row)
				}
				if row.Confirmations.ProcessLiveness.Status != "confirmed" {
					t.Errorf("process attempt lost: %+v", row)
				}
			}
		})
	}
}

func TestSweepFailedAttemptCanBeComplete(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	a := e.seedRow(t, killRowSpec{State: store.StateWaiting})
	b := e.seedRow(t, killRowSpec{State: store.StateWaiting})
	s := &busyProcessRecordStore{Store: e.st, busy: a.ID}
	got, err := runFindMissing(s, e.pc, fmSweep{canonical: true, tmux: e.rec, clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range got.Checks {
		ids = append(ids, row.InstanceID)
	}
	wantIDs := []string{a.ID, b.ID}
	slices.Sort(wantIDs)
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("selected IDs = %v; want %v once each", ids, wantIDs)
	}
	for _, row := range got.Checks {
		if row.Completion != "complete" || row.Reason != nil {
			t.Errorf("attempted row = %+v; want complete", row)
		}
		want := confirmed(e.clock.Now(), "process")
		if row.InstanceID == a.ID {
			want = "not_confirmed|-|-|" + confAt(e.clock.Now()) + "|busy"
		}
		if conf(row.Confirmations.ProcessLiveness) != want {
			t.Errorf("%s: process = %s; want %s", row.InstanceID, conf(row.Confirmations.ProcessLiveness), want)
		}
	}
	if storedProcess(t, e.st, a.ID) != "" || storedProcess(t, e.st, b.ID) == "" {
		t.Error("failed/independent records were not kept separate")
	}
}

func TestSweepFixedSnapshot(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	removed := e.seedRow(t, killRowSpec{State: store.StateWorking, Agent: agentGone})
	changed := e.seedRow(t, killRowSpec{State: store.StateWorking, Agent: agentGone})
	finished := e.seedRow(t, killRowSpec{State: store.StateEnded})
	var added killRow
	s := interleavedStore{Store: e.st, between: func() {
		cursorReadSQL(t, e.dbPath, "DELETE FROM spawns WHERE instance_id=?", removed.ID)
		cursorReadSQL(t, e.dbPath, "UPDATE spawns SET launch_token=?, row_version=row_version+1 WHERE instance_id=?", tmuxfix.OtherToken, changed.ID)
		added = e.seedRow(t, killRowSpec{State: store.StateWorking, Agent: agentGone})
	}}
	got, err := runFindMissing(s, e.pc, fmSweep{canonical: true, clock: e.clock, tmux: e.rec})
	if err != nil {
		t.Fatal(err)
	}
	assertLists(t, got, nil, nil)
	var ids []string
	for _, row := range got.Checks {
		ids = append(ids, row.InstanceID)
		if row.InstanceID == removed.ID && (row.Completion != "incomplete" || row.Reason == nil || *row.Reason != "no_evidence") {
			t.Errorf("removed row = %+v", row)
		}
		if row.InstanceID == changed.ID && (row.State != store.StateWorking || row.Confirmations.ProcessLiveness.ConfirmedAt != nil) {
			t.Errorf("old evidence qualified new owner: %+v", row)
		}
	}
	want := []string{removed.ID, changed.ID, finished.ID}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Errorf("selected IDs %v; want %v once each", ids, want)
	}
	if slices.Contains(e.pc.StartTimeCalls(), added.AgentPID) {
		t.Error("row added after selection was probed")
	}
	if e.columns(t, added.ID).State != store.StateWorking {
		t.Error("row added after selection was recovered")
	}
}

// Real BEGIN contention and a COMMIT whose reply is lost must not increment
// legacy counters. Reopen SQLite before any confirming public read.
func TestSweepSQLiteUncertainMarkDoesNotCount(t *testing.T) {
	t.Parallel()
	for _, cut := range []string{"busy", "commit_unconfirmed"} {
		t.Run(cut, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StateWorking, Agent: agentGone})
			c, _ := e.client(t)
			var lost atomic.Bool
			release := func() {}
			if cut == "busy" {
				release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
			} else {
				stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
					return sqliteobserve.Wrap(d, sqliteobserve.Hooks{AfterCommit: func(context.Context) error {
						lost.Store(true)
						return errors.New("lost real COMMIT acknowledgement")
					}})
				})
				if err != nil {
					t.Fatal(err)
				}
				release = func() {
					if err := stop(context.Background()); err != nil {
						t.Error(err)
					}
				}
			}
			got, err := c.Sweep(context.Background())
			release()
			if err != nil {
				t.Fatal(err)
			}
			assertLists(t, got, nil, nil)
			if len(got.Checks) != 1 {
				t.Fatalf("checks = %+v", got.Checks)
			}
			check := got.Checks[0]
			if check.Completion != "incomplete" || check.Reason == nil || *check.Reason != cut {
				t.Errorf("uncertain mark = %+v; want incomplete/%s", check, cut)
			}
			reopened, err := store.Open(e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			raw, err := reopened.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cut == "busy" {
				if raw.State != store.StateWorking || raw.ProcessEvidence != "" || check.Confirmations.ProcessLiveness.ConfirmedAt != nil {
					t.Errorf("busy mark invented durable success: %+v %+v", raw, check)
				}
			} else {
				value, _ := raw.ProcessConfirmation()
				if !lost.Load() || raw.State != store.StateMissing || value != store.ProcessExited {
					t.Errorf("lost COMMIT reply did not exercise a durable mark: %s %s lost=%v", raw.State, value, lost.Load())
				}
			}
		})
	}
}

func TestSweepNarrowStoreAndFatalSelection(t *testing.T) {
	t.Parallel()
	for _, canonical := range []bool{false, true} {
		pc := procfix.New()
		pc.Set(9301, procfix.Alive(fmStart))
		s := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{liveRow("legacy", withSessionStart(9301, fmStart))}}
		got, err := runFindMissing(s, pc, fmSweep{canonical: canonical})
		if err != nil || len(got.Checks) != 1 {
			t.Fatalf("narrow store: %+v %v", got, err)
		}
		check := got.Checks[0]
		if check.Completion != "incomplete" || check.Reason == nil || *check.Reason != "unsupported" {
			t.Errorf("narrow coverage = %+v; want explicit unsupported", check)
		}
		failed, err := runFindMissing(&fakeFindMissingStore{listErr: errSentinel}, pc, fmSweep{canonical: canonical})
		if !errors.Is(err, errSentinel) || jsonOf(t, failed) != `{"checks":[],"count":0,"ids":[],"unverified":0,"unverified_ids":[]}` {
			t.Errorf("fatal selection = %+v %v", failed, err)
		}
	}
}

// T1 scopes both the mark and permission closeout, without changing the
// released source on sweep or find-missing. A new Client is used per case.
func TestSweepAndReadPauseTickSources(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"sweep", "find-missing", "get", "status", "list", "pause"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			spec := codexSpec(store.StateWorking, agentGone, true)
			spec.RelayOn = true
			r := e.seedRow(t, spec)
			tokens := pcxSeedRequests(t, e, r.ID, 1)
			c, _ := e.client(t)
			mark := trailMark(t)
			var err error
			switch surface {
			case "sweep", "find-missing":
				var got api.SweepResult
				if surface == "sweep" {
					got, err = c.Sweep(context.Background())
				} else {
					got, err = c.FindMissing(context.Background())
				}
				if err == nil {
					assertLists(t, got, nil, nil)
					if len(got.Checks) != 1 || got.Checks[0].State != store.StateEnded || got.Checks[0].ProcessLiveness != "exited" || got.Checks[0].Completion != "complete" || got.Checks[0].Reason != nil {
						t.Fatalf("Codex ended must not count missing: %+v", got)
					}
				}
			case "get":
				_, err = c.Get(r.ID)
			case "status":
				_, err = c.Status(r.ID)
			case "list":
				_, err = c.List(api.ListParams{})
			case "pause":
				_, err = c.Pause(context.Background(), pauseParams(r))
			}
			if err != nil {
				t.Fatal(err)
			}
			source := "ad_recovery"
			if surface == "sweep" || surface == "find-missing" {
				source = "ad_find_missing"
			}
			assertMoveRecords(t, mark, r.ID, moveWant{source: source, prior: store.StateWorking, now: store.StateEnded, denyReason: store.DecisionReasonEnded, reason: "proc_absent", tokens: tokens})
			e.assertNothingSent(t)
		})
	}
}

func TestSweepEvidenceRetainsFirstConfirmation(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWorking})
	c, _ := e.client(t)
	first, err := c.Sweep(context.Background())
	if err != nil || len(first.Checks) != 1 {
		t.Fatalf("first sweep = %+v %v", first, err)
	}
	record := first.Checks[0].Confirmations.ProcessLiveness
	if record.Status != "confirmed" || record.ConfirmedAt == nil {
		t.Fatalf("fixture lacks first confirmation: %+v", record)
	}
	e.clock.Advance(time.Minute)
	secondAt := e.clock.Now()
	second, err := c.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := second.Checks[0].Confirmations.ProcessLiveness
	if next.Status != "confirmed" || next.ConfirmedAt == nil || *next.ConfirmedAt != *record.ConfirmedAt || next.CheckedAt == nil || *next.CheckedAt != confAt(secondAt) {
		t.Errorf("same value redated first confirmation: %+v -> %+v", record, next)
	}
	e.clock.Advance(time.Minute)
	e.pc.Set(r.AgentPID, procfix.Unreadable())
	failedAt := e.clock.Now()
	failed, err := c.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := failed.Checks[0]
	want := "not_confirmed|" + *record.ConfirmedAt + "|process|" + confAt(failedAt) + "|unreadable"
	if row.ProcessLiveness != "alive" || conf(row.Confirmations.ProcessLiveness) != want {
		t.Errorf("failed latest attempt = %+v; want retained alive and %s", row, want)
	}
	if !reflect.DeepEqual(row.Confirmations.State, first.Checks[0].Confirmations.State) {
		t.Error("process checks refreshed unrelated lifecycle confirmation")
	}
}

// Hide optional writers using an interface view of the existing real store.
// This preserves fixed reads and unrelated adapters without forwarding copies.
type sweepOptionalStore interface {
	api.FindMissingStore
	api.InputProofStore
	RecoveryRows([]string, store.SettleRead) ([]store.Spawn, error)
	PermissionRequestsForSpawn(string) ([]store.PermissionRow, error)
	PermissionRequestsForSpawnWithin(string, time.Duration) ([]store.PermissionRow, error)
	ObserveNeedsIntelligence(store.Spawn, []store.PermissionSample, time.Time, store.SettleRead) (store.DisplayDetail, map[string]string, error)
	ProvisionalTranscriptsFor(string) ([]store.ProvisionalTranscript, error)
	HealProvisionalTranscript(store.ProvisionalTranscript, string) (bool, error)
}
type sweepWithProcess struct {
	sweepOptionalStore
	backing *store.Store
}

func (s sweepWithProcess) RecordProcessEvidence(row store.Spawn, record string) (store.CondResult, error) {
	return s.backing.RecordProcessEvidence(row, record)
}

type sweepWithLegacyRepair struct {
	sweepWithProcess
	calls *int
}

func (s sweepWithLegacyRepair) RepairCheckPermissionIfSameLife(string, store.RowSnapshot) (string, store.CondResult, error) {
	*s.calls++
	return "", store.CondChanged, nil
}

type sweepWithSampleRepair struct {
	sweepWithProcess
	calls *int
}

func (s sweepWithSampleRepair) RepairCheckPermissionSample(store.Spawn, []store.PermissionRow) (string, store.CondResult, error) {
	*s.calls++
	return "", store.CondChanged, nil
}

// b.x9o cb review B3: unavailable writers are different from attempted no-ops.
func TestSweepOptionalWriterCoverage(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing process", "missing repair", "legacy unchanged", "sample unchanged", "request still open"} {
		t.Run(mode, func(t *testing.T) {
			e := newKillEnv(t)
			state := store.StateCheckPermission
			if mode == "missing process" {
				state = store.StateWaiting
			}
			r := e.seedRow(t, killRowSpec{State: state, RelayOn: true})
			var narrow sweepOptionalStore = e.st
			process := sweepWithProcess{sweepOptionalStore: narrow, backing: e.st}
			var source api.FindMissingStore = process
			calls := 0
			switch mode {
			case "missing process":
				source = struct{ sweepOptionalStore }{narrow}
			case "legacy unchanged":
				source = sweepWithLegacyRepair{process, &calls}
			case "sample unchanged":
				source = sweepWithSampleRepair{process, &calls}
			case "request still open":
				if _, err := apitest.SeedPermissionRequest(e.dbPath, r.ID, "Bash", apitest.PermissionTimes(e.clock.Now(), time.Time{}, e.clock.Now().Add(time.Hour), time.Time{}, time.Time{}, "")); err != nil {
					t.Fatal(err)
				}
			}
			mark := trailMark(t)
			got, err := runFindMissing(source, e.pc, fmSweep{canonical: true, clock: e.clock, tmux: e.rec})
			if err != nil {
				t.Fatal(err)
			}
			assertLists(t, got, nil, nil)
			apitest.AssertConfirmationsShape(t, jsonOf(t, got))
			if len(got.Checks) != 1 {
				t.Fatalf("checks=%+v", got.Checks)
			}
			row := got.Checks[0]
			if mode == "missing process" || mode == "missing repair" {
				if row.Completion != "incomplete" || row.Reason == nil || *row.Reason != "unsupported" {
					t.Errorf("missing capability=%+v", row)
				}
			} else if row.Completion != "complete" || row.Reason != nil {
				t.Errorf("attempted/inapplicable=%s", jsonOf(t, row))
			}
			wantCalls := 0
			if mode == "legacy unchanged" || mode == "sample unchanged" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Errorf("repair calls=%d; want %d", calls, wantCalls)
			}
			if mode == "missing process" && (row.Confirmations.ProcessLiveness.Status != "not_confirmed" || row.Confirmations.ProcessLiveness.Reason == nil || *row.Confirmations.ProcessLiveness.Reason != "unsupported" || row.Confirmations.ProcessLiveness.ConfirmedAt != nil) {
				t.Errorf("unsupported process writer=%+v", row)
			}
			if probes := e.pc.StartTimeCalls(); !slices.Equal(probes, []int{r.AgentPID}) {
				t.Errorf("probes=%v; want one agent probe", probes)
			}
			if raw, err := e.st.GetSpawn(r.ID); err != nil || raw.State != state {
				t.Fatalf("optional writer changed state: %+v %v", raw, err)
			}
			if records := ptRecords(t, mark, "ad.find_missing.tick", r.ID); len(records) != 0 {
				t.Errorf("unavailable/no-op repair ticked: %+v", records)
			}
			e.assertNoTmuxCall(t)
		})
	}
}
