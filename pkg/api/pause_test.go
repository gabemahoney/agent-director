package api_test

// pause_test.go: pause's state guards, the input line cleared before /exit
// (b.9o4), its wait (SR-7.1, SR-7.2, SR-17) and its trail (SR-3.16, SR-14,
// SR-15): no call event on any return path, and its ad.provenance.disagree
// records (verb pause, source ad_send_keys) written before the wait whatever
// ends it. Which pane it types /exit into, with read-pane and send-keys, is
// readpane_pane_test.go's; its failed keys calls and adoption write
// sendkeys_action_test.go's; its per-reason disagree records
// TestKeysVerbsTrailProvenanceDisagree's; a closed Client
// TestPaneVerbsUnknownIDAndClosedClient's; the unwritable trail
// TestTrailFailOpen's.

import (
	"context"
	"errors"
	"os"
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
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

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
	t.Parallel()
	seeded := func(state string) func(t *testing.T, e *killEnv) killRow {
		return func(t *testing.T, e *killEnv) killRow { return e.seedRow(t, killRowSpec{State: state}) }
	}
	cases := []struct {
		name    string
		seed    func(t *testing.T, e *killEnv) killRow
		unknown bool   // pause an id no row has
		want    string // "": the no-op success
	}{
		{"unknown id", seeded(store.StateWaiting), true, "ErrSpawnNotFound"},
		{"ended", seeded(store.StateEnded), false, ""},
		{"missing", seeded(store.StateMissing), false, ""},
		{"pending", seeded(store.StatePending), false, "ErrSpawnNotPausable"},
		{"pending beside a leftover", func(t *testing.T, e *killEnv) killRow {
			return e.seedPending(t, pendingFresh, pendingLeftover)
		}, false, "ErrSpawnNotPausable"},
		{"working", seeded(store.StateWorking), false, "ErrSpawnNotPausable"},
		{"ask_user", seeded(store.StateAskUser), false, "ErrSpawnNotPausable"},
		{"check_permission", seeded(store.StateCheckPermission), false, "ErrSpawnNotPausable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			id := r.ID
			if tc.unknown {
				id = "absent"
			}
			before := e.columns(t, r.ID)
			_, err := e.pause(api.PauseParams{ClaudeInstanceID: id})
			if tc.want == "" && err != nil {
				t.Fatalf("err = %v; want the no-op success", err)
			} else if tc.want != "" {
				assertOneName(t, err, tc.want)
			}
			e.assertNoTmuxCall(t)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestPauseClearsInputLineFirst (b.9o4): pause sends C-u to the agent's pane
// as a key before typing /exit, so a line left typed (a failed /exit, a
// draft) is cleared and the agent gets /exit alone, submitted once.
func TestPauseClearsInputLineFirst(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	for _, tc := range []struct{ name, typed string }{
		{"an unsubmitted /exit typed", exitText},
		{"a draft typed", "/mcp reconnect github"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastPausePolls(t)
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			in := paneAgentExiting(t, e, r, false, 1)
			in.box = tc.typed

			if _, err := e.pause(pauseParams(r)); err != nil {
				t.Fatalf("Pause: %v; want the row ended (agent got %q, %q left typed)", err, in.submitted, in.box)
			}

			e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
			in.assertSubmittedOnce(t, exitText)
			pauAssertState(t, e, r.ID, store.StateEnded)
		})
	}
}

// TestPauseWait: after /exit the wait polls at the interval until the row
// ends, stops on a cancelled context and returns a failed state read.
func TestPauseWait(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
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
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	const interval = 600 * time.Millisecond
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	slept := pauSleeps(t, interval, true, nil)
	start := time.Now()
	_, err := e.pause(pauseParams(r))
	elapsed := time.Since(start)
	assertOneName(t, err, "ErrPauseTimeout")
	if !strings.Contains(err.Error(), r.ID+" did not reach ended within 1s") {
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

// ptrEntry is one pause entry point: api.Pause, or Client.Pause when client.
type ptrEntry struct {
	name   string
	client bool
}

// ptrEntries are both entry points.
var ptrEntries = []ptrEntry{{"Pause", false}, {"Client.Pause", true}}

// pause runs pause on id with ctx through the entry point (api.Pause waits
// pauseTimeoutSeconds, Client.Pause the config default).
func (en ptrEntry) pause(ctx context.Context, t *testing.T, e *killEnv, id string) error {
	t.Helper()
	p := api.PauseParams{ClaudeInstanceID: id}
	if en.client {
		c, _ := e.client(t)
		_, err := c.Pause(ctx, p)
		return err
	}
	_, err := e.pauseWithin(ctx, pauseTimeoutSeconds, p)
	return err
}

// ptrCancelAfterEnter is a context cancelled when an Enter call returns,
// so a delivered /exit's wait ends at once.
func ptrCancelAfterEnter(t *testing.T, e *killEnv) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.rec.AfterCall(tmux.CallSendEnter, func(tmuxfix.SocketCall, error) { cancel() })
	return ctx
}

// ptrEnds ends r's row as its own agent after the Enter call.
func ptrEnds(t *testing.T, e *killEnv, r *killRow) { e.endAfterEnter(t, *r) }

// ptrUnusableSocket seeds a row with no recorded socket whose resolved
// socket directory is unusable.
func ptrUnusableSocket(t *testing.T, e *killEnv) killRow {
	e.ownSocketDir(t)
	r := e.seedRow(t, killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}})
	if err := os.Chmod(filepath.Dir(e.defaultSocket), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return r
}

// ptrAssertOnlyDisagrees fails when a trail record for id written after mark
// is anything but pause's ad.provenance.disagree (none at all when none) or
// the state transition of the agent's own SessionEnd.
func ptrAssertOnlyDisagrees(t *testing.T, mark int, id string, none bool) {
	t.Helper()
	for _, l := range readAPITrailLines(t)[mark:] {
		if l["claude_instance_id"] != id || l["event"] == "ad.spawn.state_transition" {
			continue
		}
		if none || l["event"] != "ad.provenance.disagree" || l["verb"] != "pause" {
			t.Errorf("trail record %v (verb %v) for %s; want only pause's disagree records", l["event"], l["verb"], id)
		}
	}
}

// TestPauseTrailNoCallEvent: no return path of pause writes a call event
// (no ad.pause.*, no ad.send_keys.called); a path with no lookup writes nothing.
// Each case checks only its own row's records, so the cases run in parallel,
// except the one that takes its own TMUX_TMPDIR (t.Setenv).
func TestPauseTrailNoCallEvent(t *testing.T) {
	// Serial: its unusable-socket-directory case sets TMUX_TMPDIR (t.Setenv); its other cases run in
	// parallel.
	cases := []struct {
		name     string
		seed     sktSeed // nil: an unknown id
		outcome  string
		noLookup bool
		apiOnly  bool // Client.Pause would wait the config's 30 s
		ownTmux  bool // the seed takes its own TMUX_TMPDIR (ptrUnusableSocket), so the case is serial
	}{
		{name: "unknown id", outcome: "ErrSpawnNotFound", noLookup: true},
		{name: "ended row", seed: sktRow(killRowSpec{State: store.StateEnded, NoSession: true}), outcome: "ok", noLookup: true},
		{name: "missing row", seed: sktRow(killRowSpec{State: store.StateMissing, NoSession: true}), outcome: "ok", noLookup: true},
		{name: "pending row", seed: sktRow(killRowSpec{State: store.StatePending}), outcome: "ErrSpawnNotPausable", noLookup: true},
		{name: "working row", seed: sktRow(killRowSpec{State: store.StateWorking}), outcome: "ErrSpawnNotPausable", noLookup: true},
		{name: "unusable socket directory", seed: ptrUnusableSocket, outcome: "ErrTmuxNotAvailable", noLookup: true,
			ownTmux: true},
		{name: "ours, row ends", seed: sktRow(killRowSpec{}, ptrEnds), outcome: "ok"},
		{name: "ours, wait times out", seed: sktRow(killRowSpec{}), outcome: "ErrPauseTimeout", apiOnly: true},
		{name: "leftover", seed: sktRow(killRowSpec{NoSession: true}, sktLeftover), outcome: "ErrTmuxSessionConflict"},
		{name: "pane not found", seed: sktRow(killRowSpec{NoSession: true}, rpnSeedPane), outcome: "ErrTmuxSessionConflict"},
		{name: "gone", seed: sktRow(killRowSpec{NoSession: true}), outcome: "ErrTmuxSendKeys"},
		{name: "different server", seed: sktRow(killRowSpec{}, ktrRebind), outcome: "ErrTmuxNotAvailable"},
		{name: "conflicting labels", seed: sktRow(killRowSpec{}, sktDuplicate), outcome: "ErrTmuxSessionConflict"},
		{name: "unreadable lookup", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallLookup)),
			outcome: "ErrTmuxUnresponsive"},
		{name: "tmux unavailable", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnavailable, tmux.CallLookup)),
			outcome: "ErrTmuxNotAvailable"},
		{name: "/exit call timed out", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallSendText)),
			outcome: "ErrTmuxUnresponsive"},
		{name: "Enter call failed, follow-up ours", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnrecognized, tmux.CallSendEnter)),
			outcome: "ErrTmuxUnresponsive"},
		{name: "follow-up gone", seed: sktRow(killRowSpec{}, sktSessionGoesAfterListing), outcome: "ErrTmuxSendKeys"},
	}
	for _, entry := range ptrEntries {
		for _, tc := range cases {
			if tc.apiOnly && entry.client {
				continue
			}
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				if !tc.ownTmux {
					t.Parallel()
				}
				e := newKillEnv(t)
				id := "pause-unknown-" + uuid.NewString()[:8]
				if tc.seed != nil {
					id = tc.seed(t, e).ID
				}
				mark := trailMark(t)

				err := entry.pause(context.Background(), t, e, id)

				if name, _ := errnames.Classify(err); (err == nil) != (tc.outcome == "ok") || (err != nil && name != tc.outcome) {
					t.Errorf("err = %v (class %q); want outcome %s", err, name, tc.outcome)
				}
				ptrAssertOnlyDisagrees(t, mark, id, tc.noLookup)
			})
		}
	}
}

// ptrLineClearDisagreeCases are pause's own rows of the keys verbs' disagree
// table (b.9o4): a line clear (C-u) that timed out or failed types no text,
// so the call's action is nothing_sent.
func ptrLineClearDisagreeCases() []keysDisagreeCase {
	type setups = []func(*testing.T, *killEnv, *killRow)
	nothingSent := []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: "nothing_sent",
		ours: true}}
	return []keysDisagreeCase{
		{name: "server_restarted, line clear timed out",
			setup: setups{ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendKey)}, want: nothingSent},
		{name: "server_restarted on the lookup and the follow-up, line clear failed",
			setup: setups{ktrRestart, ktrScript(tmux.FailUnrecognized, tmux.CallSendKey)}, want: nothingSent},
	}
}

// ptrDisagreesAtFirstSleep makes the wait's first sleep read id's pause
// disagree records; the result is nil until it slept. Knobs restored at cleanup.
func ptrDisagreesAtFirstSleep(t *testing.T, id string) func() []map[string]any {
	t.Helper()
	interval, sleep := api.PauseTestKnobs()
	t.Cleanup(func() { api.SetPauseTestKnobs(interval, sleep) })
	var seen []map[string]any
	api.SetPauseTestKnobs(interval, func(d time.Duration) {
		if seen == nil {
			seen = append([]map[string]any{}, pauseDisagrees(t, id)...)
		}
		sleep(d)
	})
	return func() []map[string]any { return seen }
}

// TestPauseTrailWrittenBeforeTheWait: the records are written before the wait
// and are the same whether the row ends, the wait times out or the caller cancels.
func TestPauseTrailWrittenBeforeTheWait(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	var rows []keysDisagreeCase
	for _, tc := range keysDisagreeCases() {
		if tc.name == "server_restarted" || tc.name == "adopted and name_changed" {
			rows = append(rows, tc)
		}
	}
	outcomes := []struct {
		name    string
		prepare func(*testing.T, *killEnv, killRow) context.Context
		want    error // nil: success
		sleeps  bool  // the wait sleeps before it ends
	}{
		{"row ends", func(t *testing.T, e *killEnv, r killRow) context.Context {
			e.endAfterEnter(t, r)
			return context.Background()
		}, nil, false},
		{"wait times out", func(*testing.T, *killEnv, killRow) context.Context { return context.Background() },
			api.ErrPauseTimeout, true},
		{"caller cancels", func(t *testing.T, e *killEnv, _ killRow) context.Context { return ptrCancelAfterEnter(t, e) },
			context.Canceled, false},
	}
	for _, row := range rows {
		for _, oc := range outcomes {
			t.Run(row.name+"/"+oc.name, func(t *testing.T) {
				e := newKillEnv(t)
				r, other := e.seedKeysDisagreeCase(t, row)
				atSleep := ptrDisagreesAtFirstSleep(t, r.ID)
				mark := trailMark(t)

				_, err := e.pauseWithin(oc.prepare(t, e, r), pauseTimeoutSeconds, pauseParams(r))

				if (oc.want == nil) != (err == nil) || !errors.Is(err, oc.want) {
					t.Fatalf("err = %v; want %v", err, oc.want)
				}
				assertKeysDisagrees(t, e, pauseDisagrees(t, r.ID), r, "pause", other, row.want)
				if oc.sleeps {
					assertKeysDisagrees(t, e, atSleep(), r, "pause", other, row.want)
				}
				ptrAssertOnlyDisagrees(t, mark, r.ID, false)
			})
		}
	}
}

// ptrFailOpenRuns pauses one row per trail shape, ids prefix-<name>, and
// returns one line per call: its error, calls and row columns. With a working
// trail each wrote disagree records.
func ptrFailOpenRuns(t *testing.T, prefix string, working bool) []string {
	t.Helper()
	type setups = []func(*testing.T, *killEnv, *killRow)
	cases := []struct {
		name     string
		spec     killRowSpec
		setups   setups
		apiOnly  bool // the wait times out: Client.Pause would wait 30 s
		disagree int
	}{
		{name: "ours", setups: setups{ptrEnds}},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true, NoPane: true}, setups: setups{ptrEnds}, disagree: 1},
		{name: "restarted-timeout", setups: setups{ktrRestart}, apiOnly: true, disagree: 1},
		{name: "restarted-exit-timeout", setups: setups{ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendText)}, disagree: 1},
		{name: "rebound", setups: setups{ktrRebind}, disagree: 1},
		{name: "gone", spec: killRowSpec{NoSession: true}},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		spec := tc.spec
		spec.ID = prefix + "-" + tc.name
		r := sktRow(spec, tc.setups...)(t, e)
		err := ptrEntry{client: !tc.apiOnly}.pause(context.Background(), t, e, r.ID)
		if n := len(pauseDisagrees(t, r.ID)); working && n != tc.disagree {
			t.Fatalf("working trail: %s wrote %d disagree records; want %d", r.ID, n, tc.disagree)
		}
		lines = append(lines, failOpenLine(t, e, tc.name, r.ID, err))
	}
	return lines
}
