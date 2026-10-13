package api_test

// pause_codex_test.go: pause on Codex rows (SR-6.5, SR-7.2, SR-7.3, MC-4, RN-19). A Codex agent's SessionEnd
// only records an end report, so pause moves the row to ended itself: before its state rules when the agent
// has already exited, and in each poll of its wait. The move is find-missing's (the same request close and
// records; assertMoveRecords) and is judged on the agent process alone. Also its line clear (127 C-u in one
// call) and a Claude row, which pause never writes. The GONE paths of send-keys and read-pane after a Codex
// exit are TestKeysVerbsCodexExitedGone's, the E8 retry advice_follow_pane_test.go's.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pcxAssertNoCallEvent fails when a trail record after mark for id is a pause or send-keys call event.
func pcxAssertNoCallEvent(t *testing.T, mark int, id string) {
	t.Helper()
	for _, l := range readAPITrailLines(t)[mark:] {
		ev, _ := l["event"].(string)
		if l["instance_id"] == id && (strings.HasPrefix(ev, "ad.pause.") || ev == "ad.send_keys.called") {
			t.Errorf("trail record %s for %s; want no call event", ev, id)
		}
	}
}

// pcxAfterLastDisagree returns the trail position just past id's last ad.provenance.disagree record at or after
// mark (mark itself when there is none).
func pcxAfterLastDisagree(t *testing.T, mark int, id string) int {
	t.Helper()
	after := mark
	for i, l := range readAPITrailLines(t)[mark:] {
		if l["instance_id"] == id && l["event"] == "ad.provenance.disagree" {
			after = mark + i + 1
		}
	}
	return after
}

// pcxSeedRequests seeds n open permission requests on id and returns their tokens in request-id order.
func pcxSeedRequests(t *testing.T, e *killEnv, id string, n int) []string {
	t.Helper()
	var tokens []string
	for range n {
		perm, err := apitest.SeedPermissionRequest(e.dbPath, id, "Bash")
		if err != nil {
			t.Fatalf("SeedPermissionRequest: %v", err)
		}
		tokens = append(tokens, perm.RequestToken)
	}
	return tokens
}

// TestPauseCodexExitedBeforePause: a Codex row in any live state whose agent has exited (its SessionEnd
// recorded, its process gone) is moved to ended before the state rules, the relay guard and every tmux call:
// success, zero tmux calls, no wait, SR-6.5's records and its requests closed denied ended. With no end
// report today's rules apply: pending, working and check_permission are ErrSpawnNotPausable, and a waiting
// row's keys find no session (ErrTmuxSendKeys).
func TestPauseCodexExitedBeforePause(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		state    string
		relay    bool
		requests int
		noReport string // the refusal with no end report
	}{
		{name: "pending", state: store.StatePending, noReport: "ErrSpawnNotPausable"},
		{name: "waiting", state: store.StateWaiting, noReport: "ErrTmuxSendKeys"},
		{name: "working", state: store.StateWorking, noReport: "ErrSpawnNotPausable"},
		{name: "check_permission", state: store.StateCheckPermission, noReport: "ErrSpawnNotPausable"},
		{name: "check_permission, relay on, an undecided relayed request", state: store.StateCheckPermission, relay: true,
			requests: 1, noReport: "ErrSpawnNotPausable"},
	}
	for _, tc := range cases {
		for _, report := range []bool{true, false} {
			name := tc.name + "/no end report"
			if report {
				name = tc.name + "/end report"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				spec := codexSpec(tc.state, agentGone, report)
				if tc.state == store.StatePending {
					spec = e.pendingSpec(pendingOurs, spec.Opts...)
					spec.Agent = agentGone
				}
				spec.RelayOn, spec.NoSession = tc.relay, tc.state == store.StateWaiting
				r := e.seedRow(t, spec)
				tokens := pcxSeedRequests(t, e, r.ID, tc.requests)
				before, mark := e.columns(t, r.ID), trailMark(t)

				_, err := e.pause(pauseParams(r))

				if e.store.stateReads != 0 {
					t.Errorf("state reads = %d; want no wait", e.store.stateReads)
				}
				if !report {
					assertOneName(t, err, tc.noReport)
					e.assertNothingSent(t)
					e.assertRowUnchanged(t, r.ID, before)
					if e.store.endedTries != 0 {
						t.Errorf("moves to ended attempted = %d; want none without an end report", e.store.endedTries)
					}
					return
				}
				if err != nil {
					t.Fatalf("Pause: %v; want the row ended", err)
				}
				e.assertNoTmuxCall(t)
				pauAssertState(t, e, r.ID, store.StateEnded)
				assertMoveRecords(t, mark, r.ID, moveWant{prior: tc.state, now: store.StateEnded,
					denyReason: store.DecisionReasonEnded, reason: "proc_absent", tokens: tokens})
				pcxAssertNoCallEvent(t, mark, r.ID)
				for _, tok := range tokens {
					if pr, err := e.st.GetPermissionRequest(r.ID, tok); err != nil || pr.Decision != "deny" || pr.DecisionReason != store.DecisionReasonEnded {
						t.Errorf("request %s = %+v, %v; want denied ended", tok, pr, err)
					}
				}
			})
		}
	}
}

// TestPauseCodexPreCheckLostRace: a move that loses its race (the row is written between pause's read and
// the move) changes nothing and writes no record; pause goes on with a fresh read of the row, whose state
// rules judge it.
func TestPauseCodexPreCheckLostRace(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	spec := codexSpec(store.StateWorking, agentGone, true)
	r := e.seedRow(t, spec)
	var mark int
	e.store.beforeEnded = func() {
		row, err := e.st.GetSpawn(r.ID)
		if err != nil {
			t.Errorf("GetSpawn: %v", err)
			return
		}
		if res, err := e.st.SetLivenessNoteIfSameLife(r.ID, row.Snapshot, "written between pause's read and the move"); err != nil || res != store.CondApplied {
			t.Errorf("versioned write = %v, %v; want applied", res, err)
		}
		mark = trailMark(t)
	}

	_, err := e.pause(pauseParams(r))

	assertOneName(t, err, "ErrSpawnNotPausable")
	if e.store.endedTries != 1 {
		t.Errorf("moves to ended attempted = %d; want 1", e.store.endedTries)
	}
	pauAssertState(t, e, r.ID, store.StateWorking)
	assertNoTrailSince(t, mark, r.ID)
	e.assertNoTmuxCall(t)
}

// TestPauseCodexWait: after the keys, a Codex row's wait polls the row and moves it to ended once its
// process is proven gone with an end report, returning success with SR-6.5's records alone: at the first
// poll, at a later one after being alive, when the end report came from SessionEnds that name other sessions
// (Codex sends one per session held at /exit); a move that loses its race to a write is made again at the
// next poll, writing nothing the first time; an adoption write that fills in the row's server identity on
// the same launch during the wait does not make it another launch.
func TestPauseCodexWait(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	cases := []struct {
		name     string
		noServer bool // the row records no server identity (a lost create reply), as seeded
		prepare  func(t *testing.T, e *killEnv, r killRow, mark *int)
		atSleep  func(e *killEnv, r killRow, n int) // runs after the nth sleep of the wait (nil: none)
		sleeps   int                                // sleeps the wait made before it ended
		tries    int                                // moves to ended attempted
	}{
		{name: "gone with an end report at the first poll", prepare: func(t *testing.T, e *killEnv, r killRow, mark *int) {
			e.codexExitsAfterEnter(t, r, mark)
		}, tries: 1},
		{name: "alive for two polls, gone at the third", prepare: func(t *testing.T, e *killEnv, r killRow, mark *int) {
			e.onceAfter(t, tmux.CallSendEnter, "Codex SessionEnd", func() (bool, string) {
				why := e.codexSessionEnds(t, r)
				*mark = trailMark(t)
				return why == "", why
			})
		}, atSleep: func(e *killEnv, r killRow, n int) {
			if n == 2 {
				e.pc.Set(r.AgentPID, procfix.Gone())
			}
		}, sleeps: 2, tries: 1},
		{name: "SessionEnds naming other sessions", prepare: func(t *testing.T, e *killEnv, r killRow, mark *int) {
			e.codexExitsAfterEnter(t, r, mark, "session-held-1", "session-held-2")
		}, tries: 1},
		{name: "the move loses its race at the first poll", prepare: func(t *testing.T, e *killEnv, r killRow, mark *int) {
			e.codexExitsAfterEnter(t, r, mark)
			e.store.beforeEnded = func() {
				row, err := e.st.GetSpawn(r.ID)
				if err != nil {
					t.Errorf("GetSpawn: %v", err)
					return
				}
				if res, err := e.st.SetLivenessNoteIfSameLife(r.ID, row.Snapshot, "written between the poll's read and the move"); err != nil || res != store.CondApplied {
					t.Errorf("versioned write = %v, %v; want applied", res, err)
				}
				*mark = trailMark(t)
			}
		}, sleeps: 1, tries: 2},
		// An adoption write (SR-3.6) fills in the identity on the SAME launch, as pause's own tmux phase or a
		// find-missing does: the launch is the token and life, so the row still ends.
		{name: "an adoption write fills in the identity during the wait", noServer: true, prepare: func(t *testing.T, e *killEnv, r killRow, mark *int) {
			e.onceAfter(t, tmux.CallSendEnter, "adoption write and Codex exit", func() (bool, string) {
				row, err := e.st.GetSpawn(r.ID)
				if err != nil {
					return false, err.Error()
				}
				li := row.Identity
				li.ServerPID, li.ServerStart, li.ServerStarttime = killServerPID, killServerStart, apitest.LinuxProcStarttime
				if res, err := e.st.AdoptIdentityIfUnchanged(r.ID, row.Snapshot, li); err != nil || res != store.CondApplied {
					return false, fmt.Sprintf("adoption write = %v, %v", res, err)
				}
				e.codexExits(t, r, mark)
				return true, ""
			})
		}, tries: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := codexSpec(store.StateWaiting, agentAlive, false)
			spec.NoServerIdentity = tc.noServer
			r := e.seedRow(t, spec)
			var mark int
			slept := pauSleeps(t, time.Millisecond, false, func(n int) {
				if tc.atSleep != nil {
					tc.atSleep(e, r, n)
				}
			})
			tc.prepare(t, e, r, &mark)

			_, err := e.pause(pauseParams(r))

			if err != nil {
				t.Fatalf("Pause: %v; want the row ended", err)
			}
			if len(*slept) != tc.sleeps {
				t.Errorf("sleeps = %d; want %d", len(*slept), tc.sleeps)
			}
			if e.store.endedTries != tc.tries {
				t.Errorf("moves to ended attempted = %d; want %d", e.store.endedTries, tc.tries)
			}
			e.assertExitDeliveredTimes(t, r.Socket, r.Spawn.Identity.PaneID, codexClearPresses)
			pauAssertState(t, e, r.ID, store.StateEnded)
			if tc.noServer {
				// Pause's own tmux phase adopted the missing identity and recorded it (ad.provenance.disagree,
				// written before the wait): not part of the move's records.
				mark = pcxAfterLastDisagree(t, mark, r.ID)
			}
			assertMoveRecords(t, mark, r.ID, moveWant{prior: store.StateWaiting, now: store.StateEnded,
				denyReason: store.DecisionReasonEnded, reason: "proc_absent"})
			pcxAssertNoCallEvent(t, 0, r.ID)
		})
	}
}

// TestPauseCodexWaitDoesNotEnd: the wait's move needs a gone agent process, an end report and the launch
// pause sent its keys to: an agent alive or unjudgeable, one gone with no end report, and a row on another
// launch are left (ErrPauseTimeout, no move attempted for the first three), so find-missing settles them.
func TestPauseCodexWaitDoesNotEnd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		do   func(t *testing.T, e *killEnv, r killRow)
		// otherLaunch: the row is no longer on the launch pause sent its keys to (its agent gone and holding an
		// end report, as the new launch's own would), so it is left in its new state.
		otherLaunch bool
	}{
		{name: "alive, end report", do: func(t *testing.T, e *killEnv, r killRow) {
			e.onceAfter(t, tmux.CallSendEnter, "Codex SessionEnd", func() (bool, string) {
				why := e.codexSessionEnds(t, r)
				return why == "", why
			})
		}},
		{name: "gone, no end report", do: func(_ *testing.T, e *killEnv, r killRow) {
			e.setAfterCall(tmux.CallSendEnter, procfix.Gone(), r.AgentPID)
		}},
		{name: "unjudgeable, end report", do: func(t *testing.T, e *killEnv, r killRow) {
			e.onceAfter(t, tmux.CallSendEnter, "Codex SessionEnd", func() (bool, string) {
				why := e.codexSessionEnds(t, r)
				e.pc.Set(r.AgentPID, procfix.Unreadable())
				return why == "", why
			})
		}},
		{name: "gone, end report, but the row is on another launch (a resume's new token)", do: func(t *testing.T, e *killEnv, r killRow) {
			e.onceAfter(t, tmux.CallSendEnter, "new launch and Codex exit", func() (bool, string) {
				return e.codexOtherLaunch(t, r, false)
			})
		}, otherLaunch: true},
		{name: "gone, end report, but the row is on another launch (a reuse's new launch)", do: func(t *testing.T, e *killEnv, r killRow) {
			e.onceAfter(t, tmux.CallSendEnter, "new launch and Codex exit", func() (bool, string) {
				return e.codexOtherLaunch(t, r, true)
			})
		}, otherLaunch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
			tc.do(t, e, r)

			_, err := e.pauseWithin(context.Background(), 0, pauseParams(r))

			assertOneName(t, err, "ErrPauseTimeout")
			if e.store.endedTries != 0 {
				t.Errorf("moves to ended attempted = %d; want none", e.store.endedTries)
			}
			want := store.StateWaiting
			if tc.otherLaunch {
				want = store.StatePending
				if row, err := e.st.GetSpawn(r.ID); err != nil || row.Agent != store.AgentCodex || row.EndReportedAtMillis <= 0 ||
					(row.Identity.Token == r.Token && row.LifeNumber == r.Spawn.LifeNumber) {
					t.Errorf("row = %+v, %v; want a Codex row with an end report on another launch (token or life)", row, err)
				}
			}
			pauAssertState(t, e, r.ID, want)
		})
	}
}

// codexOtherLaunch moves r's row onto another launch while pause waits, as a resume (reuse false: a new token,
// the same life) or a reuse (reuse true: a new token and the next life) does after the row ended, and then makes
// the new launch's agent exit: the end report is recorded on it and its process is gone. Nothing but the launch
// (token, life) tells it from the launch pause sent its keys to. It reports whether every step applied.
func (e *killEnv) codexOtherLaunch(t *testing.T, r killRow, reuse bool) (bool, string) {
	t.Helper()
	if why := e.codexSessionEnds(t, r); why != "" {
		return false, "first SessionEnd: " + why
	}
	row, err := e.st.GetSpawn(r.ID)
	if err != nil {
		return false, err.Error()
	}
	if _, res, err := e.st.MarkEndedIfSameLife(r.ID, row.Snapshot); err != nil || res != store.CondApplied {
		return false, fmt.Sprintf("move to ended = %v, %v", res, err)
	}
	if row, err = e.st.GetSpawn(r.ID); err != nil {
		return false, err.Error()
	}
	var (
		res   store.CondResult
		moved int64
		token = newToken()
	)
	if reuse {
		fresh := store.Spawn{Agent: store.AgentCodex, StartedAt: e.clock.Now(), LaunchStartedAtMillis: e.clock.Now().UnixMilli(),
			RelayMode: "off", Identity: store.LaunchIdentity{Token: token, Socket: r.Socket}}
		examined, exists, readErr := e.st.ReadForReuse(r.ID)
		if readErr != nil || !exists {
			return false, fmt.Sprintf("ReadForReuse = %v, %v", exists, readErr)
		}
		res, _, moved, err = e.st.ResetForReuse(r.ID, examined.Snapshot, examined.Agent, fresh)
	} else {
		res, moved, err = e.st.MoveToPending(r.ID, row.Snapshot, e.clock.Now().UnixMilli(), token, r.Socket, "", store.LaunchOwner{})
	}
	if err != nil || res != store.CondApplied {
		return false, fmt.Sprintf("new launch write = %v, %v", res, err)
	}
	// The new launch's identity, as resume and reuse record it after the create: the gate of its agent's hooks.
	if res, err := e.st.RecordLaunchIdentity(r.ID, moved, token, r.Spawn.Identity); err != nil || res != store.CondApplied {
		return false, fmt.Sprintf("new launch's identity write = %v, %v", res, err)
	}
	if why := e.codexSessionEnds(t, r); why != "" {
		return false, "new launch's SessionEnd: " + why
	}
	e.pc.Set(r.AgentPID, procfix.Gone())
	return true, ""
}

// TestPauseCodexWaitStoreError: a store error from the wait's move is pause's error.
func TestPauseCodexWaitStoreError(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
	e.codexExitsAfterEnter(t, r, nil)
	e.store.failEndedMove(nil)

	_, err := e.pause(pauseParams(r))

	if !errors.Is(err, errInjectedStore) {
		t.Fatalf("Pause = %v; want the move's store error", err)
	}
	pauAssertState(t, e, r.ID, store.StateWaiting)
}

// TestPauseClaudeRowNeverMoved: pause writes no state of a Claude row, even with its process gone and an end
// report column set: the pre-check leaves it to its state rules and the wait only reads it.
func TestPauseClaudeRowNeverMoved(t *testing.T) {
	t.Parallel()
	withReport := apitest.WithEndReportedAt(codexEndReport)
	cases := []struct {
		name    string
		state   string
		want    string // "": the wait times out
		session bool
	}{
		{name: "working", state: store.StateWorking, want: "ErrSpawnNotPausable"},
		{name: "waiting, wait times out", state: store.StateWaiting, want: "ErrPauseTimeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: tc.state, Agent: agentGone, Opts: []apitest.SpawnOption{withReport}})
			mark := trailMark(t)

			_, err := e.pauseWithin(context.Background(), 0, pauseParams(r))

			assertOneName(t, err, tc.want)
			if e.store.endedTries != 0 {
				t.Errorf("moves to ended attempted = %d; want none for a Claude row", e.store.endedTries)
			}
			pauAssertState(t, e, r.ID, tc.state)
			ptrAssertOnlyDisagrees(t, mark, r.ID, true)
		})
	}
}

// TestPauseLineClear (RN-19): pause clears the input line with C-u before typing /exit, one press on a
// Claude row and 127 presses in one call on a Codex row (never C-c, which quits an empty Codex), then types
// /exit and presses Enter; the agent gets /exit alone, after a draft was left typed.
func TestPauseLineClear(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    killRowSpec
		exits   func(t *testing.T, e *killEnv, r killRow)
		presses int
	}{
		{name: "claude", spec: killRowSpec{}, presses: 1, exits: func(t *testing.T, e *killEnv, r killRow) {
			if ok, why := e.endAsAgent(t, r); !ok {
				t.Errorf("SessionEnd not applied: %s", why)
			}
		}},
		{name: "codex", spec: codexSpec(store.StateWaiting, agentAlive, false), presses: codexClearPresses,
			exits: func(t *testing.T, e *killEnv, r killRow) { e.codexExits(t, r, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			in := watchPaneInput(e, r.Spawn.Identity.PaneID, false)
			in.box = "a draft left typed"
			in.onSubmit = func(line string) {
				if line == exitText {
					tc.exits(t, e, r)
				}
			}

			if _, err := e.pause(pauseParams(r)); err != nil {
				t.Fatalf("Pause: %v; want the row ended (agent got %q, %q left typed)", err, in.submitted, in.box)
			}

			e.assertExitDeliveredTimes(t, r.Socket, r.Spawn.Identity.PaneID, tc.presses)
			in.assertSubmittedOnce(t, exitText)
			for _, c := range e.rec.SocketCalls() {
				if c.Key != "" && c.Key != paneClearKey {
					t.Errorf("key send %q; want only %s", c.Key, paneClearKey)
				}
			}
			pauAssertState(t, e, r.ID, store.StateEnded)
		})
	}
}
