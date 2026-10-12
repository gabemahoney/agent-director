package store_test

// The 300 caps on real subagent hook writes (SR-10.3) and the 1 KiB bound on
// payload strings (SR-5.3; b.xjn). The eviction order and the helpers at the cap
// are in subagents_test.go; the gate at the cap is in subagent_gate_test.go.
// Fixture and helpers: subagent_transitions_test.go.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// saFillEntries and saFillLaunches seed the row to SubagentCap entries or
// claimable launches: the oldest is the victim (a closed observation, for an
// entry), the rest are running or newer. extra options apply to every row.
func saFillEntries(f *saStore, extra ...apitest.SubagentOption) {
	f.t.Helper()
	f.seedEntry("victim", append([]apitest.SubagentOption{apitest.WithSubagentConfirmed(false),
		apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentActivityAt(1)}, extra...)...)
	for i := 1; i < store.SubagentCap; i++ {
		f.seedEntry(fmt.Sprintf("filler-%d", i), append([]apitest.SubagentOption{apitest.WithSubagentActivityAt(capFillerMs + int64(i))}, extra...)...)
	}
}

func saFillLaunches(f *saStore, extra ...apitest.SubagentLaunchOption) {
	f.t.Helper()
	f.seedLaunch("toolu-victim", append([]apitest.SubagentLaunchOption{apitest.WithClaimableType("Other"), apitest.WithClaimableOpenedAt(1)}, extra...)...)
	for i := 1; i < store.SubagentCap; i++ {
		f.seedLaunch(fmt.Sprintf("toolu-filler-%d", i), append([]apitest.SubagentLaunchOption{apitest.WithClaimableType("Other"),
			apitest.WithClaimableOpenedAt(capFillerMs + int64(i))}, extra...)...)
	}
}

func (f *saStore) wantCounts(entries, launches int) {
	f.t.Helper()
	if r := f.rows(); len(r.Entries) != entries || len(r.Launches) != launches {
		f.t.Errorf("%d entries and %d claimable launches; want %d and %d", len(r.Entries), len(r.Launches), entries, launches)
	}
}

func (f *saStore) hasEntry(sub string) bool { _, ok := f.entry(sub); return ok }

func (f *saStore) hasLaunch(tool string) bool {
	for _, id := range saToolIDs(f.rows().Launches) {
		if id == tool {
			return true
		}
	}
	return false
}

// TestSubagentCapsOnRealWrites checks a write that creates an entry or opens a
// claimable launch on a full row evicts exactly one and keeps the row at 300,
// and one that creates nothing evicts nothing.
func TestSubagentCapsOnRealWrites(t *testing.T) {
	t.Run("a creating start", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		saFillEntries(f)
		f.start(store.SubagentLifecycleEvent{SubagentID: "sa-new", ReceivedMs: saE})
		f.wantCounts(store.SubagentCap, 0)
		if f.hasEntry("victim") || !f.hasEntry("sa-new") {
			t.Errorf("victim present %v, new present %v; want the closed observation evicted for the new entry", f.hasEntry("victim"), f.hasEntry("sa-new"))
		}
	})
	t.Run("a creating start that claims a launch", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		saFillEntries(f)
		f.seedLaunch("toolu-claim", apitest.WithClaimableType("Explore"))
		f.start(store.SubagentLifecycleEvent{SubagentID: "sa-new", Type: "Explore", ReceivedMs: saE})
		f.wantCounts(store.SubagentCap, 0)
		if f.hasEntry("victim") || f.mustEntry("sa-new").Confirmed != int64(1) {
			t.Errorf("want the victim evicted and the new entry confirmed")
		}
	})
	t.Run("creating launch details", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		saFillEntries(f)
		f.details(saDetailsEv(store.SubagentLaunchBackgroundStarted))
		f.wantCounts(store.SubagentCap, 0)
		if f.hasEntry("victim") || !f.hasEntry(saSub) {
			t.Errorf("want the victim evicted for the new record")
		}
	})
	t.Run("a launch request", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		saFillLaunches(f)
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-new", Type: "Explore", ReceivedMs: saE})
		f.wantCounts(0, store.SubagentCap)
		if f.hasLaunch("toolu-victim") || !f.hasLaunch("toolu-new") {
			t.Errorf("want the oldest launch evicted for the new one")
		}
	})
	t.Run("writes that create nothing evict nothing", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		saFillEntries(f)
		saFillLaunches(f)
		f.seedEntry("sa-known", apitest.WithSubagentToolUseID("toolu-known"))
		before := f.rows() // 301 entries: seeding bypasses the cap
		f.start(store.SubagentLifecycleEvent{SubagentID: "sa-known", ReceivedMs: saE})
		f.stop(store.SubagentLifecycleEvent{SubagentID: "sa-absent", ReceivedMs: saE})
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-known", Type: "Explore", ReceivedMs: saE})
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-victim", Type: "Explore", ReceivedMs: saE})
		d := saDetailsEv(store.SubagentLaunchOther)
		d.ToolUseID = ""
		f.details(d)
		after := f.rows()
		if len(after.Entries) != len(before.Entries) || len(after.Launches) != len(before.Launches) {
			t.Errorf("%d entries and %d launches; want %d and %d", len(after.Entries), len(after.Launches), len(before.Entries), len(before.Launches))
		}
		if !f.hasEntry("victim") {
			t.Errorf("entry evicted by a write that created nothing")
		}
		if !f.hasLaunch("toolu-victim") {
			t.Errorf("claimable launch evicted by a write that opened nothing")
		}
	})
}

// TestSubagentStringsAreCutTo1KiB checks a string longer than 1024 bytes is cut
// to a whole-character prefix of at most 1024 bytes before it is stored or
// matched, so a long id always finds what was stored from it.
func TestSubagentStringsAreCutTo1KiB(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		wantLen  int
	}{
		{"short", "abc", 3},
		{"exactly 1024 bytes", strings.Repeat("a", 1024), 1024},
		{"1025 bytes", strings.Repeat("a", 1025), 1024},
		{"ends inside a 3-byte character", strings.Repeat("€", 400), 1023},
		{"ends inside a 2-byte character", "a" + strings.Repeat("é", 600), 1023},
	} {
		got := store.CutSubagentString(tc.in)
		if len(got) != tc.wantLen || !strings.HasPrefix(tc.in, got) || strings.ToValidUTF8(got, "") != got {
			t.Errorf("%s: cut to %d bytes (valid %v); want %d", tc.name, len(got), strings.ToValidUTF8(got, "") == got, tc.wantLen)
		}
	}
	t.Run("through the store, a long id and a long tool_use_id still match", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		long := strings.Repeat("x", 2000)
		f.request(store.SubagentLaunchRequest{ToolUseID: long, Type: "Explore", ReceivedMs: saE})
		f.start(store.SubagentLifecycleEvent{SubagentID: long, Type: "Explore", ReceivedMs: saE})
		f.start(store.SubagentLifecycleEvent{SubagentID: long, ReceivedMs: saE})
		rows := f.rows()
		if len(rows.Entries) != 1 || len(rows.Entries[0].SubagentID.(string)) != 1024 || rows.Entries[0].Confirmed != int64(1) || len(rows.Launches) != 0 {
			t.Errorf("rows %+v; want one confirmed entry with a 1024-byte id and the launch claimed", rows)
		}
	})
}
