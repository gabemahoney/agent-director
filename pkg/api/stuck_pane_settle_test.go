package api_test

// stuck_pane_settle_test.go — how a no_screen_change episode (b.ob8) enters,
// leaves and changes reason: settling and exact transition times, both reason
// switches, immediate lifecycle exits, command guards and family. It reuses
// stuck_pane_test.go's paneEnv; capture failures and races are there.

import (
	"context"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// enterPane commits e's baseline now, then reads the pane unchanged for its
// 60 s timeout and, with settle, for the settle time too, so the episode is
// displayed; it returns that entry's status.
func enterPane(t *testing.T, e *paneEnv, c *api.Client, settle time.Duration) api.StatusResult {
	t.Helper()
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	e.now = e.now.Add(time.Minute)
	if settle > 0 {
		observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
		e.now = e.now.Add(settle)
	}
	return assertStuckState(t, c, e.id, api.StuckNoScreenChange)
}

// TestPaneStuckSettlementAndActualTransitionTime: with zero and positive
// settling, entry and exit display exactly at their settle boundary, a
// delayed read stamps itself rather than the deadline, a clearing keeps the
// displayed detail until it settles, and the trail holds exactly the entry
// and the exit at their committing reads, never a raw state transition.
func TestPaneStuckSettlementAndActualTransitionTime(t *testing.T) {
	t.Parallel()
	for _, delay := range []time.Duration{0, 5 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
			c := e.client(t, apitest.ConfigKeys{StateSettleMs: delay.Milliseconds(), NoScreenChangeTimeoutSeconds: 60})
			mark := trailMark(t)
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			start := e.now
			e.now = start.Add(time.Minute)
			if delay > 0 {
				observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced) // the entry candidate
				e.now = e.now.Add(delay - time.Millisecond)
				assertStuckState(t, c, e.id, store.StateWorking)
				e.now = start.Add(time.Minute + delay + time.Hour) // a delayed first read past the deadline
			}
			entered := assertStuckState(t, c, e.id, api.StuckNoScreenChange)
			entryTime := e.now
			if entered.Since != entryTime.Format("2006-01-02T15:04:05.000Z") || minutes(t, entered) != int64(entryTime.Sub(start)/time.Minute) {
				t.Fatalf("entry=%+v; want since this read and the floored minutes since %s", entered, start)
			}
			e.show("pane 2")
			if delay > 0 {
				got := observeStatus(t, c, e.id, api.StuckNoScreenChange, apitest.VersionAdvanced) // the exit candidate keeps the detail
				if got.Since != entered.Since || minutes(t, got) != minutes(t, entered) {
					t.Fatalf("clearing changed the detail: %+v; want %+v", got, entered)
				}
				e.now = e.now.Add(delay - time.Millisecond)
				assertStuckState(t, c, e.id, api.StuckNoScreenChange)
				e.now = e.now.Add(time.Millisecond)
			}
			observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
			var events []map[string]any
			for _, ev := range readAPITrailLines(t)[mark:] {
				if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.state_transition" {
					t.Errorf("the report impersonated a raw transition: %v", ev)
				}
			}
			events = stuckEvents(t, mark, e.id)
			if len(events) != 2 {
				t.Fatalf("events=%v; want the entry and the exit once", events)
			}
			for i, want := range []struct {
				prior, next string
				at          time.Time
			}{{store.StateWorking, "stuck", entryTime}, {"stuck", store.StateWorking, e.now}} {
				if ev := events[i]; ev["prior_state"] != want.prior || ev["new_state"] != want.next || ev["stuck_reason"] != api.StuckNoScreenChange ||
					ev["ts"] != want.at.Format("2006-01-02T15:04:05.000Z") {
					t.Errorf("transition=%v; want %+v", ev, want)
				}
			}
		})
	}
}

// TestPaneStuckReasonSwitchKeepsEpisode: while a row is displayed stuck, the
// other reason taking over (a permission request on a stuck pane, or an
// unchanged pane after a stuck request is answered) settles its reason and
// detail together, keeps since and emits no exit or entry.
func TestPaneStuckReasonSwitchKeepsEpisode(t *testing.T) {
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
		toPane bool
	}{
		{"pane to permission/settle 0", 0, false},
		{"pane to permission/settle 5s", 5 * time.Second, false},
		{"permission to pane/settle 5s", 5 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := paneSpec{agent: store.AgentClaude}
			var timeout int64 = 60
			from, to := api.StuckNoScreenChange, api.StuckPermissionUnanswered
			if tc.toPane {
				spec.state, timeout = store.StateCheckPermission, 1
				from, to = to, from
			}
			e := newPaneEnv(t, spec)
			c := e.client(t, apitest.ConfigKeys{StateSettleMs: tc.delay.Milliseconds(), NoScreenChangeTimeoutSeconds: timeout})
			var entered api.StatusResult
			var req apitest.PermissionRequestSeed
			if tc.toPane {
				req = request(t, e)
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				e.now = e.now.Add(tc.delay)
				entered = assertStuckState(t, c, e.id, from)
			} else {
				entered = enterPane(t, e, c, tc.delay)
			}
			mark := trailMark(t)
			if tc.toPane {
				// The open dialog holds the turn's next hook: the permission
				// episode stands. Once the request is proven gone, it applies.
				if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWorking); err != nil {
					t.Fatal(err)
				}
				if got := observeStatus(t, c, e.id, from, nil); e.row(t).State != store.StateCheckPermission || got.StuckInfo != entered.StuckInfo {
					t.Fatalf("a held hook moved the row or its display: %+v raw %s", got, e.row(t).State)
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
			if tc.toPane {
				wantRaw = store.StateWorking
			}
			if raw := e.row(t).State; raw != wantRaw {
				t.Fatalf("raw state %s after the hook; want %s", raw, wantRaw)
			}
			e.now = e.now.Add(time.Second)
			if tc.toPane {
				// The answered row's first pane sample starts its baseline: a
				// pending exit, which the pane's own timeout overtakes.
				if got := observeStatus(t, c, e.id, from, apitest.VersionAdvanced); got.StuckInfo != entered.StuckInfo {
					t.Fatalf("pending exit=%+v; want the permission detail %+v", got, entered)
				}
				e.now = e.now.Add(time.Second)
			}
			if tc.delay > 0 {
				// The new reason settles in behind the displayed reason and detail.
				got := observeStatus(t, c, e.id, from, apitest.VersionAdvanced)
				if got.Since != entered.Since || got.RequestToken != entered.RequestToken {
					t.Fatalf("settling=%+v; want the displayed %+v", got, entered)
				}
				e.now = e.now.Add(tc.delay - time.Millisecond)
				observeStatus(t, c, e.id, from, nil)
				e.now = e.now.Add(time.Millisecond)
			}
			got := observeStatus(t, c, e.id, to, apitest.VersionAdvanced)
			if got.Since != entered.Since {
				t.Fatalf("switch moved since: %+v; want %s", got, entered.Since)
			}
			if tc.toPane {
				if minutes(t, got) != 0 {
					t.Fatalf("switch=%+v; want no_screen_change with 0 minutes", got)
				}
			} else if got.RequestToken != req.RequestToken || got.ToolName != "Bash" {
				t.Fatalf("switch=%+v; want permission_unanswered on %s", got, req.RequestToken)
			}
			if events := stuckEvents(t, mark, e.id); len(events) != 0 {
				t.Fatalf("the switch emitted %v", events)
			}
		})
	}
}

// TestPaneStuckTerminalExitIsImmediate: a terminal write ends a displayed
// no_screen_change episode itself, with its exit at the write's commit, read
// at once whatever the settle time.
func TestPaneStuckTerminalExitIsImmediate(t *testing.T) {
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
			enterPane(t, e, e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60}), 0)
			c := e.client(t, apitest.ConfigKeys{StateSettleMs: settleMax, NoScreenChangeTimeoutSeconds: 60})
			before := e.row(t)
			mark := trailMark(t)
			tc.write(t, e)
			if after := e.row(t); after.StuckObservation != "" || after.StuckVersion <= before.StuckVersion || after.PermissionEpoch != before.PermissionEpoch+1 {
				t.Fatalf("the write left the episode: before=%+v after=%+v", before, after)
			}
			assertStuckState(t, c, e.id, tc.state)
			assertStuckState(t, c, e.id, tc.state)
			_, stamp, err := apitest.ReadSettle(e.dbPath, e.id)
			if err != nil {
				t.Fatal(err)
			}
			events := stuckEvents(t, mark, e.id)
			if len(events) != 1 || events[0]["prior_state"] != "stuck" || events[0]["new_state"] != tc.state ||
				events[0]["stuck_reason"] != api.StuckNoScreenChange || events[0]["ts"] != time.UnixMilli(stamp).UTC().Format("2006-01-02T15:04:05.000Z") {
				t.Fatalf("exit=%v; want one at the write's commit %d", events, stamp)
			}
		})
	}
}

// TestPaneStuckLeavesCommandGuards: a displayed no_screen_change grants and
// refuses no command: pause still refuses the raw working row, typing
// nothing, send-keys still types into it, and the observation itself only
// looked up, listed and captured the pane. Neither touches the episode.
func TestPaneStuckLeavesCommandGuards(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentCodex})
	c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60})
	entered := enterPane(t, e, c, 0)
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
	if got := observeStatus(t, c, e.id, api.StuckNoScreenChange, nil); got.Since != entered.Since || e.row(t).State != store.StateWorking {
		t.Errorf("after the refusal: %+v, raw %s; want the same episode on a raw working row", got, e.row(t).State)
	}
	e.rec.Reset()
	if _, err := c.SendKeys(api.SendKeysParams{InstanceID: e.id, Text: "go on"}); err != nil {
		t.Fatalf("send-keys on a displayed stuck row: %v; want delivered", err)
	}
	assertTextSent(t, e.rec, apitest.TestSocket, apitest.TestPaneID, "go on")
	assertEnterSent(t, e.rec, apitest.TestSocket, apitest.TestPaneID)
	if got := observeStatus(t, c, e.id, api.StuckNoScreenChange, nil); got.Since != entered.Since || minutes(t, got) != minutes(t, entered) || e.row(t).State != store.StateWorking {
		t.Errorf("after send-keys: %+v, raw %s; want the episode %+v on a raw working row", got, e.row(t).State, entered)
	}
}

// TestPaneStuckFamilyIsBinaryAndDetailChildLocal: a child displayed
// no_screen_change contributes working to the family, which stays binary, and
// its stuck detail stays on the child.
func TestPaneStuckFamilyIsBinaryAndDetailChildLocal(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	parent, err := apitest.SeedSpawn(e.dbPath, "", store.StateWaiting, "", "off", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedParentChild(e.dbPath, parent, e.id); err != nil {
		t.Fatal(err)
	}
	c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60})
	child := enterPane(t, e, c, 0)
	got := assertStuckState(t, c, parent, store.StateWaiting)
	if child.FamilyState != store.StateWorking || got.FamilyState != store.StateWorking {
		t.Errorf("family: child %+v, parent %+v; want working on both", child, got)
	}
}

// TestPaneStuckRawMoveSettlesOut: the turn ending (a hook moving the raw
// state on) keeps the displayed episode until a read, which clears it with
// one exit to the new state at that read.
func TestPaneStuckRawMoveSettlesOut(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{NoScreenChangeTimeoutSeconds: 60})
	enterPane(t, e, c, 0)
	mark := trailMark(t)
	if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWaiting); err != nil {
		t.Fatal(err)
	}
	if row := e.row(t); row.CommittedStuckDisplay().State != "stuck" {
		t.Fatalf("the hook cleared the episode before any read: %+v", row)
	}
	e.now = e.now.Add(time.Second)
	observeStatus(t, c, e.id, store.StateWaiting, apitest.VersionAdvanced)
	events := stuckEvents(t, mark, e.id)
	if len(events) != 1 || events[0]["prior_state"] != "stuck" || events[0]["new_state"] != store.StateWaiting ||
		events[0]["stuck_reason"] != api.StuckNoScreenChange || events[0]["ts"] != e.now.Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("exit=%v; want one to waiting at the read", events)
	}
}
