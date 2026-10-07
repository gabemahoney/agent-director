package api_test

// spawn_reuse_change_fail_test.go covers a reuse change that does not apply
// (SR-10.3, SR-10.5, SR-5.8, SR-22.9; AC-REUSE-04): a store failure is
// ErrInternal with the archive or reuse-change wording, and a row changed or
// removed after the lookup is ErrInstanceIdCollision, each with no create and
// nothing written; a hook from another process changes nothing and the reuse
// proceeds. The applied reuse is in spawn_reuse_change_test.go.

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rchgNewName is a requested session name no session holds.
func rchgNewName() string { return "newname-" + uuid.NewString()[:8] }

// rchgAfterLookup runs fn once, when the first lookup on socket returns.
func (e *killEnv) rchgAfterLookup(socket string, fn func()) {
	done := false
	e.rec.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, _ error) {
		if !done && c.Socket == socket {
			done = true
			fn()
		}
	})
}

// TestSpawnReuseStoreFailureIsInternal: an archive failure, a reset failure
// and a permission-request deletion failure are ErrInternal, the archive's
// with its own wording, with no create, no ad.spawn.reused and nothing written.
func TestSpawnReuseStoreFailureIsInternal(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name string
		kind storefix.WriteFailureKind
		want func(instanceID string) apitest.DescCase
	}{
		{"archive", storefix.WriteFailReuseArchive, apitest.DescReuseArchiveFailure},
		{"reset", storefix.WriteFailReuseReset, apitest.DescReuseChangeFailure},
		{"permission-request deletion", storefix.WriteFailReusePermissionDelete, apitest.DescReuseChangeFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			storefix.InjectWriteFailure(t, e.dbPath, tc.kind, r.ID)
			before := e.snapshotReuse(t, r)

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{Name: rchgNewName()}))

			rtabAssertInternal(t, err, tc.want(r.ID))
			e.assertWroteNothing(t, before, exceptTrust)
			if recs := before.since(t, "ad.spawn.reused"); len(recs) != 0 {
				t.Errorf("ad.spawn.reused records = %v; want none", recs)
			}
		})
	}
}

// TestSpawnReuseLostRaceAtReset: a gated write by the row's own agent after
// the lookup, or another versioned write just before the reset, makes the
// reset find the row changed: ErrInstanceIdCollision, no create, nothing written.
func TestSpawnReuseLostRaceAtReset(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name    string
		arrange func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore, write func(func()))
	}{
		{"own agent's hook after the lookup", func(t *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore, write func(func())) {
			e.rchgAfterLookup(r.Socket, func() {
				write(func() {
					if got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "Notification", r.Spawn.ClaudeSessionID); !got.Applied {
						t.Fatalf("own agent's hook = %+v; want applied", got)
					}
				})
			})
		}},
		{"parent-id write before the reset", func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore, write func(func())) {
			w.beforeReset(func() {
				write(func() {
					if err := e.st.SetParentID(r.ID, ""); err != nil {
						t.Fatalf("SetParentID(%s): %v", r.ID, err)
					}
				})
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			w := &hookedReuseStore{st: e.st}
			var before *writesSnapshot
			tc.arrange(t, e, r, w, func(fn func()) {
				fn()
				s := e.snapshotReuse(t, r)
				before = &s
			})

			_, _, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rchgNewName()}))

			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID))
			if before == nil {
				t.Fatal("the competing write never ran")
			}
			e.assertWroteNothing(t, *before, exceptTrust)
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("create calls = %d; want 0", n)
			}
		})
	}
}

// TestSpawnReuseRowRemovedAtReset: a row deleted after the lookup is
// ErrInstanceIdCollision with no create, no row, history or request written
// back, and no ad.spawn.reused.
func TestSpawnReuseRowRemovedAtReset(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{})
	sessions := e.rec.Sessions(r.Socket)
	mark := trailMark(t)
	e.rchgAfterLookup(r.Socket, func() {
		if err := e.st.DeleteSpawn(r.ID); err != nil {
			t.Fatalf("DeleteSpawn(%s): %v", r.ID, err)
		}
	})

	_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{Name: rchgNewName()}))

	assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
	apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID))
	if _, rerr := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(rerr, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns(%s) err = %v; want no row", r.ID, rerr)
	}
	if h, herr := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID); herr != nil || len(h) != 0 {
		t.Errorf("history = %+v (%v); want none", h, herr)
	}
	if perms, perr := e.st.PermissionRequestsForSpawn(r.ID); perr != nil || len(perms) != 0 {
		t.Errorf("permission requests = %+v (%v); want none", perms, perr)
	}
	if got := pendCallKinds(e.rec); len(got) != 1 || got[0] != tmux.CallLookup || len(e.rec.Sessions(r.Socket)) != len(sessions) {
		t.Errorf("tmux calls %v, sessions %d; want one lookup and the %d seeded sessions", got, len(e.rec.Sessions(r.Socket)), len(sessions))
	}
	if recs := ptRecords(t, mark, "ad.spawn.reused", r.ID); len(recs) != 0 {
		t.Errorf("ad.spawn.reused records = %v; want none", recs)
	}
}

// TestSpawnReuseForeignHookIgnored: a hook from another process carrying the
// row's id after the lookup is not applied (pid_mismatch), and the reuse proceeds.
func TestSpawnReuseForeignHookIgnored(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{})
	var hook store.HookApplied
	e.rchgAfterLookup(r.Socket, func() {
		hook = apitest.ApplyForeignHook(t, e.dbPath, r.ID, "Notification", "sess-"+uuid.NewString()[:8])
	})
	before := e.snapshotReuse(t, r)

	res, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{Name: rchgNewName()}))

	if err != nil || res.ClaudeInstanceID != r.ID {
		t.Fatalf("reuse = %+v, %v (log %q); want success", res, err, logs)
	}
	if hook.Applied || hook.Reason != store.HookReasonPIDMismatch {
		t.Errorf("foreign hook = %+v; want not applied, %s", hook, store.HookReasonPIDMismatch)
	}
	cols := e.columns(t, r.ID)
	if cols.State != store.StatePending || cols.LifeNumber != reuseLife+1 || len(e.rec.SocketCallsOf(tmux.CallCreate)) != 1 {
		t.Errorf("row state %v, life %v; want pending at life %d after one create", cols.State, cols.LifeNumber, reuseLife+1)
	}
	if recs := before.since(t, "ad.spawn.reused"); len(recs) != 1 {
		t.Errorf("ad.spawn.reused records = %v; want 1", recs)
	}
}
