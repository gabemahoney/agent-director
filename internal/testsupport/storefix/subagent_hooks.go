package storefix

// subagent_hooks.go — applying the subagent hook events (b.xjn) through the
// store's gated writes, as hook.Handle does. One helper per event, each in two
// parent forms: the row's own agent (ApplySubagent*, the hook's parent is the
// row's recorded pane process, AgentHookParent) and another process carrying
// the row's id (ApplyForeignSubagent*, a parent that is never the pane,
// ForeignHookParentPID), which the gate refuses: no entry and no claimable launch
// is written and the store reports pid_mismatch, or no_pane_recorded for a row
// with no pane. Inputs are the store's agent-neutral event values, not payloads,
// and carry the receipt time (ReceivedMs), which is injected: no helper reads a
// clock. Every helper returns the row write's store.HookApplied and fails the
// test on a store error, so tests never build a store.HookGate or SQL by hand.
//
// The call sequence mirrors the handler's: SubagentStart and SubagentStop are
// the one combined store transaction (Store.ApplySubagentLifecycle); the launch
// request, launch result and launch failure are the Agent tool's ordinary
// working write followed, only when it applied, by the entry transaction
// (Store.ApplySubagentAfterWrite); a main-agent Stop is its ordinary waiting
// write followed, when it applied and the skip read finds a claimable launch to
// close, by the turn-end transaction.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// MainAgentStop is a main-agent Stop (a Stop with no agent_id) as
// ApplySubagentMainAgentStop applies it. It has no field yet beyond the receipt
// time; Epic 2's snapshot listings are added here without breaking a caller.
type MainAgentStop struct {
	// ReceivedMs is the hook's receipt time in Unix milliseconds, injected.
	ReceivedMs int64
}

// subagentHookGate returns the gate of a hook named event from id's own agent,
// or, when foreign, from another process; caller names the helper in a failure.
func subagentHookGate(t *testing.T, s *store.Store, id, event string, foreign bool, caller string) store.HookGate {
	t.Helper()
	pid, start, err := AgentHookParent(s, id)
	if err != nil {
		t.Fatalf("storefix.%s(%q): read row: %v", caller, id, err)
	}
	if foreign {
		pid = ForeignHookParentPID(pid)
	}
	return store.HookGate{Event: event, ParentPID: pid, ParentStart: start}
}

func applySubagentLifecycleHook(t *testing.T, s *store.Store, id, event string, ev store.SubagentLifecycleEvent, foreign bool, caller string) store.HookApplied {
	t.Helper()
	gate := subagentHookGate(t, s, id, event, foreign, caller)
	_, applied, err := s.ApplySubagentLifecycle(id, gate, ev, event, "", false)
	if err != nil {
		t.Fatalf("storefix.%s(%q): %v", caller, id, err)
	}
	return applied
}

// applyAgentToolHook is an Agent-tool hook of event: the ordinary working write,
// then, only when it applied, the entry transaction w.
func applyAgentToolHook(t *testing.T, s *store.Store, id, event string, w store.SubagentAfterWrite, foreign bool, caller string) store.HookApplied {
	t.Helper()
	gate := subagentHookGate(t, s, id, event, foreign, caller)
	_, applied, err := s.ApplyHookTransitionResult(id, gate, store.StateWorking, false, event, "", false)
	if err != nil {
		t.Fatalf("storefix.%s(%q): row write: %v", caller, id, err)
	}
	if applied.Applied {
		if err := s.ApplySubagentAfterWrite(id, gate, w); err != nil {
			t.Fatalf("storefix.%s(%q): entry transaction: %v", caller, id, err)
		}
	}
	return applied
}

func applyMainAgentStopHook(t *testing.T, s *store.Store, id string, stop MainAgentStop, foreign bool, caller string) store.HookApplied {
	t.Helper()
	gate := subagentHookGate(t, s, id, hook.EventNameStop, foreign, caller)
	_, applied, err := s.ApplyHookTransitionResult(id, gate, store.StateWaiting, false, hook.EventNameStop, "", false)
	if err != nil {
		t.Fatalf("storefix.%s(%q): row write: %v", caller, id, err)
	}
	if !applied.Applied {
		return applied
	}
	work, err := s.SubagentTurnEndHasWork(id, stop.ReceivedMs)
	if err != nil {
		t.Fatalf("storefix.%s(%q): turn-end read: %v", caller, id, err)
	}
	if work {
		w := store.SubagentAfterWrite{TurnEnd: &store.SubagentTurnEnd{ReceivedMs: stop.ReceivedMs}}
		if err := s.ApplySubagentAfterWrite(id, gate, w); err != nil {
			t.Fatalf("storefix.%s(%q): entry transaction: %v", caller, id, err)
		}
	}
	return applied
}

// ApplySubagentStart applies a SubagentStart as id's own agent: the combined
// soft refresh and start of ev (its Kind is set to start; SubagentID, SessionID,
// Type and ReceivedMs are the caller's; an empty SubagentID writes no entry, as
// a start with no agent_id does, and an empty Type never claims).
func ApplySubagentStart(t *testing.T, s *store.Store, id string, ev store.SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	ev.Kind = store.SubagentLifecycleStart
	return applySubagentLifecycleHook(t, s, id, hook.EventNameSubagentStart, ev, false, "ApplySubagentStart")
}

// ApplyForeignSubagentStart is ApplySubagentStart from another process: nothing
// is written and the store reports pid_mismatch (no_pane_recorded for a row with
// no pane).
func ApplyForeignSubagentStart(t *testing.T, s *store.Store, id string, ev store.SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	ev.Kind = store.SubagentLifecycleStart
	return applySubagentLifecycleHook(t, s, id, hook.EventNameSubagentStart, ev, true, "ApplyForeignSubagentStart")
}

// ApplySubagentStop applies a SubagentStop as id's own agent: the combined soft
// refresh and stop of ev (its Kind is set to stop; SupersedesBlockedStop is the
// payload's stop_hook_active).
func ApplySubagentStop(t *testing.T, s *store.Store, id string, ev store.SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	ev.Kind = store.SubagentLifecycleStop
	return applySubagentLifecycleHook(t, s, id, hook.EventNameSubagentStop, ev, false, "ApplySubagentStop")
}

// ApplyForeignSubagentStop is ApplySubagentStop from another process.
func ApplyForeignSubagentStop(t *testing.T, s *store.Store, id string, ev store.SubagentLifecycleEvent) store.HookApplied {
	t.Helper()
	ev.Kind = store.SubagentLifecycleStop
	return applySubagentLifecycleHook(t, s, id, hook.EventNameSubagentStop, ev, true, "ApplyForeignSubagentStop")
}

// ApplySubagentLaunchRequest applies a PreToolUse of the Agent tool as id's own
// agent: the ordinary working write, then the claimable launch of req when it
// applied (an empty ToolUseID or Type opens none, as the hook's request does
// for a payload without them; the hook also opens none for a named launch, which
// a test models by not calling this).
func ApplySubagentLaunchRequest(t *testing.T, s *store.Store, id string, req store.SubagentLaunchRequest) store.HookApplied {
	t.Helper()
	return applyAgentToolHook(t, s, id, hook.EventNamePreToolUse, store.SubagentAfterWrite{LaunchRequest: &req}, false, "ApplySubagentLaunchRequest")
}

// ApplyForeignSubagentLaunchRequest is ApplySubagentLaunchRequest from another
// process: no claimable launch is opened.
func ApplyForeignSubagentLaunchRequest(t *testing.T, s *store.Store, id string, req store.SubagentLaunchRequest) store.HookApplied {
	t.Helper()
	return applyAgentToolHook(t, s, id, hook.EventNamePreToolUse, store.SubagentAfterWrite{LaunchRequest: &req}, true, "ApplyForeignSubagentLaunchRequest")
}

// ApplySubagentLaunchDetails applies a PostToolUse of the Agent tool as id's own
// agent: the ordinary working write, then the launch result d when it applied
// (it closes the claimable launch with d.ToolUseID and, with a SubagentID, writes
// the entry; no SubagentID is a teammate's result).
func ApplySubagentLaunchDetails(t *testing.T, s *store.Store, id string, d store.SubagentLaunchDetails) store.HookApplied {
	t.Helper()
	return applyAgentToolHook(t, s, id, hook.EventNamePostToolUse, store.SubagentAfterWrite{LaunchDetails: &d}, false, "ApplySubagentLaunchDetails")
}

// ApplyForeignSubagentLaunchDetails is ApplySubagentLaunchDetails from another
// process: no entry is written and no claimable launch is closed.
func ApplyForeignSubagentLaunchDetails(t *testing.T, s *store.Store, id string, d store.SubagentLaunchDetails) store.HookApplied {
	t.Helper()
	return applyAgentToolHook(t, s, id, hook.EventNamePostToolUse, store.SubagentAfterWrite{LaunchDetails: &d}, true, "ApplyForeignSubagentLaunchDetails")
}

// ApplySubagentLaunchFailure applies a PostToolUseFailure of the Agent tool as
// id's own agent: the ordinary working write, then the close of the claimable
// launch of f.ToolUseID when it applied.
func ApplySubagentLaunchFailure(t *testing.T, s *store.Store, id string, f store.SubagentLaunchFailure) store.HookApplied {
	t.Helper()
	return applyAgentToolHook(t, s, id, hook.EventNamePostToolUseFailure, store.SubagentAfterWrite{LaunchFailure: &f}, false, "ApplySubagentLaunchFailure")
}

// ApplyForeignSubagentLaunchFailure is ApplySubagentLaunchFailure from another
// process: no claimable launch is closed.
func ApplyForeignSubagentLaunchFailure(t *testing.T, s *store.Store, id string, f store.SubagentLaunchFailure) store.HookApplied {
	t.Helper()
	return applyAgentToolHook(t, s, id, hook.EventNamePostToolUseFailure, store.SubagentAfterWrite{LaunchFailure: &f}, true, "ApplyForeignSubagentLaunchFailure")
}

// ApplySubagentMainAgentStop applies a main-agent Stop as id's own agent: the
// ordinary waiting write, then, when it applied and the skip read finds a
// claimable launch of the current launch opened more than the grace period
// before stop.ReceivedMs, the turn-end transaction that closes them.
func ApplySubagentMainAgentStop(t *testing.T, s *store.Store, id string, stop MainAgentStop) store.HookApplied {
	t.Helper()
	return applyMainAgentStopHook(t, s, id, stop, false, "ApplySubagentMainAgentStop")
}

// ApplyForeignSubagentMainAgentStop is ApplySubagentMainAgentStop from another
// process: nothing is written and no claimable launch is closed.
func ApplyForeignSubagentMainAgentStop(t *testing.T, s *store.Store, id string, stop MainAgentStop) store.HookApplied {
	t.Helper()
	return applyMainAgentStopHook(t, s, id, stop, true, "ApplyForeignSubagentMainAgentStop")
}
