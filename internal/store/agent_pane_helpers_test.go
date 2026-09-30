package store

// White-box hook helpers (SR-22.9). Package store tests cannot import storefix
// (it imports store), so a row records its agent's pane with
// RecordLaunchIdentity, as spawn's create does, and hooks come from that pane
// process through agentGate; foreignGate is another process carrying the id.

import (
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/testsupport/launchfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// testLaunchToken is the launch token insertAgentRow gives a row without one.
const testLaunchToken = "a1a1a1a1a1a1a1a1"

// testPaneStart is the start time of the test pane process.
const testPaneStart = procstarttimefix.LinuxProcStarttime

// testPane is the agent pane recordAgentPane records: launchfix's test pane.
func testPane() LaunchIdentity {
	return LaunchIdentity{PaneID: launchfix.TestPaneID, PanePID: launchfix.TestPanePID, PaneStarttime: testPaneStart}
}

// insertAgentRow inserts sp pending, as spawn does, then records the test
// pane on it (recordAgentPane), so hooks through agentGate apply to it.
func insertAgentRow(s *Store, sp Spawn) error {
	if sp.Identity.Token == "" {
		sp.Identity.Token = testLaunchToken
	}
	if err := s.InsertPending(sp); err != nil {
		return err
	}
	return recordAgentPane(s, sp.ClaudeInstanceID)
}

// recordAgentPane records the test pane on id's pending row at the row's
// current version and launch token, as the create's identity write does.
func recordAgentPane(s *Store, id string) error {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	return recordPaneAt(s, id, sp.Snapshot.RowVersion, sp.Identity.Token)
}

// recordPaneAt records the test pane on id's row at version and token; a
// write that does not apply is an error.
func recordPaneAt(s *Store, id string, version int64, token string) error {
	res, err := s.RecordLaunchIdentity(id, version, token, testPane())
	if err == nil && res != CondApplied {
		err = fmt.Errorf("RecordLaunchIdentity(%s, version %d) = %v; want CondApplied", id, version, res)
	}
	return err
}

// agentGate is the gate of a hook whose parent is the test pane process: the
// row's own agent.
func agentGate(event, sessionID string) HookGate {
	return HookGate{Event: event, ParentPID: launchfix.TestPanePID, ParentStart: testPaneStart, SessionID: sessionID}
}

// foreignGate is the gate of a hook from another process than the test pane
// (a nested agent, a teammate, a leftover) carrying the row's id.
func foreignGate(event, sessionID string) HookGate {
	g := agentGate(event, sessionID)
	g.ParentPID = launchfix.TestPanePID + 1
	return g
}

// appliedErr turns a gated hook write's result into an error: a store error,
// or a write the gate did not apply.
func appliedErr(what, id string, applied HookApplied, err error) error {
	if err != nil {
		return err
	}
	if !applied.Applied {
		return fmt.Errorf("%s on %s not applied (reason %q)", what, id, applied.Reason)
	}
	return nil
}

// agentHook applies an ordinary hook write from id's own agent, with trigger
// as its event; a write the gate does not apply is an error.
func agentHook(s *Store, id, newState string, soft bool, trigger string) error {
	applied, err := s.ApplyHookTransition(id, agentGate(trigger, ""), newState, soft, trigger, "", false)
	return appliedErr("hook "+trigger, id, applied, err)
}

// agentPermissionRequest inserts an open permission request from id's own
// agent (the gated INSERT); a request the gate does not apply is an error.
func agentPermissionRequest(s *Store, id, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) error {
	applied, err := s.UpsertOpenPermissionRequest(id, agentGate("PermissionRequest", ""), requestToken, toolName, toolInputJSON, cap, writerProcess)
	return appliedErr("permission request "+requestToken, id, applied, err)
}

// fireSessionStart writes a SessionStart from gate's parent as the hook
// handler does: it examines id's row, then writes against that snapshot. A
// changed snapshot is an error.
func fireSessionStart(s *Store, id string, gate HookGate, jsonlPath string, jsonlPresent bool) (HookApplied, error) {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return HookApplied{}, err
	}
	gate.SessionStart, gate.Examined = true, sp.Snapshot
	applied, changed, err := s.RecordSessionStartIdentity(id, gate, jsonlPath, jsonlPresent)
	if err == nil && changed {
		err = errors.New("SessionStart: the row changed after it was examined")
	}
	return applied, err
}

// agentSessionStart is fireSessionStart from id's own agent reporting
// sessionID; a SessionStart the gate does not apply is an error.
func agentSessionStart(s *Store, id, sessionID, jsonlPath string, jsonlPresent bool) error {
	applied, err := fireSessionStart(s, id, agentGate("SessionStart", sessionID), jsonlPath, jsonlPresent)
	return appliedErr("SessionStart "+sessionID, id, applied, err)
}
