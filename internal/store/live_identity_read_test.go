package store_test

// Live-row read tests for find-missing's pending grace period (SR-11.2,
// SR-5.5, SR-22.8): ListLiveSpawnIdentities carries each row's state and
// launch start, and InsidePendingGrace judges them. Rows are seeded through
// apitest (SR-20.2) with the v5 fixtures in spawn_v5_read_test.go.

import (
	"math"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestListLiveSpawnIdentitiesStateAndLaunchStart checks one read returns every
// live row with its state and decoded launch start, even an unreadable one.
func TestListLiveSpawnIdentitiesStateAndLaunchStart(t *testing.T) {
	const startedAt = "2026-03-04 05:06:07"
	startedAtMillis := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).UnixMilli()

	type liveCase struct {
		name  string
		state string
		opts  []apitest.SpawnOption
		want  int64 // LaunchStartedAtMillis
	}
	cases := []liveCase{
		{"pending, default launch start is started_at", store.StatePending,
			[]apitest.SpawnOption{apitest.WithStartedAt(startedAt)}, startedAtMillis},
		{"pending, integer launch start", store.StatePending,
			[]apitest.SpawnOption{apitest.WithLaunchStartedAt(1767225600123)}, 1767225600123},
		{"pending, launch start NULL", store.StatePending,
			[]apitest.SpawnOption{apitest.WithNoLaunchStartedAt()}, 0},
		{"pending, launch start text", store.StatePending,
			[]apitest.SpawnOption{apitest.WithRawLaunchStartedAt("yesterday")}, 0},
		{"pending, launch start real", store.StatePending,
			[]apitest.SpawnOption{apitest.WithRawLaunchStartedAt(1767225600123.5)}, 0},
		{"pending, launch start blob", store.StatePending,
			[]apitest.SpawnOption{apitest.WithRawLaunchStartedAt([]byte("1767225600123"))}, 0},
		{"waiting, default", store.StateWaiting, nil, 0},
		{"working, default", store.StateWorking, nil, 0},
		{"ask_user, default", store.StateAskUser, nil, 0},
		{"check_permission, default", store.StateCheckPermission, nil, 0},
		// The read does not filter by state: a stored launch start on a
		// non-pending row reads back as stored.
		{"waiting, integer launch start", store.StateWaiting,
			[]apitest.SpawnOption{apitest.WithLaunchStartedAt(1767225600456)}, 1767225600456},
	}
	for _, b := range launchStartBoundaries {
		cases = append(cases, liveCase{"pending, launch start " + b.name, store.StatePending,
			[]apitest.SpawnOption{apitest.WithLaunchStartedAt(b.stored)}, b.want})
	}

	f := newV5Store(t)
	want := make(map[string]store.LiveSpawnIdentity, len(cases)+2)
	names := make(map[string]string, len(cases)+2)
	add := func(name, state string, w store.LiveSpawnIdentity, opts ...apitest.SpawnOption) {
		id := f.seed(state, "", opts...)
		w.ClaudeInstanceID, w.State = id, state
		want[id], names[id] = w, name
	}
	for _, tc := range cases {
		add(tc.name, tc.state, store.LiveSpawnIdentity{LaunchStartedAtMillis: tc.want}, tc.opts...)
	}
	// The process identity comes back alongside the new fields, even beside
	// an unreadable launch start.
	proc := []apitest.SpawnOption{apitest.WithPID(4242), apitest.WithProcStarttime(apitest.LinuxProcStarttime)}
	withProc := store.LiveSpawnIdentity{PID: 4242, ProcStarttime: apitest.LinuxProcStarttime}
	add("waiting, process identity recorded", store.StateWaiting, withProc, proc...)
	add("pending, text launch start, process identity recorded", store.StatePending, withProc,
		append(proc, apitest.WithRawLaunchStartedAt("soon"))...)
	// Terminal rows are never listed, whatever their launch start.
	terminal := map[string]bool{
		f.seed(store.StateEnded, "", apitest.WithLaunchStartedAt(1767225600123)):   true,
		f.seed(store.StateMissing, "", apitest.WithLaunchStartedAt(1767225600123)): true,
	}

	got, err := f.s.ListLiveSpawnIdentities()
	if err != nil {
		t.Fatalf("ListLiveSpawnIdentities: %v; no stored value may fail the read", err)
	}
	seen := make(map[string]bool, len(got))
	for _, it := range got {
		if terminal[it.ClaudeInstanceID] {
			t.Errorf("terminal row %s (%s) listed", it.ClaudeInstanceID, it.State)
			continue
		}
		w, ok := want[it.ClaudeInstanceID]
		if !ok {
			t.Errorf("unexpected row listed: %+v", it)
			continue
		}
		seen[it.ClaudeInstanceID] = true
		if it != w {
			t.Errorf("%s: got %+v; want %+v", names[it.ClaudeInstanceID], it, w)
		}
	}
	for id, name := range names {
		if !seen[id] {
			t.Errorf("%s: row %s not listed", name, id)
		}
	}
}

// TestInsidePendingGrace checks SR-11.2: only a pending row younger than the
// grace period is inside; an absent start is past, a future start is inside.
func TestInsidePendingGrace(t *testing.T) {
	now := time.UnixMilli(1767225600000)
	at := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	grace := time.Duration(config.DefaultPendingGraceSeconds) * time.Second
	minGrace := time.Duration(config.PendingGraceMinimumSeconds(
		config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs)) * time.Second
	cases := []struct {
		name  string
		state string
		start int64
		grace time.Duration
		want  bool
	}{
		{"default grace less 1 s", store.StatePending, at(-grace + time.Second), grace, true},
		{"1 ms short of default grace", store.StatePending, at(-grace + time.Millisecond), grace, true},
		{"exactly default grace old", store.StatePending, at(-grace), grace, false},
		{"default grace plus 1 s", store.StatePending, at(-grace - time.Second), grace, false},
		{"safe minimum grace less 1 s", store.StatePending, at(-minGrace + time.Second), minGrace, true},
		{"safe minimum grace plus 1 s", store.StatePending, at(-minGrace - time.Second), minGrace, false},
		{"started now", store.StatePending, at(0), grace, true},
		{"future launch start", store.StatePending, at(time.Hour), grace, true},
		{"last ms of year 9999", store.StatePending, lastInRangeLaunchMillis, grace, true},
		{"first ms of year 0", store.StatePending, firstInRangeLaunchMillis, grace, false},
		{"int64 min never overflows to young", store.StatePending, math.MinInt64, grace, false},
		{"absent launch start", store.StatePending, 0, grace, false},
		{"waiting, fresh start", store.StateWaiting, at(-time.Second), grace, false},
		{"working, fresh start", store.StateWorking, at(-time.Second), grace, false},
		{"ask_user, fresh start", store.StateAskUser, at(-time.Second), grace, false},
		{"check_permission, fresh start", store.StateCheckPermission, at(-time.Second), grace, false},
		{"missing, fresh start", store.StateMissing, at(-time.Second), grace, false},
		{"ended, fresh start", store.StateEnded, at(-time.Second), grace, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.InsidePendingGrace(tc.state, tc.start, tc.grace, now); got != tc.want {
				t.Errorf("InsidePendingGrace(%q, %d, %v, now) = %v; want %v",
					tc.state, tc.start, tc.grace, got, tc.want)
			}
		})
	}
}
