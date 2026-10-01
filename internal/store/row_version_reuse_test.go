package store_test

// SR-5.2 versioning cases for reuse's read, reset and restore (SR-10.3,
// SR-10.4), appended to row_version_test.go's applied and no-op tables.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rvReuse* are the reset's launch start, token and socket, unlike the seed's.
const (
	rvReuseStart  int64 = 1767226200789
	rvReuseToken        = "0a1b2c3d4e5f6789"
	rvReuseSocket       = "/tmp/rv/reuse-sock"
)

// rvReuseFresh is the reset's new life with parentID: pre-trust allowed, unlike
// the seed's opt-out, so the no_pre_trust write is observable.
func rvReuseFresh(parentID string) store.Spawn {
	return store.Spawn{CWD: "/tmp/rv/reuse", TmuxSessionName: "ts-rv-reuse", RelayMode: "off",
		StartedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), LaunchStartedAtMillis: rvReuseStart,
		ParentID: parentID, Identity: store.LaunchIdentity{Token: rvReuseToken, Socket: rvReuseSocket}}
}

// rvReuseRead is reuse's read of id; the row must be found.
func rvReuseRead(t *testing.T, f *v5Store, id string) store.ReuseRow {
	t.Helper()
	row, found, err := f.s.ReadForReuse(id)
	if err != nil || !found {
		t.Fatalf("ReadForReuse(%s) = found %v, %v; want found", id, found, err)
	}
	return row
}

// rvReset resets target from examined, expecting want and, when applied, the
// archived session id wantArchived and the examined version plus one.
func rvReset(t *testing.T, f *v5Store, target string, examined store.RowSnapshot, want store.CondResult, wantArchived string) int64 {
	t.Helper()
	var wantV int64
	if want == store.CondApplied {
		wantV = examined.RowVersion + 1
	}
	got, archived, v, err := f.s.ResetForReuse(target, examined, rvReuseFresh(""))
	if err != nil || got != want || archived != wantArchived || v != wantV {
		t.Fatalf("ResetForReuse = %v, %q, %d, %v; want %v, %q, %d, nil", got, archived, v, err, want, wantArchived, wantV)
	}
	return v
}

// rvResetNow returns the reset of id as read now, expecting want and wantArchived.
func rvResetNow(want store.CondResult, wantArchived string) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		rvReset(t, f, id, rvReuseRead(t, f, id).Snapshot, want, wantArchived)
	}
}

// rvReuse carries one reuse's prior life and reset version from a case's setup
// to its write.
type rvReuse struct {
	prior store.RawLife
	reset int64
}

// resetNow reads id, keeps its prior life and resets it (no session to archive).
func (r *rvReuse) resetNow(t *testing.T, f *v5Store, id string) {
	t.Helper()
	row := rvReuseRead(t, f, id)
	r.prior, r.reset = row.Life, rvReset(t, f, id, row.Snapshot, store.CondApplied, "")
}

// restore returns the restore of target ("" = the seeded row) with r's prior
// life at r's reset version, expecting want.
func (r *rvReuse) restore(target string, want store.CondResult) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		to := target
		if to == "" {
			to = id
		}
		failedAt := time.Date(2026, 2, 1, 0, 5, 0, 0, time.UTC)
		if got, err := f.s.RestoreAfterFailedReuse(to, r.reset, r.prior, failedAt); err != nil || got != want {
			t.Fatalf("RestoreAfterFailedReuse(version %d) = %v, %v; want %v, nil", r.reset, got, err, want)
		}
	}
}

// restoreRefused restores the seeded row at its current version with r's
// prior life, which must fail with nothing written.
func (r *rvReuse) restoreRefused(t *testing.T, f *v5Store, id string) {
	t.Helper()
	v := f.rawColumns(id).RowVersion.(int64)
	if got, err := f.s.RestoreAfterFailedReuse(id, v, r.prior, time.Now()); err == nil || got != 0 {
		t.Fatalf("RestoreAfterFailedReuse(bad prior) = %v, %v; want 0, an error", got, err)
	}
}

// rvLifeWritten checks the life_number and no_pre_trust c.lifeWrite says the
// write stores, and returns before's other stable columns for assertColumns.
func rvLifeWritten(t *testing.T, before, after apitest.SpawnColumns, c rowVersionCase) map[string]any {
	t.Helper()
	b, a := stableColumns(before), stableColumns(after)
	for col, want := range c.lifeWrite {
		if reflect.DeepEqual(b[col], want) {
			t.Fatalf("%s before the write is already %#v; the write check would be vacuous", col, want)
		}
		if !reflect.DeepEqual(a[col], want) {
			t.Errorf("%s %#v -> %#v, want %#v", col, b[col], a[col], want)
		}
		delete(b, col)
	}
	return b
}

// rvReuseWrites are the applied reset and restore for each finished state. The
// reset sets life+1, the call's no_pre_trust, the launch start, token and
// socket, and clears the identity; the restore writes life, no_pre_trust,
// token, socket and identity back and clears the launch start (SR-5.2).
func rvReuseWrites() []rowVersionCase {
	var cases []rowVersionCase
	for _, st := range []string{"ended", "missing"} {
		reused, full, r := store.LaunchIdentity{Token: rvReuseToken, Socket: rvReuseSocket}, fullIdentity(), &rvReuse{}
		cases = append(cases,
			rowVersionCase{name: "ResetForReuse/applied, " + st + " row", state: st, wantState: "pending",
				identity: &reused, writesToken: true, launchStart: rvReuseStart,
				lifeWrite: map[string]any{"life_number": int64(8), "no_pre_trust": int64(0)},
				write:     rvResetNow(store.CondApplied, "")},
			rowVersionCase{name: "RestoreAfterFailedReuse/applied, " + st + " row", state: st, wantState: st,
				clears: true, identity: &full, writesToken: true,
				lifeWrite: map[string]any{"life_number": int64(7), "no_pre_trust": int64(1)},
				setup:     r.resetNow, write: r.restore("", store.CondApplied)},
		)
	}
	reused := store.LaunchIdentity{Token: rvReuseToken, Socket: rvReuseSocket}
	return append(cases, rowVersionCase{name: "ResetForReuse/applied, archives the session in the transaction, one bump",
		state: "ended", session: "sess-rv", opts: []apitest.SpawnOption{apitest.WithJsonlPath("/tmp/rv/old.jsonl")},
		wantState: "pending", identity: &reused, writesToken: true, launchStart: rvReuseStart,
		lifeWrite: map[string]any{"life_number": int64(8), "no_pre_trust": int64(0)},
		write: func(t *testing.T, f *v5Store, id string) {
			n := historyLen(t, f, id)
			rvResetNow(store.CondApplied, "sess-rv")(t, f, id)
			if got := historyLen(t, f, id); got != n+1 {
				t.Fatalf("history entries %d -> %d, want one archived entry", n, got)
			}
		}})
}

// rvReuseNoOps are the read, a refused reset and a refused restore: each
// writes nothing to the row.
func rvReuseNoOps() []rowVersionCase {
	var stale store.RowSnapshot
	afterWrite, neverReset, absent, pendingPrior := &rvReuse{}, &rvReuse{}, &rvReuse{}, &rvReuse{}
	return []rowVersionCase{
		{name: "ReadForReuse/the read", state: "ended",
			write: func(t *testing.T, f *v5Store, id string) {
				if row := rvReuseRead(t, f, id); row.State != "ended" || row.Snapshot != rvExamine(t, f, id).Snapshot {
					t.Fatalf("ReadForReuse = state %q, snapshot %+v; want ended, GetSpawn's", row.State, row.Snapshot)
				}
			}},
		{name: "ResetForReuse/stale snapshot, own agent's hook first", state: "ended",
			setup: func(t *testing.T, f *v5Store, id string) {
				stale = rvReuseRead(t, f, id).Snapshot
				rvSoftRefresh(t, f, id)
			},
			write: func(t *testing.T, f *v5Store, id string) { rvReset(t, f, id, stale, store.CondChanged, "") }},
		{name: "ResetForReuse/live row", state: "waiting", write: rvResetNow(store.CondChanged, "")},
		{name: "ResetForReuse/pending row", state: "pending", write: rvResetNow(store.CondChanged, "")},
		{name: "ResetForReuse/absent row", state: "ended",
			write: func(t *testing.T, f *v5Store, id string) {
				rvReset(t, f, "rv-absent", rvReuseRead(t, f, id).Snapshot, store.CondAbsent, "")
			}},
		// The archive runs before the reset fails on the foreign key: both roll back.
		{name: "ResetForReuse/parent id names no row, archive rolled back", state: "ended", session: "sess-rv",
			write: func(t *testing.T, f *v5Store, id string) {
				n := historyLen(t, f, id)
				got, archived, v, err := f.s.ResetForReuse(id, rvReuseRead(t, f, id).Snapshot, rvReuseFresh("rv-no-such-parent"))
				if err == nil || errors.Is(err, store.ErrReuseArchive) || got != 0 || archived != "" || v != 0 {
					t.Fatalf("ResetForReuse(bad parent) = %v, %q, %d, %v; want 0, \"\", 0, a non-archive error", got, archived, v, err)
				}
				if got := historyLen(t, f, id); got != n {
					t.Errorf("history entries %d -> %d, want the archive rolled back", n, got)
				}
			}},
		// Decision 11: no hook can land on the reset row, so another versioned write comes first.
		{name: "RestoreAfterFailedReuse/another write after the reset", state: "ended",
			setup: func(t *testing.T, f *v5Store, id string) { afterWrite.resetNow(t, f, id); seedParent(t, f, id) },
			write: afterWrite.restore("", store.CondChanged)},
		{name: "RestoreAfterFailedReuse/finished row never reset", state: "ended",
			setup: func(t *testing.T, f *v5Store, id string) {
				neverReset.prior, neverReset.reset = rvReuseRead(t, f, id).Life, f.rawColumns(id).RowVersion.(int64)
			},
			write: neverReset.restore("", store.CondChanged)},
		{name: "RestoreAfterFailedReuse/absent row", state: "ended", setup: absent.resetNow,
			write: absent.restore("rv-absent", store.CondAbsent)},
		// The guard alone refuses: the row is pending at the version passed.
		{name: "RestoreAfterFailedReuse/prior read from a pending row", state: "pending",
			setup: func(t *testing.T, f *v5Store, id string) { pendingPrior.prior = rvReuseRead(t, f, id).Life },
			write: pendingPrior.restoreRefused},
		{name: "RestoreAfterFailedReuse/prior not read by ReadForReuse", state: "pending",
			write: (&rvReuse{}).restoreRefused},
	}
}
