package storefix

// agent_hooks.go — seeding and firing hooks through the store's gated hook
// path (SR-22.9). Every hook write takes a store.HookGate, the hook's parent
// process, and applies only when that process is the row's recorded pane
// process; these helpers build the gate, so tests never hand-build one.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/launchfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// SeedTrigger is the triggering event name every seeder's hook write carries
// ("test_seed"), so the trail tells seeding from a real hook.
const SeedTrigger = "test_seed"

// SeedPaneStarttime is the pane start time a seeder records with the seed
// pane (launchfix.TestPanePID), and the parent start time the agent helpers
// use when a row's pane_starttime is NULL (the gate then matches on the pid
// alone, SR-22.9).
const SeedPaneStarttime = procstarttimefix.LinuxProcStarttime

// storePaths maps each registered *store.Store to its database file, so the
// seeders that take only a store handle can open the raw connection the seed
// pane is written through.
var storePaths sync.Map // *store.Store -> string

// RegisterStorePath records that s is open on dbPath until the test ends.
// OpenTempStore registers every store it opens; a fixture that opens its own
// store registers it before handing it to a seeder that takes only the
// handle.
func RegisterStorePath(t *testing.T, s *store.Store, dbPath string) {
	t.Helper()
	storePaths.Store(s, dbPath)
	t.Cleanup(func() { storePaths.Delete(s) })
}

// StorePath returns the database file RegisterStorePath recorded for s,
// failing the test when s was neither opened by OpenTempStore nor
// registered. caller names the helper in the failure message.
func StorePath(t *testing.T, s *store.Store, caller string) string {
	t.Helper()
	p, ok := storePaths.Load(s)
	if !ok {
		t.Fatalf("%s: the store was not opened by storefix.OpenTempStore or registered with storefix.RegisterStorePath", caller)
	}
	return p.(string)
}

// WithSeedPane runs writes, the seeder's gated hook writes as id's own agent,
// with a store.HookGate that matches id's row, then writes the row's pane and
// process identity back exactly as they were. When the row records no pane,
// the seed pane (launchfix.TestPanePID with SeedPaneStarttime) is written
// first; otherwise the gate takes the recorded pane pid and its start time
// (SeedPaneStarttime when NULL). The identity writes go through a raw
// connection to dbPath and advance no row_version; only writes' own store
// calls do. The gate's Event is SeedTrigger and its SessionID is empty.
func WithSeedPane(dbPath, id string, writes func(gate store.HookGate) error) error {
	raw, err := openRaw(dbPath)
	if err != nil {
		return fmt.Errorf("WithSeedPane: %w", err)
	}
	defer func() { _ = raw.Close() }()

	// The pane identity the gate reads and the process identity an applied
	// SessionStart records, as stored: pane_id, pane_pid, pane_starttime,
	// pid, proc_starttime.
	prior := make([]any, 5)
	ptrs := make([]any, len(prior))
	for i := range prior {
		ptrs[i] = &prior[i]
	}
	if err := raw.QueryRow(`SELECT pane_id, pane_pid, pane_starttime, pid, proc_starttime
	                          FROM spawns WHERE claude_instance_id = ?`, id).Scan(ptrs...); err != nil {
		return fmt.Errorf("WithSeedPane: read identity of %q: %w", id, err)
	}

	gate := store.HookGate{Event: SeedTrigger, ParentPID: launchfix.TestPanePID, ParentStart: SeedPaneStarttime}
	if pid, ok := prior[1].(int64); ok && pid > 0 {
		gate.ParentPID = int(pid)
		if start, ok := asText(prior[2]); ok && start != "" {
			gate.ParentStart = start
		}
	} else if _, err := raw.Exec(`UPDATE spawns SET pane_pid = ?, pane_starttime = ? WHERE claude_instance_id = ?`,
		launchfix.TestPanePID, SeedPaneStarttime, id); err != nil {
		return fmt.Errorf("WithSeedPane: record seed pane on %q: %w", id, err)
	}

	werr := writes(gate)

	args := append(append([]any{}, prior...), id)
	if _, err := raw.Exec(`UPDATE spawns SET pane_id = ?, pane_pid = ?, pane_starttime = ?, pid = ?, proc_starttime = ?
	                        WHERE claude_instance_id = ?`, args...); err != nil {
		return errors.Join(werr, fmt.Errorf("WithSeedPane: write back identity of %q: %w", id, err))
	}
	return werr
}

// asText returns a scanned TEXT value as a string.
func asText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return "", false
}

// openRaw opens a raw connection to an existing store file with the store's
// busy timeout.
func openRaw(dbPath string) (*sql.DB, error) {
	raw, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, fmt.Errorf("open raw db %q: %w", dbPath, err)
	}
	return raw, nil
}

// SeedAgentWrites makes a seeder's hook writes on id's row as its own agent,
// with gate from WithSeedPane: when sessionID is non-empty, a gated soft
// refresh records it (the row records none yet; state unchanged; row_version
// +1); then, when state is not pending, the gated transition to state
// (row_version +1). A write the gate does not apply is an error.
func SeedAgentWrites(s *store.Store, gate store.HookGate, id, sessionID, state string) error {
	if sessionID != "" {
		g := gate
		g.SessionID = sessionID
		applied, err := s.ApplyHookTransition(id, g, "", true, SeedTrigger, "", false)
		if err := seedWriteErr("record session id "+sessionID, applied, err); err != nil {
			return err
		}
	}
	if state != store.StatePending {
		applied, err := s.ApplyHookTransition(id, gate, state, false, SeedTrigger, "", false)
		if err := seedWriteErr("transition to "+state, applied, err); err != nil {
			return err
		}
	}
	return nil
}

// SeedSessionStart makes a SessionStart as id's own agent with gate from
// WithSeedPane, recording sessionID (for a seeder that needs the rotation
// archive: a different id than the row's archives the outgoing pair). It sets
// the row waiting and advances row_version by one.
func SeedSessionStart(s *store.Store, gate store.HookGate, id, sessionID string) error {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return fmt.Errorf("seed SessionStart %s: %w", sessionID, err)
	}
	gate.SessionID, gate.SessionStart, gate.Examined = sessionID, true, sp.Snapshot
	applied, changed, err := s.RecordSessionStartIdentity(id, gate, "", false)
	if err == nil && changed {
		err = errors.New("the row changed while it was written")
	}
	return seedWriteErr("SessionStart "+sessionID, applied, err)
}

// seedWriteErr turns a seeder's hook write result into an error.
func seedWriteErr(what string, applied store.HookApplied, err error) error {
	if err != nil {
		return fmt.Errorf("seed %s: %w", what, err)
	}
	if !applied.Applied {
		return fmt.Errorf("seed %s: hook not applied (reason %q)", what, applied.Reason)
	}
	return nil
}

// HookOption adds a payload field to a hook FireHook fires.
type HookOption func(*hookPayload)

// hookPayload is the part of a hook's payload FireHook takes beyond the event
// and the session id.
type hookPayload struct {
	jsonlPath    string
	jsonlPresent bool
}

// HookTranscript makes the hook report transcript path jsonlPath, present on
// disk or not (the handler stats it; the store records it under the presence
// rule: SessionStart records it, an ordinary hook on a row with no session id
// records it with the id).
func HookTranscript(jsonlPath string, present bool) HookOption {
	return func(p *hookPayload) { p.jsonlPath, p.jsonlPresent = jsonlPath, present }
}

// FireHook fires one Claude Code hook event at id's row through the store's
// gated path, from a hook whose parent is parentPID with start time
// parentStart, and returns whether it applied. The event is classified by
// the hook handler's own classifier (hook.ClassifyEvent): SessionEnd is a
// terminal one (reason prompt_input_exit, so the row ends) and PreToolUse,
// PostToolUse and PermissionRequest are for the Bash tool. sessionID is the
// payload's Claude session id ("" for none); no transcript path is reported
// unless HookTranscript says so. SessionStart reads the row for its snapshot
// and retries once when only the snapshot changed.
func FireHook(s *store.Store, id, event, sessionID string, parentPID int, parentStart string, opts ...HookOption) (store.HookApplied, error) {
	var in hookPayload
	for _, o := range opts {
		o(&in)
	}
	fields := map[string]string{"hook_event_name": event}
	switch event {
	case "SessionEnd":
		fields["reason"] = "prompt_input_exit"
	case "PreToolUse", "PostToolUse", "PermissionRequest":
		fields["tool_name"] = "Bash"
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return store.HookApplied{}, err
	}
	res, err := hook.ClassifyEvent(payload)
	if err != nil {
		return store.HookApplied{}, err
	}
	gate := store.HookGate{Event: event, ParentPID: parentPID, ParentStart: parentStart, SessionID: sessionID}
	if event != "SessionStart" {
		return s.ApplyHookTransition(id, gate, res.NewState, res.SoftRefresh, event, in.jsonlPath, in.jsonlPresent)
	}
	gate.SessionStart = true
	for attempt := 0; attempt < 2; attempt++ {
		sp, err := s.GetSpawn(id)
		if errors.Is(err, store.ErrSpawnNotFound) {
			return store.HookApplied{}, nil
		}
		if err != nil {
			return store.HookApplied{}, err
		}
		gate.Examined = sp.Snapshot
		applied, changed, err := s.RecordSessionStartIdentity(id, gate, in.jsonlPath, in.jsonlPresent)
		if err != nil || !changed {
			return applied, err
		}
	}
	return store.HookApplied{}, nil
}

// AgentHookParent returns the parent process a hook from id's own agent has:
// the row's recorded pane pid and start time (SeedPaneStarttime when the
// start time is NULL). A row with no pane gives launchfix.TestPanePID, which
// matches nothing there (no_pane_recorded).
func AgentHookParent(s *store.Store, id string) (pid int, start string, err error) {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return 0, "", err
	}
	pid, start = sp.Identity.PanePID, sp.Identity.PaneStarttime
	if pid <= 0 {
		pid = launchfix.TestPanePID
	}
	if start == "" {
		start = SeedPaneStarttime
	}
	return pid, start, nil
}

// ForeignHookParentPID is the parent pid of a hook from another process than
// the agent whose pane pid is panePID: a pid that is never the pane's.
func ForeignHookParentPID(panePID int) int {
	if panePID <= 0 {
		return launchfix.TestPanePID + 1
	}
	return panePID + 1
}

// ApplyAgentHook fires event at id's row as the row's own agent would: the
// hook's parent is the row's recorded pane process (AgentHookParent), so the
// gate holds whenever the row records a pane (SR-22.9). It returns the
// store's result; a store error fails the test. See FireHook for the event,
// sessionID and opts.
func ApplyAgentHook(t *testing.T, s *store.Store, id, event, sessionID string, opts ...HookOption) store.HookApplied {
	t.Helper()
	pid, start, err := AgentHookParent(s, id)
	if err != nil {
		t.Fatalf("storefix.ApplyAgentHook(%q, %s): read row: %v", id, event, err)
	}
	applied, err := FireHook(s, id, event, sessionID, pid, start, opts...)
	if err != nil {
		t.Fatalf("storefix.ApplyAgentHook(%q, %s): %v", id, event, err)
	}
	return applied
}

// ApplyForeignHook fires event at id's row from another process carrying the
// row's id (a nested agent, a teammate, a leftover): the hook's parent is a
// pid other than the row's pane pid (ForeignHookParentPID), so the gate never
// holds and the store reports pid_mismatch, or no_pane_recorded for a row
// with no pane. A store error fails the test.
func ApplyForeignHook(t *testing.T, s *store.Store, id, event, sessionID string, opts ...HookOption) store.HookApplied {
	t.Helper()
	pid, start, err := AgentHookParent(s, id)
	if err != nil {
		t.Fatalf("storefix.ApplyForeignHook(%q, %s): read row: %v", id, event, err)
	}
	applied, err := FireHook(s, id, event, sessionID, ForeignHookParentPID(pid), start, opts...)
	if err != nil {
		t.Fatalf("storefix.ApplyForeignHook(%q, %s): %v", id, event, err)
	}
	return applied
}
