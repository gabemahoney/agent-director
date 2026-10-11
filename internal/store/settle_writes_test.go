package store_test

// settle_writes_test.go (b.3ie): every spawns write that changes the raw state
// stamps state_changed_ms with the time of the change in milliseconds, in its
// own statement, and sets settled_state by its class. A hook's write of a
// non-terminal state keeps the old raw value as the settled one when it had
// held the writer's settle time (or the writer has none), and the kept base
// otherwise; a terminal state, and every write that is not a hook's, is
// settled at once. A write that leaves the raw state as it is touches neither
// column. The write paths are row_version_test.go's table; the reads of the
// result are settle_read_test.go's helpers.

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// settleT is the settle time the tests write and read with: the largest, so
// the milliseconds between two writes of a test are always inside it.
const settleT = int64(config.MaxStateSettleMs)

// hookWriters are the store writes a hook process makes, as rowVersionWrites
// names its cases ("<writer>/<case>").
var hookWriters = []string{"ApplyHookTransition", "ApplyHookTransitionResult", "RecordSessionStartIdentity",
	"ApplyHookWaitingIfWorking", "InsertRelayRequest"}

// isHookWrite reports whether case name is a hook's write.
func isHookWrite(name string) bool {
	writer, _, _ := strings.Cut(name, "/")
	return slices.Contains(hookWriters, writer)
}

// settleOf reads id's settle columns.
func settleOf(t *testing.T, f *v5Store, id string) (settled string, stampMs int64) {
	t.Helper()
	settled, stampMs, err := apitest.ReadSettle(f.path, id)
	if err != nil {
		t.Fatal(err)
	}
	return settled, stampMs
}

// setSettle writes id's settle columns.
func setSettle(t *testing.T, f *v5Store, id, settled string, stampMs int64) {
	t.Helper()
	if err := apitest.SetSettle(f.path, id, settled, stampMs); err != nil {
		t.Fatal(err)
	}
}

// otherState is a state that is none of avoid, for a settled_state no write
// under test would itself produce.
func otherState(avoid ...string) string {
	for _, s := range []string{store.StateAskUser, store.StateWorking, store.StateWaiting, store.StatePending} {
		if !slices.Contains(avoid, s) {
			return s
		}
	}
	return ""
}

// assertStamped fails unless stampMs is within [from, to], the Go clock's
// milliseconds read before and after the write.
func assertStamped(t *testing.T, stampMs, from, to int64) {
	t.Helper()
	if stampMs < from || stampMs > to {
		t.Errorf("state_changed_ms = %d; want the write's own time, in [%d, %d]", stampMs, from, to)
	}
}

// TestSettleColumnsEveryWrite runs every spawns write (rowVersionWrites) over
// a row whose raw state had held the writer's settle time, one whose raw state
// changed just now, and one whose writer has no settle time. A write that
// changes the raw state stamps its own time and settles by its class; one
// that does not changes neither column. Read at once with the settle time,
// the row reports the settled value, and from the stamp plus it the raw one.
func TestSettleColumnsEveryWrite(t *testing.T) {
	for _, base := range []struct {
		name     string
		writerMs int64 // the writer's settle time
		fresh    bool  // the row's raw state changed just now, so has not held writerMs
	}{
		{"raw state settled", settleT, false},
		{"raw state unsettled", settleT, true},
		{"writer with no settle time", 0, true},
	} {
		for _, c := range rowVersionWrites() {
			t.Run(base.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				f := newV5Store(t)
				f.s.SetSettleMs(base.writerMs)
				id := seedCase(t, f, c)
				raw := f.rawColumns(id).State.(string)
				kept, stamp := otherState(raw, c.wantState), int64(0)
				if base.fresh {
					stamp = time.Now().UnixMilli()
				}
				setSettle(t, f, id, kept, stamp)

				from := time.Now().UnixMilli()
				c.write(t, f, id)
				to := time.Now().UnixMilli()

				now := f.rawColumns(id).State.(string)
				settled, stampAfter := settleOf(t, f, id)
				if now == raw {
					if settled != kept || stampAfter != stamp {
						t.Errorf("settle columns %q, %d -> %q, %d; want both kept by a write that keeps the raw state %s",
							kept, stamp, settled, stampAfter, raw)
					}
					return
				}
				assertStamped(t, stampAfter, from, to)
				want := now // settled at once
				switch {
				case !isHookWrite(c.name) || isTerminal(now):
				case base.writerMs == 0 || !base.fresh:
					want = raw // the old raw value had settled
				default:
					want = kept // the old raw value had not: the base is kept
				}
				if settled != want {
					t.Errorf("settled_state after %s -> %s = %q; want %q", raw, now, settled, want)
				}
				assertReportedAt(t, f, id, settleT, stampAfter, want)
				assertReportedAt(t, f, id, settleT, stampAfter+settleT, now)
			})
		}
	}
}

// isTerminal reports whether state is ended or missing.
func isTerminal(state string) bool { return state == store.StateEnded || state == store.StateMissing }

// TestSettleInsertPending: a new row is pending and settled at once, stamped
// with the insert's own time.
func TestSettleInsertPending(t *testing.T) {
	f := newV5Store(t)
	from := time.Now().UnixMilli()
	if err := f.s.InsertPending(store.Spawn{InstanceID: "settle-insert", Agent: store.AgentClaude, CWD: "/tmp",
		TmuxSessionName: "ts-settle-insert", RelayMode: "off"}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	to := time.Now().UnixMilli()
	settled, stamp := settleOf(t, f, "settle-insert")
	if settled != store.StatePending {
		t.Errorf("settled_state = %q; want pending", settled)
	}
	assertStamped(t, stamp, from, to)
	assertReportedAt(t, f, "settle-insert", settleT, stamp, store.StatePending)
}

// TestSettleSameValueKeepsTheClock: a soft refresh and a second ended leave
// the raw state as it is, so they keep state_changed_ms and settled_state and
// never restart the settle time. (A hook's same-value write of a non-terminal
// state is TestSettleColumnsEveryWrite's.)
func TestSettleSameValueKeepsTheClock(t *testing.T) {
	const stamp = int64(1_790_000_000_123)
	for _, tc := range []struct {
		name, state, event string
	}{
		{"soft refresh", store.StateWaiting, "Notification"},
		{"ended again", store.StateEnded, "SessionEnd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			f.s.SetSettleMs(settleT)
			id := f.seed(tc.state, "", rowVersionSeed()...)
			base := otherState(tc.state)
			if isTerminal(tc.state) {
				base = tc.state
			}
			setSettle(t, f, id, base, stamp)
			if got := storefix.ApplyAgentHook(t, f.s, id, tc.event, ""); !got.Applied {
				t.Fatalf("%s = %+v; want applied", tc.event, got)
			}
			if raw := f.rawColumns(id).State; raw != tc.state {
				t.Fatalf("raw state after %s = %v; want %s kept", tc.event, raw, tc.state)
			}
			if settled, got := settleOf(t, f, id); settled != base || got != stamp {
				t.Errorf("settle columns = %q, %d; want %q, %d kept", settled, got, base, stamp)
			}
		})
	}
}

// TestSettleDuplicateHooksKeepTheFirstStamp: of duplicate hooks of one change
// (the trail's waiting -> working writes 1-2 ms apart), only the first changes
// the raw state; a duplicate that read the row before that write and updates
// it after changes neither settle column (the model's red_stalebase). The
// seam (SetHookBeforeUpdateForTest) runs, between the outer hook's Go-side
// read (waiting) and its UPDATE, the first duplicate (one stamp, the old
// settled value as the base), then a sentinel base and stamp no write
// produces. The outer UPDATE sees working, so the sentinel survives it; a
// same-value test or base taken from the Go-side read would see a change from
// waiting and overwrite both columns.
func TestSettleDuplicateHooksKeepTheFirstStamp(t *testing.T) {
	const sentinelStamp = int64(1_790_000_000_123)
	sentinelBase := otherState(store.StateWaiting, store.StateWorking)
	f := newV5Store(t)
	f.s.SetSettleMs(settleT)
	id := f.seed(store.StateWaiting, "", rowVersionSeed()...)
	t.Cleanup(func() { store.SetHookBeforeUpdateForTest(f.s, nil) })
	ran := false
	store.SetHookBeforeUpdateForTest(f.s, func() {
		store.SetHookBeforeUpdateForTest(f.s, nil) // the duplicate below must not re-enter
		ran = true
		from := time.Now().UnixMilli()
		if got := storefix.ApplyAgentHook(t, f.s, id, "PreToolUse", ""); !got.Applied {
			t.Fatalf("the first duplicate PreToolUse = %+v; want applied", got)
		}
		to := time.Now().UnixMilli()
		settled, stamp := settleOf(t, f, id)
		if raw := f.rawColumns(id).State; raw != store.StateWorking || settled != store.StateWaiting {
			t.Fatalf("after the first duplicate: raw %v, settled_state %q; want working, waiting", raw, settled)
		}
		assertStamped(t, stamp, from, to)
		setSettle(t, f, id, sentinelBase, sentinelStamp)
	})

	if got := storefix.ApplyAgentHook(t, f.s, id, "PreToolUse", ""); !got.Applied {
		t.Fatalf("the outer PreToolUse = %+v; want applied", got)
	}
	if !ran {
		t.Fatal("the seam never ran between the outer hook's read and its UPDATE; the test proves nothing")
	}
	if raw := f.rawColumns(id).State; raw != store.StateWorking {
		t.Errorf("raw state after both duplicates = %v; want working", raw)
	}
	if settled, stamp := settleOf(t, f, id); settled != sentinelBase || stamp != sentinelStamp {
		t.Errorf("settle columns after the duplicate that read waiting = %q, %d; want %q, %d left as they were "+
			"(its UPDATE, not its read, decides the same value)", settled, stamp, sentinelBase, sentinelStamp)
	}
}
