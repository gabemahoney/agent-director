package api_test

// readpane_nochange_test.go: read-pane changes nothing (SR-7.5, SR-3.6,
// SR-20.6; AC-PANE-10's read-pane half): no tmux write, no store write (no
// adoption write either), no trail event of any kind, and the same answer and
// calls when re-issued.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rpnRun is one read-pane call's answer and the socket-taking calls it made.
type rpnRun struct {
	res   api.ReadPaneResult
	err   error
	calls []tmuxfix.SocketCall
}

// rpnRead runs read-pane on id through a Client (e.pc as its reader) and
// returns its answer with the calls it added to the Recorder.
func rpnRead(t *testing.T, e *killEnv, id string, nLines int) rpnRun {
	t.Helper()
	mark := len(e.rec.SocketCalls())
	res, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: id, NLines: nLines})
	return rpnRun{res: res, err: err, calls: e.rec.SocketCalls()[mark:]}
}

// rpnAssertErr fails unless err is nil when want is, else wraps want.
func rpnAssertErr(t *testing.T, err, want error) {
	t.Helper()
	if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
		t.Errorf("ReadPane err = %v; want %v", err, want)
	}
}

// rpnAssertNothingChanged fails when read-pane changed r's row (before; nil:
// no row), r's socket's sessions (as seeded in sessions), wrote tmux or
// wrote a trail record for r.ID after mark.
func rpnAssertNothingChanged(t *testing.T, e *killEnv, r killRow, before *apitest.SpawnColumns,
	sessions []tmuxfix.SeedSession, mark int) {
	t.Helper()
	if before != nil {
		e.assertRowUnchanged(t, r.ID, *before)
	}
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions after read-pane = %+v; want %+v", got, sessions)
	}
	e.assertNoCalls(t, paneWriteCalls...)
	assertNoTrailSince(t, mark, r.ID)
}

// TestReadPaneNothingChangedPane10: AC-PANE-10 with n_lines 1: each row's
// read makes at most one lookup, listing and capture and changes nothing;
// once the holding session is gone, every row gives ErrTmuxCaptureFailed.
func TestReadPaneNothingChangedPane10(t *testing.T) {
	cases := []struct {
		name     string
		spec     killRowSpec
		finished bool // the row is ended, its ended_at older than the stopping window
		// hold seeds the session holding r's name and returns it.
		hold func(t *testing.T, e *killEnv, r *killRow) tmuxfix.SeedSession
		// captured is the pane read (nil: nothing read, ErrTmuxCaptureFailed).
		captured func(r killRow, held tmuxfix.SeedSession) string
	}{
		{name: "finished row, its own session aged past the bounds",
			spec: killRowSpec{NoSession: true}, finished: true,
			hold: func(t *testing.T, e *killEnv, r *killRow) tmuxfix.SeedSession {
				created := e.clock.Now().Add(-e.cfg.EffectiveStartingSession()).Unix()
				e.seedSession(t, r, tmuxfix.WithRowSessionCreated(created))
				return r.Session
			},
			captured: func(r killRow, _ tmuxfix.SeedSession) string { return r.Spawn.Identity.PaneID }},
		{name: "finished row, an unlabelled session holds its name",
			spec: killRowSpec{NoSession: true}, finished: true,
			hold: func(t *testing.T, e *killEnv, r *killRow) tmuxfix.SeedSession {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(tmux.Label{}, false))
				return r.Session
			}},
		{name: "finished row, another row's session holds its name",
			spec: killRowSpec{NoSession: true}, finished: true,
			hold: func(t *testing.T, e *killEnv, r *killRow) tmuxfix.SeedSession {
				other := e.seedRow(t, killRowSpec{NoSession: true})
				e.ensureServer(r)
				held := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name, Label: other.current(),
					Panes: []tmuxfix.SeedPane{other.pane()}})
				e.syncServers()
				return held
			}},
		{name: "pending row, launching process stopped before its create, a leftover holds its name",
			spec: killPendSpec(store.StatePending),
			hold: func(t *testing.T, e *killEnv, r *killRow) tmuxfix.SeedSession {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
				return r.Session
			},
			captured: func(_ killRow, held tmuxfix.SeedSession) string { return held.Panes[0].ID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			if tc.finished {
				ended := e.clock.Now().Add(-e.cfg.EffectiveStoppingWindow() - time.Second)
				tc.spec.State, tc.spec.Opts = store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(ended)}
			}
			r := e.seedRow(t, tc.spec)
			held := tc.hold(t, e, &r)
			e.setPaneTexts(r.Socket)
			before, sessions, mark := e.columns(t, r.ID), e.rec.Sessions(r.Socket), trailMark(t)

			run := rpnRead(t, e, r.ID, 1)

			if tc.captured == nil {
				rpnAssertErr(t, run.err, api.ErrTmuxCaptureFailed)
				e.assertPaneCalls(t, tmux.CallLookup)
			} else {
				pane := tc.captured(r, held)
				if run.err != nil || run.res.Pane != paneText(r.Socket, pane) {
					t.Fatalf("ReadPane = %+v, %v; want pane %s's text", run.res, run.err, pane)
				}
				e.assertPaneCalls(t, tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
				e.assertCaptured(t, pane, 1, false)
			}
			rpnAssertNothingChanged(t, e, r, &before, sessions, mark)

			e.seedBystander(t, r.Socket) // tmux exits with its last session; keep the server up
			if err := e.rec.KillSessionID(r.Socket, held.ID); err != nil {
				t.Fatalf("remove the holding session %s: %v", held.ID, err)
			}
			e.rec.Reset()
			sessions = e.rec.Sessions(r.Socket)
			run = rpnRead(t, e, r.ID, 1)
			rpnAssertErr(t, run.err, api.ErrTmuxCaptureFailed)
			e.assertPaneCalls(t, tmux.CallLookup)
			rpnAssertNothingChanged(t, e, r, &before, sessions, mark)
		})
	}
}

// TestReadPaneNoAdoptionWrite: a lost reply's adopted pane is read for the
// call only; row_version and the identity columns are unchanged (SR-3.6).
func TestReadPaneNoAdoptionWrite(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{NoPane: true, NoServerIdentity: true})
	e.setPaneTexts(r.Socket)
	before, sessions, mark := e.columns(t, r.ID), e.rec.Sessions(r.Socket), trailMark(t)
	if before.PaneID != nil || before.TmuxServerPID != nil {
		t.Fatalf("seeded pane_id %v, tmux_server_pid %v; want none recorded", before.PaneID, before.TmuxServerPID)
	}

	run := rpnRead(t, e, r.ID, 1)

	pane := r.Session.Panes[0].ID
	if run.err != nil || run.res.Pane != paneText(r.Socket, pane) {
		t.Fatalf("ReadPane = %+v, %v; want the token pane %s's text", run.res, run.err, pane)
	}
	e.assertPaneCalls(t, tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
	e.assertCaptured(t, pane, 1, false)
	rpnAssertNothingChanged(t, e, r, &before, sessions, mark)
}

// rpnSeedPane seeds r's current-labelled session holding only a pane that is
// not the agent's (no row pane id or pid, no pane label).
func rpnSeedPane(t *testing.T, e *killEnv, r *killRow) {
	t.Helper()
	e.seedOurs(t, r, tmuxfix.SeedPane{PID: e.newPID()})
}

// TestReadPaneNoTrailAndRepeatable: where a writing verb would log a disagree
// reason, and on every refusal, read-pane writes no trail record and changes
// nothing; re-issued, it gives the same answer and the same calls.
func TestReadPaneNoTrailAndRepeatable(t *testing.T) {
	ours := []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture}
	lookup := []tmux.Call{tmux.CallLookup}
	cases := []struct {
		name  string
		noRow bool
		spec  killRowSpec
		setup func(*testing.T, *killEnv, *killRow)
		want  error
		calls []tmux.Call
	}{
		{name: "restarted server", setup: ktrRestart, calls: ours},
		{name: "re-bound server", setup: ktrRebind, want: api.ErrTmuxNotAvailable, calls: lookup},
		{name: "lost-reply adoption", spec: killRowSpec{NoPane: true, NoServerIdentity: true}, calls: ours},
		{name: "two sessions with the current label",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
			},
			want: api.ErrTmuxSessionConflict, calls: lookup},
		{name: "scope value",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
			},
			want: api.ErrTmuxSessionConflict, calls: lookup},
		{name: "renamed Ours session", spec: killRowSpec{NoSession: true}, setup: ktrRenamed, calls: ours},
		{name: "unknown id", noRow: true, want: api.ErrSpawnNotFound},
		{name: "gone", spec: killRowSpec{NoSession: true}, setup: ktrBystander, want: api.ErrTmuxCaptureFailed, calls: lookup},
		{name: "two leftovers", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
				e.seedLeftover(t, *r, newToken())
			},
			want: api.ErrTmuxSessionConflict, calls: lookup},
		{name: "agent's pane not found", spec: killRowSpec{NoSession: true}, setup: rpnSeedPane,
			want: api.ErrTmuxSessionConflict, calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}},
		{name: "lost reply, no pane carries the token",
			spec: killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true}, setup: rpnSeedPane,
			want: api.ErrTmuxSessionConflict, calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}},
		{name: "unreadable lookup", setup: ktrScript(tmux.FailTimeout, tmux.CallLookup), want: api.ErrTmuxUnresponsive, calls: lookup},
		{name: "tmux unavailable", setup: ktrScript(tmux.FailUnavailable, tmux.CallLookup), want: api.ErrTmuxNotAvailable, calls: lookup},
		{name: "socket permission", setup: ktrScript(tmux.FailSocketDenied, tmux.CallLookup), want: api.ErrTmuxNotAvailable, calls: lookup},
		{name: "pane listing times out", setup: ktrScript(tmux.FailTimeout, tmux.CallListPanes),
			want: api.ErrTmuxUnresponsive, calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}},
		{name: "capture times out", setup: ktrScript(tmux.FailTimeout, tmux.CallCapture), want: api.ErrTmuxUnresponsive, calls: ours},
		{name: "capture fails, follow-up finds Ours", setup: ktrScript(tmux.FailUnrecognized, tmux.CallCapture),
			want: api.ErrTmuxUnresponsive, calls: append(slices.Clone(ours), tmux.CallLookup)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := killRow{ID: "rpn-unknown-" + uuid.NewString()[:8]}
			if !tc.noRow {
				r = e.seedRow(t, tc.spec)
			}
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			e.setPaneTexts(r.Socket)
			var before *apitest.SpawnColumns
			if !tc.noRow {
				cols := e.columns(t, r.ID)
				before = &cols
			}
			sessions, mark := e.rec.Sessions(r.Socket), trailMark(t)

			first := rpnRead(t, e, r.ID, 1)

			rpnAssertErr(t, first.err, tc.want)
			e.assertPaneCalls(t, tc.calls...)
			rpnAssertNothingChanged(t, e, r, before, sessions, mark)

			again := rpnRead(t, e, r.ID, 1)
			if again.res != first.res || rpnErrText(again.err) != rpnErrText(first.err) || !reflect.DeepEqual(again.calls, first.calls) {
				t.Errorf("re-issued ReadPane = %+v, %v, calls %+v; want %+v, %v, calls %+v",
					again.res, again.err, again.calls, first.res, first.err, first.calls)
			}
		})
	}
}

// rpnErrText is err's text, "" for nil.
func rpnErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
