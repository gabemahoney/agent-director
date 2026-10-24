package api_test

// needs_intelligence_test.go — a fallen-back permission request's fallback
// clock (b.ob8), still established and stored by reads, which since b.zpv
// never turns the row needs_intelligence: it reads check_permission however
// long the request waits (permission_state_contract_test.go holds the rule's
// regression test). needs_intelligence_failure_test.go holds the
// needs_intelligence display an earlier release stored.

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// needsIntelligenceClient keeps every read on the same injected clock and
// refuses sleeps; timeout is its [state] needs_intelligence_timeout_seconds,
// which has no effect.
func needsIntelligenceClient(t *testing.T, e *relayEnv, settle, timeout int64) *api.Client {
	t.Helper()
	state, err := e.s.GetSpawnState(e.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetSettle(e.dbPath, e.id, state, 0); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, cfg, apitest.ConfigKeys{StateSettleMs: settle, RelayTimeoutSeconds: 3600, NeedsIntelligenceTimeoutSeconds: timeout})
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfg, TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetProcCheckerForTest(c, e.pc)
	api.SetClockForTest(c, func() time.Time { return e.now })
	api.SetSelfPIDNSForTest(c, func() (string, bool) { return e.ns, e.ns != "" })
	api.SetSleepForTest(c, func(d time.Duration) { t.Errorf("read slept %s", d) })
	watchDisplayClient(c, e.s)
	return c
}

// requestFacts sets facts of e's request A.
func requestFacts(t *testing.T, e *relayEnv, opts ...apitest.PermissionOption) {
	t.Helper()
	if err := apitest.SetPermissionFacts(e.dbPath, e.request(t).RequestID, opts...); err != nil {
		t.Fatal(err)
	}
}

// observationVersion is the committed observer version of id's row.
func observationVersion(t *testing.T, e *relayEnv) int64 {
	t.Helper()
	row, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	return row.ObservationVersion
}

// TestFallbackClockBoundaries: each basis (the hook process gone, a hook not
// checkable past confirm_by, a pre-v7 request's deadline, Cursor relay-off
// admission) stores its request's fallback clock at its start, from the read
// at that instant, and the row reads check_permission with no Displayed State
// before, at and long after it, the retired 30 s needs_intelligence timeout
// included (b.zpv); get and list agree and keep the raw state_changed_at.
func TestFallbackClockBoundaries(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, tc := range []struct{ basis, stored string }{
			{"process", store.FallbackProcess}, {"deadline", store.FallbackDeadline}, {"legacy", store.FallbackDeadline}, {"admission", store.FallbackAdmission},
		} {
			basis := tc.basis
			if agent == store.AgentCursor && (basis == "process" || basis == "legacy") || agent != store.AgentCursor && basis == "admission" {
				continue
			}
			t.Run(agent+"/"+basis, func(t *testing.T) {
				e := newRelayEnvAgent(t, relayEnvHook, agent)
				e.now = displayAt
				start := displayAt
				token := storefix.TestRequestTokenA
				switch basis {
				case "process":
					e.pc.Set(relayEnvHook.PID, procfix.Gone())
					requestFacts(t, e, apitest.PermissionTimes(start.Add(-24*time.Hour), time.Time{}, start.Add(time.Hour), start, time.Time{}, ""))
				case "deadline":
					e.ns = ""
					requestFacts(t, e, apitest.PermissionTimes(start.Add(-24*time.Hour), time.Time{}, start, time.Time{}, time.Time{}, ""))
				case "legacy":
					start = start.Truncate(time.Second)
					// Use the existing legacy admission helper, preserving the row's interval.
					seed, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionTimes(start.Add(-time.Hour-api.RelayKillSafetyMargin-api.CreatedAtResolution), time.Time{}, time.Time{}, time.Time{}, time.Time{}, ""))
					if err != nil {
						t.Fatal(err)
					}
					// Retire the original request so only the legacy one is fallen back.
					requestFacts(t, e, apitest.PermissionProof(start))
					token = seed.RequestToken
				case "admission":
					// A synthetic relay-off Cursor row uses a real admission fact, not creation age.
					id, err := apitest.SeedSpawn(e.dbPath, "cursor-off", store.StateCheckPermission, "", "off", "", false, apitest.WithAgent(agent), apitest.WithNoPane())
					if err != nil {
						t.Fatal(err)
					}
					seed, err := apitest.SeedPermissionRequest(e.dbPath, id, "Bash", apitest.PermissionTimes(start.Add(-24*time.Hour), start, time.Time{}, time.Time{}, time.Time{}, ""))
					if err != nil {
						t.Fatal(err)
					}
					e.id, token = id, seed.RequestToken
				}
				request := func() store.PermissionRow {
					t.Helper()
					pr, err := e.s.GetPermissionRequest(e.id, token)
					if err != nil {
						t.Fatal(err)
					}
					return pr
				}
				c := needsIntelligenceClient(t, e, 0, 0)
				// b.k6i: a raw stamp unlike any since; the reads keep it.
				rawMs := displayAt.Add(-2*time.Hour - 456*time.Millisecond).UnixMilli()
				if err := apitest.SetSettle(e.dbPath, e.id, store.StateCheckPermission, rawMs); err != nil {
					t.Fatal(err)
				}
				e.now = start.Add(-time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				e.now = start
				observeStatus(t, c, e.id, store.StateCheckPermission, func(store.Spawn, store.Spawn) bool { return !request().FallbackAt.IsZero() })
				if pr := request(); !pr.FallbackAt.Equal(start) || pr.FallbackBasis != tc.stored {
					t.Fatalf("clock=%v %q; want %v %q", pr.FallbackAt, pr.FallbackBasis, start, tc.stored)
				}
				for _, wait := range []time.Duration{time.Duration(config.DefaultNeedsIntelligenceTimeoutSeconds) * time.Second, 24 * time.Hour} {
					e.now = start.Add(wait)
					assertDisplayState(t, c, e.id, store.StateCheckPermission)
					_, row, listed := clientReported(t, c, e.id)
					if row.StateDetail != (api.StateDetail{}) || listed.DisplayedState != "" {
						t.Errorf("+%s: get=%+v list=%+v; want no Displayed State", wait, row.StateDetail, listed)
					}
					assertStateChangedAt(t, row, listed, rawMs)
				}
				if pr := request(); !pr.FallbackAt.Equal(start) || pr.FallbackBasis != tc.stored {
					t.Errorf("clock drifted: %v %q", pr.FallbackAt, pr.FallbackBasis)
				}
			})
		}
	}
}

func TestFallbackProcessClockStartsAtFirstCommittedObservation(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.now = displayAt
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-24*time.Hour), time.Time{}, displayAt.Add(-time.Hour), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 0, 0)
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
	if pr := e.request(t); !pr.HookGoneAt.IsZero() || !pr.FallbackAt.IsZero() {
		t.Fatalf("failed observation invented clock: %+v", pr)
	}
	release()
	e.now = e.now.Add(time.Hour)
	// The first committed observation starts the process clock at this read.
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	first := e.now
	if pr := e.request(t); !pr.FallbackAt.Equal(first) || pr.FallbackBasis != store.FallbackProcess {
		t.Fatalf("first committed observation=%+v", pr)
	}
	e.now = first.Add(24 * time.Hour)
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
	if pr := e.request(t); !pr.FallbackAt.Equal(first) {
		t.Errorf("clock moved: %+v", pr)
	}
}

// Package-level Status judges the relay hook with the caller's view
// (t2.sp6.2z.ej F2): a live hook stores no clock even past when a deadline
// clock would apply, and its death starts a process clock at the first read
// that sees it gone. No read past it records a display transition (b.zpv).
func TestPackageStatusJudgesHookWithCallersView(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, relayEnvHook)
	confirmBy := displayAt.Add(-time.Hour)
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-24*time.Hour), time.Time{}, confirmBy, time.Time{}, time.Time{}, ""))
	timeout := time.Duration(config.DefaultNeedsIntelligenceTimeoutSeconds) * time.Second
	var got api.StatusResult
	read := func() (string, api.StateDetail, error) {
		var err error
		got, err = api.Status(e.s, e.view(), e.id)
		return got.State, got.StateDetail, err
	}
	before, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	mark := trailMark(t)
	e.now = confirmBy.Add(timeout)
	observeDisplay(t, e.s, e.id, store.StateCheckPermission, nil, read)
	after, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	if pr := e.request(t); !pr.FallbackAt.IsZero() || pr.FallbackBasis != "" || !pr.HookGoneAt.IsZero() ||
		after.DisplayObservation != before.DisplayObservation || after.ObservationVersion != before.ObservationVersion {
		t.Fatalf("live hook stored facts: request=%+v display_observation %q -> %q, observation_version %d -> %d",
			pr, before.DisplayObservation, after.DisplayObservation, before.ObservationVersion, after.ObservationVersion)
	}
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	e.now = displayAt
	gone := e.now
	observeDisplay(t, e.s, e.id, store.StateCheckPermission, apitest.VersionAdvanced, read)
	if pr := e.request(t); !pr.FallbackAt.Equal(gone) || pr.FallbackBasis != store.FallbackProcess || !pr.HookGoneAt.Equal(gone) {
		t.Fatalf("first gone read=%+v; want a process clock and hook_gone_at at %s", pr, gone)
	}
	for _, wait := range []time.Duration{timeout, 24 * time.Hour} {
		e.now = gone.Add(wait)
		observeDisplay(t, e.s, e.id, store.StateCheckPermission, nil, read)
	}
	if events := displayEvents(t, mark, e.id); len(events) != 0 {
		t.Fatalf("events=%v; want none", events)
	}
}

func TestFallbackDeadlineSurvivesLaterProcessObservation(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.now = displayAt
	e.ns = ""
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 0, 0)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	e.ns = relayNS
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	e.now = displayAt.Add(30 * time.Second)
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
	if pr := e.request(t); !pr.FallbackAt.Equal(displayAt) || pr.FallbackBasis != store.FallbackDeadline {
		t.Errorf("deadline drifted: %+v", pr)
	}
}

// TestFallenBackReadsWriteNothing: once a read has stored a fallen-back
// request's clock, later reads write nothing, however long the request waits,
// and status and get read check_permission with no Displayed State (b.zpv).
func TestFallenBackReadsWriteNothing(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 0, 0)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	version := observationVersion(t, e)
	for _, offset := range []time.Duration{0, time.Millisecond, time.Minute, 24 * time.Hour} {
		e.now = displayAt.Add(offset)
		observeStatus(t, c, e.id, store.StateCheckPermission, nil)
		if row, err := c.Get(e.id); err != nil || row.State != store.StateCheckPermission || row.StateDetail != (api.StateDetail{}) {
			t.Fatalf("Get=%+v %v", row, err)
		}
		if got := observationVersion(t, e); got != version {
			t.Fatalf("read at +%s wrote: observation_version %d -> %d", offset, version, got)
		}
	}
}

// TestFallbackClockWriteFailureIsLocalToListRow: list --state
// check_permission lists both fallen-back rows, whatever the row order, while
// one row's observation write fails: that row's observation commits nothing,
// and the other row's own commit lands. The failing row is listed, so the
// list's hook_gone_at write, which leaves display_observation alone, stores
// its request's clock as the observation would have.
func TestFallbackClockWriteFailureIsLocalToListRow(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt.Add(time.Minute)
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, ""))
	if _, err := apitest.SeedSpawn(e.dbPath, "other", store.StateCheckPermission, "", "on", "", false); err != nil {
		t.Fatal(err)
	}
	other, err := apitest.SeedPermissionRequest(e.dbPath, "other", "Read", apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, ""))
	if err != nil {
		t.Fatal(err)
	}
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailDisplayObservation, e.id)
	c := needsIntelligenceClient(t, e, 0, 0)
	failingBefore, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := e.s.GetSpawn("other")
	if err != nil {
		t.Fatal(err)
	}
	var listed api.ListResult
	// A list whose write found the store busy writes no hook_gone_at, so
	// repeat until both the other row's commit and the failing row's clock land.
	_, _, committed := apitest.RepeatUntilCommitted(func() bool {
		var err error
		if listed, err = c.List(api.ListParams{State: []string{store.StateCheckPermission}}); err != nil {
			t.Fatal(err)
		}
		after, err := e.s.GetSpawn("other")
		if err != nil {
			t.Fatal(err)
		}
		return apitest.VersionAdvanced(otherBefore, after) && !e.request(t).FallbackAt.IsZero()
	})
	if !committed {
		t.Fatalf("the other row's observation or the failing row's clock never committed: %+v", e.request(t))
	}
	if pr, err := e.s.GetPermissionRequest("other", other.RequestToken); err != nil || !pr.FallbackAt.Equal(displayAt) || pr.FallbackBasis != store.FallbackDeadline {
		t.Errorf("other row's clock=%+v %v; want %s %q", pr, err, displayAt, store.FallbackDeadline)
	}
	got := []string{}
	for _, row := range listed.Spawns {
		got = append(got, row.InstanceID)
		if row.State != store.StateCheckPermission || row.DisplayedState != "" {
			t.Errorf("row=%+v; want check_permission, no Displayed State", row)
		}
	}
	sort.Strings(got)
	if !equalStrings(got, []string{e.id, "other"}) {
		t.Fatalf("list --state check_permission=%v; want both rows", got)
	}
	if after, err := e.s.GetSpawn(e.id); err != nil || after.ObservationVersion != failingBefore.ObservationVersion || after.DisplayObservation != failingBefore.DisplayObservation {
		t.Errorf("the failing row's observation committed: before=%+v after=%+v %v", failingBefore, after, err)
	}
	if pr := e.request(t); !pr.FallbackAt.Equal(displayAt) || pr.FallbackBasis != store.FallbackDeadline || !pr.HookGoneAt.Equal(e.now) {
		t.Errorf("the failing row's clock=%+v; want %s %q with hook_gone_at %s from the list", pr, displayAt, store.FallbackDeadline, e.now)
	}
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
}

func TestFallbackNoRequestOrTrustworthyClock(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		t.Run(agent, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			for _, state := range []string{store.StateWorking, store.StateCheckPermission} {
				id, err := apitest.SeedSpawn(path, state, state, "", "off", "", true, apitest.WithAgent(agent), apitest.WithNoPane())
				if err != nil {
					t.Fatal(err)
				}
				s, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				row, err := api.Get(s, api.RelayView{Now: func() time.Time { return displayAt.Add(24 * time.Hour) }}, id)
				_ = s.Close()
				if err != nil || row.State != state || row.StateDetail != (api.StateDetail{}) {
					t.Fatalf("silence/relay off changed the row: %+v %v", row, err)
				}
			}
		})
	}
	// A pre-v7 request whose created_at reads as zero gives no clock; the
	// trustworthy row is the positive control at the same instant. Text that
	// does not scan fails the read instead (the store tests cover the rest).
	for _, tc := range []struct {
		created time.Time
		clock   bool
	}{
		{displayAt.Add(-time.Hour), true},
		{time.Time{}, false},
	} {
		raw := tc.created.Format("2006-01-02 15:04:05")
		t.Run("created_at="+raw, func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = displayAt.Add(24 * time.Hour)
			requestFacts(t, e, apitest.PermissionTimes(time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}, ""), apitest.PermissionCreation(raw))
			c := needsIntelligenceClient(t, e, 0, 0)
			if tc.clock {
				observeStatus(t, c, e.id, store.StateCheckPermission, func(store.Spawn, store.Spawn) bool { return !e.request(t).FallbackAt.IsZero() })
			} else if assertDisplayState(t, c, e.id, store.StateCheckPermission); !e.request(t).FallbackAt.IsZero() {
				t.Errorf("an untrustworthy creation stored a clock: %+v", e.request(t))
			}
		})
	}
}
