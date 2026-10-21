package api_test

// stuck_pane_test.go — no_screen_change (b.ob8): a raw working Claude or Codex
// row's own pane, sampled by status, get and list through the Recorder on an
// injected clock. paneEnv is the shared fixture; the permission reason's
// tests are stuck_test.go's.

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// paneEnv is a raw working row of agent whose own pane the Recorder captures
// (show), read by clients on the clock now.
type paneEnv struct {
	s      *store.Store
	dbPath string
	id     string
	rec    *tmuxfix.Recorder
	pc     *procfix.Checker
	now    time.Time
}

// paneSpec adjusts newPaneEnv's row: its raw state ("" is working), its
// session's label (tmuxfix.RowLabelCurrent by default) and further columns.
type paneSpec struct {
	agent, state string
	label        tmuxfix.RowLabel
	opts         []apitest.SpawnOption
}

// newPaneEnv seeds spec's row with its own session and pane, whose capture
// reads "pane 1".
func newPaneEnv(t *testing.T, spec paneSpec) *paneEnv {
	t.Helper()
	if spec.state == "" {
		spec.state = store.StateWorking
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	id, err := apitest.SeedSpawn(dbPath, "", spec.state, "", "off", "", true, append([]apitest.SpawnOption{apitest.WithAgent(spec.agent)}, spec.opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	rec, pc := tmuxfix.NewRecorderForReadPane(t, dbPath, id, "pane 1", tmuxfix.WithReadPaneLabel(spec.label))
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &paneEnv{s: s, dbPath: dbPath, id: id, rec: rec, pc: pc, now: stuckAt}
}

// client is a Client over e with keys' config file, e's Recorder, process
// fake and clock; a read that sleeps fails the test.
func (e *paneEnv) client(t *testing.T, keys apitest.ConfigKeys) *api.Client {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, cfg, keys)
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfg, TmuxClient: e.rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetProcCheckerForTest(c, e.pc)
	api.SetClockForTest(c, func() time.Time { return e.now })
	api.SetSleepForTest(c, func(d time.Duration) { t.Errorf("read slept %s", d) })
	watchStuckClient(c, e.s)
	return c
}

// addRow seeds another working row of agent beside e's, on its own pane
// paneID (pid panePID) of socket, with its session on e's Recorder capturing
// "pane 1" and its processes alive; it returns the row's id.
func (e *paneEnv) addRow(t *testing.T, agent, socket, paneID string, panePID int) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, "", "off", "", false, apitest.WithAgent(agent),
		apitest.WithLaunchIdentity(store.LaunchIdentity{Token: fmt.Sprintf("%016x", panePID), Socket: socket, PaneID: paneID, PanePID: panePID}))
	if err != nil {
		t.Fatal(err)
	}
	e.rec.SeedRowSession(t, e.dbPath, id)
	e.rec.SetCapture(socket, paneID, "pane 1")
	e.pc.Set(panePID, procfix.Alive(apitest.LinuxProcStarttime))
	for _, s := range e.rec.Servers() {
		e.pc.Set(s.PID, procfix.Alive(apitest.LinuxProcStarttime))
	}
	return id
}

// show makes the row's pane capture text from the next read on.
func (e *paneEnv) show(text string) { e.rec.SetCapture(apitest.TestSocket, apitest.TestPaneID, text) }

// row reads e's row as committed.
func (e *paneEnv) row(t *testing.T) store.Spawn {
	t.Helper()
	row, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// minutes is the unchanged_minutes of a displayed no_screen_change status.
func minutes(t *testing.T, got api.StatusResult) int64 {
	t.Helper()
	if got.StuckReason != api.StuckNoScreenChange || got.UnchangedMinutes == nil {
		t.Fatalf("not displayed no_screen_change: %+v", got)
	}
	return *got.UnchangedMinutes
}

// paneReaders are the read surfaces that may observe a pane first.
var paneReaders = []struct {
	name string
	read func(*api.Client, string) error
}{
	{"status", func(c *api.Client, id string) error { _, err := c.Status(id); return err }},
	{"get", func(c *api.Client, id string) error { _, err := c.Get(id); return err }},
	{"list", func(c *api.Client, _ string) error { _, err := c.List(api.ListParams{}); return err }},
}

// TestPaneStuckObservationBoundaries: whichever surface reads first commits
// the baseline; identical samples keep its start, a read exactly at the
// timeout qualifies (since that read, completed minutes floored), a displayed
// row keeps being observed, and a changed fingerprint restarts the period.
func TestPaneStuckObservationBoundaries(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		for _, first := range paneReaders {
			t.Run(agent+"/"+first.name, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: agent})
				c := e.client(t, apitest.ConfigKeys{})
				timeout := time.Duration(config.DefaultNoScreenChangeTimeoutSeconds) * time.Second
				before := e.row(t)
				if err := first.read(c, e.id); err != nil {
					t.Fatal(err)
				}
				start := e.now
				if after := e.row(t); after.StuckVersion != before.StuckVersion+1 || after.RowVersion != before.RowVersion ||
					after.PaneSinceMillis != start.UnixMilli() || after.PaneEpoch != after.PermissionEpoch {
					t.Fatalf("first read committed no baseline: before=%+v after=%+v", before, after)
				}
				// The baseline is read-pane's default fingerprint of the same pane.
				if rp, err := c.ReadPane(api.ReadPaneParams{InstanceID: e.id}); err != nil || rp.PaneSHA256 != e.row(t).PaneSHA256 {
					t.Fatalf("baseline %q is not read-pane's pane_sha256: %+v %v", e.row(t).PaneSHA256, rp, err)
				}
				e.now = start.Add(timeout - time.Millisecond)
				assertStuckState(t, c, e.id, store.StateWorking)
				e.now = start.Add(timeout)
				got := assertStuckState(t, c, e.id, api.StuckNoScreenChange)
				if got.Since != e.now.Format("2006-01-02T15:04:05.000Z") || minutes(t, got) != 20 {
					t.Fatalf("entry=%+v; want since this read and 20 minutes", got)
				}
				// Displayed stuck is still observed: the minutes advance, floored.
				e.now = start.Add(timeout + time.Minute - time.Millisecond)
				if got := observeStatus(t, c, e.id, api.StuckNoScreenChange, nil); minutes(t, got) != 20 {
					t.Fatalf("minutes=%+v; want 20", got)
				}
				e.now = start.Add(timeout + time.Minute)
				if got := observeStatus(t, c, e.id, api.StuckNoScreenChange, apitest.VersionAdvanced); minutes(t, got) != 21 || got.Since != start.Add(timeout).Format("2006-01-02T15:04:05.000Z") {
					t.Fatalf("continued=%+v; want 21 minutes since the entry", got)
				}
				// A changed fingerprint clears at once (no settle) and restarts the period.
				e.show("pane 2")
				restart := e.now
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				e.now = restart.Add(timeout - time.Millisecond)
				assertStuckState(t, c, e.id, store.StateWorking)
				e.now = restart.Add(timeout)
				assertStuckState(t, c, e.id, api.StuckNoScreenChange)
			})
		}
	}
}

// TestPaneStuckTimeoutChoice: a row's own override is its timeout whatever
// the reader's [state] no_screen_change_timeout_seconds; without one (none,
// 0, or a stored value that is not a valid override) the reader's loaded
// configuration decides, its 0 the default 1200 s.
func TestPaneStuckTimeoutChoice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		override any // as stored; nil = NULL
		config   int64
		want     time.Duration
	}{
		{"override over a shorter config", int64(90), 60, 90 * time.Second},
		{"override over a longer config", int64(90), 3600, 90 * time.Second},
		{"none uses the config", nil, 60, time.Minute},
		{"none and no config", nil, 0, time.Duration(config.DefaultNoScreenChangeTimeoutSeconds) * time.Second},
		{"stored 0 is none", int64(0), 60, time.Minute},
		{"stored negative is none", int64(-5), 60, time.Minute},
		{"stored above the range is none", int64(9223372037), 60, time.Minute},
		{"stored text is none", "ninety", 60, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentCodex, opts: []apitest.SpawnOption{apitest.WithNoScreenChangeTimeout(tc.override)}})
			c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: tc.config})
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			start := e.now
			e.now = start.Add(tc.want - time.Millisecond)
			assertStuckState(t, c, e.id, store.StateWorking)
			e.now = start.Add(tc.want)
			assertStuckState(t, c, e.id, api.StuckNoScreenChange)
		})
	}
}

// TestPaneStuckNoReadsNoEvidence: nothing observes between reads, so a turn
// that ran while nobody read starts a fresh period at the next read, however
// long ago the baseline was taken.
func TestPaneStuckNoReadsNoEvidence(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60})
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	idle := e.row(t)
	e.now = e.now.Add(24 * time.Hour)
	for _, state := range []string{store.StateWaiting, store.StateWorking} {
		if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
			t.Fatal(err)
		}
	}
	if row := e.row(t); row.PaneSinceMillis != idle.PaneSinceMillis || row.StuckObservation != "" {
		t.Fatalf("observation changed without a read: %+v", row)
	}
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	if row := e.row(t); row.PaneSinceMillis != e.now.UnixMilli() || row.PaneEpoch != row.PermissionEpoch {
		t.Fatalf("the read continued the old period: %+v", row)
	}
	e.now = e.now.Add(time.Minute)
	assertStuckState(t, c, e.id, api.StuckNoScreenChange)
}

// stuckEvents are id's stuck transitions in the trail since mark.
func stuckEvents(t *testing.T, mark int, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range readAPITrailLines(t)[mark:] {
		if ev["instance_id"] == id && ev["event"] == "ad.spawn.stuck_transition" {
			out = append(out, ev)
		}
	}
	return out
}

// failOnce makes the next sample of e's pane fail: its capture, or the lookup
// of its session. It returns the CommitCheck of a read that must commit that
// failed sample: apitest.VersionAdvanced, scripting the failure again after
// each attempt that did not commit, as a repeat after a write that lost the
// lock race under load would otherwise sample the unchanged pane.
func failOnce(e *paneEnv, call tmux.Call) apitest.CommitCheck {
	fail := func() { e.rec.Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, call) }
	fail()
	return func(before, after store.Spawn) bool {
		if apitest.VersionAdvanced(before, after) {
			return true
		}
		fail()
		return false
	}
}

// TestPaneStuckObservesOnlyOwnedWorkingPanes: only a raw working Claude or
// Codex row's own current pane is captured; no other row ever qualifies.
func TestPaneStuckObservesOnlyOwnedWorkingPanes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		spec   paneSpec
		lookup bool // the session is looked up (and refused)
	}{
		{"an earlier launch's session holding the pane id", paneSpec{agent: store.AgentClaude, label: tmuxfix.RowLabelOld}, true},
		{"another row's session", paneSpec{agent: store.AgentCodex, label: tmuxfix.RowLabelForeign}, true},
		{"another store's session", paneSpec{agent: store.AgentClaude, label: tmuxfix.RowLabelOtherStore}, true},
		{"check_permission", paneSpec{agent: store.AgentClaude, state: store.StateCheckPermission}, false},
		{"ask_user", paneSpec{agent: store.AgentCodex, state: store.StateAskUser}, false},
		{"cursor working with an override", paneSpec{agent: store.AgentCursor, opts: []apitest.SpawnOption{apitest.WithNoScreenChangeTimeout(int64(1))}}, false},
		{"unknown agent working", paneSpec{agent: "gemini"}, false},
		{"no agent recorded, working", paneSpec{agent: ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, tc.spec)
			c := e.client(t, apitest.ConfigKeys{})
			raw := e.row(t).State
			for _, after := range []time.Duration{0, 21 * time.Minute, 42 * time.Minute} {
				e.now = stuckAt.Add(after)
				got, err := c.Status(e.id)
				if err != nil || got.State != raw || got.StuckInfo != (api.StuckInfo{}) {
					t.Fatalf("read at +%s = %+v, %v; want ordinary %s", after, got, err, raw)
				}
				wantAvailability := api.NoScreenChangeUnavailable
				if tc.spec.agent == store.AgentClaude || tc.spec.agent == store.AgentCodex {
					wantAvailability = api.NoScreenChangeAvailable
				}
				if got.NoScreenChangeAvailability != wantAvailability {
					t.Fatalf("availability = %q; want %q", got.NoScreenChangeAvailability, wantAvailability)
				}
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallCapture)); n != 0 {
				t.Errorf("captures = %d; want none", n)
			}
			if looked := len(e.rec.SocketCallsOf(tmux.CallLookup)) > 0; tc.lookup && !looked {
				t.Error("the row's session was never looked up")
			}
			if row := e.row(t); row.PaneSHA256 != "" || row.StuckObservation != "" {
				t.Errorf("an unobserved pane left a baseline or observation: %+v", row)
			}
		})
	}
}

// TestPaneStuckFailedCaptureResets: a failed or unowned capture (the row's
// session now labelled for another row) before, while pending or after the
// visible entry is a sample that resets the committed baseline and withdraws
// the eligibility or display, a displayed one only once its exit settles, with
// one exit at that deadline; the read still succeeds, and the next owned
// success starts a fresh period at its own read.
func TestPaneStuckFailedCaptureResets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase  string
		settle time.Duration
	}{{"before", 0}, {"pending", 5 * time.Second}, {"after", 0}, {"after", 5 * time.Second}} {
		for _, failure := range []string{string(tmux.CallCapture), string(tmux.CallLookup), "unowned"} {
			t.Run(tc.phase+"/settle "+tc.settle.String()+"/"+failure, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
				c := e.client(t, apitest.ConfigKeys{StateSettleMs: tc.settle.Milliseconds(), NoScreenChangeTimeoutSeconds: 60})
				var shown api.StatusResult
				switch tc.phase {
				case "before":
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				case "pending": // the entry candidate is committed, not yet displayed
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
					e.now = e.now.Add(time.Minute)
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				case "after":
					shown = enterPane(t, e, c, tc.settle)
				}
				// label labels the row's session for the row id, on the row's own launch token.
				label := func(id string) {
					t.Helper()
					if err := e.rec.SetLabel(apitest.TestSocket, e.rec.Sessions(apitest.TestSocket)[0].ID, apitest.TestPaneID, e.row(t).Identity.Token, id, e.s.StoreID()); err != nil {
						t.Fatal(err)
					}
				}
				failedCommit := apitest.CommitCheck(apitest.VersionAdvanced)
				if failure == "unowned" {
					label(e.id + "-foreign")
				} else {
					failedCommit = failOnce(e, tmux.Call(failure))
				}
				exiting := tc.phase == "after" && tc.settle > 0
				// read is a committing read: working, or the shown detail while the exit settles.
				read := func(commit apitest.CommitCheck) {
					t.Helper()
					if !exiting {
						observeStatus(t, c, e.id, store.StateWorking, commit)
					} else if got := observeStatus(t, c, e.id, api.StuckNoScreenChange, commit); got.Since != shown.Since || minutes(t, got) != minutes(t, shown) {
						t.Fatalf("the detail changed before the exit's deadline: %+v; want %+v", got, shown)
					}
				}
				e.now = e.now.Add(time.Second)
				failed := e.now
				mark := trailMark(t)
				read(failedCommit)
				if row := e.row(t); row.PaneSHA256 != "" || row.PaneSinceMillis != 0 || (row.StuckObservation != "") != exiting {
					t.Fatalf("a failed sample left the baseline or eligibility: %+v", row)
				}
				if failure == "unowned" {
					label(e.id)
				}
				e.now = failed.Add(10 * time.Second)
				if exiting {
					e.now = failed.Add(tc.settle - time.Millisecond)
				}
				fresh := e.now
				read(apitest.VersionAdvanced)
				if row := e.row(t); row.PaneSinceMillis != fresh.UnixMilli() {
					t.Fatalf("the next success did not start fresh: %+v", row)
				}
				exitAt := failed
				if exiting {
					e.now = failed.Add(tc.settle)
					exitAt = e.now
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				}
				if events, want := stuckEvents(t, mark, e.id), map[bool]int{false: 0, true: 1}[tc.phase == "after"]; len(events) != want ||
					want == 1 && (events[0]["new_state"] != store.StateWorking || events[0]["stuck_reason"] != api.StuckNoScreenChange ||
						events[0]["ts"] != exitAt.Format("2006-01-02T15:04:05.000Z")) {
					t.Fatalf("events = %v; want %d exit at %s", events, want, exitAt)
				}
				e.now = fresh.Add(time.Minute - time.Millisecond)
				assertStuckState(t, c, e.id, store.StateWorking)
			})
		}
	}
}

// TestPaneStuckSkippedWriteKeepsBaseline: a sample that cannot commit, a failed
// capture's reset included, changes and emits nothing. The read never waits
// for a held write lock: with a busy timeout longer than the hold, one that
// waited would take the lock when the hold ends and commit.
func TestPaneStuckSkippedWriteKeepsBaseline(t *testing.T) {
	t.Parallel()
	for _, displayed := range []bool{false, true} {
		for _, failure := range []string{"held lock", "failed write"} {
			t.Run(fmt.Sprintf("displayed=%v/%s", displayed, failure), func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
				c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60, StoreBusyTimeoutMs: (2 * time.Minute).Milliseconds()})
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				start, want := e.now, store.StateWorking
				var shown api.StatusResult
				if displayed {
					e.now = start.Add(time.Minute)
					shown, want = assertStuckState(t, c, e.id, api.StuckNoScreenChange), api.StuckNoScreenChange
				}
				committed := e.row(t)
				release := func() {}
				if failure == "held lock" {
					release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				} else {
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailStuckObservation, e.id)
				}
				failOnce(e, tmux.CallCapture)
				mark := trailMark(t)
				e.now = e.now.Add(30 * time.Second)
				got := observeStatus(t, c, e.id, want, nil)
				release()
				if row := e.row(t); row.StuckVersion != committed.StuckVersion || row.StuckObservation != committed.StuckObservation ||
					row.PaneSHA256 != committed.PaneSHA256 || row.PaneSinceMillis != committed.PaneSinceMillis {
					t.Fatalf("a skipped sample changed the row: before=%+v after=%+v", committed, row)
				}
				if displayed && (got.Since != shown.Since || minutes(t, got) != minutes(t, shown)) {
					t.Fatalf("skipped sample=%+v; want the committed %+v", got, shown)
				}
				if events := stuckEvents(t, mark, e.id); len(events) != 0 {
					t.Fatalf("a skipped sample emitted %v", events)
				}
				if failure == "held lock" {
					// The committed baseline continues: its period started at start.
					e.now = start.Add(2 * time.Minute)
					if got := assertStuckState(t, c, e.id, api.StuckNoScreenChange); minutes(t, got) != 2 {
						t.Fatalf("continued=%+v; want 2 minutes from the committed start", got)
					}
				}
			})
		}
	}
}

// TestPaneStuckOlderSampleNeverOverwrites: a write landing between a read's
// capture and its commit (another reader's newer baseline or reset, a turn, a
// new launch) refuses the older sample: the read answers its own committed
// view, the newer write stands and no transition is emitted.
func TestPaneStuckOlderSampleNeverOverwrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		write func(*testing.T, *paneEnv, *api.Client)
	}{
		{"newer baseline", func(t *testing.T, e *paneEnv, other *api.Client) {
			e.show("pane 2")
			observeStatus(t, other, e.id, store.StateWorking, apitest.VersionAdvanced)
		}},
		{"newer reset", func(t *testing.T, e *paneEnv, other *api.Client) {
			observeStatus(t, other, e.id, store.StateWorking, failOnce(e, tmux.CallCapture))
		}},
		{"turn", func(t *testing.T, e *paneEnv, _ *api.Client) {
			for _, state := range []string{store.StateWaiting, store.StateWorking} {
				if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"new launch", func(t *testing.T, e *paneEnv, _ *api.Client) {
			if got := apitest.ApplyAgentHook(t, e.dbPath, e.id, "SessionEnd", ""); !got.Applied {
				t.Fatal(got)
			}
			ended := e.row(t)
			if res, _, err := e.s.MoveToPending(e.id, ended.Snapshot, e.now.UnixMilli(), "2222222222222222", apitest.TestSocket, "", store.LaunchOwner{}); err != nil || res != store.CondApplied {
				t.Fatalf("new launch = %v, %v", res, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
			keys := apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60}
			c, other := e.client(t, keys), e.client(t, keys)
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			e.now = e.now.Add(time.Minute) // the interrupted read's sample qualifies
			var fired atomic.Bool
			var newer store.Spawn
			e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) {
				if fired.CompareAndSwap(false, true) {
					tc.write(t, e, other)
					newer = e.row(t)
				}
			})
			mark := trailMark(t)
			got, err := c.Status(e.id)
			if err != nil || got.State != store.StateWorking || got.StuckInfo != (api.StuckInfo{}) {
				t.Fatalf("interrupted read = %+v, %v; want its committed working view", got, err)
			}
			if !fired.Load() {
				t.Fatal("the interrupted read made no capture")
			}
			if row := e.row(t); row.StuckVersion != newer.StuckVersion || row.StuckObservation != newer.StuckObservation ||
				row.PaneSHA256 != newer.PaneSHA256 || row.PaneSinceMillis != newer.PaneSinceMillis {
				t.Fatalf("the older sample overwrote the newer write: newer=%+v after=%+v", newer, row)
			}
			if events := stuckEvents(t, mark, e.id); len(events) != 0 {
				t.Fatalf("the refused sample emitted %v", events)
			}
		})
	}
}

// TestPaneStuckWriteFailureIsLocalToListRow: a pane row whose observation
// cannot commit keeps its committed working view in list, which still filters
// and returns another row's own committed stuck display.
func TestPaneStuckWriteFailureIsLocalToListRow(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
	c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60})
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	other, err := apitest.SeedSpawn(e.dbPath, "", store.StateCheckPermission, "", "on", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, other, "Read", apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt, time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailStuckObservation, e.id)
	e.now = e.now.Add(time.Minute)
	listed, err := c.List(api.ListParams{State: []string{"stuck"}})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != other || listed.Spawns[0].StuckReason != api.StuckPermissionUnanswered {
		t.Fatalf("list = %+v, %v; want only %s, permission_unanswered", listed, err, other)
	}
	assertStuckState(t, c, e.id, store.StateWorking)
}

// TestPaneStuckListStopsAnUnansweringSocket: in one list, a lookup or pane
// listing that stops its socket (tmux.Result.StopsSocket) ends its tmux calls.
func TestPaneStuckListStopsAnUnansweringSocket(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		call    tmux.Call
		failure tmux.Failure
		calls   int // the list's tmux calls on the shared socket
	}{
		{"lookup timeout", tmux.CallLookup, tmux.FailTimeout, 1},
		{"tmux unavailable", tmux.CallLookup, tmux.FailUnavailable, 1},
		{"unreadable pane listing", tmux.CallListPanes, tmux.FailUnrecognized, 2},
		{"no server", tmux.CallLookup, tmux.FailNoServer, 3},
		{"capture timeout", tmux.CallCapture, tmux.FailTimeout, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
			shared := []string{e.id, e.addRow(t, store.AgentCodex, apitest.TestSocket, "%7", 4343), e.addRow(t, store.AgentClaude, apitest.TestSocket, "%8", 4344)}
			other := e.addRow(t, store.AgentCodex, fmbSocket(1), "%9", 4345)
			c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60})
			if _, err := c.List(api.ListParams{}); err != nil {
				t.Fatal(err)
			}
			committed := map[string]store.Spawn{}
			for _, id := range append(shared, other) {
				row, err := e.s.GetSpawn(id)
				if err != nil || row.PaneSHA256 == "" {
					t.Fatalf("setup: row %s has no baseline: %+v %v", id, row, err)
				}
				committed[id] = row
			}
			e.rec.Reset()
			e.rec.Script(apitest.TestSocket, tmuxfix.Script{Failure: tc.failure}, tc.call)
			e.now = e.now.Add(time.Second)
			if _, err := c.List(api.ListParams{}); err != nil {
				t.Fatal(err)
			}
			calls := map[string]int{}
			for _, call := range e.rec.SocketCalls() {
				calls[call.Socket]++
			}
			if calls[apitest.TestSocket] != tc.calls || calls[fmbSocket(1)] != 3 {
				t.Errorf("tmux calls per socket = %v; want %d on the shared socket and the other row's 3", calls, tc.calls)
			}
			for _, id := range append(shared, other) {
				row, err := e.s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				before := committed[id]
				if id == other && (row.StuckVersion != before.StuckVersion || row.PaneSHA256 != before.PaneSHA256 || row.PaneSinceMillis != before.PaneSinceMillis) {
					t.Errorf("the other socket's row lost its baseline: before=%+v after=%+v", before, row)
				}
				if id != other && (row.StuckVersion != before.StuckVersion+1 || row.PaneSHA256 != "" || row.PaneSinceMillis != 0) {
					t.Errorf("row %s kept its baseline: before=%+v after=%+v", id, before, row)
				}
			}
		})
	}
}

// unsampledReaders are the exported status, get and list, which sample no
// pane; each gives the row's displayed state and stuck_reason.
var unsampledReaders = []struct {
	name string
	read func(*store.Store, string) (string, string, error)
}{
	{"status", func(s *store.Store, id string) (string, string, error) {
		got, err := api.Status(s, api.RelayView{}, id)
		return got.State, got.StuckReason, err
	}},
	{"get", func(s *store.Store, id string) (string, string, error) {
		got, err := api.Get(s, api.RelayView{}, id)
		return got.State, got.StuckReason, err
	}},
	{"list", func(s *store.Store, id string) (string, string, error) {
		listed, err := api.List(s, api.RelayView{}, api.ListParams{})
		if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != id {
			return "", "", fmt.Errorf("list = %+v, %v; want only %s", listed, err, id)
		}
		return listed.Spawns[0].State, listed.Spawns[0].StuckReason, nil
	}},
}

// TestPaneStuckUnsampledReadKeepsOnlyCurrentFinding: the exported verbs (no pane
// sampler) keep only a NoScreenChangeCurrent finding; others settle out, one exit.
func TestPaneStuckUnsampledReadKeepsOnlyCurrentFinding(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(-time.Hour).UnixMilli() // these reads take the wall clock
	const epoch = 3
	noScreen := fmt.Sprintf(`"state":"stuck","reason":"no_screen_change","since_ms":%d,"unchanged_minutes":21`, at)
	permission := fmt.Sprintf(`"state":"stuck","reason":"permission_unanswered","since_ms":%d,"request_token":"tok","tool_name":"Bash"`, at)
	current := fmt.Sprintf(`"candidate":"stuck","candidate_since_ms":%d`, at)
	exiting := fmt.Sprintf(`"candidate":"working","candidate_since_ms":%d`, at) // a changed pane's settling exit
	ended := `"candidate":"","candidate_since_ms":0`                            // as a raw state change leaves it
	observation := func(display, candidate string, epoch int) string {
		return fmt.Sprintf(`{%s,%s,"observed_at_ms":%d,"permission_epoch":%d}`, display, candidate, at, epoch)
	}
	for _, tc := range []struct {
		name, agent, observation string
		kept                     bool
	}{
		{"current no_screen_change", store.AgentClaude, observation(noScreen, current, epoch), true},
		{"pending entry", store.AgentCodex, observation(`"state":"working"`, current, epoch), true},
		{"pending switch to no_screen_change", store.AgentClaude, observation(permission, current+`,"candidate_reason":"no_screen_change"`, epoch), true},
		{"hook-ended permission_unanswered", store.AgentCodex, observation(permission, ended, epoch-1), false},
		{"earlier turn's no_screen_change", store.AgentClaude, observation(noScreen, ended, epoch-1), false},
		{"current no_screen_change settling out", store.AgentCodex, observation(noScreen, exiting, epoch), false},
	} {
		for _, r := range unsampledReaders {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: tc.agent, opts: []apitest.SpawnOption{apitest.WithPermissionEpoch(epoch), apitest.WithStuckObservation(tc.observation)}})
				before := e.row(t)
				committed := before.CommittedStuckDisplay()
				mark := trailMark(t)
				state, reason, err := r.read(e.s, e.id)
				if err != nil {
					t.Fatal(err)
				}
				after, events := e.row(t), stuckEvents(t, mark, e.id)
				if tc.kept && (state != committed.State || reason != committed.Reason || after.StuckVersion != before.StuckVersion ||
					after.StuckObservation != before.StuckObservation || len(events) != 0) {
					t.Fatalf("read %s %q, events %v, row %+v; want the committed %+v kept unwritten", state, reason, events, after, committed)
				}
				if !tc.kept && (state != store.StateWorking || reason != "" || after.StuckVersion != before.StuckVersion+1 || after.StuckObservation != "" ||
					len(events) != 1 || events[0]["prior_state"] != "stuck" || events[0]["new_state"] != store.StateWorking || events[0]["stuck_reason"] != committed.Reason) {
					t.Fatalf("read %s %q, events %v, row %+v; want %s settled out with one exit", state, reason, events, after, committed.Reason)
				}
			})
		}
	}
}
