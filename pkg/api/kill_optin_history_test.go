package api_test

// kill_optin_history_test.go: kill with the finished-row opt-in (SR-6.5,
// SR-6.7, SR-20.6) on rows made by real verb history on the kill fixture: a
// fresh spawn's row and a resumed row finished before their agent reported
// in (AC-KILL-14), a failed resume's restored row, and a plain spawn's
// held-name row beside a leftover (AC-SPN-07). Hooks come from the row's own
// pane (SR-22.9); newReuseEnv is spawn_reuse_fixture_test.go's, the rlf
// helpers spawn_reuse_history_test.go's.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kohSpawn runs a fresh plain spawn of a new caller-supplied id through
// Client.Spawn on e's default socket; its agent sits at a startup prompt
// (alive, no hook yet). It returns the id.
func kohSpawn(t *testing.T, e *killEnv) string {
	t.Helper()
	srv := killRow{Socket: e.defaultSocket}
	e.ensureServer(&srv)
	e.seedBystander(t, srv.Socket)
	e.syncServers()
	id := "koh-" + uuid.NewString()[:8]
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	c, _ := e.client(t)
	if _, err := c.Spawn(api.SpawnParams{ClaudeInstanceID: id, CWD: t.TempDir(),
		ExtraEnv: seedTrustConfig(t, t.TempDir(), trustLacksEntry).extraEnv()}); err != nil {
		t.Fatalf("Spawn(%s): %v", id, err)
	}
	return id
}

// kohOwnSession returns the session carrying row's current label.
func kohOwnSession(t *testing.T, e *killEnv, row api.Spawn) tmuxfix.SeedSession {
	t.Helper()
	for _, s := range e.rec.Sessions(row.Identity.Socket) {
		if s.Label == tmuxfix.Valid(row.Identity.Token, row.ClaudeInstanceID, e.storeID) {
			return s
		}
	}
	t.Fatalf("no session of %s carries its current label", row.ClaudeInstanceID)
	return tmuxfix.SeedSession{}
}

// kohAdvanceTo moves e's clock forward to at, if it is not there yet.
func kohAdvanceTo(e *killEnv, at time.Time) {
	if d := at.Sub(e.clock.Now()); d > 0 {
		e.clock.Advance(d)
	}
}

// kohPastBoth moves e's clock past the stopping window after row's ended_at
// (a store write's own time) and the starting-session bound after created.
func kohPastBoth(t *testing.T, e *killEnv, row api.Spawn, created int64) {
	t.Helper()
	if row.EndedAt == nil {
		t.Fatalf("row %s has no ended_at", row.ClaudeInstanceID)
	}
	kohAdvanceTo(e, row.EndedAt.Add(e.cfg.EffectiveStoppingWindow()))
	kohAdvanceTo(e, time.Unix(created, 0).Add(e.cfg.EffectiveStartingSession()))
}

// kohAssertKilled fails unless the tmux calls since mark are the kill
// sequence on exactly the pane paneID and the session sessionID.
func kohAssertKilled(t *testing.T, e *killEnv, mark int, paneID, sessionID string) {
	t.Helper()
	var got []tmuxfix.SocketCall
	for _, c := range e.rec.SocketCalls()[mark:] {
		got = append(got, tmuxfix.SocketCall{Call: c.Call, Target: c.Target})
	}
	want := []tmuxfix.SocketCall{{Call: tmux.CallLookup}, {Call: tmux.CallListPanes},
		{Call: tmux.CallKillPane, Target: paneID}, {Call: tmux.CallKillSession, Target: sessionID}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tmux calls = %+v; want %+v", got, want)
	}
}

// kohAssertRefused runs the opt-in on id and fails unless it is the
// ErrTmuxSessionConflict desc describes after one lookup (lookup its token),
// with nothing sent, the row and every session unchanged.
func kohAssertRefused(t *testing.T, e *killEnv, id string, desc apitest.DescCase, lookup string) {
	t.Helper()
	row := rlfRow(t, e, id)
	socket := row.Identity.Socket
	if socket == "" {
		socket = e.defaultSocket
	}
	before, sessions, mark := e.columns(t, id), e.rec.Sessions(socket), len(e.rec.SocketCalls())
	res, err := e.killOptIn(id)
	if err == nil || res.KillSent {
		t.Fatalf("kill = %+v, %v; want ErrTmuxSessionConflict with kill_sent false", res, err)
	}
	assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
	apitest.AssertDescription(t, err.Error(), desc, row.Identity.Token, tmuxfix.OtherToken, e.storeID)
	if calls := e.rec.SocketCalls()[mark:]; len(calls) != 1 || calls[0].Call != tmux.CallLookup {
		t.Errorf("tmux calls = %+v; want one lookup", calls)
	}
	e.assertRowUnchanged(t, id, before)
	if len(sessions) == 0 {
		t.Errorf("no session on %s; want the row's own or its leftover", socket)
	}
	if got := e.rec.Sessions(socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions after kill = %+v; want untouched %+v", got, sessions)
	}
	kolAssertCalled(t, id, map[string]any{"include_finished": true, "outcome": "ErrTmuxSessionConflict",
		"lookup_outcome": lookup, "kill_sent": false, "pane_killed": false})
}

// kohFinish finishes id's pending row as state: ended by its own agent's
// SessionEnd (SR-22.9), or missing by find-missing's store mark.
func kohFinish(t *testing.T, e *killEnv, id, state string) {
	t.Helper()
	if state == store.StateEnded {
		if got := apitest.ApplyAgentHook(t, e.dbPath, id, "SessionEnd", ""); !got.Applied {
			t.Fatalf("SessionEnd from the agent = %+v; want applied", got)
		}
		return
	}
	if _, res, err := e.st.MarkMissingIfSameLife(id, rlfRow(t, e, id).Snapshot); err != nil || res != store.CondApplied {
		t.Fatalf("MarkMissingIfSameLife(%s) = %v, %v; want applied", id, res, err)
	}
}

// kohResumed returns a row whose agent reported in and ended, then was
// resumed: pending, its session id kept, its pid cleared by the move.
func kohResumed(t *testing.T, e *killEnv) string {
	t.Helper()
	id := kohSpawn(t, e)
	sid := rlfNewSession()
	rlfReportIn(t, e, id, sid, true)
	rlfEndLife(t, e, id)
	if _, err := e.resume(id); err != nil {
		t.Fatalf("Resume(%s): %v", id, err)
	}
	if row := rlfRow(t, e, id); row.State != store.StatePending || row.PID != 0 || row.ClaudeSessionID != sid {
		t.Fatalf("after the resume: state %s, pid %d, session %q; want pending, none, %s", row.State, row.PID,
			row.ClaudeSessionID, sid)
	}
	return id
}

// TestKillIncludeFinishedNeverReportedInHistory (AC-KILL-14): a fresh
// spawn's row and a resumed row, finished before their agent reported in
// (no pid), get "never reported in" past both, with nothing sent.
func TestKillIncludeFinishedNeverReportedInHistory(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	rows := []struct {
		name string
		make func(t *testing.T, e *killEnv) string
	}{
		{"fresh spawn", kohSpawn},
		{"resumed, session id kept", kohResumed},
	}
	for _, rc := range rows {
		for _, state := range []string{store.StateEnded, store.StateMissing} {
			t.Run(rc.name+"/"+state, func(t *testing.T) {
				e := newReuseEnv(t)
				id := rc.make(t, e)
				kohFinish(t, e, id, state)
				row := rlfRow(t, e, id)
				if row.State != state || row.PID != 0 {
					t.Fatalf("row: state %s, pid %d; want %s with no pid", row.State, row.PID, state)
				}
				kohPastBoth(t, e, row, kohOwnSession(t, e, row).Created)
				kohAssertRefused(t, e, id, apitest.DescKillOptInNeverReportedIn(id, row.TmuxSessionName,
					e.cfg.EffectiveStartingSession()), "ours")
			})
		}
	}
}

// TestKillIncludeFinishedRestoredRow: a failed resume restores a row whose
// earlier launch reported in; its old session, created before the restored
// ended_at, is killed, and a session created after the restore is refused.
func TestKillIncludeFinishedRestoredRow(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	for _, after := range []bool{false, true} {
		name := "old session killed"
		if after {
			name = "session created after the restore: never reported in"
		}
		t.Run(name, func(t *testing.T) {
			e := newReuseEnv(t)
			id := kohSpawn(t, e)
			rlfReportIn(t, e, id, rlfNewSession(), true)
			rlfEndLife(t, e, id)
			ended := rlfRow(t, e, id)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			if _, err := e.resume(id); !errors.Is(err, api.ErrTmuxSessionCreate) {
				t.Fatalf("Resume(%s) = %v; want ErrTmuxSessionCreate", id, err)
			}
			row := rlfRow(t, e, id)
			if row.State != store.StateEnded || row.PID == 0 || row.PID != ended.PID ||
				row.EndedAtText != ended.EndedAtText || row.Identity != ended.Identity {
				t.Fatalf("row after the failed resume = %+v; want restored to %+v", row, ended)
			}
			created := row.EndedAt.Unix() - 1
			if after {
				kohAdvanceTo(e, row.EndedAt.Add(time.Second))
				created = e.clock.Now().Unix()
			}
			// Seeded after the restore: before it, resume's lookup would refuse.
			own := e.rec.SeedRowSession(t, e.dbPath, id, tmuxfix.WithRowSessionCreated(created))
			kohPastBoth(t, e, row, created)
			if after {
				kohAssertRefused(t, e, id, apitest.DescKillOptInNeverReportedIn(id, row.TmuxSessionName,
					e.cfg.EffectiveStartingSession()), "ours")
				return
			}
			e.pc.Set(row.PID, procfix.Alive(row.ProcStarttime))
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), row.PID)
			before, mark := e.columns(t, id), len(e.rec.SocketCalls())
			res, err := e.killOptIn(id)
			if err != nil || !res.KillSent {
				t.Fatalf("kill = %+v, %v; want kill_sent true, nil", res, err)
			}
			kohAssertKilled(t, e, mark, row.Identity.PaneID, own.ID)
			e.assertRowUnchanged(t, id, before)
			kolAssertCalled(t, id, map[string]any{"include_finished": true, "outcome": "ok", "lookup_outcome": "ours",
				"kill_sent": true})
		})
	}
}

// TestKillIncludeFinishedPlainSpawnHeldName (AC-SPN-07): a plain spawn's
// row ended by "duplicate session" from a leftover of its id placed after the
// scan (SR-20.9) gets "never reported in" (Leftover); the leftover still runs.
func TestKillIncludeFinishedPlainSpawnHeldName(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	e := newKillEnv(t)
	r, holder, err := e.plainSpawnHeld(t, holderOld, true)
	assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
	if st := e.columns(t, r.ID).State; st != store.StateEnded {
		t.Fatalf("row after the plain spawn: state %v; want ended", st)
	}
	kohAssertRefused(t, e, r.ID, apitest.DescKillOptInNeverReportedInLeftover(r.ID,
		[]apitest.DescSession{{Name: holder.Name, ID: holder.ID}}), "leftover")
}
