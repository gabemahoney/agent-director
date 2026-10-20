package api_test

import (
	"path/filepath"
	"sync"
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

var stuckAt = time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)

// stuckClient keeps every read on the same injected clock and refuses sleeps.
func stuckClient(t *testing.T, e *relayEnv, settle, timeout int64) *api.Client {
	t.Helper()
	state, err := e.s.GetSpawnState(e.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetSettle(e.dbPath, e.id, state, 0); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, cfg, apitest.ConfigKeys{StateSettleMs: settle, RelayTimeoutSeconds: 3600, PermissionUnansweredTimeoutSeconds: timeout})
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfg, TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetProcCheckerForTest(c, e.pc)
	api.SetClockForTest(c, func() time.Time { return e.now })
	api.SetSelfPIDNSForTest(c, func() (string, bool) { return e.ns, e.ns != "" })
	api.SetSleepForTest(c, func(d time.Duration) { t.Errorf("read slept %s", d) })
	watchStuckClient(c, e.s)
	return c
}

func stuckFacts(t *testing.T, e *relayEnv, opts ...apitest.PermissionOption) {
	t.Helper()
	if err := apitest.SetPermissionFacts(e.dbPath, e.request(t).RequestID, opts...); err != nil {
		t.Fatal(err)
	}
}

// stuckProbes maps a client to the store its committed rows are read from, so
// the observation helpers can tell whether a read committed.
var stuckProbes sync.Map

func watchStuckClient(c *api.Client, probe apitest.StuckProbe) { stuckProbes.Store(c, probe) }

// observeStuck runs one reader through apitest.ObserveStuckRead. commit is
// nil for a read that is not expected to commit.
func observeStuck(t *testing.T, probe apitest.StuckProbe, id, want string, commit apitest.CommitCheck, read func() (string, api.StuckInfo, error)) {
	t.Helper()
	apitest.ObserveStuckRead(t, probe, id, want, commit, func() (apitest.StuckReading, error) {
		state, info, err := read()
		return apitest.StuckReading{State: state, Reason: info.StuckReason, Since: info.Since, RequestToken: info.RequestToken, ToolName: info.ToolName}, err
	})
}

// observeStatus is c.Status judged against the committed row: see
// apitest.ObserveStuckRead. commit names the commit the read must make (nil
// when it makes none).
func observeStatus(t *testing.T, c *api.Client, id, want string, commit apitest.CommitCheck) api.StatusResult {
	t.Helper()
	probe, ok := stuckProbes.Load(c)
	if !ok {
		t.Fatal("client is not watched: build it with stuckClient or watchStuckClient")
	}
	var got api.StatusResult
	observeStuck(t, probe.(apitest.StuckProbe), id, want, commit, func() (string, api.StuckInfo, error) {
		var err error
		got, err = c.Status(id)
		return got.State, got.StuckInfo, err
	})
	if want == "stuck" {
		if got.StuckReason != "permission_unanswered" || got.RequestToken == "" || got.Since == "" {
			t.Fatalf("incoherent stuck: %+v", got)
		}
	} else if got.StuckInfo != (api.StuckInfo{}) || got.TmuxSessionName != "" {
		t.Fatalf("ordinary row leaked details: %+v", got)
	}
	return got
}

// assertStuckState reads Status and requires want. Entering stuck must commit
// (stuck_version advances); an already committed stuck display must be read
// back unchanged without any commit.
func assertStuckState(t *testing.T, c *api.Client, id, want string) api.StatusResult {
	t.Helper()
	var commit apitest.CommitCheck
	if want == "stuck" {
		probe, ok := stuckProbes.Load(c)
		if !ok {
			t.Fatal("client is not watched: build it with stuckClient or watchStuckClient")
		}
		before, err := probe.(apitest.StuckProbe).GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		if before.CommittedStuckDisplay().State != "stuck" {
			commit = apitest.VersionAdvanced
		}
	}
	return observeStatus(t, c, id, want, commit)
}

func TestPermissionStuckFallbackBoundaries(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, basis := range []string{"process", "deadline", "legacy", "admission"} {
			if agent == store.AgentCursor && (basis == "process" || basis == "legacy") || agent != store.AgentCursor && basis == "admission" {
				continue
			}
			t.Run(agent+"/"+basis, func(t *testing.T) {
				e := newRelayEnvAgent(t, relayEnvHook, agent)
				e.now = stuckAt
				start := stuckAt
				switch basis {
				case "process":
					e.pc.Set(relayEnvHook.PID, procfix.Gone())
					stuckFacts(t, e, apitest.PermissionTimes(start.Add(-24*time.Hour), time.Time{}, start.Add(time.Hour), start, time.Time{}, ""))
				case "deadline":
					e.ns = ""
					stuckFacts(t, e, apitest.PermissionTimes(start.Add(-24*time.Hour), time.Time{}, start, time.Time{}, time.Time{}, ""))
				case "legacy":
					start = start.Truncate(time.Second)
					// Use the existing legacy admission helper, preserving the row's interval.
					_, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionTimes(start.Add(-time.Hour-api.RelayKillSafetyMargin-api.CreatedAtResolution), time.Time{}, time.Time{}, time.Time{}, time.Time{}, ""))
					if err != nil {
						t.Fatal(err)
					}
					// Retire the original request so only the legacy one qualifies.
					stuckFacts(t, e, apitest.PermissionProof(start))
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
				c := stuckClient(t, e, 0, 0)
				e.now = start.Add(-time.Millisecond)
				assertStuckState(t, c, e.id, store.StateCheckPermission)
				e.now = start
				assertStuckState(t, c, e.id, store.StateCheckPermission)
				e.now = start.Add(time.Duration(config.DefaultPermissionUnansweredTimeoutSeconds)*time.Second - time.Millisecond)
				assertStuckState(t, c, e.id, store.StateCheckPermission)
				e.now = e.now.Add(time.Millisecond)
				got := assertStuckState(t, c, e.id, "stuck")
				if got.Since != e.now.Format("2006-01-02T15:04:05.000Z") {
					t.Errorf("since=%s, want actual read %s", got.Since, e.now)
				}
				row, err := c.Get(e.id)
				if err != nil || row.State != "stuck" || row.StuckInfo != got.StuckInfo {
					t.Errorf("Get=%+v, %v", row, err)
				}
			})
		}
	}
}

func TestPermissionStuckProcessStartsAtFirstCommittedObservation(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.now = stuckAt
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-24*time.Hour), time.Time{}, stuckAt.Add(-time.Hour), time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 0, 0)
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	assertStuckState(t, c, e.id, store.StateCheckPermission)
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
	assertStuckState(t, c, e.id, store.StateCheckPermission)
	e.now = e.now.Add(time.Millisecond)
	assertStuckState(t, c, e.id, "stuck")
}

func TestPermissionStuckDeadlineSurvivesLaterProcessObservation(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.now = stuckAt
	e.ns = ""
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 0, 0)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	e.ns = relayNS
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	e.now = stuckAt.Add(30 * time.Second)
	assertStuckState(t, c, e.id, "stuck")
	if pr := e.request(t); !pr.FallbackAt.Equal(stuckAt) || pr.FallbackBasis != store.FallbackDeadline {
		t.Errorf("deadline drifted: %+v", pr)
	}
}

func TestPermissionStuckExcludesNoncurrentAndAnsweredRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		option apitest.PermissionOption
		live   bool
		want   string
	}{
		// Positive controls: the same row is stuck unless a fact below excludes it.
		{"unchanged request", nil, false, "stuck"},
		// A decision that was never delivered does not close the dialog.
		{"decided but undelivered", apitest.PermissionDecision("allow", ""), false, "stuck"},
		{"live hook", nil, true, store.StateCheckPermission},
		{"stale launch", apitest.PermissionOwner("0000000000000000", 1), false, store.StateCheckPermission},
		{"stale interval", apitest.PermissionOwner("", 0), false, store.StateCheckPermission},
		{"pane answer", apitest.PermissionAnswer(store.PaneAnswerOutside), false, store.StateCheckPermission},
		{"acknowledged", apitest.PermissionDelivered(stuckAt), false, store.StateCheckPermission},
		{"proven gone", apitest.PermissionProof(stuckAt), false, store.StateCheckPermission},
		{"closed historical", apitest.PermissionClosed(stuckAt), false, store.StateCheckPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelayEnv(t, relayEnvHook)
			e.now = stuckAt.Add(time.Hour)
			stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-24*time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, ""))
			if tc.option != nil {
				stuckFacts(t, e, tc.option)
			}
			if !tc.live {
				e.ns = ""
			}
			c := stuckClient(t, e, 0, 0)
			assertStuckState(t, c, e.id, tc.want)
		})
	}
}

func TestPermissionStuckWriteFailureIsLocalToListRow(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt.Add(time.Minute)
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, ""))
	if _, err := apitest.SeedSpawn(e.dbPath, "other", store.StateCheckPermission, "", "on", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, "other", "Read", apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailStuckObservation, e.id)
	c := stuckClient(t, e, 0, 0)
	// No limit: the failing row is filtered out whatever the row order, and
	// the other row's own write is unaffected.
	listed, err := c.List(api.ListParams{State: []string{"stuck"}})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != "other" {
		t.Fatalf("row-local failure/list filtering: %+v %v", listed, err)
	}
	assertStuckState(t, c, e.id, store.StateCheckPermission)
}

// A row displayed stuck matches --state stuck and no longer --state
// check_permission, which keeps only rows really displayed check_permission
// (docs/caller-contract.md "Filtering").
func TestPermissionStuckListFilterMatchesDisplayedState(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt.Add(time.Minute)
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, ""))
	plain, err := apitest.SeedSpawn(e.dbPath, "plain", store.StateCheckPermission, "", "on", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c := stuckClient(t, e, 0, 0)
	for _, tc := range []struct {
		states []string
		want   []string
	}{
		{[]string{"stuck"}, []string{e.id}},
		{[]string{store.StateCheckPermission}, []string{plain}},
		{[]string{"stuck", store.StateCheckPermission}, []string{e.id, plain}},
	} {
		listed, err := c.List(api.ListParams{State: tc.states})
		if err != nil {
			t.Fatalf("%v: %v", tc.states, err)
		}
		got := map[string]bool{}
		for _, row := range listed.Spawns {
			got[row.InstanceID] = true
		}
		if len(got) != len(tc.want) || len(listed.Spawns) != len(tc.want) {
			t.Fatalf("filter %v matched %+v; want %v", tc.states, listed.Spawns, tc.want)
		}
		for _, id := range tc.want {
			if !got[id] {
				t.Errorf("filter %v missing %s: %+v", tc.states, id, listed.Spawns)
			}
		}
	}
}

func TestPermissionStuckNoRequestOrTrustworthyClock(t *testing.T) {
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
				row, err := api.Get(s, api.RelayView{Now: func() time.Time { return stuckAt.Add(24 * time.Hour) }}, id)
				_ = s.Close()
				if err != nil || row.State != state {
					t.Fatalf("silence/relay off supplied eligibility: %+v %v", row, err)
				}
			}
		})
	}
	for _, raw := range []string{"", "not-a-time", "10000-01-01 00:00:00"} {
		t.Run("untrustworthy="+raw, func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = stuckAt.Add(24 * time.Hour)
			stuckFacts(t, e, apitest.PermissionTimes(time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}, ""), apitest.PermissionCreation(raw))
			c := stuckClient(t, e, 0, 0)
			assertStuckState(t, c, e.id, store.StateCheckPermission)
		})
	}
}

func TestPermissionStuckLoadedTimeoutControlsEligibility(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 0, 7)
	e.now = stuckAt.Add(7*time.Second - time.Nanosecond)
	assertStuckState(t, c, e.id, store.StateCheckPermission)
	e.now = e.now.Add(time.Nanosecond)
	assertStuckState(t, c, e.id, "stuck")
}
