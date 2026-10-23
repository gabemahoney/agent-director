package api_test

import (
	"path/filepath"
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
// refuses sleeps; timeout is its [state] needs_intelligence_timeout_seconds.
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

func TestNeedsIntelligenceFallbackBoundaries(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, basis := range []string{"process", "deadline", "legacy", "admission"} {
			if agent == store.AgentCursor && (basis == "process" || basis == "legacy") || agent != store.AgentCursor && basis == "admission" {
				continue
			}
			t.Run(agent+"/"+basis, func(t *testing.T) {
				e := newRelayEnvAgent(t, relayEnvHook, agent)
				e.now = displayAt
				start := displayAt
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
					_, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionTimes(start.Add(-time.Hour-api.RelayKillSafetyMargin-api.CreatedAtResolution), time.Time{}, time.Time{}, time.Time{}, time.Time{}, ""))
					if err != nil {
						t.Fatal(err)
					}
					// Retire the original request so only the legacy one qualifies.
					requestFacts(t, e, apitest.PermissionProof(start))
				case "admission":
					// A synthetic relay-off Cursor row uses a real admission fact, not creation age.
					id, err := apitest.SeedSpawn(e.dbPath, "cursor-off", store.StateCheckPermission, "", "off", "", false, apitest.WithAgent(agent), apitest.WithNoPane())
					if err != nil {
						t.Fatal(err)
					}
					_, err = apitest.SeedPermissionRequest(e.dbPath, id, "Bash", apitest.PermissionTimes(start.Add(-24*time.Hour), start, time.Time{}, time.Time{}, time.Time{}, ""))
					if err != nil {
						t.Fatal(err)
					}
					e.id = id
				}
				c := needsIntelligenceClient(t, e, 0, 0)
				// b.k6i: a raw stamp unlike any since; the report-only entry keeps it.
				rawMs := displayAt.Add(-2*time.Hour - 456*time.Millisecond).UnixMilli()
				if err := apitest.SetSettle(e.dbPath, e.id, store.StateCheckPermission, rawMs); err != nil {
					t.Fatal(err)
				}
				e.now = start.Add(-time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				e.now = start
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				e.now = start.Add(time.Duration(config.DefaultNeedsIntelligenceTimeoutSeconds)*time.Second - time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				e.now = e.now.Add(time.Millisecond)
				got := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
				if got.Since != e.now.Format(displayTS) {
					t.Errorf("since=%s, want actual read %s", got.Since, e.now)
				}
				_, row, listed := clientReported(t, c, e.id)
				if row.State != api.StateNeedsIntelligence || row.StateDetail != got.StateDetail {
					t.Errorf("Get=%+v", row)
				}
				assertStateChangedAt(t, row, listed, rawMs)
			})
		}
	}
}

func TestNeedsIntelligenceProcessStartsAtFirstCommittedObservation(t *testing.T) {
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
	e.now = first.Add(30*time.Second - time.Millisecond)
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
	e.now = e.now.Add(time.Millisecond)
	assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
}

// Package-level Status judges the relay hook with the caller's view
// (t2.sp6.2z.ej F2): a live hook stores no clock even past when a deadline
// clock would qualify, and its death starts a process clock at the first read
// that sees it gone.
func TestNeedsIntelligencePackageStatusJudgesHookWithCallersView(t *testing.T) {
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
	e.now = gone.Add(timeout - time.Millisecond)
	observeDisplay(t, e.s, e.id, store.StateCheckPermission, nil, read)
	e.now = gone.Add(timeout)
	observeDisplay(t, e.s, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced, read)
	if got.Since != e.now.Format(displayTS) {
		t.Errorf("needs_intelligence read=%+v; want since %s", got.StateDetail, e.now.Format(displayTS))
	}
	events := displayEvents(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want the entry once", events)
	}
	assertDisplayEvent(t, events[0], store.StateCheckPermission, api.StateNeedsIntelligence, e.now)
}

func TestNeedsIntelligenceDeadlineSurvivesLaterProcessObservation(t *testing.T) {
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
	assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
	if pr := e.request(t); !pr.FallbackAt.Equal(displayAt) || pr.FallbackBasis != store.FallbackDeadline {
		t.Errorf("deadline drifted: %+v", pr)
	}
}

func TestNeedsIntelligenceExcludesNoncurrentAndAnsweredRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		option apitest.PermissionOption
		live   bool
		want   string
	}{
		// Positive controls: the same row qualifies unless a fact below excludes it.
		{"unchanged request", nil, false, api.StateNeedsIntelligence},
		// A decision that was never delivered does not close the dialog.
		{"decided but undelivered", apitest.PermissionDecision("allow", ""), false, api.StateNeedsIntelligence},
		{"live hook", nil, true, store.StateCheckPermission},
		{"stale launch", apitest.PermissionOwner("0000000000000000", 1), false, store.StateCheckPermission},
		{"stale interval", apitest.PermissionOwner("", 0), false, store.StateCheckPermission},
		{"pane answer", apitest.PermissionAnswer(store.PaneAnswerOutside), false, store.StateCheckPermission},
		{"acknowledged", apitest.PermissionDelivered(displayAt), false, store.StateCheckPermission},
		{"proven gone", apitest.PermissionProof(displayAt), false, store.StateCheckPermission},
		{"closed historical", apitest.PermissionClosed(displayAt), false, store.StateCheckPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelayEnv(t, relayEnvHook)
			e.now = displayAt.Add(time.Hour)
			requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-24*time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, ""))
			if tc.option != nil {
				requestFacts(t, e, tc.option)
			}
			if !tc.live {
				e.ns = ""
			}
			c := needsIntelligenceClient(t, e, 0, 0)
			assertDisplayState(t, c, e.id, tc.want)
		})
	}
}

func TestNeedsIntelligenceWriteFailureIsLocalToListRow(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt.Add(time.Minute)
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, ""))
	if _, err := apitest.SeedSpawn(e.dbPath, "other", store.StateCheckPermission, "", "on", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, "other", "Read", apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailDisplayObservation, e.id)
	c := needsIntelligenceClient(t, e, 0, 0)
	// No limit: the failing row is filtered out whatever the row order, and
	// the other row's own write is unaffected.
	listed, err := c.List(api.ListParams{State: []string{api.StateNeedsIntelligence}})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != "other" {
		t.Fatalf("row-local failure/list filtering: %+v %v", listed, err)
	}
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
}

func TestNeedsIntelligenceNoRequestOrTrustworthyClock(t *testing.T) {
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
				if err != nil || row.State != state {
					t.Fatalf("silence/relay off supplied eligibility: %+v %v", row, err)
				}
			}
		})
	}
	// A pre-v7 request whose created_at reads as zero gives no clock; the
	// trustworthy row is the positive control at the same instant. Text that
	// does not scan fails the read instead (the store tests cover the rest).
	for _, tc := range []struct {
		created time.Time
		want    string
	}{
		{displayAt.Add(-time.Hour), api.StateNeedsIntelligence},
		{time.Time{}, store.StateCheckPermission},
	} {
		raw := tc.created.Format("2006-01-02 15:04:05")
		t.Run("created_at="+raw, func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = displayAt.Add(24 * time.Hour)
			requestFacts(t, e, apitest.PermissionTimes(time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}, ""), apitest.PermissionCreation(raw))
			c := needsIntelligenceClient(t, e, 0, 0)
			assertDisplayState(t, c, e.id, tc.want)
		})
	}
}

func TestNeedsIntelligenceLoadedTimeoutControlsEligibility(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 0, 7)
	e.now = displayAt.Add(7*time.Second - time.Nanosecond)
	assertDisplayState(t, c, e.id, store.StateCheckPermission)
	e.now = e.now.Add(time.Nanosecond)
	assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
}
