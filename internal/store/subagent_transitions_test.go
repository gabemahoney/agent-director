package store_test

// The subagent entry transitions of SR-7.1 to SR-7.3 and the field rules of
// SR-6.3 and SR-6.5 (b.xjn), through the store's gated writes: the six events
// (start, stop, a stop that supersedes a blocked one, and the three launch
// results) against every prior state an entry can be in, the confirmation
// rules, source precedence and the time invariants. Repeats and arrival order
// are in subagent_order_test.go; claimable launches and the gate in
// subagent_launches_test.go and subagent_gate_test.go. The saStore fixture and
// the saEvents table are in subagent_helpers_test.go, shared by those files.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// saDelta says how an entry reads after an event, from how it read before.
type saDelta func(f *saStore, e *apitest.SubagentEntryColumns)

func saNone(*saStore, *apitest.SubagentEntryColumns) {}

// saRestart: stopped or lost becomes running again (SR-7.1).
func saRestart(_ *saStore, e *apitest.SubagentEntryColumns) {
	e.State, e.CloseReason, e.LostAt = store.SubagentStateRunning, nil, nil
	e.StartedAt, e.OpenedAt, e.ActivityAt = saE, saE, saE
}

// saStopped: running or lost becomes stopped, its stopped_at from source.
func saStopped(source string) saDelta {
	return func(_ *saStore, e *apitest.SubagentEntryColumns) {
		e.State, e.CloseReason, e.LostAt = store.SubagentStateStopped, store.SubagentCloseStopEvent, nil
		e.StoppedAt, e.StoppedAtSource, e.ActivityAt = saE, source, saE
	}
}

// saRestamp: a stopped entry's stopped_at is replaced.
func saRestamp(_ *saStore, e *apitest.SubagentEntryColumns) {
	e.StoppedAt, e.StoppedAtSource, e.ActivityAt = saE, store.SubagentSourceStop, saE
}

// saMoved: the entry joins the row's current launch.
func saMoved(f *saStore, e *apitest.SubagentEntryColumns) {
	e.LaunchToken, e.ActivityAt = f.token(), saE
}

func saThen(first, second saDelta) saDelta {
	return func(f *saStore, e *apitest.SubagentEntryColumns) { first(f, e); second(f, e) }
}

// TestSubagentTransitions applies each event to an entry in each prior state
// (SR-7.1 to SR-7.3, SR-6.5): the whole entry is compared, so nothing but the
// delta changes, and an event that changes nothing leaves activity_at alone.
func TestSubagentTransitions(t *testing.T) {
	stopSrc, detSrc, stop, restamp := store.SubagentSourceStop, store.SubagentSourceDetails, saStopped(store.SubagentSourceStop), saDelta(saRestamp)
	priors := []struct {
		name   string
		opts   []apitest.SubagentOption
		deltas [6]saDelta // per saEvents
	}{
		{"running", saSaturated(), [6]saDelta{saNone, stop, stop, saNone, saStopped(detSrc), saNone}},
		{"stopped by a stop", saStoppedSeed(stopSrc), [6]saDelta{saRestart, saNone, restamp, saNone, saNone, saNone}},
		{"stopped by a launch result", saStoppedSeed(detSrc), [6]saDelta{saRestart, restamp, restamp, saNone, saNone, saNone}},
		{"lost", saSaturated(apitest.WithSubagentState(store.SubagentStateLost), apitest.WithSubagentCloseReason(store.SubagentCloseAgentGone),
			apitest.WithSubagentLostAt(saAt(3)), apitest.WithSubagentActivityAt(saAt(3))),
			[6]saDelta{saRestart, stop, stop, saNone, saStopped(detSrc), saNone}},
		{"stopped, of an earlier launch", saStoppedSeed(stopSrc, apitest.WithSubagentLaunchToken("earlier-launch")),
			[6]saDelta{saThen(saRestart, saMoved), saNone, saNone, saMoved, saMoved, saMoved}},
	}
	for _, p := range priors {
		for i, ev := range saEvents {
			t.Run(p.name+"/"+ev.name, func(t *testing.T) {
				t.Parallel()
				f := newSAStore(t, store.StateWaiting)
				f.seedEntry(saSub, p.opts...)
				before := f.mustEntry(saSub)
				want := before
				p.deltas[i](f, &want)

				ev.apply(f)

				got := f.mustEntry(saSub)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("entry after the event\n got %+v\nwant %+v\nfrom %+v", got, want, before)
				}
				saCheckInvariants(t, got)
				if n := len(f.rows().Entries); n != 1 {
					t.Errorf("row holds %d entries; want 1", n)
				}
			})
		}
	}
}

// TestSubagentTransitionsFromNoEntry applies each event to an id with no entry:
// a start opens an observation, a result that started or finished the subagent
// opens a record, and a stop or any other result opens nothing (SR-7.1 to SR-7.3).
func TestSubagentTransitionsFromNoEntry(t *testing.T) {
	str := store.SubagentStateRunning
	record := func(f *saStore, e *apitest.SubagentEntryColumns) {
		e.Confirmed, e.SessionID = int64(1), "sess-ev"
		e.SubagentType, e.SubagentTypeSource = "details-type", store.SubagentSourceDetails
		e.Description, e.Model, e.ToolUseID = "event description", "event-model", "toolu-ev"
		e.ParentType, e.ParentID = store.SubagentParentAgent, saAgentID
	}
	cases := []struct {
		ev   int
		want func(f *saStore, e *apitest.SubagentEntryColumns) // nil = no entry
	}{
		{0, func(f *saStore, e *apitest.SubagentEntryColumns) {
			e.Confirmed, e.SessionID, e.SubagentType, e.SubagentTypeSource = int64(0), "sess-ev", "start-type", store.SubagentSourceStart
			e.FirstStartedAt, e.StartedAt = saE, saE
		}},
		{1, nil},
		{2, nil},
		{3, func(f *saStore, e *apitest.SubagentEntryColumns) {
			record(f, e)
			e.RunMode, e.RunModeSource, e.FirstStartedAt, e.StartedAt = store.SubagentRunModeBackground, store.SubagentSourceDetails, saE, saE
		}},
		{4, func(f *saStore, e *apitest.SubagentEntryColumns) {
			record(f, e)
			e.RunMode, e.RunModeSource = store.SubagentRunModeForeground, store.SubagentSourceDetails
			e.State, e.CloseReason, e.StoppedAt, e.StoppedAtSource = store.SubagentStateStopped, store.SubagentCloseStopEvent, saE, store.SubagentSourceDetails
		}},
		{5, nil},
	}
	for _, earlier := range []bool{false, true} {
		for _, tc := range cases {
			ev := saEvents[tc.ev]
			t.Run(ev.name+fmt.Sprintf("/earlier-only=%v", earlier), func(t *testing.T) {
				t.Parallel()
				f := newSAStore(t, store.StateWaiting, apitest.WithLifeNumber(4))
				var hidden apitest.SubagentEntryColumns
				if earlier {
					f.seedEntry(saSub, saStoppedSeed(store.SubagentSourceStop, apitest.WithSubagentLife(3))...)
					hidden, _ = f.entryInLife(saSub, 3)
				}
				ev.apply(f)
				if earlier {
					got, ok := f.entryInLife(saSub, 3)
					if !ok || !reflect.DeepEqual(got, hidden) {
						t.Fatalf("hidden entry changed: %+v -> %+v", hidden, got)
					}
				}

				entries := []apitest.SubagentEntryColumns{}
				for _, e := range f.rows().Entries {
					if e.LifeNumber == 4 {
						entries = append(entries, e)
					}
				}
				if tc.want == nil {
					if len(entries) != 0 {
						t.Fatalf("entries %+v; want none", entries)
					}
					return
				}
				want := apitest.SubagentEntryColumns{LifeNumber: 4, InstanceID: f.id, SubagentID: saSub, LaunchToken: f.token(), State: str,
					OpenedAt: saE, ActivityAt: saE}
				tc.want(f, &want)
				if len(entries) != 1 {
					t.Fatalf("entries %+v; want one", entries)
				}
				want.CreatedSeq = entries[0].CreatedSeq
				if !reflect.DeepEqual(entries[0], want) {
					t.Errorf("new entry\n got %+v\nwant %+v", entries[0], want)
				}
				saCheckInvariants(t, entries[0])
			})
		}
	}
}

// TestSubagentConfirmation checks which events make an observation a record and
// that a start never claims for an entry that exists (SR-4.2, SR-4.5).
func TestSubagentConfirmation(t *testing.T) {
	observation := func() []apitest.SubagentOption {
		return saSaturated(apitest.WithSubagentConfirmed(false))
	}
	cases := []struct {
		name          string
		apply         func(f *saStore)
		wantConfirmed int64
	}{
		{"a launch result confirms it", func(f *saStore) { f.details(saDetailsEv(store.SubagentLaunchOther)) }, 1},
		{"a background result confirms it", func(f *saStore) { f.details(saDetailsEv(store.SubagentLaunchBackgroundStarted)) }, 1},
		{"a start does not, even with a claimable launch of its type open", func(f *saStore) {
			f.seedLaunch("toolu-open", apitest.WithClaimableType("start-type"))
			f.start(saStartEv())
		}, 0},
		{"a stop does not", func(f *saStore) { f.stop(saStopEv(false)) }, 0},
		{"a main-agent Stop does not", func(f *saStore) { f.mainStop(saE) }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			f.seedEntry(saSub, observation()...)
			tc.apply(f)
			if got := f.mustEntry(saSub).Confirmed; got != tc.wantConfirmed {
				t.Errorf("confirmed = %v; want %d", got, tc.wantConfirmed)
			}
		})
	}
	t.Run("a start for an existing entry leaves the claimable launch open", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub, observation()...)
		f.seedLaunch("toolu-open", apitest.WithClaimableType("start-type"))
		f.start(saStartEv())
		if l := f.rows().Launches; len(l) != 1 || l[0].ToolUseID != "toolu-open" {
			t.Errorf("claimable launches %+v; want toolu-open still open", l)
		}
	})
}

// TestSubagentFieldPrecedence checks the sources of SR-6.3: a higher source
// replaces a lower, a lower fills only an unknown value, a source never replaces
// its own value, a field with one source keeps its first value, and an
// unsupplied value changes nothing.
func TestSubagentFieldPrecedence(t *testing.T) {
	start, details, snapshot := store.SubagentSourceStart, store.SubagentSourceDetails, store.SubagentSourceSnapshot
	typ := func(v, src string) []apitest.SubagentOption {
		if v == "" {
			return nil
		}
		return []apitest.SubagentOption{apitest.WithSubagentType(v), apitest.WithSubagentTypeSource(src)}
	}
	for _, tc := range []struct {
		name      string
		seed      []apitest.SubagentOption
		applyType func(f *saStore, v string)
		sentType  string
		wantType  string
		wantSrc   string
	}{
		{"details fills an unknown type", typ("", ""), saDetailsType, "d", "d", details},
		{"start fills an unknown type", typ("", ""), saStartType, "s", "s", start},
		{"start replaces a details type", typ("d", details), saStartType, "s", "s", start},
		{"start replaces a snapshot type", typ("p", snapshot), saStartType, "s", "s", start},
		{"details replaces a snapshot type", typ("p", snapshot), saDetailsType, "d", "d", details},
		{"details does not replace a start type", typ("s", start), saDetailsType, "d", "s", start},
		{"start does not replace its own type", typ("s1", start), saStartType, "s2", "s1", start},
		{"details does not replace its own type", typ("d1", details), saDetailsType, "d2", "d1", details},
		{"an unsupplied type changes nothing (start)", typ("d", details), saStartType, "", "d", details},
		{"an unsupplied type changes nothing (details)", typ("s", start), saDetailsType, "", "s", start},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			f.seedEntry(saSub, tc.seed...)
			tc.applyType(f, tc.sentType)
			e := f.mustEntry(saSub)
			if e.SubagentType != nilIfEmpty(tc.wantType) || e.SubagentTypeSource != nilIfEmpty(tc.wantSrc) {
				t.Errorf("type %v from %v; want %q from %q", e.SubagentType, e.SubagentTypeSource, tc.wantType, tc.wantSrc)
			}
		})
	}

	t.Run("run mode: details fills unknown, replaces a snapshot's, keeps its own", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name              string
			seed              []apitest.SubagentOption
			outcome           store.SubagentLaunchOutcome
			wantMode, wantSrc any
		}{
			{"unknown", nil, store.SubagentLaunchBackgroundStarted, "background", details},
			{"snapshot's", []apitest.SubagentOption{apitest.WithSubagentRunMode("background"), apitest.WithSubagentRunModeSource(snapshot)},
				store.SubagentLaunchForegroundFinished, "foreground", details},
			{"its own", []apitest.SubagentOption{apitest.WithSubagentRunMode("background"), apitest.WithSubagentRunModeSource(details)},
				store.SubagentLaunchForegroundFinished, "background", details},
			{"other result sets none", nil, store.SubagentLaunchOther, nil, nil},
		} {
			f := newSAStore(t, store.StateWaiting)
			f.seedEntry(saSub, tc.seed...)
			f.details(saDetailsEv(tc.outcome))
			if e := f.mustEntry(saSub); e.RunMode != tc.wantMode || e.RunModeSource != tc.wantSrc {
				t.Errorf("%s: run_mode %v from %v; want %v from %v", tc.name, e.RunMode, e.RunModeSource, tc.wantMode, tc.wantSrc)
			}
		}
	})

	t.Run("description, model, tool_use_id and parent keep their first value", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub, apitest.WithSubagentDescription("first"), apitest.WithSubagentModel("first-model"),
			apitest.WithSubagentToolUseID("toolu-first"), apitest.WithSubagentParent(store.SubagentParentSubagent, "first-parent"))
		d := saDetailsEv(store.SubagentLaunchBackgroundStarted)
		d.LauncherID = "other-parent"
		f.details(d)
		e := f.mustEntry(saSub)
		if e.Description != "first" || e.Model != "first-model" || e.ToolUseID != "toolu-first" ||
			e.ParentType != store.SubagentParentSubagent || e.ParentID != "first-parent" {
			t.Errorf("fields %+v; want the seeded values kept", e)
		}
	})

	t.Run("a start and a stop fill no payload field", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.start(saStartEv())
		f.stop(saStopEv(false))
		e := f.mustEntry(saSub)
		if e.Description != nil || e.Model != nil || e.ParentType != nil || e.ParentID != nil || e.ToolUseID != nil || e.RunMode != nil {
			t.Errorf("entry %+v; want description, model, parent, tool_use_id and run_mode null", e)
		}
	})

	t.Run("a launching subagent is the parent of what it launches", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		d := saDetailsEv(store.SubagentLaunchBackgroundStarted)
		d.LauncherID = "launcher-sub"
		f.details(d)
		if e := f.mustEntry(saSub); e.ParentType != store.SubagentParentSubagent || e.ParentID != "launcher-sub" {
			t.Errorf("parent %v %v; want subagent launcher-sub", e.ParentType, e.ParentID)
		}
	})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func saStartType(f *saStore, v string) {
	f.t.Helper()
	ev := saStartEv()
	ev.Type = v
	f.start(ev)
}

func saDetailsType(f *saStore, v string) {
	f.t.Helper()
	d := saDetailsEv(store.SubagentLaunchOther)
	d.Type = v
	f.details(d)
}

// TestSubagentSessionIDIsTheCreatingEvents checks session_id is recorded by the
// event that creates the entry and never changed (SR-6.3).
func TestSubagentSessionIDIsTheCreatingEvents(t *testing.T) {
	t.Parallel()
	f := newSAStore(t, store.StateWaiting)
	first := saStartEv()
	first.SessionID = "sess-first"
	f.start(first)
	later := saStartEv()
	later.SessionID = "sess-later"
	f.start(later)
	d := saDetailsEv(store.SubagentLaunchBackgroundStarted)
	d.SessionID = "sess-details"
	f.details(d)
	if got := f.mustEntry(saSub).SessionID; got != "sess-first" {
		t.Errorf("session_id = %v; want sess-first", got)
	}
}

// TestSubagentTimeInvariants checks the times of SR-6.5 and SR-7 that a table
// cell does not reach: a start earlier than first_started_at lowers it, and a
// start or a background result fills the null start times of an entry a
// snapshot created.
func TestSubagentTimeInvariants(t *testing.T) {
	t.Run("an earlier start lowers first_started_at and leaves started_at", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub, apitest.WithSubagentFirstStartedAt(saAt(5)), apitest.WithSubagentStartedAt(saAt(5)))
		ev := saStartEv()
		ev.ReceivedMs = saAt(3)
		f.start(ev)
		if e := f.mustEntry(saSub); e.FirstStartedAt != saAt(3) || e.StartedAt != saAt(5) || e.ActivityAt != saAt(3) {
			t.Errorf("first %v started %v activity %v; want %d, %d, %d", e.FirstStartedAt, e.StartedAt, e.ActivityAt, saAt(3), saAt(5), saAt(3))
		}
	})
	t.Run("a later start changes nothing of a running entry", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub, apitest.WithSubagentFirstStartedAt(saAt(5)), apitest.WithSubagentStartedAt(saAt(5)))
		before := f.mustEntry(saSub)
		f.start(saStartEv())
		if got := f.mustEntry(saSub); got.FirstStartedAt != before.FirstStartedAt || got.StartedAt != before.StartedAt {
			t.Errorf("first %v started %v; want %v, %v", got.FirstStartedAt, got.StartedAt, before.FirstStartedAt, before.StartedAt)
		}
	})
	for _, tc := range []struct {
		name  string
		apply func(f *saStore)
	}{
		{"a start", func(f *saStore) { f.start(saStartEv()) }},
		{"a background result", func(f *saStore) { f.details(saDetailsEv(store.SubagentLaunchBackgroundStarted)) }},
	} {
		t.Run(tc.name+" fills the null start times of a running entry", func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			f.seedEntry(saSub, apitest.WithSubagentConfirmed(true), apitest.WithSubagentRunMode("background"), apitest.WithSubagentRunModeSource(store.SubagentSourceSnapshot))
			tc.apply(f)
			e := f.mustEntry(saSub)
			if e.FirstStartedAt != saE || e.StartedAt != saE || e.OpenedAt != apitest.SeededSubagentMs {
				t.Errorf("first %v started %v opened %v; want %d, %d and the seeded opened_at", e.FirstStartedAt, e.StartedAt, e.OpenedAt, saE, saE)
			}
			saCheckInvariants(t, e)
		})
	}
}
