package api_test

// idle_screen_test.go — idle_screen (b.ob8): a raw working Claude or Codex
// row's own pane, sampled by status, get and list through the Recorder on an
// injected clock. A row showing it reads state working with displayed_state
// idle_screen (b.zpv). paneEnv is the shared fixture; the fallback clock's
// tests are needs_intelligence_test.go's.

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
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
	id, err := apitest.SeedSpawn(dbPath, "", spec.state, "", "off", "", true, append([]apitest.SpawnOption{apitest.WithAgent(spec.agent), apitest.WithPID(apitest.TestPanePID), apitest.WithProcStarttime(apitest.LinuxProcStarttime)}, spec.opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	rec, pc := tmuxfix.NewRecorderForReadPane(t, dbPath, id, "pane 1", tmuxfix.WithReadPaneLabel(spec.label))
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &paneEnv{s: s, dbPath: dbPath, id: id, rec: rec, pc: pc, now: displayAt}
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
	watchDisplayClient(c, e.s)
	return c
}

// addRow seeds another working row of agent beside e's, on its own pane
// paneID (pid panePID) of socket, with its session on e's Recorder capturing
// "pane 1" and its processes alive; it returns the row's id.
func (e *paneEnv) addRow(t *testing.T, agent, socket, paneID string, panePID int) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, "", "off", "", false, apitest.WithAgent(agent), apitest.WithPID(panePID), apitest.WithProcStarttime(apitest.LinuxProcStarttime),
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

// minutes is the unchanged_minutes of a status showing idle_screen: state
// working, displayed_state idle_screen.
func minutes(t *testing.T, got api.StatusResult) int64 {
	t.Helper()
	if got.State != store.StateWorking || got.DisplayedState != api.StateIdleScreen || got.UnchangedMinutes == nil {
		t.Fatalf("not displayed idle_screen: %+v", got)
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

// b.x9o: FindMissing must own both pane samples, without a public read
// establishing the baseline or committing the threshold on its behalf.
func TestIdleScreenSweepOnlyBaselineAndThreshold(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	before := e.row(t)
	start := e.now
	for i := 0; i < 2; i++ {
		e.now = start.Add(time.Duration(i) * time.Minute)
		got, err := c.FindMissing(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		assertLists(t, got, nil, nil)
		// Store.GetSpawn is a SQLite read only; no Client read has run.
		row := e.row(t)
		if row.State != store.StateWorking || row.PaneSHA256 == "" || row.PaneSinceMillis != start.UnixMilli() || row.PaneEpoch != row.PermissionEpoch ||
			row.ObservationVersion != before.ObservationVersion+int64(i)+1 || row.RowVersion != before.RowVersion || !row.LastSeenAt.Equal(before.LastSeenAt) {
			t.Fatalf("sweep %d stored observation: before=%+v after=%+v", i, before, row)
		}
		display := row.CommittedDisplay()
		wantRecord := store.Confirmation{Status: store.ConfirmationConfirmed, ConfirmedAtMillis: e.now.UnixMilli(), Source: store.EvidencePane, CheckedAtMillis: e.now.UnixMilli()}
		if rec := row.DisplayConfirmation(display, row.DisplayEvidence); rec != wantRecord {
			t.Fatalf("sweep record %+v; want %+v", rec, wantRecord)
		}
		if i == 0 && display.State != store.StateWorking {
			t.Fatalf("baseline already displayed idle: %+v", display)
		}
		if i == 1 && (display.State != store.StateIdleScreen || display.UnchangedMinutes != 1 || display.SinceMillis != e.now.UnixMilli()) {
			t.Fatalf("second sweep did not commit threshold: %+v", display)
		}
	}
	captures := 0
	for _, call := range e.rec.SocketCalls() {
		switch call.Call {
		case tmux.CallCapture:
			captures++
		case tmux.CallLookup, tmux.CallListPanes:
		default:
			t.Errorf("sweep dispatched %+v", call)
		}
	}
	if captures != 2 {
		t.Fatalf("sweeps captured %d panes, want exactly two", captures)
	}
}

// TestIdleScreenObservationBoundaries: whichever surface reads first commits
// the baseline; identical samples keep its start, a read exactly at the
// timeout qualifies (since that read, completed minutes floored), a displayed
// row keeps being observed, and a changed fingerprint restarts the period.
func TestIdleScreenObservationBoundaries(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		for _, first := range paneReaders {
			t.Run(agent+"/"+first.name, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: agent})
				c := e.client(t, apitest.ConfigKeys{})
				timeout := time.Duration(config.DefaultIdleScreenTimeoutSeconds) * time.Second
				before := e.row(t)
				if err := first.read(c, e.id); err != nil {
					t.Fatal(err)
				}
				start := e.now
				stored := e.row(t)
				if rec := stored.DisplayConfirmation(stored.CommittedDisplay(), stored.DisplayEvidence); rec != (store.Confirmation{Status: store.ConfirmationConfirmed, ConfirmedAtMillis: start.UnixMilli(), Source: store.EvidencePane, CheckedAtMillis: start.UnixMilli()}) {
					t.Fatalf("first sample record %+v", rec)
				}
				if after := e.row(t); after.ObservationVersion != before.ObservationVersion+1 || after.RowVersion != before.RowVersion ||
					after.PaneSinceMillis != start.UnixMilli() || after.PaneEpoch != after.PermissionEpoch {
					t.Fatalf("first read committed no baseline: before=%+v after=%+v", before, after)
				}
				// The baseline is read-pane's default fingerprint of the same pane.
				if rp, err := c.ReadPane(api.ReadPaneParams{InstanceID: e.id}); err != nil || rp.PaneSHA256 != e.row(t).PaneSHA256 {
					t.Fatalf("baseline %q is not read-pane's pane_sha256: %+v %v", e.row(t).PaneSHA256, rp, err)
				}
				e.now = start.Add(timeout - time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateWorking)
				e.now = start.Add(timeout)
				got := assertDisplayState(t, c, e.id, api.StateIdleScreen)
				entryTime := e.now
				assertPaneRecord(t, got.Confirmations.DisplayedState, entryTime, entryTime, "")
				if got.Since != e.now.Format(displayTS) || minutes(t, got) != 20 {
					t.Fatalf("entry=%+v; want since this read and 20 minutes", got)
				}
				// A displayed idle_screen is still observed: the minutes advance, floored.
				e.now = start.Add(timeout + time.Minute - time.Millisecond)
				if got := observeStatus(t, c, e.id, api.StateIdleScreen, nil); minutes(t, got) != 20 {
					t.Fatalf("minutes=%+v; want 20", got)
				}
				e.now = start.Add(timeout + time.Minute)
				continued := observeStatus(t, c, e.id, api.StateIdleScreen, apitest.VersionAdvanced)
				assertPaneRecord(t, continued.Confirmations.DisplayedState, entryTime, e.now, "")
				if got := continued; minutes(t, got) != 21 || got.Since != start.Add(timeout).Format(displayTS) {
					t.Fatalf("continued=%+v; want 21 minutes since the entry", got)
				}
				// A changed fingerprint clears at once (no settle) and restarts the period.
				e.show("pane 2")
				restart := e.now
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				e.now = restart.Add(timeout - time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateWorking)
				e.now = restart.Add(timeout)
				assertDisplayState(t, c, e.id, api.StateIdleScreen)
			})
		}
	}
}

// TestIdleScreenTimeoutChoice: a row's own override is its timeout whatever
// the reader's [state] idle_screen_timeout_seconds; without one (none, 0, or a
// stored value that is not a valid override) the reader's loaded
// configuration decides, its 0 the default 1200 s.
func TestIdleScreenTimeoutChoice(t *testing.T) {
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
		{"none and no config", nil, 0, time.Duration(config.DefaultIdleScreenTimeoutSeconds) * time.Second},
		{"stored 0 is none", int64(0), 60, time.Minute},
		{"stored negative is none", int64(-5), 60, time.Minute},
		{"stored above the range is none", int64(9223372037), 60, time.Minute},
		{"stored text is none", "ninety", 60, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentCodex, opts: []apitest.SpawnOption{apitest.WithIdleScreenTimeout(tc.override)}})
			c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: tc.config})
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			start := e.now
			e.now = start.Add(tc.want - time.Millisecond)
			assertDisplayState(t, c, e.id, store.StateWorking)
			e.now = start.Add(tc.want)
			assertDisplayState(t, c, e.id, api.StateIdleScreen)
		})
	}
}

// TestIdleScreenNoReadsNoEvidence: nothing observes between reads, so a turn
// that ran while nobody read starts a fresh period at the next read, however
// long ago the baseline was taken.
func TestIdleScreenNoReadsNoEvidence(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	idle := e.row(t)
	e.now = e.now.Add(24 * time.Hour)
	for _, state := range []string{store.StateWaiting, store.StateWorking} {
		if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
			t.Fatal(err)
		}
	}
	if row := e.row(t); row.PaneSinceMillis != idle.PaneSinceMillis || row.DisplayObservation != "" {
		t.Fatalf("observation changed without a read: %+v", row)
	}
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	if row := e.row(t); row.PaneSinceMillis != e.now.UnixMilli() || row.PaneEpoch != row.PermissionEpoch {
		t.Fatalf("the read continued the old period: %+v", row)
	}
	e.now = e.now.Add(time.Minute)
	assertDisplayState(t, c, e.id, api.StateIdleScreen)
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

// TestIdleScreenObservesOnlyOwnedWorkingPanes: only a raw working Claude or
// Codex row's own current pane is captured; no other row ever qualifies.
func TestIdleScreenObservesOnlyOwnedWorkingPanes(t *testing.T) {
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
		{"cursor working with an override", paneSpec{agent: store.AgentCursor, opts: []apitest.SpawnOption{apitest.WithIdleScreenTimeout(int64(1))}}, false},
		{"unknown agent working", paneSpec{agent: "gemini"}, false},
		{"no agent recorded, working", paneSpec{agent: ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, tc.spec)
			c := e.client(t, apitest.ConfigKeys{})
			raw := e.row(t).State
			for _, after := range []time.Duration{0, 21 * time.Minute, 42 * time.Minute} {
				e.now = displayAt.Add(after)
				// b.ob8 F2: exercise sampling-enabled clients on every surface.
				got, row, listed := clientReported(t, c, e.id)
				if got.State != raw || got.StateDetail != (api.StateDetail{}) || row.StateDetail != (api.StateDetail{}) {
					t.Fatalf("read at +%s = %+v, %+v; want ordinary %s", after, got, row, raw)
				}
				wantAvailability := api.IdleScreenUnavailable
				if tc.spec.agent == "" || tc.spec.agent == store.AgentClaude || tc.spec.agent == store.AgentCodex {
					wantAvailability = api.IdleScreenAvailable
				}
				assertIdleAvailability(t, got, got.IdleScreenAvailability, wantAvailability)
				assertIdleAvailability(t, row, row.IdleScreenAvailability, wantAvailability)
				assertIdleAvailability(t, listed, listed.IdleScreenAvailability, wantAvailability)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallCapture)); n != 0 {
				t.Errorf("captures = %d; want none", n)
			}
			if looked := len(e.rec.SocketCallsOf(tmux.CallLookup)) > 0; tc.lookup && !looked {
				t.Error("the row's session was never looked up")
			}
			if row := e.row(t); row.PaneSHA256 != "" || row.DisplayObservation != "" {
				t.Errorf("an unobserved pane left a baseline or observation: %+v", row)
			}
		})
	}
}

// TestIdleScreenFailedCaptureResets: a failed or unowned capture (the row's
// session now labelled for another row) before, while pending or after the
// visible entry is a sample that resets the committed baseline and withdraws
// the eligibility or display, a displayed one only once its exit settles, with
// one exit at that deadline; the read still succeeds, and the next owned
// success starts a fresh period at its own read.
func TestIdleScreenFailedCaptureResets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase  string
		settle time.Duration
	}{{"before", 0}, {"pending", 5 * time.Second}, {"after", 0}, {"after", 5 * time.Second}} {
		for _, failure := range []string{string(tmux.CallCapture), string(tmux.CallLookup), "unowned"} {
			t.Run(tc.phase+"/settle "+tc.settle.String()+"/"+failure, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
				c := e.client(t, apitest.ConfigKeys{StateSettleMs: tc.settle.Milliseconds(), IdleScreenTimeoutSeconds: 60})
				var shown api.StatusResult
				switch tc.phase {
				case "before":
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				case "pending": // the entry candidate is committed, not yet displayed
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
					e.now = e.now.Add(time.Minute)
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				case "after":
					shown = enterIdleScreen(t, e, c, tc.settle)
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
				read := func(commit apitest.CommitCheck) api.StatusResult {
					t.Helper()
					if !exiting {
						return observeStatus(t, c, e.id, store.StateWorking, commit)
					}
					got := observeStatus(t, c, e.id, api.StateIdleScreen, commit)
					if got.Since != shown.Since || minutes(t, got) != minutes(t, shown) {
						t.Fatalf("the detail changed before the exit's deadline: %+v; want %+v", got, shown)
					}
					return got
				}
				e.now = e.now.Add(time.Second)
				failed := e.now
				mark := trailMark(t)
				read(failedCommit)
				failedRow := e.row(t)
				wantRec := store.Confirmation{Status: store.ConfirmationNotConfirmed, CheckedAtMillis: failed.UnixMilli(), Reason: store.ReasonUnreadable}
				if exiting {
					at, err := time.Parse(displayTS, shown.Since)
					if err != nil {
						t.Fatal(err)
					}
					wantRec.ConfirmedAtMillis, wantRec.Source = at.UnixMilli(), store.EvidencePane
				} else if tc.phase != "after" {
					wantRec.ConfirmedAtMillis, wantRec.Source = displayAt.UnixMilli(), store.EvidencePane
				}
				if rec := failedRow.DisplayConfirmation(failedRow.CommittedDisplay(), failedRow.DisplayEvidence); rec != wantRec {
					t.Fatalf("failed sample record %+v; want %+v", rec, wantRec)
				}
				if row := e.row(t); row.PaneSHA256 != "" || row.PaneSinceMillis != 0 || (row.DisplayObservation != "") != exiting {
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
				got := read(apitest.VersionAdvanced)
				recovered := wantRec
				if !exiting {
					recovered.Status, recovered.Source, recovered.Reason = store.ConfirmationConfirmed, store.EvidencePane, ""
					recovered.CheckedAtMillis = fresh.UnixMilli()
					if recovered.ConfirmedAtMillis == 0 {
						// Settle-zero ended the idle episode; none has no earlier confirmation.
						recovered.ConfirmedAtMillis = fresh.UnixMilli()
					}
				}
				// While idle still settles out, the successful none candidate cannot confirm it.
				assertPaneRecord(t, got.Confirmations.DisplayedState, time.UnixMilli(recovered.ConfirmedAtMillis), time.UnixMilli(recovered.CheckedAtMillis), recovered.Reason)
				row := e.row(t)
				if rec := row.DisplayConfirmation(row.CommittedDisplay(), row.DisplayEvidence); rec != recovered {
					t.Errorf("recovered sample record %+v; want %+v", rec, recovered)
				}
				if row := e.row(t); row.PaneSinceMillis != fresh.UnixMilli() {
					t.Fatalf("the next success did not start fresh: %+v", row)
				}
				exitAt := failed
				if exiting {
					e.now = failed.Add(tc.settle)
					exitAt = e.now
					observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				}
				events := displayEvents(t, mark, e.id)
				if want := map[bool]int{false: 0, true: 1}[tc.phase == "after"]; len(events) != want {
					t.Fatalf("events = %v; want %d exit at %s", events, want, exitAt)
				}
				if len(events) == 1 {
					assertDisplayEvent(t, events[0], api.StateIdleScreen, store.StateWorking, exitAt)
				}
				e.now = fresh.Add(time.Minute - time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateWorking)
			})
		}
	}
}

// TestIdleScreenSkippedWriteKeepsBaseline: a sample that cannot commit, a
// failed capture's reset included, changes and emits nothing. The read never
// waits for a held write lock: with a busy timeout longer than the hold, one
// that waited would take the lock when the hold ends and commit.
func TestIdleScreenSkippedWriteKeepsBaseline(t *testing.T) {
	t.Parallel()
	for _, displayed := range []bool{false, true} {
		for _, failure := range []string{"held lock", "failed write"} {
			t.Run(fmt.Sprintf("displayed=%v/%s", displayed, failure), func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
				c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60, StoreBusyTimeoutMs: (2 * time.Minute).Milliseconds()})
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				start, want := e.now, store.StateWorking
				var shown api.StatusResult
				if displayed {
					e.now = start.Add(time.Minute)
					shown, want = assertDisplayState(t, c, e.id, api.StateIdleScreen), api.StateIdleScreen
				}
				committed := e.row(t)
				release := func() {}
				if failure == "held lock" {
					release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				} else {
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailDisplayObservation, e.id)
				}
				failOnce(e, tmux.CallCapture)
				mark := trailMark(t)
				e.now = e.now.Add(30 * time.Second)
				got := observeStatus(t, c, e.id, want, nil)
				release()
				reason := store.ReasonUnreadable
				if failure == "held lock" {
					reason = store.ReasonBusy
				}
				first := start
				if displayed {
					first = start.Add(time.Minute)
				}
				assertPaneRecord(t, got.Confirmations.DisplayedState, first, e.now, reason)
				if row := e.row(t); row.ObservationVersion != committed.ObservationVersion || row.DisplayObservation != committed.DisplayObservation ||
					row.PaneSHA256 != committed.PaneSHA256 || row.PaneSinceMillis != committed.PaneSinceMillis || row.DisplayEvidence != committed.DisplayEvidence {
					t.Fatalf("a skipped sample changed the row: before=%+v after=%+v", committed, row)
				}
				if displayed && (got.Since != shown.Since || minutes(t, got) != minutes(t, shown)) {
					t.Fatalf("skipped sample=%+v; want the committed %+v", got, shown)
				}
				if events := displayEvents(t, mark, e.id); len(events) != 0 {
					t.Fatalf("a skipped sample emitted %v", events)
				}
				if failure == "held lock" {
					// The committed baseline continues: its period started at start.
					e.now = start.Add(2 * time.Minute)
					if got := assertDisplayState(t, c, e.id, api.StateIdleScreen); minutes(t, got) != 2 {
						t.Fatalf("continued=%+v; want 2 minutes from the committed start", got)
					}
				}
			})
		}
	}
}

// TestIdleScreenOlderSampleNeverOverwrites: a write landing between a read's
// capture and its commit (another reader's newer baseline or reset, a turn, a
// new launch) refuses the older sample: the newer write stands, the final
// stored refresh can report a new launch, and no display transition is emitted.
func TestIdleScreenOlderSampleNeverOverwrites(t *testing.T) {
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
			keys := apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60}
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
			wantState := store.StateWorking
			if tc.name == "new launch" {
				wantState = store.StatePending
			}
			if err != nil || got.State != wantState || got.StateDetail != (api.StateDetail{}) {
				t.Fatalf("interrupted read = %+v, %v; want committed %s without display detail", got, err, wantState)
			}
			if !fired.Load() {
				t.Fatal("the interrupted read made no capture")
			}
			if row := e.row(t); row.ObservationVersion != newer.ObservationVersion || row.DisplayObservation != newer.DisplayObservation ||
				row.PaneSHA256 != newer.PaneSHA256 || row.PaneSinceMillis != newer.PaneSinceMillis {
				t.Fatalf("the older sample overwrote the newer write: newer=%+v after=%+v", newer, row)
			}
			if events := displayEvents(t, mark, e.id); len(events) != 0 {
				t.Fatalf("the refused sample emitted %v", events)
			}
		})
	}
}

// TestIdleScreenWriteFailureIsLocalToListRow: a pane row whose observation
// cannot commit keeps its committed working view in list, which still filters
// and returns another row by its own state: a long fallen-back request's row,
// check_permission with no displayed_state (b.zpv).
func TestIdleScreenWriteFailureIsLocalToListRow(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	other, err := apitest.SeedSpawn(e.dbPath, "", store.StateCheckPermission, "", "on", "", false, apitest.WithPID(apitest.TestPanePID), apitest.WithProcStarttime(apitest.LinuxProcStarttime))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, other, "Read", apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt, time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailDisplayObservation, e.id)
	e.now = e.now.Add(time.Minute)
	listed, err := c.List(api.ListParams{State: []string{api.StateIdleScreen, store.StateCheckPermission}})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != other || listed.Spawns[0].State != store.StateCheckPermission || listed.Spawns[0].DisplayedState != "" {
		t.Fatalf("list = %+v, %v; want only %s, check_permission", listed, err, other)
	}
	assertDisplayState(t, c, e.id, store.StateWorking)
}

// TestIdleScreenListStopsAnUnansweringSocket: in one list, a lookup or pane
// listing that stops its socket (tmux.Result.StopsSocket) ends its tmux calls.
func TestIdleScreenListStopsAnUnansweringSocket(t *testing.T) {
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
		{"no server", tmux.CallLookup, tmux.FailNoServer, 1},
		{"capture timeout", tmux.CallCapture, tmux.FailTimeout, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
			shared := []string{e.id, e.addRow(t, store.AgentCodex, apitest.TestSocket, "%7", 4343), e.addRow(t, store.AgentClaude, apitest.TestSocket, "%8", 4344)}
			other := e.addRow(t, store.AgentCodex, fmbSocket(1), "%9", 4345)
			c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
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
				if id == other && (row.ObservationVersion != before.ObservationVersion || row.PaneSHA256 != before.PaneSHA256 || row.PaneSinceMillis != before.PaneSinceMillis) {
					t.Errorf("the other socket's row lost its baseline: before=%+v after=%+v", before, row)
				}
				if id != other && (row.ObservationVersion != before.ObservationVersion+1 || row.PaneSHA256 != "" || row.PaneSinceMillis != 0) {
					t.Errorf("row %s kept its baseline: before=%+v after=%+v", id, before, row)
				}
			}
		})
	}
}

// unsampledReaders are the exported status, get and list, which sample no
// pane; each gives the row's state and displayed_state.
var unsampledReaders = []struct {
	name string
	read func(*store.Store, string) (string, string, error)
}{
	{"status", func(s *store.Store, id string) (string, string, error) {
		got, err := api.Status(s, api.RelayView{}, id)
		return got.State, got.DisplayedState, err
	}},
	{"get", func(s *store.Store, id string) (string, string, error) {
		got, err := api.Get(s, api.RelayView{}, id)
		return got.State, got.DisplayedState, err
	}},
	{"list", func(s *store.Store, id string) (string, string, error) {
		listed, err := api.List(s, api.RelayView{}, api.ListParams{})
		if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != id {
			return "", "", fmt.Errorf("list = %+v, %v; want only %s", listed, err, id)
		}
		return listed.Spawns[0].State, listed.Spawns[0].DisplayedState, nil
	}},
}

// TestIdleScreenUnsampledReadKeepsOnlyCurrentFinding: the exported verbs (no
// pane sampler) keep only an IdleScreenCurrent finding, unwritten; others
// settle out, one exit. Every read is state working; only a kept idle_screen
// shows displayed_state, and a stored needs_intelligence never shows (b.zpv).
func TestIdleScreenUnsampledReadKeepsOnlyCurrentFinding(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(-time.Hour).UnixMilli() // these reads take the wall clock
	const epoch = 3
	idleScreen := fmt.Sprintf(`"state":"idle_screen","since_ms":%d,"unchanged_minutes":21`, at)
	needsIntelligence := fmt.Sprintf(`"state":"needs_intelligence","since_ms":%d,"request_token":"tok","tool_name":"Bash"`, at)
	current := fmt.Sprintf(`"candidate":"idle_screen","candidate_since_ms":%d`, at)
	exiting := fmt.Sprintf(`"candidate":"working","candidate_since_ms":%d`, at) // a changed pane's settling exit
	ended := `"candidate":"","candidate_since_ms":0`                            // as a raw state change leaves it
	observation := func(display, candidate string, epoch int) string {
		return fmt.Sprintf(`{%s,%s,"observed_at_ms":%d,"permission_epoch":%d}`, display, candidate, at, epoch)
	}
	for _, tc := range []struct {
		name, agent, observation string
		kept                     bool
	}{
		{"current idle_screen", store.AgentClaude, observation(idleScreen, current, epoch), true},
		{"pending entry", store.AgentCodex, observation(`"state":"working"`, current, epoch), true},
		{"pending move to idle_screen", store.AgentClaude, observation(needsIntelligence, current, epoch), true},
		{"hook-ended needs_intelligence", store.AgentCodex, observation(needsIntelligence, ended, epoch-1), false},
		{"earlier turn's idle_screen", store.AgentClaude, observation(idleScreen, ended, epoch-1), false},
		{"current idle_screen settling out", store.AgentCodex, observation(idleScreen, exiting, epoch), false},
	} {
		for _, r := range unsampledReaders {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: tc.agent, opts: []apitest.SpawnOption{apitest.WithPermissionEpoch(epoch), apitest.WithDisplayObservation(tc.observation)}})
				before := e.row(t)
				committed := before.CommittedDisplay()
				mark := trailMark(t)
				state, displayed, err := r.read(e.s, e.id)
				if err != nil {
					t.Fatal(err)
				}
				after, events := e.row(t), displayEvents(t, mark, e.id)
				wantDisplayed := ""
				if tc.kept && committed.State == api.StateIdleScreen {
					wantDisplayed = api.StateIdleScreen
				}
				if state != store.StateWorking || displayed != wantDisplayed {
					t.Fatalf("read state %q displayed_state %q; want working, %q", state, displayed, wantDisplayed)
				}
				if tc.kept && (after.ObservationVersion != before.ObservationVersion ||
					after.DisplayObservation != before.DisplayObservation || len(events) != 0) {
					t.Fatalf("events %v, row %+v; want the committed %+v kept unwritten", events, after, committed)
				}
				if !tc.kept && (after.ObservationVersion != before.ObservationVersion+1 || after.DisplayObservation != "" ||
					len(events) != 1 || events[0]["prior_state"] != committed.State || events[0]["new_state"] != store.StateWorking) {
					t.Fatalf("events %v, row %+v; want %s settled out with one exit", events, after, committed.State)
				}
			})
		}
	}
}

// Selection limits every adapter, including a child's ended rollout and
// another row's permission request, before process or pane observation.
func TestIdleScreenFilteredReadNeverProbesExcludedRows(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	excluded := []string{e.addRow(t, store.AgentCursor, apitest.TestSocket, "%70", 7070), e.addRow(t, store.AgentClaude, apitest.TestSocket, "%71", 7171)}
	child := e.id + "-child"
	path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Ended())
	seedCodexTurn(t, e.s, e.dbPath, child, path, e.id, apitest.WithLaunchIdentity(store.LaunchIdentity{Token: "1234567890abcdef", Socket: apitest.TestSocket, PaneID: "%72", PanePID: 7272, PaneStarttime: apitest.LinuxProcStarttime}))
	excluded = append(excluded, child)
	req, err := apitest.SeedPermissionRequest(e.dbPath, excluded[1], "Bash")
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]store.Spawn{}
	for _, id := range excluded {
		row, err := e.s.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		before[id] = row
	}
	requestBefore, err := e.s.GetPermissionRequest(excluded[1], req.RequestToken)
	if err != nil {
		t.Fatal(err)
	}
	row := e.row(t)
	c := e.client(t, apitest.ConfigKeys{})
	callsBefore := len(e.pc.StartTimeCalls())
	got, err := c.List(api.ListParams{TmuxSessionName: row.TmuxSessionName, Limit: 1})
	if err != nil || len(got.Spawns) != 1 || got.Spawns[0].InstanceID != e.id || got.Spawns[0].FamilyState != store.StateWorking {
		t.Fatalf("selected list=%+v err=%v", got, err)
	}
	for _, id := range excluded {
		after, err := e.s.GetSpawn(id)
		if err != nil || !reflect.DeepEqual(after, before[id]) {
			t.Errorf("excluded %s changed: before=%+v after=%+v err=%v", id, before[id], after, err)
		}
	}
	requestAfter, err := e.s.GetPermissionRequest(excluded[1], req.RequestToken)
	if err != nil || !reflect.DeepEqual(requestBefore, requestAfter) {
		t.Fatal("excluded request changed")
	}
	for _, pid := range []int{7070, 7171, 7272} {
		if slices.Contains(e.pc.StartTimeCalls()[callsBefore:], pid) {
			t.Errorf("excluded process %d was probed", pid)
		}
	}
	for _, call := range e.rec.SocketCalls() {
		if call.Call == tmux.CallCapture && call.Target != row.Identity.PaneID {
			t.Errorf("excluded pane captured: %+v", call)
		}
	}
}

// assertPaneRecord checks all five fields, including D2's first confirmation
// and the independent latest attempt. Zero times mean no evidence/attempt.
func assertPaneRecord(t *testing.T, got api.Confirmation, first, checked time.Time, reason string) {
	t.Helper()
	want := unknownRecord(store.ReasonNoEvidence)
	if !checked.IsZero() {
		status, at, source, why := "confirmed", confAt(first), "pane", "-"
		if first.IsZero() {
			at, source = "-", "-"
		}
		if reason != "" {
			status, why = "not_confirmed", reason
		}
		want = status + "|" + at + "|" + source + "|" + confAt(checked) + "|" + why
	}
	if actual := conf(got); actual != want {
		t.Errorf("display record %s; want %s", actual, want)
	}
}

// b.x9o: same-value samples commit only their latest check. Reopened and
// non-sampling public reads retain the exact record; process/state/family
// stay independent, and a record-only write emits no transition.
func TestIdleScreenRecordOnlyAndStoredReads(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: agent})
			c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
			first := e.now
			got := observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			assertPaneRecord(t, got.Confirmations.DisplayedState, first, first, "")
			before, mark := e.row(t), trailMark(t)
			var refreshes atomic.Int64
			stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
				return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
					if strings.HasPrefix(q, "WITH RECURSIVE working(id)") {
						refreshes.Add(1)
					}
					return nil
				}})
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := stop(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			e.now = first.Add(time.Second)
			got = observeStatus(t, c, e.id, store.StateWorking, func(a, b store.Spawn) bool { return a.DisplayEvidence != b.DisplayEvidence })
			assertPaneRecord(t, got.Confirmations.DisplayedState, first, e.now, "")
			after := e.row(t)
			if refreshes.Load() != 0 {
				t.Fatal("record-only sample refreshed families")
			}
			if before.DisplayEvidence == after.DisplayEvidence || before.ObservationVersion != after.ObservationVersion || before.DisplayObservation != after.DisplayObservation || before.PaneSinceMillis != after.PaneSinceMillis || before.StateEvidence != after.StateEvidence || before.FamilyState != after.FamilyState || before.RowVersion != after.RowVersion {
				t.Fatal("record-only sample changed the display, baseline, state, family or version")
			}
			if events := displayEvents(t, mark, e.id); len(events) != 0 {
				t.Fatalf("record-only sample emitted %v", events)
			}
			reopened, err := store.Open(e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			for _, read := range readMain(t, reopened, api.RelayView{}, e.id) {
				assertPaneRecord(t, read.records.DisplayedState, first, e.now, "")
			}
			if row := e.row(t); row.DisplayEvidence != after.DisplayEvidence {
				t.Fatal("stored read redated evidence")
			}
			// Active reads agree at one injected instant, including the sweep
			// which exposes its record through storage rather than its result.
			for _, read := range paneReaders {
				if err := read.read(c, e.id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.FindMissing(context.Background()); err != nil {
				t.Fatal(err)
			}
			if row := e.row(t); row.DisplayEvidence != after.DisplayEvidence {
				t.Fatal("equivalent active readers disagree")
			}
		})
	}
}

// b.x9o: a newer record-only commit has no observation-version change; it
// still fences an older sample and its response-only stale overlay.
func TestIdleScreenNewerRecordWins(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	keys := apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60}
	c, other := e.client(t, keys), e.client(t, keys)
	start := e.now
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	before := e.row(t)
	e.now = start.Add(time.Second)
	var fired atomic.Bool
	var newer store.Spawn
	e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) {
		if fired.CompareAndSwap(false, true) {
			e.now = start.Add(2 * time.Second)
			observeStatus(t, other, e.id, store.StateWorking, func(a, b store.Spawn) bool { return a.DisplayEvidence != b.DisplayEvidence })
			newer = e.row(t)
			e.now = start.Add(3 * time.Second) // the older sample finishes after the newer commit
		}
	})
	got, err := c.Status(e.id)
	if err != nil {
		t.Fatal(err)
	}
	if !fired.Load() || newer.DisplayEvidence == before.DisplayEvidence || newer.ObservationVersion != before.ObservationVersion {
		t.Fatal("fixture did not commit a record alone")
	}
	if row := e.row(t); row.DisplayEvidence != newer.DisplayEvidence {
		t.Fatal("older sample overwrote the newer record")
	}
	assertPaneRecord(t, got.Confirmations.DisplayedState, start, start.Add(2*time.Second), "")
}

// b.x9o: a refused COMMIT is an invocation-local failed attempt; a committed
// observation still owns its display/evidence pair if the final refresh fails.
func TestIdleScreenCommitAndRefreshFailures(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"status", "get", "list"} {
		for _, cut := range []string{"commit unconfirmed", "final refresh"} {
			t.Run(verb+"/"+cut, func(t *testing.T) {
				t.Parallel()
				e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
				c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
				start := e.now
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
				before := e.row(t)
				e.now = start.Add(time.Minute)
				var observing, committed, refused atomic.Bool
				injected := errors.New("injected display boundary failure")
				stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
					return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
						Before: func(_ context.Context, q string) error {
							if q == "BEGIN IMMEDIATE" {
								observing.Store(false)
							}
							if strings.HasPrefix(q, "UPDATE spawns SET display_observation=") {
								observing.Store(true)
							}
							if cut == "commit unconfirmed" && q == "COMMIT" && observing.Load() {
								refused.Store(true)
								return injected
							}
							if cut == "final refresh" && committed.Load() && strings.HasPrefix(q, "SELECT ") && strings.Contains(q, "FROM spawns") {
								refused.Store(true)
								return injected
							}
							return nil
						},
						AfterCommit: func(context.Context) error {
							if observing.Load() {
								committed.Store(true)
							}
							return nil
						},
					})
				})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := stop(context.Background()); err != nil {
						t.Error(err)
					}
				}()
				var displayed string
				var record api.Confirmation
				switch verb {
				case "status":
					r, er := c.Status(e.id)
					displayed, record, err = r.DisplayedState, r.Confirmations.DisplayedState, er
				case "get":
					r, er := c.Get(e.id)
					displayed, record, err = r.DisplayedState, r.Confirmations.DisplayedState, er
				case "list":
					r, er := c.List(api.ListParams{})
					err = er
					if len(r.Spawns) != 1 {
						t.Fatalf("list lost selected row: %+v %v", r, er)
					}
					displayed, record = r.Spawns[0].DisplayedState, r.Spawns[0].Confirmations.DisplayedState
				}
				if err != nil || !refused.Load() {
					t.Fatalf("read did not cross the intended cut: %v refused=%v", err, refused.Load())
				}
				after := e.row(t)
				if cut == "final refresh" {
					if !committed.Load() || displayed != api.StateIdleScreen || after.CommittedDisplay().State != displayed {
						t.Fatal("committed display lost on final refresh failure")
					}
					assertPaneRecord(t, record, e.now, e.now, "")
				} else {
					if displayed != "" || after.DisplayEvidence != before.DisplayEvidence || after.DisplayObservation != before.DisplayObservation || after.ObservationVersion != before.ObservationVersion {
						t.Fatal("unconfirmed commit advertised or persisted new evidence")
					}
					assertPaneRecord(t, record, start, e.now, store.ReasonCommitUnconfirmed)
					for _, read := range readMain(t, e.s, api.RelayView{}, e.id) {
						assertPaneRecord(t, read.records.DisplayedState, start, start, "")
					}
				}
			})
		}
	}
}

// b.x9o: a spent tmux budget is a persisted deadline attempt, distinct from
// capture/ownership failures. No real waiting is needed to exhaust it.
func TestIdleScreenDeadlineRecord(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	start := e.now
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	clock := tmuxfix.NewClock(start.Add(time.Second))
	e.rec.WithVirtualTime(clock, tmux.Timeouts{Query: time.Minute})
	api.SetClockForTest(c, clock.Now)
	got, err := c.Status(e.id)
	if err != nil {
		t.Fatal(err)
	}
	assertPaneRecord(t, got.Confirmations.DisplayedState, start, clock.Now(), store.ReasonDeadline)
	row := e.row(t)
	want := store.Confirmation{Status: store.ConfirmationNotConfirmed, ConfirmedAtMillis: start.UnixMilli(), Source: store.EvidencePane, CheckedAtMillis: clock.Now().UnixMilli(), Reason: store.ReasonDeadline}
	if rec := row.DisplayConfirmation(row.CommittedDisplay(), row.DisplayEvidence); rec != want || row.PaneSHA256 != "" {
		t.Fatalf("deadline record/reset not durable: %+v %+v", rec, row)
	}
}
