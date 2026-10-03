package api_test

// advice_follow_spawn_test.go follows, literally, the next step plain spawn's
// error descriptions and spawn's manifest texts prescribe (b.fji, inventory
// A1-A8): each test triggers the error, checks the advice phrase, does what it
// says and checks the promised outcome. Reuse's (A9-A13) are in
// advice_follow_spawn_reuse_test.go; knownBrokenAdvice gates a step that does
// not work as written. It uses the held-name fixture (spawn_held_test.go) and
// the shared helpers (advice_follow_helpers_test.go).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advSpawnHeldName is the requested name the held-name tests find held.
const advSpawnHeldName = "adv-held"

// advSpawnParams is a plain spawn of id (minted when "") in a fresh cwd,
// requesting name unless it is "".
func advSpawnParams(t *testing.T, id, name string) api.SpawnParams {
	t.Helper()
	p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id}
	if name != "" {
		p.TmuxSessionName, p.TmuxSessionNameSupplied = name, true
	}
	return p
}

// advSpawnReuse is p with the reuse opt-in, the retry the advice names.
func advSpawnReuse(p api.SpawnParams) api.SpawnParams {
	p.ReuseFinished = true
	return p
}

// advSpawnRelookupTimesOut makes the re-lookup after "duplicate session"
// (the lookup after the scan) time out once.
func advSpawnRelookupTimesOut(e heldEnv) {
	adviceOnceAfter(e.rec, tmux.CallLookup, func() {
		e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallLookup)
	})
}

// advSpawnLaunched fails unless the spawn returned id with no error and its
// row is pending, in life wantLife when that is not nil.
func advSpawnLaunched(t *testing.T, dbPath, id string, wantLife any, res api.SpawnResult, err error) {
	t.Helper()
	if err != nil || res.ClaudeInstanceID != id {
		t.Fatalf("spawn = %+v, %v; want %s launched", res, err, id)
	}
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	if cols.State != store.StatePending || (wantLife != nil && cols.LifeNumber != wantLife) {
		t.Errorf("row %s: state %v, life %v; want pending, life %v", id, cols.State, cols.LifeNumber, wantLife)
	}
}

// advSpawnNextLife is the life a reuse of cols' row starts.
func advSpawnNextLife(cols apitest.SpawnColumns) any {
	life, _ := cols.LifeNumber.(int64)
	return life + 1
}

// TestAdviceFollow_A1_LaunchTimeoutRetryAfterFinished: A1 "the session may have been created; the row stays pending; do not retry until get shows the row ended or missing".
func TestAdviceFollow_A1_LaunchTimeoutRetryAfterFinished(t *testing.T) {
	const phrase = "the session may have been created; the row stays pending; do not retry until get shows the row ended or missing"
	cases := []struct {
		name, id string
		broken   string // why the literal retry fails, gated by knownBrokenAdvice; "" = it works
	}{
		{name: "minted id"},
		{name: "explicit id", id: heldID(), broken: "the identical plain spawn of an explicit id collides with its finished row (ErrInstanceIdCollision); " +
			"only a retry with the reuse opt-in works, and the description does not name it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallCreate)
			p := advSpawnParams(t, tc.id, "")

			_, err := e.c.Spawn(p)

			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, phrase)
			id := e.rec.SocketCallsOf(tmux.CallCreate)[0].InstanceID
			adviceAwaitFinished(t, e.c, e.clock, id, nil)
			if tc.broken != "" {
				knownBrokenAdvice(t, "A1", tc.broken)
			}

			res, err := e.c.Spawn(p)

			if err != nil {
				t.Fatalf("identical spawn after get showed the row finished = %v; want it to launch", err)
			}
			if tc.id == "" && res.ClaudeInstanceID == id {
				t.Errorf("retry's id = %s; want a newly minted one", id)
			}
			advSpawnLaunched(t, e.dbPath, res.ClaudeInstanceID, nil, res, err)
		})
	}
}

// advSpawnScanRetry runs a plain spawn of a new id refused by its label scan
// after setup, checks want and phrase and that no row was written, then
// re-issues the identical spawn while the condition holds (the same refusal)
// and once clear has cleared it, which must launch it.
func advSpawnScanRetry(t *testing.T, want error, phrase string, setup func(e heldEnv, id string), clear func(e heldEnv)) {
	t.Helper()
	e := newHeldEnv(t)
	id := heldID()
	setup(e, id)
	p := advSpawnParams(t, id, "")

	_, err := e.c.Spawn(p)

	adviceAssertAdvice(t, err, want, phrase)
	if _, rerr := apitest.ReadSpawnColumns(e.dbPath, id); !errors.Is(rerr, store.ErrSpawnNotFound) {
		t.Fatalf("ReadSpawnColumns(%s) = %v; want no row written", id, rerr)
	}
	e.clock.Advance(time.Second)
	if _, again := e.c.Spawn(p); again == nil || again.Error() != err.Error() {
		t.Fatalf("retry while the condition holds = %v; want the same refusal %q", again, err)
	}
	clear(e)

	res, err := e.c.Spawn(p)

	advSpawnLaunched(t, e.dbPath, id, nil, res, err)
}

// TestAdviceFollow_A2_ScanUnreadableRetryLater: A2 "nothing was done: nothing was written and no row was created; retry later".
func TestAdviceFollow_A2_ScanUnreadableRetryLater(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script tmuxfix.Script
	}{
		{"timeout", tmuxfix.Script{Failure: tmux.FailTimeout}},
		{"unrecognised reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: "scan: odd reply", ExitStatus: 1, HadStdout: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.script
			s.Times = 2 // the first spawn and its retry while tmux still cannot answer
			advSpawnScanRetry(t, api.ErrTmuxUnresponsive, "nothing was done: nothing was written and no row was created; retry later",
				func(e heldEnv, _ string) { e.rec.Script(e.socket, s, tmux.CallLookup) },
				func(e heldEnv) { e.clock.Advance(time.Second) })
		})
	}
}

// TestAdviceFollow_A3_ScanLeftoverHumanEnds: A3 "ending such a session is a human's decision, see "Operator actions" in the agent-director README".
func TestAdviceFollow_A3_ScanLeftoverHumanEnds(t *testing.T) {
	const leftover = "$3"
	advSpawnScanRetry(t, api.ErrTmuxSessionConflict,
		`nothing was written and no row was created; ending such a session is a human's decision, see "Operator actions" in the agent-director README`,
		func(e heldEnv, id string) { e.rec.SeedSessions(e.socket, e.leftover("adv-old-life", leftover, id, 0)) },
		func(e heldEnv) { adviceEndSession(t, e.rec, e.socket, leftover) })
}

// advSpawnFreeName ends every session on e's socket, as a human or the
// holder's own exit frees the requested name.
func advSpawnFreeName(t *testing.T, e heldEnv) {
	t.Helper()
	for _, s := range e.rec.Sessions(e.socket) {
		adviceEndSession(t, e.rec, e.socket, s.ID)
	}
}

// TestAdviceFollow_A4_HeldUnreadableReuseOnceFree: A4 "the new row was ended; a retry with this id uses the reuse opt-in (reuse_finished) once the name is free, since a plain spawn of the id now collides".
func TestAdviceFollow_A4_HeldUnreadableReuseOnceFree(t *testing.T) {
	const phrase = "the new row was ended; a retry with this id uses the reuse opt-in (reuse_finished) once the name is free, since a plain spawn of the id now collides"
	holder := func(id string) tmuxfix.SeedSession { return heldSession(advSpawnHeldName, id, tmux.Label{}, false) }
	cases := []struct {
		name      string
		holders   []tmuxfix.SeedSession
		timeout   bool  // the re-lookup times out
		whileHeld error // the opted-in retry's refusal while the name is still held
	}{
		{"re-lookup timeout", []tmuxfix.SeedSession{holder("$4")}, true, api.ErrTmuxSessionConflict},
		{"more than one session matches the name", []tmuxfix.SeedSession{holder("$4"), holder("$5")}, false, api.ErrTmuxUnresponsive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			e.rec.SeedSessions(e.socket, tc.holders...)
			if tc.timeout {
				advSpawnRelookupTimesOut(e)
			}
			p := advSpawnParams(t, id, advSpawnHeldName)

			_, err := e.c.Spawn(p)

			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, phrase)
			ended := e.readRow(t, id)
			if ended.State != store.StateEnded {
				t.Fatalf("row state = %v; want ended", ended.State)
			}
			_, err = e.c.Spawn(p)
			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			_, err = e.c.Spawn(advSpawnReuse(p))
			assertOneSentinel(t, err, tc.whileHeld)
			e.assertRowIs(t, id, "after the opted-in retry while the name is held", ended)

			advSpawnFreeName(t, e)
			res, err := e.c.Spawn(advSpawnReuse(p))

			advSpawnLaunched(t, e.dbPath, id, advSpawnNextLife(ended), res, err)
		})
	}
}

// TestAdviceFollow_A5_HeldUnendedRowRetryAfterFinished: A5 "the new row changed after this spawn inserted it and was left as it is" / "the new row could not be ended and stays pending"; "do not retry until get shows the row ended or missing".
func TestAdviceFollow_A5_HeldUnendedRowRetryAfterFinished(t *testing.T) {
	cases := []struct {
		name, row string
		arrange   func(t *testing.T, e heldEnv, id string) // before the spawn, in the trigger's subtest
	}{
		{"end write not applied", "the new row changed after this spawn inserted it and was left as it is",
			func(t *testing.T, e heldEnv, id string) {
				adviceOnceAfter(e.rec, tmux.CallCreate, func() { apitest.SeedSessionID(t, e.dbPath, id, uuid.NewString()) })
			}},
		{"end write failed", "the new row could not be ended and stays pending", func(t *testing.T, e heldEnv, id string) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			e.rec.SeedSessions(e.socket, heldSession(advSpawnHeldName, "$4", tmux.Label{}, false))
			advSpawnRelookupTimesOut(e)
			p := advSpawnParams(t, id, advSpawnHeldName)
			if !t.Run("trigger", func(t *testing.T) { // an injected write failure is removed when it ends
				tc.arrange(t, e, id)
				_, err := e.c.Spawn(p)
				adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, tc.row+"; do not retry until get shows the row ended or missing")
			}) {
				t.FailNow()
			}
			advSpawnFreeName(t, e)
			adviceAwaitFinished(t, e.c, e.clock, id, nil)
			knownBrokenAdvice(t, "A5", "once get shows the row finished, the identical plain spawn of its explicit id collides with it "+
				"(ErrInstanceIdCollision); only a retry with the reuse opt-in works, and the description does not name it")

			res, err := e.c.Spawn(p)

			advSpawnLaunched(t, e.dbPath, id, nil, res, err)
		})
	}
}

// TestAdviceFollow_A6_HolderVanishedReuseRetry: A6 "no session held the name when it was looked up again; the new row was ended" names no
// retry; reuse-finished's "after a held-name refusal (duplicate session) the row is already ended unless the error says otherwise".
func TestAdviceFollow_A6_HolderVanishedReuseRetry(t *testing.T) {
	e := newHeldEnv(t)
	id := heldID()
	e.rec.SeedSessions(e.socket, heldSession(advSpawnHeldName, "$4", tmux.Label{}, false))
	e.rec.RemoveSessionAfter(tmux.CallCreate, e.socket, "$4")
	p := advSpawnParams(t, id, advSpawnHeldName)

	_, err := e.c.Spawn(p)

	adviceAssertAdvice(t, err, api.ErrTmuxSessionCreate, "no session held the name when it was looked up again; the new row was ended")
	adviceAssertManifest(t, "spawn", "reuse-finished", "after a held-name refusal (duplicate session) the row is already ended unless the error says otherwise, "+
		"so the retry's lookup decides it at once")
	ended := e.readRow(t, id)
	res, rerr := e.c.Spawn(advSpawnReuse(p))
	advSpawnLaunched(t, e.dbPath, id, advSpawnNextLife(ended), res, rerr)

	knownBrokenAdvice(t, "A6", "the description names no next step, so a caller reading it alone has none (a plain retry collides "+
		"with the ended row); b.fji wants the reuse-retry sentence on every plain-spawn error after \"duplicate session\"")
	adviceAssertPhrase(t, err, "a retry with this id uses the reuse opt-in (reuse_finished) once the name is free")
}

// TestAdviceFollow_A7_HeldConflictHumanEndsThenReuse: A7 "ending the session is a human's decision, ..." / "a human must look, ..." and spawn's
// "then, if the refusal was for a held name, spawn the id again with --reuse-finished".
func TestAdviceFollow_A7_HeldConflictHumanEndsThenReuse(t *testing.T) {
	const (
		decision = `ending the session is a human's decision, see "Operator actions" in the agent-director README`
		look     = `a human must look, see "Operator actions" in the agent-director README`
	)
	cases := []struct {
		name, phrase string
		leftover     bool // the holder is a leftover of this id, placed as the scan returns
		failEnd      bool // the end write fails: the row stays pending
	}{
		{"leftover, row ended", "the new row was ended; " + decision, true, false},
		{"no valid instance id, row ended", "the new row was ended; " + look, false, false},
		{"no valid instance id, row stays pending", "the new row could not be ended and stays pending; " + look, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			if tc.leftover {
				adviceOnceAfter(e.rec, tmux.CallLookup, func() { e.rec.SeedSessions(e.socket, e.leftover(advSpawnHeldName, "$4", id, 0)) })
			} else {
				e.rec.SeedSessions(e.socket, heldSession(advSpawnHeldName, "$4", tmux.Label{}, false))
			}
			p := advSpawnParams(t, id, advSpawnHeldName)
			if !t.Run("trigger", func(t *testing.T) { // an injected write failure is removed when it ends
				if tc.failEnd {
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
				}
				_, err := e.c.Spawn(p)
				adviceAssertAdvice(t, err, api.ErrTmuxSessionConflict, tc.phrase)
			}) {
				t.FailNow()
			}
			adviceAssertManifest(t, "spawn", "", "a leftover of an earlier life, or a session with no valid instance id, is a human's to end "+
				`(see the README's "Operator actions"); then, if the refusal was for a held name, spawn the id again with --reuse-finished`)
			advSpawnFreeName(t, e)
			before := e.readRow(t, id)
			if tc.failEnd {
				// reuse-finished: a pending row collides until find-missing marks it missing past the pending grace period.
				_, err := e.c.Spawn(advSpawnReuse(p))
				assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
				e.assertRowIs(t, id, "after the opted-in retry of the pending row", before)
				adviceAwaitFinished(t, e.c, e.clock, id, nil)
			}

			res, err := e.c.Spawn(advSpawnReuse(p))

			advSpawnLaunched(t, e.dbPath, id, advSpawnNextLife(before), res, err)
		})
	}
}

// TestAdviceFollow_A8_PendingRowReuseAfterGrace: A8 "after any other failed launch the row stays pending and an opted-in retry collides
// until find-missing marks it missing, which happens only after the pending grace period (60 s by default)".
func TestAdviceFollow_A8_PendingRowReuseAfterGrace(t *testing.T) {
	cases := []struct {
		name    string
		scripts map[tmux.Call]tmuxfix.Script // each answers once
		want    error
	}{
		{"launch timeout", map[tmux.Call]tmuxfix.Script{tmux.CallCreate: {Failure: tmux.FailTimeout}}, api.ErrTmuxUnresponsive},
		{"create failed", map[tmux.Call]tmuxfix.Script{tmux.CallCreate: {Failure: tmux.FailNoServer}}, api.ErrTmuxSessionCreate},
		{"tmux unavailable", map[tmux.Call]tmuxfix.Script{tmux.CallCreate: {Failure: tmux.FailUnavailable}}, api.ErrTmuxNotAvailable},
		{"session could not be labelled", map[tmux.Call]tmuxfix.Script{tmux.CallCreate: {Failure: tmux.FailLabel},
			tmux.CallSetLabel: {Failure: tmux.FailTimeout}}, api.ErrTmuxSessionCreate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			for call, s := range tc.scripts {
				s.Times = 1
				e.rec.Script(e.socket, s, call)
			}
			p := advSpawnParams(t, id, "")

			_, err := e.c.Spawn(p)

			adviceAssertAdvice(t, err, tc.want, "the row stays pending")
			adviceAssertManifest(t, "spawn", "reuse-finished", "after any other failed launch the row stays pending and an opted-in retry collides "+
				"until find-missing marks it missing, which happens only after the pending grace period (60 s by default)")
			pending := e.readRow(t, id)
			launch, _ := pending.LaunchStartedAt.(int64)
			insideGrace := func(when string) {
				if res, err := e.c.FindMissing(context.Background()); err != nil || res.Count != 0 {
					t.Fatalf("FindMissing %s = %+v, %v; want nothing marked", when, res, err)
				}
				_, err := e.c.Spawn(advSpawnReuse(p))
				assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
				e.assertRowIs(t, id, "after the opted-in retry "+when, pending)
			}
			insideGrace("right after the failure")
			e.clock.Advance(time.UnixMilli(launch).Add(fmGrace - time.Second).Sub(e.clock.Now()))
			insideGrace("at launch start + grace - 1 s")
			e.clock.Advance(2 * time.Second)
			if res, err := e.c.FindMissing(context.Background()); err != nil || res.Count != 1 || res.IDs[0] != id {
				t.Fatalf("FindMissing past grace = %+v, %v; want %s marked missing", res, err, id)
			}

			res, err := e.c.Spawn(advSpawnReuse(p))

			advSpawnLaunched(t, e.dbPath, id, advSpawnNextLife(pending), res, err)
		})
	}
}
