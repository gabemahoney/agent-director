package store_test

// Live-row reads (SR-11.2, SR-11.7, SR-5.5, SR-22.8): ListLiveSpawnIdentities
// and InsidePendingGrace. Rows are seeded through apitest (SR-20.2).

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestListLiveSpawnIdentities checks one read returns every live row and no
// finished one, each with its state, decoded launch start, name, process
// identity, liveness note, snapshot and launch identity as seeded (NULL as the
// zero value), whatever else the row holds.
func TestListLiveSpawnIdentities(t *testing.T) {
	const startedAt, note = "2026-03-04 05:06:07", "tmux server not answering"
	type live = store.LiveSpawnIdentity
	cases := []struct {
		name, state, session string
		opts                 []apitest.SpawnOption
		edit                 func(*live) // what the case changes from the seeded defaults
	}{
		{"pending, default launch start is started_at", store.StatePending, "", nil,
			func(w *live) { w.LaunchStartedAtMillis = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).UnixMilli() }},
		{"pending, launch start text", store.StatePending, "",
			[]apitest.SpawnOption{apitest.WithRawLaunchStartedAt("yesterday")}, nil},
		{"waiting, integer launch start", store.StateWaiting, "",
			[]apitest.SpawnOption{apitest.WithLaunchStartedAt(1767225600456)}, func(w *live) { w.LaunchStartedAtMillis = 1767225600456 }},
		{"working, session and process identity", store.StateWorking, "sess-life",
			[]apitest.SpawnOption{apitest.WithPID(4242), apitest.WithProcStarttime(apitest.LinuxProcStarttime)}, func(w *live) {
				w.PID, w.ProcStarttime = 4242, apitest.LinuxProcStarttime
				w.Snapshot.PID, w.Snapshot.ProcStarttime, w.Snapshot.ClaudeSessionID = 4242, apitest.LinuxProcStarttime, "sess-life"
			}},
		{"ask_user, liveness note", store.StateAskUser, "", []apitest.SpawnOption{apitest.WithLivenessNote(note)},
			func(w *live) { w.LivenessNote = note }},
		{"check_permission, no launch identity", store.StateCheckPermission, "", []apitest.SpawnOption{apitest.WithNoLaunchToken()},
			func(w *live) { w.Identity = store.LaunchIdentity{} }},
		{"pane without start time, malformed token", store.StateWaiting, "", []apitest.SpawnOption{apitest.WithLaunchIdentity(
			store.LaunchIdentity{Token: "NOT-A-TOKEN", Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID})},
			func(w *live) {
				w.Identity = store.LaunchIdentity{Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID}
			}},
		{"malformed labels, claude_args and extra_env", store.StateWaiting, "", []apitest.SpawnOption{apitest.WithRawLabels("{not json"),
			apitest.WithRawClaudeArgs("[unterminated"), apitest.WithRawExtraEnv("not an object")}, nil},
	}
	f := newV5Store(t)
	want := map[string]live{}
	for i, tc := range cases {
		version, name := int64(100+i), fmt.Sprintf("live-%d", i)
		id := f.seed(tc.state, tc.session, append([]apitest.SpawnOption{apitest.WithRowVersion(version),
			apitest.WithStartedAt(startedAt), apitest.WithTmuxSessionName(name), apitest.WithLaunchIdentity(fullIdentity())}, tc.opts...)...)
		w := live{ClaudeInstanceID: id, State: tc.state, TmuxSessionName: name, Identity: fullIdentity(),
			Snapshot: store.RowSnapshot{RowVersion: version, StartedAt: startedAt, TmuxSessionName: name}}
		if tc.edit != nil {
			tc.edit(&w)
		}
		want[id] = w
	}
	for _, st := range []string{store.StateEnded, store.StateMissing} {
		f.seed(st, "", apitest.WithLaunchStartedAt(1767225600123))
	}
	got, err := f.s.ListLiveSpawnIdentities()
	if err != nil {
		t.Fatalf("ListLiveSpawnIdentities: %v; no stored value may fail the read", err)
	}
	if len(got) != len(want) {
		t.Errorf("listed %d rows; want the %d live ones", len(got), len(want))
	}
	for _, it := range got {
		if w, ok := want[it.ClaudeInstanceID]; !ok || it != w {
			t.Errorf("listed %+v;\n want %+v", it, w)
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
		{"1 ms short of default grace", store.StatePending, at(-grace + time.Millisecond), grace, true},
		{"exactly default grace old", store.StatePending, at(-grace), grace, false},
		{"safe minimum grace less 1 s", store.StatePending, at(-minGrace + time.Second), minGrace, true},
		{"safe minimum grace plus 1 s", store.StatePending, at(-minGrace - time.Second), minGrace, false},
		{"future launch start", store.StatePending, at(time.Hour), grace, true},
		{"last ms of year 9999", store.StatePending, lastInRangeLaunchMillis, grace, true},
		{"first ms of year 0", store.StatePending, firstInRangeLaunchMillis, grace, false},
		{"int64 min never overflows to young", store.StatePending, math.MinInt64, grace, false},
		{"absent launch start", store.StatePending, 0, grace, false},
		{"waiting, fresh start", store.StateWaiting, at(-time.Second), grace, false},
		{"missing, fresh start", store.StateMissing, at(-time.Second), grace, false},
	}
	for _, tc := range cases {
		if got := store.InsidePendingGrace(tc.state, tc.start, tc.grace, now); got != tc.want {
			t.Errorf("%s: InsidePendingGrace(%q, %d, %v) = %v; want %v", tc.name, tc.state, tc.start, tc.grace, got, tc.want)
		}
	}
}
