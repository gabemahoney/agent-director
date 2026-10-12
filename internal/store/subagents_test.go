package store_test

// The subagents and subagent_launches tables (b.xjn, SR-10.3): a row's delete
// takes its entries and claimable launches with it (the expire delete's cascade
// is in expire_candidates_test.go), and both 300 caps evict in the order
// SR-10.3 gives, through the gated evictions' export shim with a gate the row
// passes (a hook is gated: SR-3.1). The hook writes that call them and the
// refusals of the gate are subagent_gate_test.go's.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// subagentRowsOf reads every entry and claimable launch of id, failing on error.
func (f *v5Store) subagentRowsOf(id string) apitest.SubagentRows {
	f.t.Helper()
	rows, err := apitest.ReadSubagentRows(f.path, id)
	if err != nil {
		f.t.Fatalf("ReadSubagentRows(%s): %v", id, err)
	}
	return rows
}

// seedSubagentsOf gives id, whose current launch is "seed-launch", the
// subagent records the move and reuse seeds carry: a running entry
// ("sa-running"), a stopped one ("sa-stopped") and one claimable launch
// ("toolu-seed"), all of launch "seed-launch".
func (f *v5Store) seedSubagentsOf(id string) {
	f.t.Helper()
	for _, e := range []struct {
		id   string
		opts []apitest.SubagentOption
	}{{"sa-running", nil}, {"sa-stopped", []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateStopped),
		apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent)}}} {
		// An explicit launch: a row seeded with no launch token has no current one.
		if err := apitest.SeedSubagent(f.path, id, e.id, append(e.opts, apitest.WithSubagentLaunchToken("seed-launch"))...); err != nil {
			f.t.Fatalf("SeedSubagent(%s): %v", e.id, err)
		}
	}
	if err := apitest.SeedSubagentLaunch(f.path, id, "toolu-seed", apitest.WithClaimableLaunchToken("seed-launch")); err != nil {
		f.t.Fatalf("SeedSubagentLaunch: %v", err)
	}
}

// TestDeleteSpawnCascadesSubagents checks the admin delete removes the row's
// entries (of every launch, records and observations) and claimable launches,
// and leaves another row's untouched.
func TestDeleteSpawnCascadesSubagents(t *testing.T) {
	f := newV5Store(t)
	doomed, kept := f.seed(store.StateEnded, "sess-del-a"), f.seed(store.StateEnded, "sess-del-b")
	for _, id := range []string{doomed, kept} {
		for _, e := range []struct {
			subagentID string
			opts       []apitest.SubagentOption
		}{
			{"running", nil},
			{"observation", []apitest.SubagentOption{apitest.WithSubagentConfirmed(false), apitest.WithSubagentState(store.SubagentStateLost)}},
			{"earlier-launch", []apitest.SubagentOption{apitest.WithSubagentLaunchToken("earlier-launch-token")}},
		} {
			if err := apitest.SeedSubagent(f.path, id, e.subagentID, e.opts...); err != nil {
				t.Fatalf("SeedSubagent(%s, %s): %v", id, e.subagentID, err)
			}
		}
		for _, toolUseID := range []string{"toolu-1", "toolu-2"} {
			if err := apitest.SeedSubagentLaunch(f.path, id, toolUseID); err != nil {
				t.Fatalf("SeedSubagentLaunch(%s, %s): %v", id, toolUseID, err)
			}
		}
	}
	keptBefore := f.subagentRowsOf(kept)
	if got := f.subagentRowsOf(doomed); len(got.Entries) != 3 || len(got.Launches) != 2 || len(keptBefore.Entries) != 3 || len(keptBefore.Launches) != 2 {
		t.Fatalf("seeded rows hold %d entries, %d launches (kept row %d, %d); want 3 and 2 each", len(got.Entries), len(got.Launches),
			len(keptBefore.Entries), len(keptBefore.Launches))
	}

	if err := f.s.DeleteSpawn(doomed); err != nil {
		t.Fatalf("DeleteSpawn: %v", err)
	}
	if got := f.subagentRowsOf(doomed); len(got.Entries) != 0 || len(got.Launches) != 0 {
		t.Errorf("after the delete the row holds %d entries and %d claimable launches; want none", len(got.Entries), len(got.Launches))
	}
	if got := f.subagentRowsOf(kept); !reflect.DeepEqual(got, keptBefore) {
		t.Errorf("another row's subagent rows %+v -> %+v; want unchanged", keptBefore, got)
	}
}

// capCase is one filled-to-the-cap row: cands are inserted first, in order, then
// newer running fillers bring the row to total entries.
type capCase struct {
	name  string
	cands []capEntry
	total int    // entries in the row before the call; 0 = store.SubagentCap
	want  string // the subagent_id evicted; "" = nothing
}

// capEntry is one candidate entry: state "" = running, activity in ms past the
// seed epoch, observation = confirmed 0, earlier = a launch before the current.
type capEntry struct {
	id          string
	state       string
	observation bool
	earlier     bool
	activity    int64
}

// capFillerMs is past every candidate's activity, so a filler is evicted last.
const capFillerMs = apitest.SeededSubagentMs + 1_000_000

// TestSubagentEntryEvictionOrder checks the gated entry eviction on a live,
// tracked row whose hook is its own agent, so the gate passes (an eviction that
// deletes nothing where a case wants one fails): at 300 entries one is
// deleted, a closed observation before any closed record before any running
// entry, then the oldest activity_at, then the lowest created_seq; below the cap
// none; another row's entries neither count nor go.
func TestSubagentEntryEvictionOrder(t *testing.T) {
	stopped, lost := store.SubagentStateStopped, store.SubagentStateLost
	cases := []capCase{
		{name: "closed observation before an older closed record and an older running entry", want: "obs", cands: []capEntry{
			{id: "run", activity: 10}, {id: "rec", state: stopped, activity: 20}, {id: "obs", state: lost, observation: true, activity: 500}}},
		{name: "closed record before an older running entry", want: "rec", cands: []capEntry{
			{id: "run", activity: 10}, {id: "rec", state: stopped, activity: 500}}},
		{name: "closed record of an earlier launch counts", want: "rec-earlier", cands: []capEntry{
			{id: "run", activity: 10}, {id: "rec-earlier", state: lost, earlier: true, activity: 30}, {id: "rec-current", state: stopped, activity: 40}}},
		{name: "running observation is a running entry, not an observation", want: "rec", cands: []capEntry{
			{id: "run-obs", observation: true, activity: 10}, {id: "rec", state: stopped, activity: 500}}},
		{name: "nothing closed: the running entry with the oldest activity", want: "run-old", cands: []capEntry{
			{id: "run-new", activity: 300}, {id: "run-old", activity: 100}, {id: "run-mid", activity: 200}}},
		{name: "oldest activity among closed observations", want: "obs-old", cands: []capEntry{
			{id: "obs-new", state: stopped, observation: true, activity: 300}, {id: "obs-old", state: lost, observation: true, activity: 100}}},
		{name: "oldest activity among closed records", want: "rec-old", cands: []capEntry{
			{id: "rec-new", state: stopped, activity: 300}, {id: "rec-old", state: stopped, activity: 100}}},
		{name: "tie among closed observations goes to the lowest created_seq", want: "obs-first", cands: []capEntry{
			{id: "obs-first", state: stopped, observation: true, activity: 100}, {id: "obs-second", state: stopped, observation: true, activity: 100}}},
		{name: "tie among closed records goes to the lowest created_seq", want: "rec-first", cands: []capEntry{
			{id: "rec-first", state: lost, activity: 100}, {id: "rec-second", state: stopped, activity: 100}}},
		{name: "tie among running entries goes to the lowest created_seq", want: "run-first", cands: []capEntry{
			{id: "run-first", activity: 100}, {id: "run-second", activity: 100}}},
		{name: "below the cap nothing is deleted", total: store.SubagentCap - 1, cands: []capEntry{
			{id: "obs", state: lost, observation: true, activity: 10}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			id, other, gate := f.id, f.seed(store.StateEnded, "sess-cap-b"), saGateVariants[0].gate(f, "SubagentStart")
			// Another row's oldest closed observations: they neither count toward id's cap nor go.
			for i := 0; i < 5; i++ {
				if err := apitest.SeedSubagent(f.path, other, fmt.Sprintf("other-%d", i), apitest.WithSubagentConfirmed(false),
					apitest.WithSubagentState(stopped), apitest.WithSubagentActivityAt(1)); err != nil {
					t.Fatalf("SeedSubagent(other): %v", err)
				}
			}
			total := tc.total
			if total == 0 {
				total = store.SubagentCap
			}
			for _, c := range tc.cands {
				opts := []apitest.SubagentOption{apitest.WithSubagentActivityAt(apitest.SeededSubagentMs + c.activity),
					apitest.WithSubagentConfirmed(!c.observation)}
				if c.state != "" {
					opts = append(opts, apitest.WithSubagentState(c.state))
				}
				if c.earlier {
					opts = append(opts, apitest.WithSubagentLaunchToken("earlier-launch-token"))
				}
				if err := apitest.SeedSubagent(f.path, id, c.id, opts...); err != nil {
					t.Fatalf("SeedSubagent(%s): %v", c.id, err)
				}
			}
			for i := len(tc.cands); i < total; i++ {
				if err := apitest.SeedSubagent(f.path, id, fmt.Sprintf("filler-%d", i), apitest.WithSubagentActivityAt(capFillerMs+int64(i))); err != nil {
					t.Fatalf("SeedSubagent(filler): %v", err)
				}
			}
			before, otherBefore := f.subagentRowsOf(id), f.subagentRowsOf(other)

			evicted, err := store.EvictSubagentEntryGatedForTest(f.s, id, gate)
			if err != nil || evicted != (tc.want != "") {
				t.Fatalf("evict = %v, %v; want evicted %v", evicted, err, tc.want != "")
			}
			var wantRows []apitest.SubagentEntryColumns
			for _, e := range before.Entries {
				if e.SubagentID != tc.want {
					wantRows = append(wantRows, e)
				}
			}
			if got := f.subagentRowsOf(id).Entries; !reflect.DeepEqual(got, wantRows) {
				t.Errorf("row holds %d entries after the call; want the %d of before minus %q only", len(got), len(wantRows), tc.want)
			}
			if got := f.subagentRowsOf(other); !reflect.DeepEqual(got, otherBefore) {
				t.Errorf("another row's entries %+v -> %+v; want unchanged", otherBefore, got)
			}
		})
	}
}

// TestSubagentLaunchEvictionOrder checks the gated claimable-launch eviction,
// the gate passing as in TestSubagentEntryEvictionOrder: at 300 the one with the
// oldest opened_at goes, a tie to the lowest created_seq; below the cap
// none; another row's launches neither count nor go.
func TestSubagentLaunchEvictionOrder(t *testing.T) {
	type launch struct {
		id       string
		openedAt int64 // ms past the seed epoch
	}
	cases := []struct {
		name  string
		cands []launch
		total int
		want  string // the tool_use_id evicted; "" = nothing
	}{
		{"the oldest opened_at goes", []launch{{"new", 300}, {"old", 100}, {"mid", 200}}, 0, "old"},
		{"tie goes to the lowest created_seq", []launch{{"first", 100}, {"second", 100}}, 0, "first"},
		{"below the cap nothing is deleted", []launch{{"old", 10}}, store.SubagentCap - 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			id, other, gate := f.id, f.seed(store.StateEnded, "sess-lcap-b"), saGateVariants[0].gate(f, "SubagentStart")
			for i := 0; i < 5; i++ {
				if err := apitest.SeedSubagentLaunch(f.path, other, fmt.Sprintf("other-%d", i), apitest.WithClaimableOpenedAt(1)); err != nil {
					t.Fatalf("SeedSubagentLaunch(other): %v", err)
				}
			}
			total := tc.total
			if total == 0 {
				total = store.SubagentCap
			}
			for _, c := range tc.cands {
				if err := apitest.SeedSubagentLaunch(f.path, id, c.id, apitest.WithClaimableOpenedAt(apitest.SeededSubagentMs+c.openedAt)); err != nil {
					t.Fatalf("SeedSubagentLaunch(%s): %v", c.id, err)
				}
			}
			for i := len(tc.cands); i < total; i++ {
				if err := apitest.SeedSubagentLaunch(f.path, id, fmt.Sprintf("filler-%d", i), apitest.WithClaimableOpenedAt(capFillerMs+int64(i))); err != nil {
					t.Fatalf("SeedSubagentLaunch(filler): %v", err)
				}
			}
			before, otherBefore := f.subagentRowsOf(id), f.subagentRowsOf(other)

			evicted, err := store.EvictSubagentLaunchGatedForTest(f.s, id, gate)
			if err != nil || evicted != (tc.want != "") {
				t.Fatalf("evict = %v, %v; want evicted %v", evicted, err, tc.want != "")
			}
			var wantRows []apitest.SubagentLaunchColumns
			for _, l := range before.Launches {
				if l.ToolUseID != tc.want {
					wantRows = append(wantRows, l)
				}
			}
			if got := f.subagentRowsOf(id).Launches; !reflect.DeepEqual(got, wantRows) {
				t.Errorf("row holds %d claimable launches after the call; want the %d of before minus %q only", len(got), len(wantRows), tc.want)
			}
			if got := f.subagentRowsOf(other); !reflect.DeepEqual(got, otherBefore) {
				t.Errorf("another row's launches %+v -> %+v; want unchanged", otherBefore, got)
			}
		})
	}
}
