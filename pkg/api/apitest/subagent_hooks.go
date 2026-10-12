package apitest

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// The subagent hook helpers (b.xjn) apply one subagent hook event to id's row in
// the store at dbPath through the store's gated writes, as hook.Handle does, and
// return the row write's store.HookApplied; a store error fails the test. Tests
// use them instead of building a store.HookGate or SQL by hand. The
// ApplySubagent* form is the row's own agent (the hook's parent is its recorded
// pane process, so the gate holds whenever the row records a pane); the
// ApplyForeignSubagent* form is another process carrying the row's id (a nested
// agent, a teammate pane, a leftover), which the gate refuses: no entry and no
// claimable launch is written, and the store reports pid_mismatch, or
// no_pane_recorded for a row with no pane. Every event carries its receipt time
// in ReceivedMs (Unix milliseconds); none reads a clock. See storefix for the
// call sequence of each.

// SubagentLifecycleEvent is a SubagentStart or SubagentStop's event
// (store.SubagentLifecycleEvent); the helper sets its Kind. A stop's Listing is
// its background_tasks.
type SubagentLifecycleEvent = store.SubagentLifecycleEvent

// SubagentLaunchRequest is an Agent-tool launch request (store.SubagentLaunchRequest).
type SubagentLaunchRequest = store.SubagentLaunchRequest

// SubagentLaunchDetails is an Agent-tool launch result (store.SubagentLaunchDetails).
type SubagentLaunchDetails = store.SubagentLaunchDetails

// SubagentLaunchFailure is an Agent-tool launch failure (store.SubagentLaunchFailure).
type SubagentLaunchFailure = store.SubagentLaunchFailure

// SubagentListed is a subagent a snapshot or listing names: its id and optional
// type (store.SubagentListed). No description is ever carried.
type SubagentListed = store.SubagentListed

// SubagentSnapshot is a main-agent Stop's background_tasks, present
// (store.SubagentSnapshot); MainAgentStop.Snapshot nil is no snapshot.
type SubagentSnapshot = store.SubagentSnapshot

// MainAgentStop is a main-agent Stop with its optional snapshot
// (storefix.MainAgentStop). A SubagentStop's listing is
// SubagentLifecycleEvent.Listing.
type MainAgentStop = storefix.MainAgentStop

// ApplySubagentStart applies a SubagentStart (the combined soft refresh and
// start) to id's row in the store at dbPath as the row's own agent.
func ApplySubagentStart(t *testing.T, dbPath, id string, ev SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplySubagentStart")
	defer func() { _ = s.Close() }()
	return storefix.ApplySubagentStart(t, s, id, ev)
}

// ApplyForeignSubagentStart is ApplySubagentStart from another process.
func ApplyForeignSubagentStart(t *testing.T, dbPath, id string, ev SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignSubagentStart")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignSubagentStart(t, s, id, ev)
}

// ApplySubagentStop applies a SubagentStop (the combined soft refresh and stop;
// ev.SupersedesBlockedStop is stop_hook_active) as the row's own agent.
func ApplySubagentStop(t *testing.T, dbPath, id string, ev SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplySubagentStop")
	defer func() { _ = s.Close() }()
	return storefix.ApplySubagentStop(t, s, id, ev)
}

// ApplyForeignSubagentStop is ApplySubagentStop from another process.
func ApplyForeignSubagentStop(t *testing.T, dbPath, id string, ev SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignSubagentStop")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignSubagentStop(t, s, id, ev)
}

// ApplySubagentLaunchRequest applies an Agent tool's PreToolUse (the ordinary
// working write, then the claimable launch of req when it applied) as the row's
// own agent.
func ApplySubagentLaunchRequest(t *testing.T, dbPath, id string, req SubagentLaunchRequest) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplySubagentLaunchRequest")
	defer func() { _ = s.Close() }()
	return storefix.ApplySubagentLaunchRequest(t, s, id, req)
}

// ApplyForeignSubagentLaunchRequest is ApplySubagentLaunchRequest from another
// process.
func ApplyForeignSubagentLaunchRequest(t *testing.T, dbPath, id string, req SubagentLaunchRequest) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignSubagentLaunchRequest")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignSubagentLaunchRequest(t, s, id, req)
}

// ApplySubagentLaunchDetails applies an Agent tool's PostToolUse (the ordinary
// working write, then the launch result d when it applied) as the row's own
// agent.
func ApplySubagentLaunchDetails(t *testing.T, dbPath, id string, d SubagentLaunchDetails) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplySubagentLaunchDetails")
	defer func() { _ = s.Close() }()
	return storefix.ApplySubagentLaunchDetails(t, s, id, d)
}

// ApplyForeignSubagentLaunchDetails is ApplySubagentLaunchDetails from another
// process.
func ApplyForeignSubagentLaunchDetails(t *testing.T, dbPath, id string, d SubagentLaunchDetails) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignSubagentLaunchDetails")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignSubagentLaunchDetails(t, s, id, d)
}

// ApplySubagentLaunchFailure applies an Agent tool's PostToolUseFailure (the
// ordinary working write, then the close of the claimable launch of
// f.ToolUseID when it applied) as the row's own agent.
func ApplySubagentLaunchFailure(t *testing.T, dbPath, id string, f SubagentLaunchFailure) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplySubagentLaunchFailure")
	defer func() { _ = s.Close() }()
	return storefix.ApplySubagentLaunchFailure(t, s, id, f)
}

// ApplyForeignSubagentLaunchFailure is ApplySubagentLaunchFailure from another
// process.
func ApplyForeignSubagentLaunchFailure(t *testing.T, dbPath, id string, f SubagentLaunchFailure) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignSubagentLaunchFailure")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignSubagentLaunchFailure(t, s, id, f)
}

// ApplySubagentMainAgentStop applies a main-agent Stop (the ordinary waiting
// write, then, when it applied and the skip read finds something to change, the
// turn-end transaction with stop.Snapshot) as the row's own agent.
func ApplySubagentMainAgentStop(t *testing.T, dbPath, id string, stop MainAgentStop) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplySubagentMainAgentStop")
	defer func() { _ = s.Close() }()
	return storefix.ApplySubagentMainAgentStop(t, s, id, stop)
}

// ApplyForeignSubagentMainAgentStop is ApplySubagentMainAgentStop from another
// process.
func ApplyForeignSubagentMainAgentStop(t *testing.T, dbPath, id string, stop MainAgentStop) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignSubagentMainAgentStop")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignSubagentMainAgentStop(t, s, id, stop)
}
