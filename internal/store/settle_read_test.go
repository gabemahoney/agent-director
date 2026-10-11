package store_test

// settle_read_test.go (b.3ie): the reported state with a settle time T, read
// at an injected clock (no sleeps) through every read surface's store read
// (SpawnStatusSettled, GetSpawnSettled, ListSpawns with ListFilters.Settle),
// over rows the real write paths produced. A hook-written change reads as the
// last settled state until it has held T, and as the new state from then on;
// a flap back to the settled state never shows; terminal states and
// command-driven changes read at once; a negative age is not settled; with no
// settle time every read is the raw state.

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reportedAt returns id's state as status, get and list report it with
// settle time settleMs at nowMs, failing t unless the three reads agree and
// list's state filter selects the row by that state.
func reportedAt(t *testing.T, f *v5Store, id string, settleMs, nowMs int64) string {
	t.Helper()
	r := store.SettleRead{SettleMs: settleMs, NowMs: nowMs}
	status, raw, _, err := f.s.SpawnStatusSettled(id, r)
	if err != nil {
		t.Fatalf("SpawnStatusSettled: %v", err)
	}
	if cols := f.rawColumns(id); raw != cols.State {
		t.Errorf("SpawnStatusSettled raw = %q; want the raw state %v", raw, cols.State)
	}
	sp, err := f.s.GetSpawnSettled(id, r)
	if err != nil {
		t.Fatalf("GetSpawnSettled: %v", err)
	}
	if sp.State != raw {
		t.Errorf("GetSpawnSettled State = %q; want the raw state %q (guards read it)", sp.State, raw)
	}
	listed := func(states ...string) (string, bool) {
		rows, err := f.s.ListSpawns(store.ListFilters{State: states, Settle: r})
		if err != nil {
			t.Fatalf("ListSpawns: %v", err)
		}
		for _, row := range rows {
			if row.InstanceID == id {
				return row.ReportedState, true
			}
		}
		return "", false
	}
	list, _ := listed()
	if sp.ReportedState != status || list != status {
		t.Errorf("reported at %d: status %q, get %q, list %q; want all three the same", nowMs, status, sp.ReportedState, list)
	}
	if _, ok := listed(status); !ok {
		t.Errorf("list --state %s does not select the row reported %s", status, status)
	}
	if status != raw {
		if _, ok := listed(raw); ok {
			t.Errorf("list --state %s selects the row reported %s; want the filter on the reported state", raw, status)
		}
	}
	return status
}

// assertReportedAt fails unless id reads want with settleMs at nowMs.
func assertReportedAt(t *testing.T, f *v5Store, id string, settleMs, nowMs int64, want string) {
	t.Helper()
	if got := reportedAt(t, f, id, settleMs, nowMs); got != want {
		t.Errorf("reported with settle time %d at %d = %q; want %q", settleMs, nowMs, got, want)
	}
}

// settleStep is one write of a derivation sequence and the reads after it,
// each at an offset from the write's own stamp.
type settleStep struct {
	write func(t *testing.T, f *v5Store, id string)
	reads []settleRead
}

// settleRead is a read offset ms after the last write's stamp, wanting state.
type settleRead struct {
	offset int64
	want   string
}

// hookTo is the row's own agent's hook write of state (ApplyHookTransitionResult).
func hookTo(state string) func(t *testing.T, f *v5Store, id string) {
	return func(t *testing.T, f *v5Store, id string) {
		t.Helper()
		if _, applied, err := f.s.ApplyHookTransitionResult(id, rvAgentGate(t, f, id), state, false, "settle_test", "", false); err != nil || !applied.Applied {
			t.Fatalf("hook to %s = %+v, %v; want applied", state, applied, err)
		}
	}
}

// agentEvent is the row's own agent firing event (storefix.ApplyAgentHook).
func agentEvent(event string) func(t *testing.T, f *v5Store, id string) {
	return func(t *testing.T, f *v5Store, id string) {
		t.Helper()
		if got := storefix.ApplyAgentHook(t, f.s, id, event, ""); !got.Applied {
			t.Fatalf("%s = %+v; want applied", event, got)
		}
	}
}

// markMissing is find-missing's mark of the row as examined now.
func markMissing(t *testing.T, f *v5Store, id string) {
	t.Helper()
	if _, res, err := f.s.MarkMissingIfSameLife(id, rvExamine(t, f, id).Snapshot); err != nil || res != store.CondApplied {
		t.Fatalf("MarkMissingIfSameLife = %v, %v; want applied", res, err)
	}
}

// TestSettleDerivation runs each sequence of real writes with the writer's
// settle time T and reads it at offsets from each write's stamp (the clock
// injected through SettleRead): a hook-written change reads as the last
// settled state while younger than T (a negative age included) and as the new
// state from exactly T; flaps back to the settled state never show; terminal
// states read at once; a hook moving a row out of missing settles like any.
func TestSettleDerivation(t *testing.T) {
	T := settleT
	cases := []struct {
		name  string
		seed  string
		steps []settleStep
	}{
		{"a change younger than T reads the last settled state, at T and after the new one", store.StateWaiting,
			[]settleStep{{hookTo(store.StateWorking), []settleRead{{-1, store.StateWaiting}, {0, store.StateWaiting},
				{T - 1, store.StateWaiting}, {T, store.StateWorking}, {T + 1, store.StateWorking}, {10 * T, store.StateWorking}}}}},
		{"A to B to A inside T reads A throughout", store.StateWorking, []settleStep{
			{hookTo(store.StateWaiting), []settleRead{{0, store.StateWorking}, {T - 1, store.StateWorking}}},
			{hookTo(store.StateWorking), []settleRead{{0, store.StateWorking}, {T - 1, store.StateWorking}, {T, store.StateWorking}}}}},
		{"the short turn waiting, working, waiting reads waiting throughout", store.StateWaiting, []settleStep{
			{agentEvent("UserPromptSubmit"), []settleRead{{0, store.StateWaiting}, {T - 1, store.StateWaiting}}},
			{agentEvent("Stop"), []settleRead{{0, store.StateWaiting}, {T, store.StateWaiting}}}}},
		{"A to B to C inside T reads A, then C once C has held T from the B to C change", store.StateWaiting, []settleStep{
			{hookTo(store.StateWorking), []settleRead{{0, store.StateWaiting}}},
			{hookTo(store.StateAskUser), []settleRead{{0, store.StateWaiting}, {T - 1, store.StateWaiting},
				{T, store.StateAskUser}}}}},
		{"ended reads at once, an unsettled change before it too", store.StateWaiting, []settleStep{
			{hookTo(store.StateWorking), []settleRead{{0, store.StateWaiting}}},
			{agentEvent("SessionEnd"), []settleRead{{-1, store.StateEnded}, {0, store.StateEnded}, {T, store.StateEnded}}}}},
		{"missing reads at once, an unsettled change before it too", store.StateWaiting, []settleStep{
			{hookTo(store.StateWorking), []settleRead{{0, store.StateWaiting}}},
			{markMissing, []settleRead{{-1, store.StateMissing}, {0, store.StateMissing}, {T, store.StateMissing}}}}},
		{"a hook moving a row out of missing settles", store.StateMissing, []settleStep{
			{agentEvent("SessionStart"), []settleRead{{0, store.StateMissing}, {T - 1, store.StateMissing},
				{T, store.StateWaiting}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newV5Store(t)
			f.s.SetSettleMs(T)
			id := f.seed(tc.seed, "", rowVersionSeed()...)
			for i, step := range tc.steps {
				step.write(t, f, id)
				_, stamp := settleOf(t, f, id)
				for _, r := range step.reads {
					if got := reportedAt(t, f, id, T, stamp+r.offset); got != r.want {
						t.Errorf("step %d, %+d ms after its stamp: reported %q; want %q", i+1, r.offset, got, r.want)
					}
				}
			}
		})
	}
}

// TestSettleDerivationColumns reads hand-placed settle columns (the clock
// and the stored values injected): with no settle time the raw state whatever
// the columns; an empty settled_state (no write gave the row one) the raw
// state; and a hook change on such a row stores the old raw value as its base.
func TestSettleDerivationColumns(t *testing.T) {
	const now = int64(1_790_000_000_000)
	f := newV5Store(t)
	f.s.SetSettleMs(settleT)
	id := f.seed(store.StateWorking, "", rowVersionSeed()...)

	setSettle(t, f, id, store.StateEnded, now)
	for _, settleMs := range []int64{0, -1} {
		assertReportedAt(t, f, id, settleMs, now, store.StateWorking)
	}
	assertReportedAt(t, f, id, settleT, now, store.StateEnded)

	setSettle(t, f, id, "", now)
	assertReportedAt(t, f, id, settleT, now, store.StateWorking)
	setSettle(t, f, id, "", apitest.ReadSurfaceStampMs) // a stamp ahead of the writer's clock: not settled
	hookTo(store.StateWaiting)(t, f, id)
	if settled, _ := settleOf(t, f, id); settled != store.StateWorking {
		t.Errorf("settled_state after a hook on a row with none = %q; want the old raw value %q", settled, store.StateWorking)
	}
	_, stamp := settleOf(t, f, id)
	assertReportedAt(t, f, id, settleT, stamp, store.StateWorking)
}

// TestSettleWriterAndReaderTimesDiffer: a writer with no settle time records
// every old raw value as settled, so a reader with one reports, inside its
// time, the value the row held just before (never a value the row's raw
// history did not hold), and the raw state once that has held it.
func TestSettleWriterAndReaderTimesDiffer(t *testing.T) {
	f := newV5Store(t) // the writer's settle time is 0, as before SetSettleMs
	id := f.seed(store.StateWaiting, "", rowVersionSeed()...)
	held := []string{store.StateWaiting}
	for _, to := range []string{store.StateWorking, store.StateWaiting, store.StateAskUser} {
		hookTo(to)(t, f, id)
		_, stamp := settleOf(t, f, id)
		got := reportedAt(t, f, id, settleT, stamp)
		if got != held[len(held)-1] || !slices.Contains(held, got) {
			t.Errorf("after the move to %s, read inside T = %q; want the value held before, %q", to, got, held[len(held)-1])
		}
		assertReportedAt(t, f, id, settleT, stamp+settleT, to)
		held = append(held, to)
	}
}
