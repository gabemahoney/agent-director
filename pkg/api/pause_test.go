package api_test

// pause_test.go: pause's state guards, the agent's pane it types /exit into
// (by pane id on the row's recorded socket, never a session name, a
// neighbour's session, a session holding the name or another store's) and
// its unchanged wait (SR-7.1, SR-7.2, SR-3.3, SR-3.4, SR-3.7, SR-17). On the
// kill, pane-verb and pause fixtures and tmuxfix.Recorder.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pauDeliver pauses r with its row ended by its agent after Enter and fails
// unless /exit went to r's agent pane by id, then Enter, and the row ended.
func pauDeliver(t *testing.T, e *killEnv, r killRow) {
	t.Helper()
	fastPausePolls(t)
	e.endAfterEnter(t, r)
	if _, err := e.pause(pauseParams(r)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
	pauAssertState(t, e, r.ID, store.StateEnded)
}

// pauAssertState fails unless id's row is in state want.
func pauAssertState(t *testing.T, e *killEnv, id, want string) {
	t.Helper()
	if got, err := e.st.GetSpawnState(id); err != nil || got != want {
		t.Errorf("row %s state = %q (%v); want %q", id, got, err, want)
	}
}

// pauSleeps sets the wait's poll interval and a sleeper that records each
// sleep, runs at(n) after the nth (when at is set) and really sleeps only when sleep;
// cleanup restores the knobs.
func pauSleeps(t *testing.T, interval time.Duration, sleep bool, at func(n int)) *[]time.Duration {
	t.Helper()
	prevInterval, prevSleep := api.PauseTestKnobs()
	t.Cleanup(func() { api.SetPauseTestKnobs(prevInterval, prevSleep) })
	var slept []time.Duration
	api.SetPauseTestKnobs(interval, func(d time.Duration) {
		slept = append(slept, d)
		if sleep {
			time.Sleep(d)
		}
		if at != nil {
			at(len(slept))
		}
	})
	return &slept
}

// TestPauseGuards: an unknown id, a finished row and a non-waiting row are
// answered before any tmux call though the row's own session is up; the row is unchanged.
func TestPauseGuards(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, e *killEnv) killRow
		id   func(r killRow) string
		want error // nil: no-op success
	}{
		{"unknown id", pauSeedState(store.StateWaiting), func(killRow) string { return "absent" }, store.ErrSpawnNotFound},
		{"ended", pauSeedState(store.StateEnded), nil, nil},
		{"missing", pauSeedState(store.StateMissing), nil, nil},
		{"pending", pauSeedState(store.StatePending), nil, api.ErrSpawnNotPausable},
		{"pending beside a leftover", func(t *testing.T, e *killEnv) killRow {
			return e.seedPending(t, pendingFresh, pendingLeftover)
		}, nil, api.ErrSpawnNotPausable},
		{"working", pauSeedState(store.StateWorking), nil, api.ErrSpawnNotPausable},
		{"ask_user", pauSeedState(store.StateAskUser), nil, api.ErrSpawnNotPausable},
		{"check_permission", pauSeedState(store.StateCheckPermission), nil, api.ErrSpawnNotPausable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			id := r.ID
			if tc.id != nil {
				id = tc.id(r)
			}
			before := e.columns(t, r.ID)
			_, err := e.pause(api.PauseParams{ClaudeInstanceID: id})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			e.assertNoTmuxCall(t)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// pauSeedState seeds a row in state with its current-labelled session up.
func pauSeedState(state string) func(t *testing.T, e *killEnv) killRow {
	return func(t *testing.T, e *killEnv) killRow { return e.seedRow(t, killRowSpec{State: state}) }
}

// TestPauseDelivers: on Ours, one lookup and one listing, then /exit and
// Enter to the agent's pane by id; the row ended by its agent is success.
func TestPauseDelivers(t *testing.T) {
	cases := []struct {
		name      string
		teammates int
		client    bool
	}{
		{"api.Pause", 0, false},
		{"agent's pane among teammates", 2, false},
		{"Client.Pause", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			fastPausePolls(t)
			r := e.seedRow(t, killRowSpec{Teammates: tc.teammates})
			e.endAfterEnter(t, r)
			var err error
			if tc.client {
				_, _, err = e.pauseClient(t, pauseParams(r))
			} else {
				_, err = e.pause(pauseParams(r))
			}
			if err != nil {
				t.Fatalf("Pause: %v", err)
			}
			e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
			pauAssertState(t, e, r.ID, store.StateEnded)
		})
	}
}

// TestPauseWait: after /exit the wait polls at the interval until the row
// ends, stops on a cancelled context and returns a failed state read.
func TestPauseWait(t *testing.T) {
	const interval = 50 * time.Millisecond
	cases := []struct {
		name      string
		at        int // the sleep after which act runs (0: none)
		act       func(t *testing.T, e *killEnv, r killRow, cancel context.CancelFunc)
		failReads bool
		want      error
		state     string
	}{
		{"ended during the third sleep", 3, func(t *testing.T, e *killEnv, r killRow, _ context.CancelFunc) {
			if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", r.Spawn.ClaudeSessionID); !a.Applied {
				t.Errorf("SessionEnd not applied: %s", a.Reason)
			}
		}, false, nil, store.StateEnded},
		{"context cancelled during the second sleep", 2, func(_ *testing.T, _ *killEnv, _ killRow, cancel context.CancelFunc) {
			cancel()
		}, false, context.Canceled, store.StateWaiting},
		{"state read fails", 0, nil, true, errInjectedStore, store.StateWaiting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			slept := pauSleeps(t, interval, false, func(n int) {
				if n == tc.at {
					tc.act(t, e, r, cancel)
				}
			})
			if tc.failReads {
				e.store.failStateReads(nil)
			}
			_, err := e.pauseWithin(ctx, pauseTimeoutSeconds, pauseParams(r))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			if len(*slept) != tc.at {
				t.Errorf("sleeps = %v; want %d", *slept, tc.at)
			}
			for _, d := range *slept {
				if d != interval {
					t.Errorf("slept %v; want the poll interval %v", d, interval)
				}
			}
			e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
			pauAssertState(t, e, r.ID, tc.state)
		})
	}
}

// TestPauseTimeout: a row that stays waiting gives ErrPauseTimeout at the
// real-clock deadline, the last sleep cut short so none passes it.
func TestPauseTimeout(t *testing.T) {
	const interval = 600 * time.Millisecond
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	slept := pauSleeps(t, interval, true, nil)
	start := time.Now()
	_, err := e.pause(pauseParams(r))
	elapsed := time.Since(start)
	if !errors.Is(err, api.ErrPauseTimeout) || !strings.Contains(err.Error(), r.ID+" did not reach ended within 1s") {
		t.Fatalf("err = %v; want ErrPauseTimeout for %s within 1s", err, r.ID)
	}
	if elapsed < pauseTimeoutSeconds*time.Second {
		t.Errorf("returned after %v; want at least the %d s timeout", elapsed, pauseTimeoutSeconds)
	}
	var total time.Duration
	for _, d := range *slept {
		if d <= 0 || d > interval {
			t.Errorf("slept %v; want within (0, %v]", d, interval)
		}
		total += d
	}
	if len(*slept) < 2 || (*slept)[0] != interval || total > pauseTimeoutSeconds*time.Second {
		t.Errorf("sleeps = %v (total %v); want the interval first, then one cut at the deadline", *slept, total)
	}
	e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
	pauAssertState(t, e, r.ID, store.StateWaiting)
}

// TestPauseRenamedSession: the renamed session's agent pane gets /exit by
// id; a session now holding the recorded name never does.
func TestPauseRenamedSession(t *testing.T) {
	cases := []struct {
		name   string
		holder func(r killRow) *tmuxfix.SeedSession
	}{
		{"renamed", func(killRow) *tmuxfix.SeedSession { return nil }},
		{"renamed, unlabelled session holds the name", func(r killRow) *tmuxfix.SeedSession {
			return &tmuxfix.SeedSession{Name: r.Name}
		}},
		{"renamed, another store's session holds the name", func(r killRow) *tmuxfix.SeedSession {
			return &tmuxfix.SeedSession{Name: r.Name, Label: r.otherStore(r.Token),
				Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}}
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
			pauDeliver(t, e, r)
		})
	}
}

// TestPauseNeighbours (AC-LKP-01/02/03): a row with no session gets the gone
// error, never sends to a prefix-, name- or 8-character-id-sharing neighbour, and never waits.
func TestPauseNeighbours(t *testing.T) {
	cases := []struct {
		name         string
		xID, yID     string
		xName, yName string
	}{
		{"name prefix", "", "", "proj-abc", "proj-abc123"},
		{"same name, held by the other row's session", "", "", "proj-same", "proj-same"},
		{"ids share first 8 characters, same-named folders", "abcd1234-x-row", "abcd1234-y-row", "work", "work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			x := e.seedRow(t, killRowSpec{ID: tc.xID, NoSession: true,
				Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.xName)}})
			y := e.seedRow(t, killRowSpec{ID: tc.yID, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.yName)}})
			before := e.columns(t, x.ID)
			_, err := e.pause(pauseParams(x))
			if !errors.Is(err, api.ErrTmuxSendKeys) {
				t.Fatalf("err = %v; want ErrTmuxSendKeys", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescPaneGone(apitest.PaneGone{
				Verb: apitest.PanePause, InstanceID: x.ID, Name: x.Name}), y.ID, y.Token)
			e.assertPaneCalls(t, tmux.CallLookup)
			e.assertRowUnchanged(t, x.ID, before)
		})
	}
}

// TestPauseStoredNames (AC-LKP-09): a row whose recorded name holds $ or \
// is found by its label under tmux's stored form and gets /exit by pane id.
func TestPauseStoredNames(t *testing.T) {
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID {
			continue
		}
		t.Run(n.Raw, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(n.Raw)}})
			if r.Session.Name != n.Stored {
				t.Fatalf("seeded session name %q; want the stored form %q", r.Session.Name, n.Stored)
			}
			pauDeliver(t, e, r)
		})
	}
}

// TestPauseRecordedSocket (AC-LKP-19): with TMUX and TMUX_TMPDIR naming
// another server, every call names the row's recorded socket.
func TestPauseRecordedSocket(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	// No commas: TMUX's socket field ends at one.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("TMUX", elsewhere+",4242,0")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	e.seedOther(t, elsewhere, tmuxfix.SeedSession{Name: r.Name, Label: r.current(),
		Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}})
	pauDeliver(t, e, r)
	for _, c := range e.rec.SocketCalls() {
		if c.Socket != r.Socket {
			t.Errorf("%v on socket %q; want the recorded %q", c.Call, c.Socket, r.Socket)
		}
	}
}

// pauOtherStore seeds, under a new name, another store's session labelled
// with r's id and token, whose one pane carries token.
func pauOtherStore(t *testing.T, e *killEnv, r killRow, token string) {
	t.Helper()
	e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "other-store-" + uuid.NewString()[:8],
		Label: r.otherStore(token), Panes: []tmuxfix.SeedPane{{AdPane: token}}})
}

// TestPauseOtherStore (SR-3.4, AC-LKP-20): another store's session naming the
// row's id is never Ours or Leftover: alone it is Gone; beside this store's session it changes nothing.
func TestPauseOtherStore(t *testing.T) {
	tokens := []struct {
		name  string
		token func(r killRow) string
	}{
		{"this row's token", func(r killRow) string { return r.Token }},
		{"another token", func(killRow) string { return newToken() }},
	}
	worlds := []struct {
		name string
		// seed seeds the sessions beside r (no session yet) and returns the
		// error and description pause gives (nil: /exit delivered to r's pane).
		seed func(t *testing.T, e *killEnv, r *killRow, token string) (error, apitest.DescCase)
	}{
		{"alone, holding the recorded name and pane", func(t *testing.T, e *killEnv, r *killRow, token string) (error, apitest.DescCase) {
			e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.otherStore(token), true))
			return api.ErrTmuxSendKeys, apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PanePause,
				InstanceID: r.ID, Name: r.Name})
		}},
		{"beside this store's Ours", func(t *testing.T, e *killEnv, r *killRow, token string) (error, apitest.DescCase) {
			e.seedSession(t, r)
			pauOtherStore(t, e, *r, token)
			return nil, apitest.DescCase{}
		}},
		{"beside this store's leftover", func(t *testing.T, e *killEnv, r *killRow, token string) (error, apitest.DescCase) {
			lo := e.seedLeftover(t, *r, tmuxfix.OtherToken)
			pauOtherStore(t, e, *r, token)
			return api.ErrTmuxSessionConflict, apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PanePause,
				InstanceID: r.ID, Sessions: []apitest.DescSession{{Name: lo.Name, ID: lo.ID}}})
		}},
	}
	for _, tk := range tokens {
		for _, w := range worlds {
			t.Run(tk.name+"/"+w.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{NoSession: true})
				want, desc := w.seed(t, e, &r, tk.token(r))
				if want == nil {
					pauDeliver(t, e, r)
					return
				}
				before := e.columns(t, r.ID)
				_, err := e.pause(pauseParams(r))
				if !errors.Is(err, want) {
					t.Fatalf("err = %v; want %v", err, want)
				}
				apitest.AssertDescription(t, err.Error(), desc)
				e.assertPaneCalls(t, tmux.CallLookup)
				e.assertRowUnchanged(t, r.ID, before)
			})
		}
	}
}
