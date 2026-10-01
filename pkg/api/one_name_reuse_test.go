package api_test

// one_name_reuse_test.go holds reuse's SR-1.5 one-name rows (Epic 17;
// SR-10.2 to SR-10.8): one per error spawn with the reuse opt-in returns, at
// the old-row lookup and new-name pre-check, the change, the create, and the
// re-lookup after "duplicate session", driven through Client.Spawn (or its
// reuse-store seam, for interleavings) on the kill fixture
// (spawn_reuse_fixture_test.go) and checked by assertOneName. They run under
// TestOneNameReuseReturnedErrors, so -run Reuse selects them.

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestOneNameReuseReturnedErrors: every error reuse returns matches exactly
// one catalogued sentinel, and each ErrInternal none (as TestOneNameReturnedErrors).
func TestOneNameReuseReturnedErrors(t *testing.T) {
	for _, row := range slices.Concat(oneNameReuseRows(), oneNameReuseHeldRows()) {
		t.Run(row.name, func(t *testing.T) { assertOneName(t, row.run(t), row.want) })
	}
}

// oneReuseNewName is the new session name a pre-check row requests.
const oneReuseNewName = "one-reuse-new"

// oneNameReuseSetup shapes e, r and the request q before the reuse; a
// non-nil store it returns is the reuse path's (api.SpawnWithReuseStore).
type oneNameReuseSetup func(t *testing.T, e *killEnv, r *reuseRow, q *reuseRequest) api.ReuseStore

// oneNameReuse is a row that reuses a row that ended age before the rule's
// instant, its agent in state a, requesting its recorded name unless setup
// (when set) says otherwise.
func oneNameReuse(name, want string, age time.Duration, a agentState, setup oneNameReuseSetup) oneNameRow {
	return oneNameRow{name: "spawn Reuse/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedReusable(t, a, reuseRowSpec{Age: age})
		var q reuseRequest
		var rs api.ReuseStore
		if setup != nil {
			rs = setup(t, e, &r, &q)
		}
		var err error
		if rs != nil {
			_, _, err = e.reuseWith(t, rs, reuseParams(t, r, q))
		} else {
			_, _, err = e.reuse(t, reuseParams(t, r, q))
		}
		return err
	}}
}

// oneReuseOnRow is setup running f on e and the row's killRow.
func oneReuseOnRow(f func(t *testing.T, e *killEnv, r *killRow)) oneNameReuseSetup {
	return func(t *testing.T, e *killEnv, r *reuseRow, _ *reuseRequest) api.ReuseStore {
		f(t, e, &r.killRow)
		return nil
	}
}

// oneReuseHooked is setup giving the reuse a hookedReuseStore that hook arranges.
func oneReuseHooked(hook func(t *testing.T, e *killEnv, r *reuseRow, w *hookedReuseStore)) oneNameReuseSetup {
	return func(t *testing.T, e *killEnv, r *reuseRow, _ *reuseRequest) api.ReuseStore {
		w := &hookedReuseStore{st: e.st}
		hook(t, e, r, w)
		return w
	}
}

// oneReuseChange is fn's store write on r's row, failing the test on error.
func oneReuseChange(t *testing.T, r *reuseRow, fn func(id string) error) func() {
	return func() {
		if err := fn(r.ID); err != nil {
			t.Errorf("changing %s during the reuse: %v", r.ID, err)
		}
	}
}

// oneNameReuseRows are reuse's errors before and at the launch (SR-10.2 to
// SR-10.5, SR-10.8, SR-4.2, SR-5.8): each conflict and unanswered refusal,
// tmux not available, a failed or timed-out create, the live row, the lost
// races, the change's ErrInternal failures and an unusable recorded name's.
func oneNameReuseRows() []oneNameRow {
	conflict, unresponsive, unavailable := "ErrTmuxSessionConflict", "ErrTmuxUnresponsive", "ErrTmuxNotAvailable"
	created, collision, hour := "ErrTmuxSessionCreate", "ErrInstanceIdCollision", time.Hour
	session := func(age time.Duration) oneNameReuseSetup {
		return oneReuseOnRow(func(t *testing.T, e *killEnv, r *killRow) { e.seedSession(t, r, e.createdBefore(age)) })
	}
	held := func(k holderKind) oneNameReuseSetup {
		return func(t *testing.T, e *killEnv, r *reuseRow, q *reuseRequest) api.ReuseStore {
			q.Name = oneReuseNewName
			e.seedHolder(t, r.withName(q.Name), k)
			return nil
		}
	}
	script := func(call tmux.Call, s tmuxfix.Script) oneNameReuseSetup { return oneReuseOnRow(scriptKill(call, s)) }
	lookup, create := tmux.CallLookup, tmux.CallCreate
	inject := func(kind storefix.WriteFailureKind) oneNameReuseSetup {
		return oneReuseOnRow(func(t *testing.T, e *killEnv, r *killRow) { storefix.InjectWriteFailure(t, e.dbPath, kind, r.ID) })
	}
	unparseable := tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, HadStdout: true}
	return []oneNameRow{
		oneNameReuse("leftover", conflict, hour, agentGone, oneReuseOnRow(func(t *testing.T, e *killEnv, r *killRow) {
			e.seedHolder(t, *r, holderOld)
		})),
		oneNameReuse("pre-check: an earlier life of this id", conflict, hour, agentGone, held(holderOld)),
		oneNameReuse("pre-check: a different instance id", conflict, hour, agentGone, held(holderForeign)),
		oneNameReuse("pre-check: another agent-director store", conflict, hour, agentGone, held(holderOtherStore)),
		oneNameReuse("pre-check: no valid instance id", conflict, hour, agentGone, held(holderNone)),
		oneNameReuse("conflicting labels", conflict, hour, agentGone, oneReuseOnRow(func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		})),
		oneNameReuse("own id, its old session", conflict, hour, agentAlive, session(hour)),
		oneNameReuse("own id, no session and the process running", conflict, hour, agentAlive, nil),
		oneNameReuse("still stopping", unresponsive, 0, agentAlive, session(hour)),
		oneNameReuse("still starting", unresponsive, hour, agentAlive, session(0)),
		oneNameReuse("lookup timeout", unresponsive, hour, agentAlive, script(lookup, tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameReuse("lookup unrecognised reply", unresponsive, hour, agentAlive, script(lookup, tmuxfix.Script{
			Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1})),
		oneNameReuse("pre-check: more than one entry matches", unresponsive, hour, agentGone, held(holderAmbiguous)),
		oneNameReuse("create: timeout", unresponsive, hour, agentGone, script(create, tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameReuse("create: non-zero exit, unparseable reply", unresponsive, hour, agentGone, script(create, unparseable)),
		oneNameReuse("different server", unavailable, hour, agentAlive, oneReuseOnRow(rebindServer)),
		oneNameReuse("lookup: binary unavailable", unavailable, hour, agentAlive,
			script(lookup, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameReuse("lookup: socket permission", unavailable, hour, agentAlive,
			script(lookup, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameReuse("create: binary unavailable", unavailable, hour, agentGone,
			script(create, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameReuse("create: socket permission", unavailable, hour, agentGone,
			script(create, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameReuse("create: no server", created, hour, agentGone, script(create, tmuxfix.Script{Failure: tmux.FailNoServer})),
		oneNameReuse("create: session not labelled", created, hour, agentGone, oneReuseOnRow(func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create).
				Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel)
		})),
		oneNameReuse("live row: pending after a reuse", collision, hour, agentGone,
			func(t *testing.T, e *killEnv, r *reuseRow, _ *reuseRequest) api.ReuseStore {
				*r = e.reusePending(t, agentAlive, reuseRowSpec{Age: hour}, reuseRequest{})
				return nil
			}),
		oneNameReuse("row changed before the reset", collision, hour, agentGone,
			oneReuseHooked(func(t *testing.T, e *killEnv, r *reuseRow, w *hookedReuseStore) {
				w.beforeReset(oneReuseChange(t, r, func(id string) error { return e.st.SetParentID(id, "") }))
			})),
		oneNameReuse("row removed before the reset", collision, hour, agentGone,
			oneReuseHooked(func(t *testing.T, e *killEnv, r *reuseRow, w *hookedReuseStore) {
				w.beforeReset(oneReuseChange(t, r, e.st.DeleteSpawn))
			})),
		oneNameReuse("leftover, the row changed before the re-read", collision, hour, agentGone,
			oneReuseHooked(func(t *testing.T, e *killEnv, r *reuseRow, w *hookedReuseStore) {
				e.seedHolder(t, r.killRow, holderOld)
				w.afterRead(oneReuseChange(t, r, func(id string) error { return e.st.SetParentID(id, "") }))
			})),
		oneNameReuse("archive fails", "", hour, agentGone, inject(storefix.WriteFailReuseArchive)),
		oneNameReuse("reset fails", "", hour, agentGone, inject(storefix.WriteFailReuseReset)),
		oneNameReuse("permission-request delete fails", "", hour, agentGone, inject(storefix.WriteFailReusePermissionDelete)),
		oneNameReuse("unusable recorded name", "", hour, agentGone,
			func(t *testing.T, e *killEnv, r *reuseRow, q *reuseRequest) api.ReuseStore {
				*r = e.seedReusable(t, agentGone, reuseRowSpec{Age: hour,
					Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(preGqeDefaultName)}})
				q.Name = oneReuseNewName // Spawn refuses an unusable requested name before the reuse path
				return nil
			}),
	}
}

// oneNameReuseHeld is a row that reuses a row whose agent is gone and that
// ended age before the re-lookup, its create answering "duplicate session" as
// arrangeHeld sets up spec.
func oneNameReuseHeld(name, want string, age time.Duration, spec heldSpec) oneNameRow {
	return oneNameRow{name: "spawn Reuse/after duplicate session: " + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedReusable(t, agentGone, reuseRowSpec{Age: age, Held: true})
		e.arrangeHeld(t, r.resumeRow, spec)
		_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
		return err
	}}
}

// oneNameReuseHeldRows are reuse's errors after "duplicate session" (SR-10.4,
// SR-4.2, SR-3.10): each holder class, the starting-session rule's three
// outcomes, an ambiguous or unreadable re-lookup, tmux not available, and the
// vanished holder.
func oneNameReuseHeldRows() []oneNameRow {
	conflict, unresponsive, unavailable := "ErrTmuxSessionConflict", "ErrTmuxUnresponsive", "ErrTmuxNotAvailable"
	stopping, hour := config.Tmux{}.EffectiveStoppingWindow()/2, time.Hour
	relookup := func(s tmuxfix.Script) heldSpec { return heldSpec{Holder: holderForeign, Relookup: s} }
	return []oneNameRow{
		oneNameReuseHeld("leftover", conflict, hour, heldSpec{Holder: holderOld}),
		oneNameReuseHeld("a different instance id", conflict, hour, heldSpec{Holder: holderForeign}),
		oneNameReuseHeld("another agent-director store", conflict, hour, heldSpec{Holder: holderOtherStore}),
		oneNameReuseHeld("no valid instance id", conflict, hour, heldSpec{Holder: holderNone}),
		oneNameReuseHeld("conflicting labels", conflict, hour, heldSpec{Holder: holderConflicting}),
		oneNameReuseHeld("own id", conflict, hour, heldSpec{Holder: holderCurrent, Created: hour}),
		oneNameReuseHeld("still stopping", unresponsive, stopping, heldSpec{Holder: holderCurrent, Created: hour}),
		oneNameReuseHeld("still starting", unresponsive, hour, heldSpec{Holder: holderCurrent}),
		oneNameReuseHeld("ambiguous holder", unresponsive, hour, heldSpec{Holder: holderAmbiguous}),
		oneNameReuseHeld("re-lookup timeout", unresponsive, hour, relookup(tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameReuseHeld("re-lookup unrecognised reply", unresponsive, hour, relookup(tmuxfix.Script{
			Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1})),
		oneNameReuseHeld("different server", unavailable, hour, heldSpec{Holder: holderForeign, Server: heldServerRebound}),
		oneNameReuseHeld("tmux unavailable", unavailable, hour, relookup(tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameReuseHeld("socket permission", unavailable, hour, relookup(tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameReuseHeld("holder vanished", "ErrTmuxSessionCreate", hour, heldSpec{Holder: holderVanished}),
	}
}
