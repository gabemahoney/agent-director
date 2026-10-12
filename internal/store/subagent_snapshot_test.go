package store_test

// What a main-agent Stop's snapshot and a SubagentStop's listing create, confirm
// and fill (SR-7.4, SR-7.6), the dedupe of a listing (SR-5.3) and the snapshot's
// precedence against the other sources of a field (SR-6.3; b.xjn). The snapshot's
// close and its grace edges are in subagent_snapshot_close_test.go, the skip read
// in subagent_launches_test.go, the gate in subagent_gate_test.go, order
// sequences in subagent_order_test.go. Fixture and helpers:
// subagent_helpers_test.go.

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const saSnapSess = "sess-snap" // the session id of a snapshot's Stop

func saL(id, typ string) store.SubagentListed { return store.SubagentListed{ID: id, Type: typ} }

// snapStop applies a main-agent Stop at ms whose background_tasks lists listed
// (none listed is an empty array: a snapshot that can close).
func (f *saStore) snapStop(ms int64, listed ...store.SubagentListed) {
	f.t.Helper()
	snap := &store.SubagentSnapshot{SessionID: saSnapSess, Listed: listed}
	f.applied(storefix.ApplySubagentMainAgentStop(f.t, f.s, f.id, storefix.MainAgentStop{ReceivedMs: ms, Snapshot: snap}))
}

// listStop applies the SubagentStop of stopper at ms whose background_tasks lists listed.
func (f *saStore) listStop(stopper string, ms int64, listed ...store.SubagentListed) {
	f.t.Helper()
	f.stop(store.SubagentLifecycleEvent{SubagentID: stopper, ReceivedMs: ms, Listing: listed})
}

// saListSources are the two ways a listing reaches the store; stopper is the
// subagent whose SubagentStop carries it (it has no entry).
var saListSources = []struct {
	name  string
	apply func(f *saStore, ms int64, listed ...store.SubagentListed)
}{
	{"main-agent Stop", func(f *saStore, ms int64, l ...store.SubagentListed) { f.snapStop(ms, l...) }},
	{"SubagentStop", func(f *saStore, ms int64, l ...store.SubagentListed) { f.listStop("sa-stopper", ms, l...) }},
}

func TestSubagentSnapshotCreates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		listed   store.SubagentListed
		session  string
		wantType any
		wantSrc  any
		wantSess any
	}{
		{"a typed listing", saL(saSub, "Explore"), saSnapSess, "Explore", store.SubagentSourceSnapshot, saSnapSess},
		{"a listing with no type", saL(saSub, ""), saSnapSess, nil, nil, saSnapSess},
		{"a Stop with no session id", saL(saSub, "Explore"), "", "Explore", store.SubagentSourceSnapshot, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWorking)
			snap := &store.SubagentSnapshot{SessionID: tc.session, Listed: []store.SubagentListed{tc.listed}}
			f.applied(storefix.ApplySubagentMainAgentStop(t, f.s, f.id, storefix.MainAgentStop{ReceivedMs: saE, Snapshot: snap}))

			got := f.mustEntry(saSub)
			want := apitest.SubagentEntryColumns{CreatedSeq: got.CreatedSeq, InstanceID: f.id, SubagentID: saSub, LaunchToken: f.token(),
				Confirmed: int64(1), SessionID: tc.wantSess, SubagentType: tc.wantType, RunMode: store.SubagentRunModeBackground,
				State: store.SubagentStateRunning, OpenedAt: saE, ActivityAt: saE,
				SubagentTypeSource: tc.wantSrc, RunModeSource: store.SubagentSourceSnapshot}
			if got != want {
				t.Errorf("entry\n got %+v\nwant %+v", got, want)
			}
			if n := len(f.rows().Entries); n != 1 {
				t.Errorf("%d entries; want 1", n)
			}
		})
	}
}

// saListPrior is an entry a listing meets; changes says whether the listing has
// anything to change on it (so activity_at moves).
type saListPrior struct {
	name    string
	opts    []apitest.SubagentOption
	changes bool
}

// saListPriors are the entries of SR-7.4/7.6's cells: an unconfirmed running
// observation, running records without and with a run_mode, and stopped and lost
// entries of the current and of an earlier launch, unconfirmed and confirmed.
func saListPriors() []saListPrior {
	base := []apitest.SubagentOption{apitest.WithSubagentSessionID("sess-seed"), apitest.WithSubagentFirstStartedAt(saAt(1)),
		apitest.WithSubagentStartedAt(saAt(1)), apitest.WithSubagentOpenedAt(saAt(1)), apitest.WithSubagentActivityAt(saAt(1))}
	full := append([]apitest.SubagentOption{apitest.WithSubagentType("seed-type"), apitest.WithSubagentTypeSource(store.SubagentSourceStart),
		apitest.WithSubagentDescription("seed description")}, base...)
	opts := func(groups ...[]apitest.SubagentOption) []apitest.SubagentOption {
		var all []apitest.SubagentOption
		for _, g := range groups {
			all = append(all, g...)
		}
		return all
	}
	one := func(o apitest.SubagentOption) []apitest.SubagentOption { return []apitest.SubagentOption{o} }
	bg := []apitest.SubagentOption{apitest.WithSubagentRunMode(store.SubagentRunModeBackground), apitest.WithSubagentRunModeSource(store.SubagentSourceDetails)}
	fg := []apitest.SubagentOption{apitest.WithSubagentRunMode(store.SubagentRunModeForeground), apitest.WithSubagentRunModeSource(store.SubagentSourceDetails)}
	unconf := one(apitest.WithSubagentConfirmed(false))
	earlier := one(apitest.WithSubagentLaunchToken("earlier-launch"))
	stopped := []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent),
		apitest.WithSubagentStoppedAt(saAt(2)), apitest.WithSubagentStoppedAtSource(store.SubagentSourceStop), apitest.WithSubagentActivityAt(saAt(2))}
	lost := []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateLost), apitest.WithSubagentCloseReason(store.SubagentCloseAgentGone),
		apitest.WithSubagentLostAt(saAt(3)), apitest.WithSubagentActivityAt(saAt(3))}

	priors := []saListPrior{
		{"an unconfirmed running observation", opts(base, unconf), true},
		{"a running record with no run_mode", opts(full), true},
		{"a confirmed foreground record of the current launch", opts(full, fg), false},
		{"a foreground record of an earlier launch", opts(full, fg, earlier), true},
		{"a stopped record with no run_mode", opts(full, stopped), true},
	}
	for _, st := range []struct {
		name string
		opts []apitest.SubagentOption
	}{{"stopped", stopped}, {"lost", lost}} {
		for _, l := range []struct {
			name string
			opts []apitest.SubagentOption
			move bool
		}{{"current launch", nil, false}, {"earlier launch", earlier, true}} {
			for _, c := range []struct {
				name string
				opts []apitest.SubagentOption
				conf bool
			}{{"unconfirmed", unconf, false}, {"confirmed", nil, true}} {
				priors = append(priors, saListPrior{st.name + ", " + l.name + ", " + c.name, opts(full, bg, st.opts, l.opts, c.opts), l.move || !c.conf})
			}
		}
	}
	return priors
}

// TestSubagentListingConfirmsMovesAndFills checks a listed id that has an entry:
// the entry is confirmed, moved to the current launch and, with no run_mode,
// given background; its type, description, state and times never change, so a
// stopped or lost entry is not reopened, and activity_at moves only when
// something changed (SR-7.4, SR-7.6, SR-6.3).
func TestSubagentListingConfirmsMovesAndFills(t *testing.T) {
	for _, src := range saListSources {
		for _, p := range saListPriors() {
			t.Run(src.name+"/"+p.name, func(t *testing.T) {
				t.Parallel()
				f := newSAStore(t, store.StateWorking)
				f.seedEntry(saSub, p.opts...)
				before := f.mustEntry(saSub)

				src.apply(f, saE, saL(saSub, "listing-type"))

				want := before
				want.Confirmed, want.LaunchToken = int64(1), f.token()
				if before.RunMode == nil {
					want.RunMode, want.RunModeSource = store.SubagentRunModeBackground, store.SubagentSourceSnapshot
				}
				if p.changes {
					want.ActivityAt = saE
				}
				if got := f.mustEntry(saSub); got != want {
					t.Errorf("entry\n got %+v\nwant %+v", got, want)
				}
			})
		}
	}
}

// TestSubagentStopListingRules checks what a SubagentStop's listing never does
// (SR-7.6) and that it applies whatever the stop did (SR-4.4).
func TestSubagentStopListingRules(t *testing.T) {
	t.Run("it never creates an entry for an unseen id", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.listStop("sa-stopper", saE, saL("sa-new", "Explore"))
		f.wantCounts(0, 0)
	})
	t.Run("it never closes an omitted running background entry", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedEntry("sa-bg", apitest.WithSubagentRunMode(store.SubagentRunModeBackground))
		f.seedEntry("sa-other")
		before := f.mustEntry("sa-bg")
		f.listStop("sa-stopper", saE, saL("sa-other", ""))
		if got := f.mustEntry("sa-bg"); got != before {
			t.Errorf("entry %+v; want %+v unchanged", got, before)
		}
	})
	t.Run("a stop of an earlier launch's entry leaves it alone and applies the listing to another id", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedEntry("sa-stopper", apitest.WithSubagentLaunchToken("earlier-launch"))
		f.seedEntry("sa-other", apitest.WithSubagentConfirmed(false))
		before := f.mustEntry("sa-stopper")
		f.listStop("sa-stopper", saE, saL("sa-other", ""))
		if got := f.mustEntry("sa-stopper"); got != before {
			t.Errorf("stopping entry %+v; want %+v unchanged", got, before)
		}
		if got := f.mustEntry("sa-other"); got.Confirmed != int64(1) || got.RunMode != store.SubagentRunModeBackground {
			t.Errorf("listed entry %+v; want it confirmed with run_mode background", got)
		}
	})
	t.Run("a stop of an earlier launch's entry whose listing names it confirms and moves it, not stopped", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedEntry("sa-stopper", apitest.WithSubagentLaunchToken("earlier-launch"), apitest.WithSubagentConfirmed(false), apitest.WithSubagentStartedAt(saAt(1)))
		before := f.mustEntry("sa-stopper")
		f.listStop("sa-stopper", saE, saL("sa-stopper", ""))
		want := before
		want.Confirmed, want.LaunchToken, want.RunMode, want.RunModeSource, want.ActivityAt =
			int64(1), f.token(), store.SubagentRunModeBackground, store.SubagentSourceSnapshot, saE
		if got := f.mustEntry("sa-stopper"); got != want {
			t.Errorf("entry\n got %+v\nwant %+v", got, want)
		}
	})
	t.Run("a stop with no subagent id applies no listing", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedEntry(saSub, apitest.WithSubagentConfirmed(false))
		before := f.rows()
		f.listStop("", saE, saL(saSub, "Explore"))
		if got := f.rows(); len(got.Entries) != 1 || got.Entries[0] != before.Entries[0] {
			t.Errorf("rows %+v; want %+v unchanged", got, before)
		}
	})
}

// TestSubagentListingDedupe checks an id repeated in one array, after the 1 KiB
// cut, gives one record typed by the first and the transaction commits (a stale
// claimable launch closed by the same Stop is gone).
func TestSubagentListingDedupe(t *testing.T) {
	long := strings.Repeat("x", 1024)
	for _, tc := range []struct {
		name   string
		listed []store.SubagentListed
		wantID string
		want   string // the type of the one record
	}{
		{"one id twice with different types", []store.SubagentListed{saL(saSub, "first-type"), saL(saSub, "second-type")}, saSub, "first-type"},
		{"two long ids that differ past 1024 bytes", []store.SubagentListed{saL(long+"A", "first-type"), saL(long+"B", "second-type")}, long, "first-type"},
		{"an empty id is dropped", []store.SubagentListed{saL("", "empty-type"), saL(saSub, "first-type")}, saSub, "first-type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWorking)
			f.seedLaunch("toolu-stale", apitest.WithClaimableOpenedAt(saAt(1)))

			f.snapStop(saE, tc.listed...)

			rows := f.rows()
			if len(rows.Entries) != 1 || rows.Entries[0].SubagentID != tc.wantID || rows.Entries[0].SubagentType != tc.want {
				t.Errorf("entries %+v; want one with id of %d bytes and type %q", rows.Entries, len(tc.wantID), tc.want)
			}
			f.wantLaunches()
		})
	}
}

// TestSubagentListingPrecedence checks the snapshot source ranks below the start
// and the launch result for subagent_type and below the launch result for
// run_mode, in both orders, and that a listing never sets a description or
// retypes an entry it did not create (SR-6.3, N13).
func TestSubagentListingPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  store.SubagentLaunchOutcome
		wantMode string
	}{
		{"background", store.SubagentLaunchBackgroundStarted, store.SubagentRunModeBackground},
		{"foreground", store.SubagentLaunchForegroundFinished, store.SubagentRunModeForeground},
	} {
		for _, listingFirst := range []bool{true, false} {
			order := "the launch result, then the listing"
			if listingFirst {
				order = "the listing, then the launch result"
			}
			t.Run(tc.name+" result: "+order, func(t *testing.T) {
				t.Parallel()
				f := newSAStore(t, store.StateWorking)
				if listingFirst {
					f.snapStop(saAt(5), saL(saSub, "listing-type"))
					if e := f.mustEntry(saSub); e.SubagentType != "listing-type" || e.Description != nil {
						t.Fatalf("listing-created entry %+v; want its type and a null description", e)
					}
				}
				d := saDetailsEv(tc.outcome)
				d.ReceivedMs = saAt(6)
				f.details(d)
				if !listingFirst {
					f.snapStop(saAt(7), saL(saSub, "listing-type"))
				}

				e := f.mustEntry(saSub)
				if e.SubagentType != "details-type" || e.SubagentTypeSource != store.SubagentSourceDetails ||
					e.RunMode != tc.wantMode || e.RunModeSource != store.SubagentSourceDetails || e.Description != "event description" {
					t.Errorf("entry %+v; want the launch result's type, run_mode %s and description, each from details", e, tc.wantMode)
				}
			})
		}
	}
	t.Run("a later start replaces a listing's type", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.snapStop(saAt(5), saL(saSub, "listing-type"))
		ev := saStartEv()
		ev.ReceivedMs = saAt(6)
		f.start(ev)
		if e := f.mustEntry(saSub); e.SubagentType != "start-type" || e.SubagentTypeSource != store.SubagentSourceStart {
			t.Errorf("type %v from %v; want start-type from start", e.SubagentType, e.SubagentTypeSource)
		}
	})
	t.Run("a second Stop listing another type changes nothing", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.snapStop(saAt(5), saL(saSub, "first-type"))
		before := f.mustEntry(saSub)
		f.snapStop(saAt(6), saL(saSub, "second-type"))
		if got := f.mustEntry(saSub); got != before {
			t.Errorf("entry %+v; want %+v unchanged, activity_at included", got, before)
		}
	})
}
