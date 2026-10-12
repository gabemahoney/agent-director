package store_test

// The move of a live Codex row holding an end report to ended
// (MarkEndedIfSameLife; SR-6.5, MC-2): the end-report guard's refusals and the
// one instant at which it closes the row's requests and subagent records. Its
// columns and snapshot guards are in find_missing_writes_test.go and
// row_version_find_missing_test.go, its deny reason and trail in
// relay_close_test.go (package store), its rollbacks in store_errors_test.go.

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// storeEndReportRaw writes v to id's end_reported_at as it is, bypassing the
// store's writes, so a value only a hand edit stores is expressible.
func (f *v5Store) storeEndReportRaw(id string, v any) {
	f.t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		f.t.Fatalf("raw sql.Open(%q): %v", f.path, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE spawns SET end_reported_at = ? WHERE instance_id = ?`, v, id); err != nil {
		f.t.Fatalf("set end_reported_at = %v: %v", v, err)
	}
}

// TestMarkEndedRefusedWithoutEndReport checks the move is refused, writing
// nothing, for a row that is not a Codex row holding an end report: a Claude
// row (even one a hand gave an end report), a Codex row whose end report is
// NULL, zero, negative, text or a real (only a positive integer is a report),
// and one whose report was cleared after the snapshot was read (a racing
// SessionStart). The row, its open and decided requests, its subagent records
// and the trail stay as they were.
func TestMarkEndedRefusedWithoutEndReport(t *testing.T) {
	cases := []struct {
		name  string
		opts  []apitest.SpawnOption
		raw   any  // stored over end_reported_at before the snapshot is read, when set
		clear bool // clear end_reported_at after the snapshot is read
	}{
		{name: "Claude row holding an end report", opts: []apitest.SpawnOption{apitest.WithEndReportedAt(rvEndReported)}},
		{name: "Codex row, NULL", opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}},
		{name: "Codex row, zero", opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex), apitest.WithEndReportedAt(0)}},
		{name: "Codex row, negative", opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex), apitest.WithEndReportedAt(-5)}},
		{name: "Codex row, text", opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}, raw: "reported"},
		{name: "Codex row, real", opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}, raw: 1.5},
		{name: "report cleared after the snapshot", opts: codexEnded, clear: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(store.StateWorking, "", append([]apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity())}, tc.opts...)...)
			if tc.raw != nil {
				f.storeEndReportRaw(id, tc.raw)
			}
			decided := seedRequest(t, f, id)
			seedRequest(t, f, id)
			ok, err := f.s.DecidePermissionRequest(id, decided, "allow", "", store.WriterProcessDecide)
			wantBool(t, "DecidePermissionRequest", ok, err, true)
			mix := f.seedCloseMix(id)
			sp := rvExamine(t, f, id)
			if tc.clear {
				f.storeEndReportRaw(id, nil)
			}
			before, requests := f.rawColumns(id), f.reuseRequests(t, id)
			mark := store.TrailMark(t)

			prior, res, err := f.s.MarkEndedIfSameLife(id, sp.Snapshot)

			if err != nil || res != store.CondChanged || prior != "" {
				t.Fatalf("MarkEndedIfSameLife = %q, %v, %v; want \"\", CondChanged, nil", prior, res, err)
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed:\n before %+v\n after  %+v", before, after)
			}
			if after := f.reuseRequests(t, id); len(requests) != 2 || !reflect.DeepEqual(after, requests) {
				t.Errorf("requests %+v -> %+v; want the two seeded requests unchanged", requests, after)
			}
			f.assertMixUnchanged(mix)
			for _, ev := range []string{"ad.spawn.state_transition", "ad.row_mutation.committed", "ad.find_missing.tick"} {
				if lines := store.TrailEventsSince(t, mark, ev, id); len(lines) != 0 {
					t.Errorf("%s lines = %v; want none", ev, lines)
				}
			}
		})
	}
}

// TestMarkEndedClosesRecordsAtOneInstant checks an applied move closes the
// row's requests and its subagent records at one instant of the store's clock
// (SR-8.1, b.xjn): an undecided request is denied with reason ended and closed,
// a decided one keeps its verdict (and stays open for its ack), every request is
// proven agent_gone, and each entry's lost_at equals those instants. It emits no state transition: the
// row's record is the caller's.
func TestMarkEndedClosesRecordsAtOneInstant(t *testing.T) {
	f := newV5Store(t)
	id := f.seed(store.StateWaiting, "", append([]apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity())}, codexEnded...)...)
	decided, undecided := seedRequest(t, f, id), seedRequest(t, f, id)
	ok, err := f.s.DecidePermissionRequest(id, decided, "allow", "", store.WriterProcessDecide)
	wantBool(t, "DecidePermissionRequest", ok, err, true)
	mix := f.seedCloseMix(id)
	sp := rvExamine(t, f, id)
	mark := store.TrailMark(t)

	var res store.CondResult
	lo, hi := cmWindow(func() {
		_, res, err = f.s.MarkEndedIfSameLife(id, sp.Snapshot)
	})

	if err != nil || res != store.CondApplied {
		t.Fatalf("MarkEndedIfSameLife = %v, %v; want CondApplied, nil", res, err)
	}
	f.assertMixClosedInWindow(mix, lo, hi)
	entry, _ := f.entryOf(id, cmRecord)
	at := entry.LostAt.(int64)
	reqs := f.reuseRequests(t, id)
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v; want the two seeded", reqs)
	}
	for _, r := range reqs {
		wantDecision, wantReason := "allow", ""
		if r.RequestToken == undecided {
			wantDecision, wantReason = "deny", store.DecisionReasonEnded
		}
		closedOK := r.RequestToken != undecided || r.ClosedAt.UnixMilli() == at // a decided request awaits its ack: not closed
		if r.Decision != wantDecision || r.DecisionReason != wantReason || !closedOK ||
			r.ProvenGoneHow != store.ProvenGoneAgentGone || r.ProvenGoneAt.UnixMilli() != at {
			t.Errorf("request %s = decision %q (%q), closed_at %v, proven %q at %v; want %q (%q), proven agent_gone (closed too if undecided) at %d",
				r.RequestToken, r.Decision, r.DecisionReason, r.ClosedAt, r.ProvenGoneHow, r.ProvenGoneAt, wantDecision, wantReason, at)
		}
	}
	if lines := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id); len(lines) != 0 {
		t.Errorf("state transition lines = %v; want none from the move", lines)
	}
}
