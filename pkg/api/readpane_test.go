package api_test

// readpane_test.go: read-pane's parameters, its freedom from a state guard,
// its refusal of an unusable recorded name in any state (SR-3.2), and which
// pane it reads (SR-7.1, SR-7.2, SR-7.3, SR-3.3, SR-3.7, SR-20.5):
// always the agent's pane by its pane id on the row's recorded socket, never
// a session name, a neighbour's session or a session holding the name. On
// the pane-verb fixture (pane_verb_fixture_test.go) and tmuxfix.Recorder.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rpAssertRead fails unless read-pane returned paneID's text from exactly
// paneReadCalls, capturing paneID by its id with the default parameters.
func rpAssertRead(t *testing.T, e *killEnv, r killRow, paneID string, res api.ReadPaneResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if want := paneText(r.Socket, paneID); res.Pane != want {
		t.Errorf("Pane = %q; want %q", res.Pane, want)
	}
	e.assertPaneCalls(t, paneReadCalls...)
	e.assertCaptured(t, paneID, api.DefaultReadPaneLines, false)
}

// rpAssertRefused fails unless err wraps want with a description matching
// c (forbidding forbid), the result is empty and nothing was captured.
func rpAssertRefused(t *testing.T, e *killEnv, res api.ReadPaneResult, err, want error, c apitest.DescCase, forbid ...string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v; want %v", err, want)
	}
	apitest.AssertDescription(t, err.Error(), c, forbid...)
	if res.Pane != "" {
		t.Errorf("Pane = %q; want none", res.Pane)
	}
	e.assertNoCalls(t, tmux.CallCapture)
}

// TestReadPaneParameters: n_lines (0 is the default, no cap) and ansi (stripped
// with glyphs kept, or raw) reach the one capture of the agent's pane by id.
func TestReadPaneParameters(t *testing.T) {
	t.Parallel()
	const raw = "\x1b[31m❯\x1b[0m what is 2+2?\n\x1b[1m4\x1b[0m\n🐝 Brewed for 1s\n"
	const stripped = "❯ what is 2+2?\n4\n🐝 Brewed for 1s\n"
	cases := []struct {
		name      string
		nLines    int
		ansi      bool
		wantLines int
		want      string
	}{
		{"defaults", 0, false, api.DefaultReadPaneLines, stripped},
		{"one line", 1, false, 1, stripped},
		{"no upper cap", 1000, false, 1000, stripped},
		{"ansi on", 0, true, api.DefaultReadPaneLines, raw},
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

// TestReadPaneDefaultLinesIs25 pins the SRD §12 default.
func TestReadPaneDefaultLinesIs25(t *testing.T) {
	t.Parallel()
	if api.DefaultReadPaneLines != 25 {
		t.Fatalf("DefaultReadPaneLines = %d; want 25", api.DefaultReadPaneLines)
	}
}

// TestReadPaneSpawnNotFound: an unknown id is ErrSpawnNotFound with no tmux call.
func TestReadPaneSpawnNotFound(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.seedRow(t, killRowSpec{})
	_, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	e.assertPaneCalls(t)
}

// TestReadPaneNoStateGuard: a pending, live or finished row's agent pane is
// read by pane id, whatever allow_pending says (SR-7.1).
func TestReadPaneNoStateGuard(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StatePending, store.StateWaiting, store.StateEnded, store.StateMissing} {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s allow_pending=%v", state, allow), func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{State: state})
				e.setPaneTexts(r.Socket)
				res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID, AllowPending: allow})
				rpAssertRead(t, e, r, r.Spawn.Identity.PaneID, res, err)
			})
		}
	}
}

// TestReadPaneUnusableName (SR-3.2, AC-LKP-10): in every state, a recorded
// name that cannot be used is ErrInternal quoting it, with no tmux call and the row unchanged.
func TestReadPaneUnusableName(t *testing.T) {
	t.Parallel()
	cases := []struct{ state, fixture string }{
		{store.StatePending, "pre-b.gqe default name"},
		{store.StateWaiting, "empty"},
		{store.StateWorking, "newline"},
		{store.StateEnded, "colon"},
		{store.StateMissing, "invalid UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.state+"/"+tc.fixture, func(t *testing.T) {
			e := newKillEnv(t)
			f := unusableFixture(t, tc.fixture)
			r := e.seedUnusableRow(t, killRowSpec{State: tc.state}, f)
			before := e.columns(t, r.ID)

			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})

			assertOneName(t, err, "ErrInternal")
			apitest.AssertDescription(t, err.Error(), f.desc, r.Token)
			if res.Pane != "" {
				t.Errorf("Pane = %q; want none", res.Pane)
			}
			e.assertNoTmuxCall(t)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestReadPaneUnusableNameUnknownID: an unknown id stays ErrSpawnNotFound
// beside a row whose recorded name cannot be used, with no tmux call.
func TestReadPaneUnusableNameUnknownID(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.seedUnusableRow(t, killRowSpec{}, unusableFixture(t, "pre-b.gqe default name"))
	_, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: "absent"})
	assertOneName(t, err, "ErrSpawnNotFound")
	e.assertNoTmuxCall(t)
}

// TestReadPaneRenamedSession: the renamed session's agent pane is read by id;
// a session now holding the recorded name is never captured.
func TestReadPaneRenamedSession(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		holder func(r killRow) *tmuxfix.SeedSession
	}{
		{"renamed", func(killRow) *tmuxfix.SeedSession { return nil }},
		{"renamed, unlabelled session holds the name", func(r killRow) *tmuxfix.SeedSession {
			return &tmuxfix.SeedSession{Name: r.Name}
		}},
		{"renamed, another store's session holds the name", func(r killRow) *tmuxfix.SeedSession {
			return &tmuxfix.SeedSession{Name: r.Name, Label: r.otherStore(newToken()),
				Panes: []tmuxfix.SeedPane{{AdPane: newToken()}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{NoSession: true})
			e.seedSession(t, &r, tmuxfix.WithRowSessionName("renamed-"+r.ID))
			if h := tc.holder(r); h != nil {
				e.seedOther(t, r.Socket, *h)
			}
			e.setPaneTexts(r.Socket)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})
			rpAssertRead(t, e, r, r.Spawn.Identity.PaneID, res, err)
		})
	}
}

// TestReadPaneNeighbours (AC-LKP-01/02/03): a row with no session gets the gone
// error and never reads a prefix-, name- or 8-character-id-sharing neighbour.
func TestReadPaneNeighbours(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		xID, yID     string
		xName, yName string
	}{
		{"name prefix", "", "", "proj-abc", "proj-abc123"},
		{"same name", "", "", "proj-same", "proj-same"},
		{"ids share first 8 characters, same-named folders", "abcd1234-x-row", "abcd1234-y-row", "work", "work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			x := e.seedRow(t, killRowSpec{ID: tc.xID, NoSession: true,
				Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.xName)}})
			y := e.seedRow(t, killRowSpec{ID: tc.yID, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.yName)}})
			e.setPaneTexts(y.Socket)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: x.ID})
			rpAssertRefused(t, e, res, err, tmux.ErrTmuxCaptureFailed, apitest.DescPaneGone(apitest.PaneGone{
				Verb: apitest.PaneReadPane, InstanceID: x.ID, Name: x.Name}), y.ID, y.Token)
			e.assertPaneCalls(t, tmux.CallLookup)
		})
	}
}

// TestReadPaneStoredNames (AC-LKP-09): a row whose recorded name holds $ or
// \ is found by its label under tmux's stored form and read by pane id.
func TestReadPaneStoredNames(t *testing.T) {
	t.Parallel()
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID || strings.ContainsAny(n.Raw, ".:") { // '.' and ':' names are unusable (Epic 19)
			continue
		}
		t.Run(n.Raw, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(n.Raw)}})
			if r.Session.Name != n.Stored {
				t.Fatalf("seeded session name %q; want the stored form %q", r.Session.Name, n.Stored)
			}
			e.setPaneTexts(r.Socket)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})
			rpAssertRead(t, e, r, r.Spawn.Identity.PaneID, res, err)
		})
	}
}

// TestReadPaneRecordedSocket (AC-LKP-19): with TMUX and TMUX_TMPDIR naming
// another server, every call names the row's recorded socket.
func TestReadPaneRecordedSocket(t *testing.T) {
	// Serial: it sets TMUX, TMUX_TMPDIR with t.Setenv.
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	// No commas: TMUX's socket field ends at one.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("TMUX", elsewhere+",4242,0")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	e.seedOther(t, elsewhere, tmuxfix.SeedSession{Name: r.Name, Label: r.current(),
		Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}})
	e.setPaneTexts(r.Socket)
	e.setPaneTexts(elsewhere)
	res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})
	rpAssertRead(t, e, r, r.Spawn.Identity.PaneID, res, err)
	for _, c := range e.rec.SocketCalls() {
		if c.Socket != r.Socket {
			t.Errorf("%v on socket %q; want the recorded %q", c.Call, c.Socket, r.Socket)
		}
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
			calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}, want: tmux.ErrTmuxSessionConflict,
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

// TestReadPaneListingFails (AC-PANE-03): a failed pane listing gives
// ErrTmuxUnresponsive or ErrTmuxNotAvailable, and nothing is captured.
func TestReadPaneListingFails(t *testing.T) {
	t.Parallel()
	const firstLine = "list-panes: unexpected reply"
	cases := []struct {
		name   string
		script tmuxfix.Script
		want   error
		desc   func(r killRow) apitest.DescCase
	}{
		{"timeout", tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.ErrTmuxUnresponsive,
			func(killRow) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallListPanes, config.Default().Tmux.EffectiveQueryTimeout())
			}},
		{"unrecognised reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: firstLine, ExitStatus: 1,
			HadStdout: true}, tmux.ErrTmuxUnresponsive,
			func(killRow) apitest.DescCase { return apitest.DescUnrecognisedReply(tmux.CallListPanes, firstLine) }},
		{"tmux not run", tmuxfix.Script{Failure: tmux.FailUnavailable}, tmux.ErrTmuxNotAvailable,
			func(killRow) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"socket permission", tmuxfix.Script{Failure: tmux.FailSocketDenied}, tmux.ErrTmuxNotAvailable,
			func(r killRow) apitest.DescCase { return apitest.DescSocketPermission(r.Socket) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.rec.Script(r.Socket, tc.script, tmux.CallListPanes)
			res, err := e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})
			rpAssertRefused(t, e, res, err, tc.want, tc.desc(r))
			e.assertPaneCalls(t, tmux.CallLookup, tmux.CallListPanes)
		})
	}
}
