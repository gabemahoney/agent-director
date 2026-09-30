package api_test

// find_missing_trail_tmux_test.go: the ticks of rows find-missing decides by
// the tmux lookup (SR-11.3, SR-11.4, SR-14; SR-20.6): the mark ticks'
// reasons and fields per path and their order, and the note ticks written
// once. Each test sweeps a real store seeded through apitest.SeedSpawn,
// judged by procfix and asked about through a tmuxfix.Recorder. The shared
// trail helpers are in find_missing_trail_test.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// trailTmux builds the Recorder a trail case's sweeps ask about the row id of the store at dbPath.
type trailTmux func(t *testing.T, dbPath, id string) *tmuxfix.Recorder

// trailNoServer: no server on any socket, so every lookup is Gone with the name free.
func trailNoServer(*testing.T, string, string) *tmuxfix.Recorder { return tmuxfix.NewRecorder() }

// trailCantTell: every lookup and pane listing is Can't tell (unreadable).
func trailCantTell(*testing.T, string, string) *tmuxfix.Recorder { return fmCantTell() }

// trailSession returns a trailTmux holding the row's own session under its recorded name, adjusted by opts
// (none: the row's current label, so the lookup is Ours).
func trailSession(opts ...tmuxfix.RowSessionOption) trailTmux {
	return func(t *testing.T, dbPath, id string) *tmuxfix.Recorder {
		rec := tmuxfix.NewRecorder()
		rec.SeedRowSession(t, dbPath, id, opts...)
		return rec
	}
}

// trailUnlabelled: an unlabelled session holds the row's recorded name, so the lookup is Gone with the name held.
var trailUnlabelled = trailSession(tmuxfix.WithRowSessionLabel(tmux.Label{}, false))

// trailLeftover: a session with the row's id and an earlier launch's token holds its name (Leftover).
func trailLeftover(t *testing.T, dbPath, id string) *tmuxfix.Recorder {
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return trailSession(tmuxfix.WithRowSessionLabel(tmuxfix.Valid(tmuxfix.OtherToken, id, storeID), true))(t, dbPath, id)
}

// trailOursUnlisted: the row's own session (Ours), but the adoption's pane listing is Can't tell.
func trailOursUnlisted(t *testing.T, dbPath, id string) *tmuxfix.Recorder {
	return trailSession()(t, dbPath, id).Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailUnrecognized},
		tmux.CallListPanes)
}

// trailCase is one row swept by a trail test: its seeding, process answer, tmux (nil: no server) and sweep budget
// (0: the default), and the first tick it expects: reason, the lookup_outcome it carries ("" = none), whether it
// carries tmux_session_name (the recorded name), and whether it is a mark.
type trailCase struct {
	name    string
	row     trailRow
	proc    procfix.Process
	tmux    trailTmux
	budget  time.Duration
	reason  string
	outcome string
	named   bool
	marked  bool
}

// setUp seeds c's row, scripts its process answer and builds its Recorder (nil when c.tmux is nil).
func (c trailCase) setUp(t *testing.T) (*store.Store, string, *procfix.Checker, *tmuxfix.Recorder) {
	t.Helper()
	pc := procfix.New()
	if pid := c.row.pid(); pid > 0 {
		pc.Set(pid, c.proc)
	}
	st, dbPath := seedTrailStore(t, c.row)
	var rec *tmuxfix.Recorder
	if c.tmux != nil {
		rec = c.tmux(t, dbPath, c.row.id)
	}
	return st, dbPath, pc, rec
}

// assertFirstTick fails unless ticks holds exactly c's expected tick; name is the row's recorded session name.
func (c trailCase) assertFirstTick(t *testing.T, ticks []map[string]any, name string) {
	t.Helper()
	if len(ticks) != 1 {
		t.Fatalf("ticks = %v; want exactly one %s", ticks, c.reason)
	}
	extra := map[string]any{}
	if c.outcome != "" {
		extra["lookup_outcome"] = c.outcome
	}
	if c.named {
		extra["tmux_session_name"] = name
	}
	prior := ""
	if c.marked {
		prior = store.StateWorking
	}
	assertTick(t, ticks[0], c.reason, prior, extra)
}

// runTickOnce sweeps c's row twice and fails unless the first sweep writes exactly c's tick and the repeat none: a
// marked row leaves the live set, a noted one stays unverified with its note unchanged (SR-11.4).
func runTickOnce(t *testing.T, c trailCase) {
	t.Helper()
	st, _, pc, rec := c.setUp(t)
	for sweep, wantTicks := range []int{1, 0} {
		before := trailLen(t)
		res, err := runFindMissing(st, pc, fmSweep{budget: c.budget, tmux: rec})
		if err != nil {
			t.Fatalf("sweep %d: %v", sweep+1, err)
		}
		var ids, unverified []string
		switch {
		case !c.marked:
			unverified = []string{c.row.id}
		case sweep == 0:
			ids = []string{c.row.id}
		}
		assertLists(t, res, ids, unverified)
		ticks := trailOf(apiFindMissingTicksAt(t, before), c.row.id)
		if wantTicks == 0 {
			if len(ticks) != 0 {
				t.Errorf("sweep %d ticks = %v; want none", sweep+1, ticks)
			}
			continue
		}
		c.assertFirstTick(t, ticks, recordedName(t, st, c.row.id))
	}
}

// TestFindMissingProbeEaccesEmitsExactlyOnceTick: an unreadable process is decided by its lookup: Gone marks it
// with one tmux_absent tick and no note tick; Ours or Can't tell tick probe_eacces once, and never on a repeat.
func TestFindMissingProbeEaccesEmitsExactlyOnceTick(t *testing.T) {
	cases := []trailCase{
		{name: "gone", tmux: trailNoServer, reason: "tmux_absent", outcome: "gone", marked: true},
		{name: "ours", tmux: trailSession(), reason: "probe_eacces"},
		{name: "cant tell unreadable", tmux: trailCantTell, reason: "probe_eacces"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.row, c.proc = trailRow{id: "pe-" + strings.Fields(c.name)[0], panePID: 1301}, procfix.Unreadable()
			runTickOnce(t, c)
		})
	}
}

// TestFindMissingUnverifiedNoteTicksOnce: a row whose process cannot decide and whose lookup left it live gets one
// tick naming its note on entry and none on a repeat; it stays unverified on both (SR-11.4).
func TestFindMissingUnverifiedNoteTicksOnce(t *testing.T) {
	for _, c := range []trailCase{
		{name: "unreadable, not called", row: trailRow{id: "un-unreadable", ssPID: 1311}, proc: procfix.Unreadable(),
			budget: fmBudgetSpent, reason: "probe_eacces"},
		{name: "pid-only pane alive, not called", row: trailRow{id: "un-pid-only", panePID: 1312, pidOnly: true},
			proc: procfix.Alive(fmStart), budget: fmBudgetSpent, reason: "probe_eacces"},
		{name: "no identity, socket skipped", row: trailRow{id: "un-skipped"}, budget: fmBudgetSpent,
			reason: "process_not_seen_tmux_unchecked"},
		{name: "no identity, ours, listing cant tell", row: trailRow{id: "un-ours"}, tmux: trailOursUnlisted,
			reason: "process_not_seen_session_present"},
	} {
		t.Run(c.name, func(t *testing.T) { runTickOnce(t, c) })
	}
}

// orderStore is a real store that notes the trail's line count when the mark returns and when the
// permission-request close starts.
type orderStore struct {
	*store.Store
	t                     *testing.T
	afterMark, atClose    int
	markCalls, closeCalls int
}

func (o *orderStore) MarkMissingIfSameLife(id string, examined store.RowSnapshot) (string, store.CondResult, error) {
	prior, res, err := o.Store.MarkMissingIfSameLife(id, examined)
	o.markCalls++
	o.afterMark = trailLen(o.t)
	return prior, res, err
}

func (o *orderStore) CloseOrphanedPermissionRequests(id string) error {
	o.closeCalls++
	o.atClose = trailLen(o.t)
	return o.Store.CloseOrphanedPermissionRequests(id)
}

// TestFindMissingMarkOrderTrail: on each path a row's mark tick carries its path's reason and SR-11.4 fields and
// is written after the mark and before the permission-request close, whose permission_orphan_closeout tick
// follows; the open request is denied, and a held name gives exactly one ad.launch.name_held.
func TestFindMissingMarkOrderTrail(t *testing.T) {
	for _, c := range []trailCase{
		{name: "process gone", proc: procfix.Gone(), reason: "proc_absent"},
		{name: "lookup gone name free", proc: procfix.Unreadable(), tmux: trailNoServer, reason: "tmux_absent",
			outcome: "gone"},
		{name: "lookup gone name held", proc: procfix.Unreadable(), tmux: trailUnlabelled, reason: "tmux_name_held",
			outcome: "gone", named: true},
		{name: "lookup leftover", proc: procfix.Unreadable(), tmux: trailLeftover, reason: "tmux_name_held",
			outcome: "leftover", named: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.row, c.marked = trailRow{id: "ord-" + strings.ReplaceAll(c.name, " ", "-"), ssPID: 1501}, true
			st, dbPath, pc, rec := c.setUp(t)
			id := c.row.id
			if _, err := apitest.SeedPermissionRequest(dbPath, id, "Bash"); err != nil {
				t.Fatalf("SeedPermissionRequest: %v", err)
			}
			ord := &orderStore{Store: st, t: t}
			before := trailLen(t)

			res, err := runFindMissing(ord, pc, fmSweep{tmux: rec})
			if err != nil {
				t.Fatalf("FindMissing: %v", err)
			}
			assertLists(t, res, []string{id}, nil)
			if ord.markCalls != 1 || ord.closeCalls != 1 {
				t.Fatalf("mark calls = %d, close calls = %d; want 1, 1", ord.markCalls, ord.closeCalls)
			}
			var ticks []map[string]any
			var at []int // trail line index of each of id's ticks
			for i, line := range readAPITrailLines(t) {
				if i >= before && line["event"] == "ad.find_missing.tick" && line["claude_instance_id"] == id {
					ticks, at = append(ticks, line), append(at, i)
				}
			}
			if len(ticks) != 2 || ticks[1]["reconciliation_reason"] != "permission_orphan_closeout" {
				t.Fatalf("ticks = %v; want [%s permission_orphan_closeout]", ticks, c.reason)
			}
			c.assertFirstTick(t, ticks[:1], recordedName(t, st, id))
			if at[0] < ord.afterMark || at[0] >= ord.atClose || at[1] < ord.atClose {
				t.Errorf("%s at line %d, closeout at %d; mark returned at %d, close began at %d: want mark, tick, close",
					c.reason, at[0], at[1], ord.afterMark, ord.atClose)
			}
			if open, err := st.OpenPermissionRequestsForSpawn(id); err != nil || len(open) != 0 {
				t.Errorf("open permission requests = %v (err %v); want none", open, err)
			}
			held := trailOf(trailSince(t, before, "ad.launch.name_held"), id)
			switch {
			case !c.named && len(held) != 0:
				t.Errorf("ad.launch.name_held = %v; want none", held)
			case c.named && len(held) != 1:
				t.Errorf("ad.launch.name_held = %v; want exactly one", held)
			case c.named:
				assertAPITrailStr(t, held[0], "row_result", "marked_missing")
			}
		})
	}
}
