package store_test

// What a main-agent Stop's snapshot closes (SR-7.5; b.xjn): only a running
// background entry of the current launch that the snapshot omits and that was
// opened strictly more than 2 s before the Stop. The grace edges of the close are
// pinned here only. Fixture and helpers: subagent_helpers_test.go; the snapshot's
// creates and fills are in subagent_snapshot_test.go.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// saClosed is e as a snapshot at ms closes it.
func saClosed(e apitest.SubagentEntryColumns, ms int64) apitest.SubagentEntryColumns {
	e.State, e.CloseReason, e.LostAt, e.ActivityAt = store.SubagentStateLost, store.SubagentCloseAbsentFromSnapshot, ms, ms
	return e
}

func saRunMode(mode string) []apitest.SubagentOption {
	if mode == "" {
		return nil
	}
	return []apitest.SubagentOption{apitest.WithSubagentRunMode(mode)}
}

// TestSubagentSnapshotClosesOnlyStaleBackground checks run_mode x opened_at: an
// empty snapshot closes a background entry opened more than 2 s before the Stop
// and nothing else, 2 s exactly and a time after the Stop included.
func TestSubagentSnapshotClosesOnlyStaleBackground(t *testing.T) {
	for _, mode := range []string{store.SubagentRunModeBackground, store.SubagentRunModeForeground, ""} {
		for _, o := range []struct {
			name   string
			opened int64
			stale  bool
		}{
			{"exactly 2 s before", saE - store.SubagentGraceMs, false},
			{"2 s and 1 ms before", saE - store.SubagentGraceMs - 1, true},
			{"after the Stop", saE + 5000, false},
		} {
			name := mode
			if name == "" {
				name = "null"
			}
			t.Run(name+" run_mode/opened "+o.name, func(t *testing.T) {
				t.Parallel()
				f := newSAStore(t, store.StateWorking)
				f.seedEntry(saSub, append(saRunMode(mode), apitest.WithSubagentOpenedAt(o.opened), apitest.WithSubagentActivityAt(o.opened))...)
				before := f.mustEntry(saSub)

				f.snapStop(saE)

				want := before
				if o.stale && mode == store.SubagentRunModeBackground {
					want = saClosed(before, saE)
				}
				if got := f.mustEntry(saSub); got != want {
					t.Errorf("entry\n got %+v\nwant %+v", got, want)
				}
			})
		}
	}
}

// TestSubagentSnapshotCloseScope checks which entries one snapshot closes among
// several: listed ids, other states and other launches are kept; another
// session's and a nested subagent's entries close alike (N3).
func TestSubagentSnapshotCloseScope(t *testing.T) {
	bg := apitest.WithSubagentRunMode(store.SubagentRunModeBackground)
	stopped := []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent),
		apitest.WithSubagentStoppedAt(saAt(2)), apitest.WithSubagentStoppedAtSource(store.SubagentSourceStop)}
	lost := []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateLost), apitest.WithSubagentCloseReason(store.SubagentCloseAgentGone), apitest.WithSubagentLostAt(saAt(3))}
	entries := []struct {
		id     string
		opts   []apitest.SubagentOption
		closes bool
	}{
		{"closed-plain", []apitest.SubagentOption{bg}, true},
		{"closed-other-session", []apitest.SubagentOption{bg, apitest.WithSubagentSessionID("sess-other")}, true},
		{"closed-nested", []apitest.SubagentOption{bg, apitest.WithSubagentParent(store.SubagentParentSubagent, "sa-parent")}, true},
		{"closed-unconfirmed", []apitest.SubagentOption{bg, apitest.WithSubagentConfirmed(false)}, true},
		{"kept-listed", []apitest.SubagentOption{bg}, false},
		{"kept-stopped", append([]apitest.SubagentOption{bg}, stopped...), false},
		{"kept-lost", append([]apitest.SubagentOption{bg}, lost...), false},
		{"kept-earlier-launch", []apitest.SubagentOption{bg, apitest.WithSubagentLaunchToken("earlier-launch")}, false},
	}
	f := newSAStore(t, store.StateWorking)
	for _, e := range entries {
		f.seedEntry(e.id, e.opts...)
	}
	before := f.rows().Entries

	f.snapStop(saE, saL("kept-listed", ""), saL("not-an-entry", ""))

	for i, e := range entries {
		want := before[i]
		if e.closes {
			want = saClosed(want, saE)
		}
		if got := f.mustEntry(e.id); got != want {
			t.Errorf("%s\n got %+v\nwant %+v", e.id, got, want)
		}
	}
	if got := len(f.rows().Entries); got != len(entries)+1 {
		t.Errorf("%d entries; want %d: the listed unseen id is created", got, len(entries)+1)
	}
}

// TestSubagentSnapshotKeepsARecentlyReopenedEntry checks an entry a start
// reopened inside the grace stays running (SR-7.5).
func TestSubagentSnapshotKeepsARecentlyReopenedEntry(t *testing.T) {
	t.Parallel()
	f := newSAStore(t, store.StateWorking)
	f.seedEntry(saSub, saStoppedSeed(store.SubagentSourceStop, apitest.WithSubagentRunMode(store.SubagentRunModeBackground))...)
	ev := saStartEv()
	ev.ReceivedMs = saE - 1000
	f.start(ev)

	f.snapStop(saE)

	if e := f.mustEntry(saSub); e.State != store.SubagentStateRunning || e.CloseReason != nil || e.LostAt != nil {
		t.Errorf("entry %+v; want running", e)
	}
}
