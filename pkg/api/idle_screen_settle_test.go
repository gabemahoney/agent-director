package api_test

// idle_screen_settle_test.go — how an idle_screen episode (b.ob8) enters and
// leaves: settling and exact transition times, a raw move out (to waiting, or
// to check_permission, which never shows needs_intelligence: b.zpv), a new
// interval never inheriting the old episode, immediate lifecycle exits,
// command guards and family. It reuses
// idle_screen_test.go's paneEnv; capture failures and races are there.

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

// TestIdleScreenNewIntervalEndsOldEpisodeAndStartsFresh: a new working
// interval qualifying for idle_screen never inherits the previous interval's
// episode. The turn ends and a new one starts with no read in between, so the
// hooks keep the displayed episode across the new permission epoch. The new
// interval's pane baseline starts at its first read, which therefore cannot
// qualify yet and leaves the old episode settling out; the read that first
// qualifies (the timeout, 1 s, inside the 5 s settle time) ends the old
// episode with its own exit, without settling, and starts a fresh candidate,
// which shows only after its settle time with its own since and minutes.
// With no settle time the first read ends the old episode itself, so the rule
// shows only with a settle time longer than the timeout.
func TestIdleScreenNewIntervalEndsOldEpisodeAndStartsFresh(t *testing.T) {
	t.Parallel()
	const settle = 5 * time.Second
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	// Entry precedes the hooks' real writer clock on any execution date.
	e.now = time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Millisecond)
	c := e.client(t, apitest.ConfigKeys{StateSettleMs: settle.Milliseconds(), IdleScreenTimeoutSeconds: 1})
	old := enterIdleScreen(t, e, c, settle)
	before := e.row(t)
	for _, state := range []string{store.StateWaiting, store.StateWorking} {
		if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
			t.Fatal(err)
		}
	}
	if row := e.row(t); row.State != store.StateWorking || row.PermissionEpoch != before.PermissionEpoch+2 || row.CommittedDisplay().State != api.StateIdleScreen {
		t.Fatalf("the hooks did not start a new interval keeping the episode: before=%+v after=%+v", before, row)
	}
	_, stamp, err := apitest.ReadSettle(e.dbPath, e.id)
	if err != nil {
		t.Fatal(err)
	}
	mark := trailMark(t)
	// The new interval's first read: its baseline starts now, so it does not
	// qualify and the old episode starts settling out, still displayed.
	start := time.UnixMilli(stamp).Add(time.Hour).UTC()
	e.now = start
	if got := observeStatus(t, c, e.id, api.StateIdleScreen, apitest.VersionAdvanced); got.Since != old.Since {
		t.Fatalf("settling-out read=%+v; want the old episode %+v", got, old)
	}
	// The first qualifying read ends the old episode at once.
	e.now = start.Add(time.Second)
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	events := displayEvents(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want only the old episode's exit", events)
	}
	assertDisplayEvent(t, events[0], api.StateIdleScreen, store.StateWorking, e.now)
	candidate := e.now
	e.now = candidate.Add(settle - time.Millisecond)
	observeStatus(t, c, e.id, store.StateWorking, nil)
	e.now = candidate.Add(settle)
	got := observeStatus(t, c, e.id, api.StateIdleScreen, apitest.VersionAdvanced)
	if got.Since != e.now.Format(displayTS) || minutes(t, got) != int64(e.now.Sub(start)/time.Minute) {
		t.Fatalf("new episode inherited the old one: old=%+v new=%+v", old, got)
	}
	events = displayEvents(t, mark, e.id)
	if len(events) != 2 {
		t.Fatalf("events=%v; want one exit and one entry", events)
	}
	assertDisplayEvent(t, events[1], store.StateWorking, api.StateIdleScreen, e.now)
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

// TestIdleScreenRawMoveSettlesOut: the turn ending, or a permission request
// (a hook moving the raw state on), keeps the displayed episode until a read,
// which clears it with one exit to the new state at that read. A request that
// has fallen back reads check_permission with no Displayed State (b.zpv).
func TestIdleScreenRawMoveSettlesOut(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{store.StateWaiting, store.StateCheckPermission} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
			c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
			enterIdleScreen(t, e, c, 0)
			mark := trailMark(t)
			if err := seedAgentState(e.s, e.dbPath, e.id, raw); err != nil {
				t.Fatal(err)
			}
			if raw == store.StateCheckPermission {
				if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Bash", apitest.PermissionTimes(e.now.Add(-time.Hour), time.Time{},
					e.now.Add(-time.Minute), time.Time{}, e.now.Add(-time.Minute), store.FallbackDeadline)); err != nil {
					t.Fatal(err)
				}
			}
			if row := e.row(t); row.CommittedDisplay().State != api.StateIdleScreen {
				t.Fatalf("the hook cleared the episode before any read: %+v", row)
			}
			e.now = e.now.Add(time.Second)
			observeStatus(t, c, e.id, raw, apitest.VersionAdvanced)
			exitAt := e.now
			// A day later the row still reads its raw state, displaying nothing.
			e.now = e.now.Add(24 * time.Hour)
			if got, row, listed := clientReported(t, c, e.id); got.State != raw || got.StateDetail != (api.StateDetail{}) ||
				row.StateDetail != (api.StateDetail{}) || listed.DisplayedState != "" {
				t.Fatalf("status=%+v get=%+v list=%+v; want ordinary %s", got, row.StateDetail, listed, raw)
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("exit=%v; want one to %s at the read", events, raw)
			}
			assertDisplayEvent(t, events[0], api.StateIdleScreen, raw, exitAt)
		})
	}
}
