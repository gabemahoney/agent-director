package store_test

// Helpers shared by the liveness-close tests (b.f5h, SR-8.1): the close mix a
// closing write is run over, and the assertions that every running entry of the
// row turned lost at one instant, every claimable launch of the row went, and
// nothing else changed (a closed entry, another column, another row). Used by
// hook_gate_test.go, find_missing_writes_test.go, resume_move_test.go,
// reuse_test.go, reuse_refused_test.go and store_errors_test.go.

import (
	"reflect"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cmRecord is the running record of the close mix: the entry the restart checks
// reopen.
const cmRecord = "cm-record"

// closeMix is a row seeded with the close mix, and a bystander row with one
// running entry and one claimable launch, each with its subagent rows as seeded.
type closeMix struct {
	id, bystander       string
	rows, bystanderRows apitest.SubagentRows
}

// cmMs is the n-th second past the seed epoch, so a seeded time never equals an
// instant a write chooses.
func cmMs(n int64) int64 { return apitest.SeededSubagentMs + n*1000 }

// seedCloseMix adds to id (a row whose launch token, if any, is its current
// launch) a running record and a running observation of the current launch, a
// stopped record, a lost absent_from_snapshot record, a stopped and a running
// entry of an earlier launch, and a claimable launch in each launch; and gives a
// new row a running entry and a claimable launch. Whatever id already holds
// stays, and mix.rows is every subagent row of id as it is after seeding.
func (f *v5Store) seedCloseMix(id string) closeMix {
	f.t.Helper()
	current, _ := f.rawColumns(id).LaunchToken.(string)
	if current == "" {
		current = "cm-current-launch"
	}
	const earlier = "cm-earlier-launch"
	record := func(n int64, launch string, extra ...apitest.SubagentOption) []apitest.SubagentOption {
		return append([]apitest.SubagentOption{
			apitest.WithSubagentLaunchToken(launch), apitest.WithSubagentType("cm-type"), apitest.WithSubagentDescription("cm description"),
			apitest.WithSubagentModel("cm-model"), apitest.WithSubagentParent(store.SubagentParentAgent, id),
			apitest.WithSubagentRunMode(store.SubagentRunModeBackground), apitest.WithSubagentFirstStartedAt(cmMs(n)),
			apitest.WithSubagentStartedAt(cmMs(n)), apitest.WithSubagentOpenedAt(cmMs(n)), apitest.WithSubagentActivityAt(cmMs(n)),
		}, extra...)
	}
	stopped := func(n int64) []apitest.SubagentOption {
		return []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent),
			apitest.WithSubagentStoppedAt(cmMs(n)), apitest.WithSubagentStoppedAtSource(store.SubagentSourceStop), apitest.WithSubagentActivityAt(cmMs(n))}
	}
	for _, e := range []struct {
		id   string
		opts []apitest.SubagentOption
	}{
		{cmRecord, record(1, current)},
		{"cm-observation", []apitest.SubagentOption{apitest.WithSubagentLaunchToken(current), apitest.WithSubagentConfirmed(false), apitest.WithSubagentActivityAt(cmMs(2))}},
		{"cm-stopped", record(3, current, stopped(4)...)},
		{"cm-lost", record(5, current, apitest.WithSubagentState(store.SubagentStateLost), apitest.WithSubagentCloseReason(store.SubagentCloseAbsentFromSnapshot),
			apitest.WithSubagentLostAt(cmMs(6)), apitest.WithSubagentActivityAt(cmMs(6)))},
		{"cm-earlier-stopped", record(7, earlier, stopped(8)...)},
		{"cm-earlier-running", record(9, earlier)},
	} {
		if err := apitest.SeedSubagent(f.path, id, e.id, e.opts...); err != nil {
			f.t.Fatalf("SeedSubagent(%s): %v", e.id, err)
		}
	}
	bystander := f.seed(store.StateWaiting, "")
	for _, l := range []struct{ row, toolUseID, launch string }{{id, "toolu-cm-current", current}, {id, "toolu-cm-earlier", earlier}, {bystander, "toolu-cm-bystander", ""}} {
		var opts []apitest.SubagentLaunchOption
		if l.launch != "" {
			opts = append(opts, apitest.WithClaimableLaunchToken(l.launch))
		}
		if err := apitest.SeedSubagentLaunch(f.path, l.row, l.toolUseID, opts...); err != nil {
			f.t.Fatalf("SeedSubagentLaunch(%s): %v", l.toolUseID, err)
		}
	}
	if err := apitest.SeedSubagent(f.path, bystander, "cm-bystander"); err != nil {
		f.t.Fatalf("SeedSubagent(bystander): %v", err)
	}
	return closeMix{id: id, bystander: bystander, rows: f.subagentRowsOf(id), bystanderRows: f.subagentRowsOf(bystander)}
}

// cmClosed is rows as a close at instant at leaves them: every running entry
// lost, agent_gone, lost_at and activity_at at, every other column and every
// closed entry as it was, and no claimable launch.
func cmClosed(rows apitest.SubagentRows, at int64) apitest.SubagentRows {
	want := apitest.SubagentRows{Entries: slices.Clone(rows.Entries), Launches: []apitest.SubagentLaunchColumns{}}
	for i, e := range want.Entries {
		if e.State == store.SubagentStateRunning {
			want.Entries[i].State, want.Entries[i].CloseReason = store.SubagentStateLost, store.SubagentCloseAgentGone
			want.Entries[i].LostAt, want.Entries[i].ActivityAt = at, at
		}
	}
	return want
}

// assertMixClosedAt checks the write closed m's row at exactly at and left the
// bystander as seeded.
func (f *v5Store) assertMixClosedAt(m closeMix, at int64) {
	f.t.Helper()
	if got, want := f.subagentRowsOf(m.id), cmClosed(m.rows, at); !reflect.DeepEqual(got, want) {
		f.t.Errorf("the row's entries and claimable launches after the write:\n got  %+v\n want %+v", got, want)
	}
	f.assertBystanderKept(m)
}

// assertMixClosedInWindow is assertMixClosedAt for a write that takes the
// store's own clock: the one instant it closed at (read from the running record)
// lies between lo and hi, the clock read just before and just after the write.
func (f *v5Store) assertMixClosedInWindow(m closeMix, lo, hi int64) {
	f.t.Helper()
	e, ok := f.entryOf(m.id, cmRecord)
	at, isInt := e.LostAt.(int64)
	if !ok || !isInt || at < lo || at > hi {
		f.t.Fatalf("lost_at of %s = %#v; want an instant in [%d, %d]", cmRecord, e.LostAt, lo, hi)
	}
	f.assertMixClosedAt(m, at)
}

// assertMixUnchanged checks neither m's row nor the bystander changed an entry
// or a claimable launch since the seed.
func (f *v5Store) assertMixUnchanged(m closeMix) {
	f.t.Helper()
	if got := f.subagentRowsOf(m.id); !reflect.DeepEqual(got, m.rows) {
		f.t.Errorf("the row's entries and claimable launches:\n before %+v\n after  %+v\nwant them unchanged", m.rows, got)
	}
	f.assertBystanderKept(m)
}

func (f *v5Store) assertBystanderKept(m closeMix) {
	f.t.Helper()
	if got := f.subagentRowsOf(m.bystander); !reflect.DeepEqual(got, m.bystanderRows) {
		f.t.Errorf("the bystander's entries and claimable launches %+v -> %+v; want unchanged", m.bystanderRows, got)
	}
}

// entryOf returns id's entry with subagent id sub.
func (f *v5Store) entryOf(id, sub string) (apitest.SubagentEntryColumns, bool) {
	f.t.Helper()
	for _, e := range f.subagentRowsOf(id).Entries {
		if e.SubagentID == sub {
			return e, true
		}
	}
	return apitest.SubagentEntryColumns{}, false
}

// cmWindow runs write and returns the clock in Unix ms read just before and just after it.
func cmWindow(write func()) (lo, hi int64) {
	lo = time.Now().UnixMilli()
	write()
	return lo, time.Now().UnixMilli()
}

// assertStartReopens applies a SubagentStart at ms for the mix's running record,
// closed earlier, as id's own agent (its pane recorded) and checks it runs again
// in the launch with token and carries no close; every other entry is as it was.
func (f *v5Store) assertStartReopens(id, token string, ms int64) {
	f.t.Helper()
	before := f.subagentRowsOf(id)
	if a := storefix.ApplySubagentStart(f.t, f.s, id, store.SubagentLifecycleEvent{SubagentID: cmRecord, ReceivedMs: ms}); !a.Applied {
		f.t.Fatalf("SubagentStart = %+v; want applied", a)
	}
	after := f.subagentRowsOf(id)
	if len(after.Entries) != len(before.Entries) || !reflect.DeepEqual(after.Launches, before.Launches) {
		f.t.Fatalf("a start for an existing entry changed the entry count or claimable launches:\n before %+v\n after  %+v", before, after)
	}
	for i, e := range after.Entries {
		if e.SubagentID != cmRecord {
			if !reflect.DeepEqual(e, before.Entries[i]) {
				f.t.Errorf("entry %v changed by another entry's start:\n before %+v\n after  %+v", e.SubagentID, before.Entries[i], e)
			}
			continue
		}
		if e.State != store.SubagentStateRunning || e.CloseReason != nil || e.LostAt != nil || e.LaunchToken != token {
			f.t.Errorf("%s after the start: state %v, close_reason %v, lost_at %v, launch %v; want running, no close, launch %q",
				cmRecord, e.State, e.CloseReason, e.LostAt, e.LaunchToken, token)
		}
	}
}
