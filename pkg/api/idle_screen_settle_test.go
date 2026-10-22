package api_test

// idle_screen_settle_test.go — how an idle_screen episode (b.ob8) enters,
// leaves and moves to or from needs_intelligence: settling and exact
// transition times, the direct move in each direction, immediate lifecycle
// exits, command guards and family. It reuses idle_screen_test.go's paneEnv;
// capture failures and races are there.

import (
	"context"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// enterIdleScreen commits e's baseline now, then reads the pane unchanged for
// its 60 s timeout and, with settle, for the settle time too, so the episode
// is displayed; it returns that entry's status.
func enterIdleScreen(t *testing.T, e *paneEnv, c *api.Client, settle time.Duration) api.StatusResult {
	t.Helper()
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	e.now = e.now.Add(time.Minute)
	if settle > 0 {
		observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
		e.now = e.now.Add(settle)
	}
	return assertDisplayState(t, c, e.id, api.StateIdleScreen)
}

// TestIdleScreenSettlementAndActualTransitionTime: with zero and positive
// settling, entry and exit display exactly at their settle boundary, a
// delayed read stamps itself rather than the deadline, a clearing keeps the
// displayed detail until it settles, and the trail holds exactly the entry
// and the exit at their committing reads, never a raw state transition.
func TestIdleScreenSettlementAndActualTransitionTime(t *testing.T) {
	t.Parallel()
	for _, delay := range []time.Duration{0, 5 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
			c := e.client(t, apitest.ConfigKeys{StateSettleMs: delay.Milliseconds(), IdleScreenTimeoutSeconds: 60})
			mark := trailMark(t)
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			start := e.now
			e.now = start.Add(time.Minute)
			if delay > 0 {
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced) // the entry candidate
				e.now = e.now.Add(delay - time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateWorking)
				e.now = start.Add(time.Minute + delay + time.Hour) // a delayed first read past the deadline
			}
			entered := assertDisplayState(t, c, e.id, api.StateIdleScreen)
			entryTime := e.now
			if entered.Since != entryTime.Format(displayTS) || minutes(t, entered) != int64(entryTime.Sub(start)/time.Minute) {
				t.Fatalf("entry=%+v; want since this read and the floored minutes since %s", entered, start)
			}
			e.show("pane 2")
			if delay > 0 {
				got := observeStatus(t, c, e.id, api.StateIdleScreen, apitest.VersionAdvanced) // the exit candidate keeps the detail
				if got.Since != entered.Since || minutes(t, got) != minutes(t, entered) {
					t.Fatalf("clearing changed the detail: %+v; want %+v", got, entered)
				}
				e.now = e.now.Add(delay - time.Millisecond)
				assertDisplayState(t, c, e.id, api.StateIdleScreen)
				e.now = e.now.Add(time.Millisecond)
			}
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			for _, ev := range readAPITrailLines(t)[mark:] {
				if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.state_transition" {
					t.Errorf("the report impersonated a raw transition: %v", ev)
				}
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 2 {
				t.Fatalf("events=%v; want the entry and the exit once", events)
			}
			assertDisplayEvent(t, events[0], store.StateWorking, api.StateIdleScreen, entryTime)
			assertDisplayEvent(t, events[1], api.StateIdleScreen, store.StateWorking, e.now)
		})
	}
}

// TestDisplayDirectMove: while one Displayed State is shown and the other
// qualifies (a permission request on an idle_screen pane, or an unchanged pane
// after a needs_intelligence request is answered), the other settles in as an
// ordinary candidate (SR-5.1): until the settle deadline the first stays with
// its last committed detail, then the first read after it shows the second
// with since at that read, only its own detail, and exactly one record from
// the first to the second. The idle_screen to needs_intelligence move is a new
// check_permission interval qualifying for the other state: the idle_screen
// display stays while needs_intelligence settles in as its candidate.
func TestDisplayDirectMove(t *testing.T) {
	t.Parallel()
	request := func(t *testing.T, e *paneEnv) apitest.PermissionRequestSeed {
		t.Helper()
		seeded, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Bash", apitest.PermissionTimes(e.now.Add(-time.Hour), time.Time{},
			e.now.Add(-time.Minute), time.Time{}, e.now.Add(-time.Minute), store.FallbackDeadline))
		if err != nil {
			t.Fatal(err)
		}
		return seeded
	}
	for _, tc := range []struct {
		name   string
		delay  time.Duration
		toIdle bool
	}{
		{"idle_screen to needs_intelligence/settle 0", 0, false},
		{"idle_screen to needs_intelligence/settle 5s", 5 * time.Second, false},
		{"needs_intelligence to idle_screen/settle 5s", 5 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := paneSpec{agent: store.AgentClaude}
			var timeout int64 = 60
			from, to := api.StateIdleScreen, api.StateNeedsIntelligence
			if tc.toIdle {
				spec.state, timeout = store.StateCheckPermission, 1
				from, to = to, from
			}
			e := newPaneEnv(t, spec)
			c := e.client(t, apitest.ConfigKeys{StateSettleMs: tc.delay.Milliseconds(), IdleScreenTimeoutSeconds: timeout})
			var entered api.StatusResult
			var req apitest.PermissionRequestSeed
			if tc.toIdle {
				req = request(t, e)
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				e.now = e.now.Add(tc.delay)
				entered = assertDisplayState(t, c, e.id, from)
			} else {
				entered = enterIdleScreen(t, e, c, tc.delay)
			}
			// holds fails unless got still shows the first state with its
			// detail as entered.
			holds := func(got api.StatusResult) {
				t.Helper()
				if got.Since != entered.Since || got.RequestToken != entered.RequestToken || got.ToolName != entered.ToolName ||
					(entered.UnchangedMinutes != nil && minutes(t, got) != *entered.UnchangedMinutes) {
					t.Fatalf("settling=%+v; want the displayed %+v", got.StateDetail, entered.StateDetail)
				}
			}
			mark := trailMark(t)
			if tc.toIdle {
				// The open dialog holds the turn's next hook: the
				// needs_intelligence episode stands. Once the request is proven
				// gone, it applies.
				if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWorking); err != nil {
					t.Fatal(err)
				}
				holds(observeStatus(t, c, e.id, from, nil))
				if raw := e.row(t).State; raw != store.StateCheckPermission {
					t.Fatalf("a held hook moved the row to %s", raw)
				}
				if err := apitest.SetPermissionFacts(e.dbPath, req.RequestID, apitest.PermissionProof(e.now)); err != nil {
					t.Fatal(err)
				}
				if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWorking); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := seedAgentState(e.s, e.dbPath, e.id, store.StateCheckPermission); err != nil {
					t.Fatal(err)
				}
				req = request(t, e)
			}
			wantRaw := store.StateCheckPermission
			if tc.toIdle {
				wantRaw = store.StateWorking
			}
			if raw := e.row(t).State; raw != wantRaw {
				t.Fatalf("raw state %s after the hook; want %s", raw, wantRaw)
			}
			e.now = e.now.Add(time.Second)
			if tc.toIdle {
				// The answered row's first pane sample starts its baseline: a
				// pending exit, which the pane's own timeout overtakes.
				holds(observeStatus(t, c, e.id, from, apitest.VersionAdvanced))
				e.now = e.now.Add(time.Second)
			}
			if tc.delay > 0 {
				// The other state settles in as the candidate behind the
				// displayed state and its detail.
				holds(observeStatus(t, c, e.id, from, apitest.VersionAdvanced))
				e.now = e.now.Add(tc.delay - time.Millisecond)
				holds(observeStatus(t, c, e.id, from, nil))
				if events := displayEvents(t, mark, e.id); len(events) != 0 {
					t.Fatalf("records before the move's deadline: %v", events)
				}
				// The first read after the deadline, a second past it, moves.
				e.now = e.now.Add(time.Millisecond + time.Second)
			}
			got := observeStatus(t, c, e.id, to, apitest.VersionAdvanced)
			if got.Since != e.now.Format(displayTS) {
				t.Fatalf("move=%+v; want since this read, %s", got.StateDetail, e.now.Format(displayTS))
			}
			if tc.toIdle {
				if minutes(t, got) != 0 {
					t.Fatalf("move=%+v; want idle_screen with 0 minutes", got)
				}
			} else if got.RequestToken != req.RequestToken || got.ToolName != "Bash" {
				t.Fatalf("move=%+v; want needs_intelligence on %s", got, req.RequestToken)
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("events=%v; want the move once", events)
			}
			assertDisplayEvent(t, events[0], from, to, e.now)
		})
	}
}

// TestIdleScreenTerminalExitIsImmediate: a terminal write ends a displayed
// idle_screen episode itself, with its exit at the write's commit, read at
// once whatever the settle time.
func TestIdleScreenTerminalExitIsImmediate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state string
		write func(*testing.T, *paneEnv)
	}{
		{store.StateEnded, func(t *testing.T, e *paneEnv) {
			if err := seedAgentState(e.s, e.dbPath, e.id, store.StateEnded); err != nil {
				t.Fatal(err)
			}
		}},
		{store.StateMissing, func(t *testing.T, e *paneEnv) {
			if _, res, err := e.s.MarkMissingIfSameLife(e.id, e.row(t).Snapshot); err != nil || res != store.CondApplied {
				t.Fatalf("missing=%v %v", res, err)
			}
		}},
	} {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
			enterIdleScreen(t, e, e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60}), 0)
			c := e.client(t, apitest.ConfigKeys{StateSettleMs: settleMax, IdleScreenTimeoutSeconds: 60})
			before := e.row(t)
			mark := trailMark(t)
			tc.write(t, e)
			if after := e.row(t); after.DisplayObservation != "" || after.ObservationVersion <= before.ObservationVersion || after.PermissionEpoch != before.PermissionEpoch+1 {
				t.Fatalf("the write left the episode: before=%+v after=%+v", before, after)
			}
			assertDisplayState(t, c, e.id, tc.state)
			assertDisplayState(t, c, e.id, tc.state)
			_, stamp, err := apitest.ReadSettle(e.dbPath, e.id)
			if err != nil {
				t.Fatal(err)
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("exit=%v; want one at the write's commit %d", events, stamp)
			}
			assertDisplayEvent(t, events[0], api.StateIdleScreen, tc.state, time.UnixMilli(stamp))
		})
	}
}

// TestIdleScreenLeavesCommandGuards: a displayed idle_screen grants and
// refuses no command: pause still refuses the raw working row, typing
// nothing, send-keys still types into it, and the observation itself only
// looked up, listed and captured the pane. Neither touches the episode.
func TestIdleScreenLeavesCommandGuards(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	entered := enterIdleScreen(t, e, c, 0)
	for _, call := range e.rec.SocketCalls() {
		if call.Call != tmux.CallLookup && call.Call != tmux.CallListPanes && call.Call != tmux.CallCapture {
			t.Errorf("observation made a %s call", call.Call)
		}
	}
	e.rec.Reset()
	_, err := c.Pause(context.Background(), api.PauseParams{InstanceID: e.id})
	assertOneSentinel(t, err, api.ErrSpawnNotPausable)
	if calls := e.rec.SocketCalls(); len(calls) != 0 {
		t.Errorf("pause made tmux calls %+v; want none", calls)
	}
	if got := observeStatus(t, c, e.id, api.StateIdleScreen, nil); got.Since != entered.Since || e.row(t).State != store.StateWorking {
		t.Errorf("after the refusal: %+v, raw %s; want the same episode on a raw working row", got, e.row(t).State)
	}
	e.rec.Reset()
	if _, err := c.SendKeys(api.SendKeysParams{InstanceID: e.id, Text: "go on"}); err != nil {
		t.Fatalf("send-keys on a row displayed idle_screen: %v; want delivered", err)
	}
	assertTextSent(t, e.rec, apitest.TestSocket, apitest.TestPaneID, "go on")
	assertEnterSent(t, e.rec, apitest.TestSocket, apitest.TestPaneID)
	if got := observeStatus(t, c, e.id, api.StateIdleScreen, nil); got.Since != entered.Since || minutes(t, got) != minutes(t, entered) || e.row(t).State != store.StateWorking {
		t.Errorf("after send-keys: %+v, raw %s; want the episode %+v on a raw working row", got, e.row(t).State, entered)
	}
}

// TestIdleScreenFamilyIsBinaryAndDetailChildLocal: a child displayed
// idle_screen contributes working to the family, which stays binary, and its
// detail stays on the child.
func TestIdleScreenFamilyIsBinaryAndDetailChildLocal(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	parent, err := apitest.SeedSpawn(e.dbPath, "", store.StateWaiting, "", "off", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedParentChild(e.dbPath, parent, e.id); err != nil {
		t.Fatal(err)
	}
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	child := enterIdleScreen(t, e, c, 0)
	got := assertDisplayState(t, c, parent, store.StateWaiting)
	if child.FamilyState != store.StateWorking || got.FamilyState != store.StateWorking {
		t.Errorf("family: child %+v, parent %+v; want working on both", child, got)
	}
}

// TestIdleScreenRawMoveSettlesOut: the turn ending (a hook moving the raw
// state on) keeps the displayed episode until a read, which clears it with
// one exit to the new state at that read.
func TestIdleScreenRawMoveSettlesOut(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
	enterIdleScreen(t, e, c, 0)
	mark := trailMark(t)
	if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWaiting); err != nil {
		t.Fatal(err)
	}
	if row := e.row(t); row.CommittedDisplay().State != api.StateIdleScreen {
		t.Fatalf("the hook cleared the episode before any read: %+v", row)
	}
	e.now = e.now.Add(time.Second)
	observeStatus(t, c, e.id, store.StateWaiting, apitest.VersionAdvanced)
	events := displayEvents(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("exit=%v; want one to waiting at the read", events)
	}
	assertDisplayEvent(t, events[0], api.StateIdleScreen, store.StateWaiting, e.now)
}
