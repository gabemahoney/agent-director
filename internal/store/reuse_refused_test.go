package store_test

// Resets that write nothing (SR-10.3, SR-5.3, SR-5.6, SR-5.8): not applied,
// failed store writes, and the write lock taken before the read under a
// concurrent reset; over reuse_test.go's seedReuseRow and reuseFresh. The
// stale-snapshot cases are row_version_reuse_test.go's.

import (
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reuseState is what a reset may touch: the row, its history and requests
// (read on the store's own connection, so a transaction left open shows),
// its subagent entries and claimable launches (the reset closes them, SR-8.1, SR-10.3)
// and its child.
type reuseState struct {
	row       apitest.SpawnColumns
	history   []apitest.HistoryEntry
	requests  []store.PermissionRow
	subagents apitest.SubagentRows
	child     apitest.SpawnColumns
}

// reuseStateOf reads r's reuseState now.
func (f *v5Store) reuseStateOf(t *testing.T, r reuseRow) reuseState {
	t.Helper()
	return reuseState{f.rawColumns(r.id), f.historyAllLives(r.id), f.reuseRequests(t, r.id), f.subagentRowsOf(r.id), f.rawColumns(r.child)}
}

// TestReuseResetNotApplied checks a reset after its own agent's hook moved the
// snapshot, of a live row, or of a row deleted after examination, gives
// CondChanged or CondAbsent with no archived id or version, and archives,
// deletes and writes nothing: no running entry is closed and no claimable
// launch deleted, on the row or another (SR-8.1, SR-10.3).
func TestReuseResetNotApplied(t *testing.T) {
	cases := []struct {
		name  string
		spec  reuseSpec
		write func(t *testing.T, f *v5Store, r reuseRow) // after examination
		want  store.CondResult
	}{
		{"own agent's hook after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) {
			if got := apitest.ApplyAgentHook(t, f.path, r.id, "Notification", reuseSessionID); !got.Applied {
				t.Fatalf("own agent's Notification = %+v; want applied", got)
			}
		}, store.CondChanged},
		{"agent-only text change after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) { f.reuseAgentRaw(r.id, store.AgentClaude) }, store.CondChanged},
		{"agent-only blob change after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) { f.reuseAgentRaw(r.id, []byte(store.AgentCodex)) }, store.CondChanged},
		{"agent-only NULL change after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) { f.reuseAgentRaw(r.id, nil) }, store.CondChanged},
		{"live row", reuseSpec{state: store.StateWaiting}, nil, store.CondChanged},
		{"row deleted after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) {
			if err := f.s.DeleteSpawn(r.id); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}, store.CondAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			mix := f.seedCloseMix(r.id)
			if tc.write != nil {
				tc.write(t, f, r)
			}
			if tc.want == store.CondChanged && tc.spec.state == "" && tc.name != "own agent's hook after examination" {
				if row := f.readForReuse(t, r.id); row.Snapshot != r.read.Snapshot {
					t.Fatal("agent edit changed snapshot; agent guard check is vacuous")
				}
			}
			var before reuseState
			if tc.want != store.CondAbsent {
				before = f.reuseStateOf(t, r)
			}
			history := f.historyAllLives(r.id)
			res, archived, v, err := f.reuseReset(r, reuseFresh(r.parent, false))
			if err != nil || res != tc.want || archived != "" || v != 0 {
				t.Fatalf("ResetForReuse = %v, %q, %d, %v; want %v, \"\", 0, nil", res, archived, v, err, tc.want)
			}
			if tc.want == store.CondAbsent {
				if _, err := apitest.ReadSpawnColumns(f.path, r.id); !errors.Is(err, store.ErrSpawnNotFound) || !reflect.DeepEqual(f.historyAllLives(r.id), history) {
					t.Errorf("the reset of a deleted row made a row (%v) or history", err)
				}
			} else if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
				t.Errorf("a reset that did not apply wrote:\n before %+v\n after  %+v", before, after)
			}
			f.assertBystanderKept(mix)
		})
	}
}

// TestReuseResetFailClosed checks each injected store failure and a parent id
// naming no row return an error (the archive marker only for the archive), no
// outcome, and leave row, history, requests, entries, claimable launches and
// child as before; a failed close of the entries rolls the whole reset back, and
// so does a fresh launch with no instant (0), which the close refuses
// (SR-8.1, SR-10.3).
func TestReuseResetFailClosed(t *testing.T) {
	cases := []struct {
		name        string
		kind        storefix.WriteFailureKind // 0: no injection
		parent      string
		wantArchive bool // errors.Is(err, store.ErrReuseArchive)
		noInstant   bool // the fresh launch has LaunchStartedAtMillis 0
	}{
		{"archive", storefix.WriteFailReuseArchive, "", true, false},
		{"reset", storefix.WriteFailReuseReset, "", false, false},
		{"permission-request deletion", storefix.WriteFailReusePermissionDelete, "", false, false},
		{"subagent entry and launch close", storefix.WriteFailSubagentRecord, "", false, false},
		{"parent id names no row", 0, "no-such-parent", false, false},
		{"fresh launch with no instant", 0, "", false, true},
	}
	for _, agent := range []string{store.AgentCodex, store.AgentCursor} {
		for _, tc := range cases {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				f := newV5Store(t)
				r := seedReuseRow(t, f, reuseSpec{opts: []apitest.SpawnOption{apitest.WithAgent(agent)}})
				mix := f.seedCloseMix(r.id) // before the injection, which fails the seed inserts too
				if tc.kind != 0 {
					storefix.InjectWriteFailure(t, f.path, tc.kind, r.id)
				}
				before := f.reuseStateOf(t, r)
				fresh := reuseFresh(tc.parent, false)
				if tc.noInstant {
					fresh.LaunchStartedAtMillis = 0
				}
				res, archived, v, err := f.reuseReset(r, fresh)
				if err == nil || res != 0 || archived != "" || v != 0 {
					t.Fatalf("ResetForReuse = %v, %q, %d, %v; want an error with no outcome", res, archived, v, err)
				}
				if got := errors.Is(err, store.ErrReuseArchive); got != tc.wantArchive {
					t.Errorf("errors.Is(%v, ErrReuseArchive) = %v; want %v", err, got, tc.wantArchive)
				}
				if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
					t.Errorf("a failed reset wrote:\n before %+v\n after  %+v", before, after)
				}
				f.assertBystanderKept(mix)
			})
		}
	}
}

// TestReuseResetConcurrent checks two stores on one file resetting one row
// from one snapshot: exactly one applies, the other sees CondChanged, no store
// error, one archived entry. Several rounds, a fresh row each.
func TestReuseResetConcurrent(t *testing.T) {
	f := newV5Store(t)
	s2, err := store.Open(f.path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	for _, agent := range []string{store.AgentCodex, store.AgentCursor} {
		for round := 0; round < 5; round++ {
			r := seedReuseRow(t, f, reuseSpec{opts: []apitest.SpawnOption{apitest.WithAgent(agent)}})
			var (
				wg    sync.WaitGroup
				start = make(chan struct{})
				res   [2]store.CondResult
				errs  [2]error
			)
			for i, s := range []*store.Store{f.s, s2} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					res[i], _, _, errs[i] = s.ResetForReuse(r.id, r.read.Snapshot, r.read.Agent, reuseFresh("", false))
				}()
			}
			close(start)
			wg.Wait()
			if errs[0] != nil || errs[1] != nil || (res != [2]store.CondResult{store.CondApplied, store.CondChanged} &&
				res != [2]store.CondResult{store.CondChanged, store.CondApplied}) {
				t.Fatalf("round %d: outcomes %v, errors %v; want one CondApplied and one CondChanged", round, res, errs)
			}
			if cur, _ := splitHistory(f.historyAllLives(r.id), reuseSessionID); len(cur) != 1 {
				t.Errorf("round %d: archived entries %+v; want exactly one", round, cur)
			}
			if after := f.rawColumns(r.id); after.LifeNumber != reuseLife+1 || after.RowVersion != r.read.Snapshot.RowVersion+1 {
				t.Errorf("round %d: life %#v, row_version %#v; want one reset applied", round, after.LifeNumber, after.RowVersion)
			}
		}
	}
}

// TestReuseResetAgentGuardAfterArchive models a trigger changing agent after the transactional read.
func TestReuseResetAgentGuardAfterArchive(t *testing.T) {
	f := newV5Store(t)
	r := seedReuseRow(t, f, reuseSpec{})
	mix := f.seedCloseMix(r.id)
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER change_agent_after_archive AFTER INSERT ON session_history
 BEGIN UPDATE spawns SET agent = 'claude' WHERE instance_id = NEW.instance_id; END`); err != nil {
		t.Fatal(err)
	}
	before := f.reuseStateOf(t, r)
	res, archived, version, err := f.reuseReset(r, reuseFresh("", false))
	if err == nil || res != 0 || archived != "" || version != 0 {
		t.Fatalf("reset = %v, %q, %d, %v; want error and no outcome", res, archived, version, err)
	}
	if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
		t.Errorf("reset after trigger edit wrote:\n before %+v\n after %+v", before, after)
	}
	f.assertBystanderKept(mix)
}
