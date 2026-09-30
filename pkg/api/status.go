package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// StatusStore is the narrow store surface Status needs. *store.Store
// satisfies it; tests pass the real store.
type StatusStore interface {
	GetSpawnState(instanceID string) (string, error)
}

// StatusResult is the typed return shape of Status — the row's state value,
// plus launch_started_at on a pending row, surfaced as a JSON object on the
// CLI.
type StatusResult struct {
	// State is the current lifecycle state of the Spawn. One of:
	// pending, waiting, working, ask_user, check_permission, ended, missing.
	// pending means a launch (spawn, reuse or resume) is in progress and the
	// agent has not reported in yet (Claude Code's SessionStart); it may be
	// loading or waiting at a startup prompt. A resumed pending row keeps its
	// session id and history; a caller tells it from a fresh one by its
	// non-empty claude_session_id, shown by [Client.Get].
	State string `json:"state"`
	// LaunchStartedAt is the start of the launch in progress, RFC3339 UTC
	// with millisecond precision. Present only on a pending row; nil (and
	// omitted from JSON) otherwise (SR-22.2).
	LaunchStartedAt *time.Time `json:"launch_started_at,omitempty"`
}

// statusReader is the optional narrow read Status uses when the store it is
// given provides it: the state and the launch start in one read (SR-16.1).
// *store.Store satisfies it; a StatusStore without it yields no launch start.
type statusReader interface {
	SpawnStatus(instanceID string) (state string, launchStartedAtMillis int64, err error)
}

// Status returns the current state column for the given claude_instance_id,
// with launch_started_at on a pending row when s provides the narrow read.
// Missing rows surface store.ErrSpawnNotFound; the CLI translates that to
// the canonical err_name envelope.
func Status(s StatusStore, instanceID string) (StatusResult, error) {
	if r, ok := s.(statusReader); ok {
		state, launchMillis, err := r.SpawnStatus(instanceID)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: state, LaunchStartedAt: launchStartedAt(state, launchMillis)}, nil
	}
	state, err := s.GetSpawnState(instanceID)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{State: state}, nil
}

// Status returns the current state column (pending/waiting/working/ask_user/
// check_permission/ended/missing) for the identified Spawn. On a pending row
// the result also carries launch_started_at, when the agent's launch began.
//
// CLI: agent-director status
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for claudeInstanceID.
//
// Nondeterminism: none.
func (c *Client) Status(claudeInstanceID string) (StatusResult, error) {
	if err := c.checkClosed(); err != nil {
		return StatusResult{}, err
	}
	return Status(c.st, claudeInstanceID)
}

// Compile-time assertion that *store.Store provides the optional narrow read
// Status uses, so the production wiring keeps showing the launch start.
var _ statusReader = (*store.Store)(nil)
