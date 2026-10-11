package api_test

// readpane_test.go: read-pane's parameters, its freedom from a state guard, a
// session replaced mid-call (SR-7.1, SR-7.2, SR-7.3, SR-20.5), and that it
// changes nothing (SR-7.5, SR-3.6, SR-20.6; AC-PANE-10's read-pane half): no
// tmux or store write (no adoption write either), no trail event, the same
// answer and calls when re-issued. Which pane it reads, with send-keys and
// pause, is readpane_pane_test.go's, whose check asserts the same on every read.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestReadPaneParameters: n_lines (0 is SRD §12's default of 25, no cap) and
// ansi (stripped with glyphs kept, or raw) reach the one capture of the
// agent's pane by id.
func TestReadPaneParameters(t *testing.T) {
	t.Parallel()
	if api.DefaultReadPaneLines != 25 {
		t.Fatalf("DefaultReadPaneLines = %d; want 25", api.DefaultReadPaneLines)
	}
	const raw = "\x1b[31m❯\x1b[0m what is 2+2?\n\x1b[1m4\x1b[0m\n🐝 Brewed for 1s\n"
	const stripped = "❯ what is 2+2?\n4\n🐝 Brewed for 1s\n"
	cases := []struct {
		name      string
		nLines    int
		ansi      bool
		wantLines int
		want      string
	}{
		{"defaults", 0, false, 25, stripped},
		{"one line", 1, false, 1, stripped},
		{"no upper cap", 1000, false, 1000, stripped},
		{"ansi on", 0, true, 25, raw},
		{"ansi on with n_lines", 7, true, 7, raw},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, raw)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: tc.nLines, ANSI: tc.ansi})
			if err != nil {
				t.Fatalf("ReadPane: %v", err)
			}
			if res.Pane != tc.want {
				t.Errorf("Pane = %q; want %q", res.Pane, tc.want)
			}
			e.assertPaneCalls(t, paneReadCalls...)
			e.assertCaptured(t, r.Spawn.Identity.PaneID, tc.wantLines, tc.ansi)
		})
	}
}

// TestReadPaneNegativeNLinesRefused: a negative n_lines is ErrInvalidFlags from
// ReadPane and Client.ReadPane, before the row is read and with no tmux call (b.c4n).
func TestReadPaneNegativeNLinesRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		client  bool
		unknown bool // an id with no row: still ErrInvalidFlags, not ErrSpawnNotFound
	}{
		{"ReadPane", false, false},
		{"Client.ReadPane", true, false},
		{"Client.ReadPane, unknown id", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.setPaneTexts(r.Socket)
			p := api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: -1}
			if tc.unknown {
				p.ClaudeInstanceID = "no-such-row"
			}
			read := e.readPane
			if tc.client {
				read = func(p api.ReadPaneParams) (api.ReadPaneResult, error) { return e.readPaneClient(t, p) }
			}
			res, err := read(p)
			assertOneSentinel(t, err, api.ErrInvalidFlags)
			if res.Pane != "" {
				t.Errorf("Pane = %q; want none", res.Pane)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// TestReadPaneNoStateGuard: a pending, live or finished row's agent pane is
// read by pane id, allow_pending or not (SR-7.1; read-pane never reads it).
func TestReadPaneNoStateGuard(t *testing.T) {
	t.Parallel()
	for i, state := range []string{store.StatePending, store.StateWaiting, store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: state})
			e.setPaneTexts(r.Socket)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID, AllowPending: i%2 == 0})
			if want := paneText(r.Socket, r.Spawn.Identity.PaneID); err != nil || res.Pane != want {
				t.Errorf("ReadPane = %q, %v; want %q", res.Pane, err, want)
			}
			e.assertPaneCalls(t, paneReadCalls...)
			e.assertCaptured(t, r.Spawn.Identity.PaneID, api.DefaultReadPaneLines, false)
		})
	}
}

// TestReadPaneSessionReplaced (AC-LKP-06): a session replaced after the lookup
// or listing never has its panes captured; the result follows SR-7.2 / SR-7.3.
func TestReadPaneSessionReplaced(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		after    tmux.Call
		current  bool // the replacement carries the row's current label
		calls    []tmux.Call
		want     error
		desc     func(r killRow) apitest.DescCase
		captured bool // the agent's old pane id was captured (and failed)
	}{
		{name: "after the lookup", after: tmux.CallLookup, current: true,
			calls: paneListedCalls, want: tmux.ErrTmuxSessionConflict,
			desc: func(r killRow) apitest.DescCase {
				return apitest.DescPaneNotFound(apitest.PaneNotFound{Verb: apitest.PaneReadPane, InstanceID: r.ID,
					Name: r.Name})
			}},
		{name: "after the listing, unlabelled replacement", after: tmux.CallListPanes,
			calls: withFollowUp(paneReadCalls), want: tmux.ErrTmuxCaptureFailed, captured: true,
			desc: func(r killRow) apitest.DescCase {
				return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneReadPane, InstanceID: r.ID,
					Name: r.Name, FailedCall: tmux.CallCapture})
			}},
		{name: "after the listing, replacement with the current label", after: tmux.CallListPanes, current: true,
			calls: withFollowUp(paneReadCalls), want: tmux.ErrTmuxUnresponsive, captured: true,
			desc: func(killRow) apitest.DescCase { return apitest.DescUnrecognisedReply(tmux.CallCapture, "") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			label := tmux.Label{}
			if tc.current {
				label = r.current()
			}
			e.rec.ReplaceSessionAfter(tc.after, r.Socket, r.Session.ID, label)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			apitest.AssertDescription(t, err.Error(), tc.desc(r))
			if res.Pane != "" {
				t.Errorf("Pane = %q; want none", res.Pane)
			}
			e.assertPaneCalls(t, tc.calls...)
			if tc.captured {
				e.assertCaptured(t, r.Spawn.Identity.PaneID, api.DefaultReadPaneLines, false)
			}
		})
	}
}

// rpnSeedPane seeds r's current-labelled session holding only a pane that is
// not the agent's (no row pane id or pid, no pane label).
func rpnSeedPane(t *testing.T, e *killEnv, r *killRow) {
	t.Helper()
	e.seedOurs(t, r, e.otherPane())
}

// TestReadPaneNoTrailAndRepeatable: where a writing verb would log a disagree
// reason or write a lost reply's server identity, on AC-PANE-10's finished
// rows (its own session past the bound: the agent's pane read; its name held
// by an unlabelled session or another row's session, and still gone once that
// session is removed), and on the lookup's and the capture's refusals,
// read-pane (n_lines 1) changes no row, session or tmux state and writes no
// trail record; re-issued, it gives the same answer and the same calls.
func TestReadPaneNoTrailAndRepeatable(t *testing.T) {
	t.Parallel()
	finished := killRowSpec{State: store.StateEnded, NoSession: true, Opts: []apitest.SpawnOption{
		apitest.WithEndedAt(killClockStart.Add(-(defWindow + time.Second)))}}
	cases := []struct {
		name    string
		spec    killRowSpec
		setup   func(*testing.T, *killEnv, *killRow)
		want    error
		calls   []tmux.Call
		release bool // then remove the name's holder (setup's r.Session): still ErrTmuxCaptureFailed
	}{
		{name: "restarted server", setup: ktrRestart, calls: paneReadCalls},
		{name: "re-bound server", setup: ktrRebind, want: api.ErrTmuxNotAvailable, calls: paneLookupCalls},
		{name: "two sessions with the current label", setup: sktDuplicate, want: api.ErrTmuxSessionConflict, calls: paneLookupCalls},
		{name: "scope value",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
			},
			want: api.ErrTmuxSessionConflict, calls: paneLookupCalls},
		{name: "lost reply, no pane carries the token",
			spec: killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true}, setup: rpnSeedPane,
			want: api.ErrTmuxSessionConflict, calls: paneListedCalls},
		{name: "finished row, its own session the bound old (AC-PANE-10)", spec: finished,
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionCreated(e.clock.Now().Add(-defBound).Unix()))
			}, calls: paneReadCalls},
		{name: "finished row, an unlabelled session holds its name", spec: finished,
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(tmux.Label{}, false))
			}, want: api.ErrTmuxCaptureFailed, calls: paneLookupCalls},
		{name: "finished row, another row's session holds its name (AC-PANE-10)", spec: finished,
			setup: func(t *testing.T, e *killEnv, r *killRow) { r.Session = e.seedHolder(t, *r, holderForeign) },
			want:  api.ErrTmuxCaptureFailed, calls: paneLookupCalls, release: true},
		{name: "unreadable lookup", setup: ktrScript(tmux.FailTimeout, tmux.CallLookup), want: api.ErrTmuxUnresponsive,
			calls: paneLookupCalls},
		{name: "tmux unavailable", setup: ktrScript(tmux.FailUnavailable, tmux.CallLookup), want: api.ErrTmuxNotAvailable,
			calls: paneLookupCalls},
		{name: "socket permission", setup: ktrScript(tmux.FailSocketDenied, tmux.CallLookup), want: api.ErrTmuxNotAvailable,
			calls: paneLookupCalls},
		{name: "capture times out", setup: ktrScript(tmux.FailTimeout, tmux.CallCapture), want: api.ErrTmuxUnresponsive,
			calls: paneReadCalls},
		{name: "capture fails, follow-up finds Ours", setup: ktrScript(tmux.FailUnrecognized, tmux.CallCapture),
			want: api.ErrTmuxUnresponsive, calls: withFollowUp(paneReadCalls)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			tc.setup(t, e, &r)
			e.setPaneTexts(r.Socket)
			before, sessions, mark := e.columns(t, r.ID), e.rec.Sessions(r.Socket), trailMark(t)
			read := func() verbRun[api.ReadPaneResult] {
				return runVerb(e, func() (api.ReadPaneResult, error) {
					return e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: 1})
				})
			}

			first := read()

			if (tc.want == nil) != (first.err == nil) || !errors.Is(first.err, tc.want) {
				t.Errorf("ReadPane err = %v; want %v", first.err, tc.want)
			}
			e.assertPaneCalls(t, tc.calls...)
			if tc.want == nil {
				pane := r.Spawn.Identity.PaneID
				e.assertCaptured(t, pane, 1, false)
				if first.res.Pane != paneText(r.Socket, pane) {
					t.Errorf("Pane = %q; want the agent's pane %q", first.res.Pane, paneText(r.Socket, pane))
				}
			}
			e.assertRowUnchanged(t, r.ID, before)
			if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
				t.Errorf("sessions after read-pane = %+v; want %+v", got, sessions)
			}
			assertNoTrailSince(t, mark, r.ID)
			assertSameRun(t, read(), first)
			if tc.release {
				e.seedBystander(t, r.Socket) // tmux exits with its last session; keep the server up
				adviceEndSession(t, e.rec, r.Socket, r.Session.ID)
				again := read()
				if !errors.Is(again.err, api.ErrTmuxCaptureFailed) || !reflect.DeepEqual(again.calls, first.calls) {
					t.Errorf("holder removed: ReadPane = %v, calls %+v; want ErrTmuxCaptureFailed, %+v",
						again.err, again.calls, first.calls)
				}
				e.assertRowUnchanged(t, r.ID, before)
				assertNoTrailSince(t, mark, r.ID)
			}
		})
	}
}
