package store_test

// Store-level proof of the Codex end report (SR-6.5, MC-1, MC-3): the gated
// ApplyHookEndReport write (a soft refresh that sets end_reported_at and keeps
// the state), the gate misses that change nothing, and the SessionStart identity
// write that clears the report. Fixtures are in hook_gate_test.go.

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// erSettleMs is the state_changed_ms every end-report row is seeded with,
	// so an unchanged settle column is distinguishable from the default 0.
	erSettleMs = 4242
	// erOldSeen is the last_seen_at the rows are backdated to, so a bump shows.
	erOldSeen = "2000-01-01 00:00:00"
)

// erSeed seeds a Codex row in state with settled_state waiting, state_changed_ms
// erSettleMs and last_seen_at backdated, and returns its id.
func erSeed(t *testing.T, f *v5Store, state string, opts ...apitest.SpawnOption) string {
	t.Helper()
	all := append([]apitest.SpawnOption{apitest.WithAgent(store.AgentCodex), apitest.WithSettle(store.StateWaiting, erSettleMs)}, opts...)
	id := hgSeed(t, f, hgRow{state: state, opts: all})
	raw, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE spawns SET last_seen_at = ? WHERE instance_id = ?`, erOldSeen, id); err != nil {
		t.Fatalf("backdate last_seen_at: %v", err)
	}
	return id
}

// erEndReport is a SessionEnd end report for id from p carrying hgHookSession
// and the hook transcript (present).
func erEndReport(f *v5Store, id string, p hgParent) (store.UpsertOutcome, store.HookApplied, error) {
	return f.s.ApplyHookEndReport(id, hgGate("SessionEnd", p, hgHookSession), "SessionEnd", hgHookPath, true)
}

// erSettle reads id's settle columns.
func erSettle(t *testing.T, f *v5Store, id string) [2]any {
	t.Helper()
	settled, ms, err := apitest.ReadSettle(f.path, id)
	if err != nil {
		t.Fatalf("ReadSettle(%s): %v", id, err)
	}
	return [2]any{settled, ms}
}

// TestEndReportWrite pins SR-6.5's applied write from the row's own pane
// process, on every row state: end_reported_at and last_seen_at set, the state
// kept, row_version advanced by one, the settle columns and open requests left
// alone, and one soft-refresh state_transition with the prior state as the new.
func TestEndReportWrite(t *testing.T) {
	states := []struct {
		name, state string
		openRequest bool
		opts        []apitest.SpawnOption
	}{
		{"pending", store.StatePending, false, nil},
		{"waiting", store.StateWaiting, false, nil},
		{"working", store.StateWorking, false, nil},
		{"check_permission with open request", store.StateCheckPermission, true, nil},
		{"ended", store.StateEnded, false, []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}},
		{"missing", store.StateMissing, false, nil},
	}
	f := newV5Store(t)
	for _, c := range states {
		t.Run(c.name, func(t *testing.T) {
			id := erSeed(t, f, c.state, c.opts...)
			if c.openRequest {
				if _, err := apitest.SeedPermissionRequest(f.path, id, "Bash"); err != nil {
					t.Fatalf("SeedPermissionRequest: %v", err)
				}
			}
			before, settle, requests, mark := hgColumns(t, f, id), erSettle(t, f, id), hgOpenRequests(t, f, id), store.TrailMark(t)
			var out store.UpsertOutcome
			var got store.HookApplied
			var err error
			lo, hi := cmWindow(func() { out, got, err = erEndReport(f, id, hgAgentParent(t, f, id)) })
			if err != nil || out != store.UpsertUpdated || !got.Applied {
				t.Fatalf("end report = %q, %+v, %v; want %q, applied", out, got, err, store.UpsertUpdated)
			}
			after := hgColumns(t, f, id)
			hgAssertApplied(t, before, after, "", true)
			// nowMsSQL rounds the clock to the millisecond, so allow one either side.
			if at, ok := after.EndReportedAt.(int64); !ok || at < lo-1 || at > hi+1 {
				t.Errorf("end_reported_at = %#v; want Unix ms in [%d, %d]", after.EndReportedAt, lo, hi)
			}
			if after.LastSeenAt == erOldSeen {
				t.Errorf("last_seen_at = %v; want bumped from %s", after.LastSeenAt, erOldSeen)
			}
			if s := erSettle(t, f, id); s != settle {
				t.Errorf("settle columns = %v; want %v unchanged", s, settle)
			}
			if n := hgOpenRequests(t, f, id); n != requests {
				t.Errorf("open requests %d -> %d; want unchanged", requests, n)
			}
			lines := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
			if len(lines) != 1 || lines[0]["prior_state"] != c.state || lines[0]["new_state"] != c.state ||
				lines[0]["triggering_event_name"] != "SessionEnd" || lines[0]["soft_refresh"] != true || lines[0]["source"] != "ad_spawn_store" {
				t.Errorf("transition lines = %v; want one %s -> %s, trigger SessionEnd, soft_refresh true, source ad_spawn_store", lines, c.state, c.state)
			}
		})
	}
}

// TestEndReportGateMisses pins MC-1 at the store: an end report from any parent
// but the row's pane process, for a row with no pane or no row, or racing a new
// launch (resume's move to pending, the new launch's identity write, a delete)
// changes nothing and gets the gate's reason.
func TestEndReportGateMisses(t *testing.T) {
	f := newV5Store(t)
	newPane := store.LaunchIdentity{PaneID: "%9", PanePID: 444, PaneStarttime: apitest.DarwinProcStarttime}
	type miss struct {
		name  string
		setup func(t *testing.T) (id string, p hgParent) // p: the parent the hook read before any race
		want  store.HookApplied
	}
	cases := []miss{
		{"no row", func(*testing.T) (string, hgParent) {
			return "er-no-such-row", hgParent{apitest.TestPanePID, apitest.LinuxProcStarttime}
		}, store.HookApplied{}},
	}
	for _, g := range hgParents[1:] {
		cases = append(cases, miss{g.name, func(t *testing.T) (string, hgParent) {
			id := hgSeed(t, f, hgRow{state: store.StateWorking, noPane: g.noPane, opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}})
			return id, g.parent(hgAgentParent(t, f, id))
		}, store.HookApplied{Reason: g.reason}})
	}
	races := []struct {
		name string
		race func(t *testing.T, id string, examined store.RowSnapshot)
		want store.HookApplied
	}{
		{"race: move to pending", func(t *testing.T, id string, examined store.RowSnapshot) { hgMove(t, f, id, examined) },
			store.HookApplied{Reason: store.HookReasonNoPaneRecorded}},
		{"race: new launch identity write", func(t *testing.T, id string, examined store.RowSnapshot) {
			hgMove(t, f, id, examined)
			if res, err := f.s.RecordLaunchIdentity(id, examined.RowVersion+1, moveToken, newPane); err != nil || res != store.CondApplied {
				t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
			}
		}, store.HookApplied{Reason: store.HookReasonPIDMismatch}},
		{"race: row deleted", func(t *testing.T, id string, _ store.RowSnapshot) {
			if err := f.s.DeleteSpawn(id); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}, store.HookApplied{}},
	}
	for _, r := range races {
		cases = append(cases, miss{r.name, func(t *testing.T) (string, hgParent) {
			id := hgSeed(t, f, hgRow{state: store.StateEnded, session: "cx-old", opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}})
			p, examined := hgAgentParent(t, f, id), hgExamine(t, f, id) // the hook's reads
			r.race(t, id, examined)
			return id, p
		}, r.want})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, p := c.setup(t)
			var before apitest.SpawnColumns
			if c.want != (store.HookApplied{}) {
				before = hgColumns(t, f, id)
			}
			mark := store.TrailMark(t)
			out, got, err := erEndReport(f, id, p)
			if err != nil || out != store.UpsertNoChange {
				t.Fatalf("end report = %q, %+v, %v; want %q, nil error", out, got, err, store.UpsertNoChange)
			}
			if c.want == (store.HookApplied{}) {
				if got != c.want {
					t.Errorf("result = %+v; want %+v", got, c.want)
				}
				return
			}
			hgAssertIgnored(t, f, id, mark, got, c.want, before)
			if after := hgColumns(t, f, id); after.EndReportedAt != nil {
				t.Errorf("end_reported_at = %v; want NULL", after.EndReportedAt)
			}
		})
	}
}

// TestEndReportClearedBySessionStart pins SR-6.5's clear: a SessionStart from
// the row's own process, with the same or a new session id, clears
// end_reported_at in its one write (row_version +1, one transition line) and
// still archives a rotated session; one from another process changes nothing.
func TestEndReportClearedBySessionStart(t *testing.T) {
	cases := []struct {
		name        string
		foreign     bool
		session     string // the SessionStart's session id; the row's is cx-old
		wantCleared bool
		wantHistory int // archived pairs
	}{
		{"same session", false, "cx-old", true, 0},
		{"new session archives the outgoing pair", false, "cx-new", true, 1},
		{"foreign process changes nothing", true, "cx-new", false, 0},
	}
	f := newV5Store(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldPath := codexfix.TempRolloutPath(t, "cx-old", true)
			id := erSeed(t, f, store.StateWorking, apitest.WithJsonlPath(oldPath))
			p := hgAgentParent(t, f, id)
			if _, got, err := f.s.ApplyHookEndReport(id, hgGate("SessionEnd", p, "cx-old"), "SessionEnd", oldPath, true); err != nil || !got.Applied {
				t.Fatalf("end report = %+v, %v; want applied", got, err)
			}
			if c.foreign {
				p = hgParent{storefix.ForeignHookParentPID(p.pid), p.start}
			}
			reported, mark := hgColumns(t, f, id), store.TrailMark(t)
			got, changed, err := hgSessionStart(t, f, id, p, c.session)
			if err != nil || changed {
				t.Fatalf("SessionStart: changed %v, %v; want false, nil", changed, err)
			}
			if !c.wantCleared {
				hgAssertIgnored(t, f, id, mark, got, store.HookApplied{Reason: store.HookReasonPIDMismatch}, reported)
				return
			}
			if !got.Applied {
				t.Fatalf("SessionStart = %+v; want applied", got)
			}
			after := hgColumns(t, f, id)
			if after.EndReportedAt != nil || after.State != store.StateWaiting || after.SessionID != c.session ||
				after.RowVersion != reported.RowVersion.(int64)+1 {
				t.Errorf("end_reported_at, state, session, version = %v, %v, %v, %v; want NULL, waiting, %s, %v+1",
					after.EndReportedAt, after.State, after.SessionID, after.RowVersion, c.session, reported.RowVersion)
			}
			if lines := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id); len(lines) != 1 ||
				lines[0]["prior_state"] != store.StateWorking || lines[0]["new_state"] != store.StateWaiting {
				t.Errorf("transition lines = %v; want one working -> waiting", lines)
			}
			wantHistory := []apitest.HistoryEntry(nil)
			if c.wantHistory > 0 {
				wantHistory = []apitest.HistoryEntry{{SessionID: "cx-old", JSONLPath: sql.NullString{String: oldPath, Valid: true}, LifeNumber: 0}}
			}
			var history []apitest.HistoryEntry
			for _, e := range hgHistory(t, f, id) {
				e.RecordedAt = ""
				history = append(history, e)
			}
			if !reflect.DeepEqual(history, wantHistory) {
				t.Errorf("history = %+v; want %+v", history, wantHistory)
			}
		})
	}
}

// TestEndReportLeavesSubagentRecords is the Epic 4 regression row: an applied
// end report on a Codex row, fired through storefix's Codex SessionEnd, keeps
// the row's running subagent entry and claimable launch exactly as they were;
// only the move to ended closes them.
func TestEndReportLeavesSubagentRecords(t *testing.T) {
	f := newV5Store(t)
	id := erSeed(t, f, store.StateWorking)
	if err := apitest.SeedSubagent(f.path, id, "sa-running"); err != nil {
		t.Fatalf("SeedSubagent: %v", err)
	}
	if err := apitest.SeedSubagentLaunch(f.path, id, "toolu-claimable"); err != nil {
		t.Fatalf("SeedSubagentLaunch: %v", err)
	}
	before := f.subagentRowsOf(id)
	if len(before.Entries) != 1 || len(before.Launches) != 1 {
		t.Fatalf("seeded %d entries and %d launches; want 1 and 1", len(before.Entries), len(before.Launches))
	}
	if got := storefix.ApplyAgentHook(t, f.s, id, "SessionEnd", hgHookSession, storefix.HookAgent(store.AgentCodex)); !got.Applied {
		t.Fatalf("Codex SessionEnd = %+v; want applied", got)
	}
	if c := hgColumns(t, f, id); c.EndReportedAt == nil || c.State != store.StateWorking {
		t.Errorf("end_reported_at, state = %v, %v; want set and working", c.EndReportedAt, c.State)
	}
	if after := f.subagentRowsOf(id); !reflect.DeepEqual(after, before) {
		t.Errorf("subagent records changed by an end report:\n before %+v\n after  %+v", before, after)
	}
}
