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

// settledStatusReader is the optional read status uses with a settle time
// (b.3ie): the reported state, the raw state and the launch start in one
// read. *store.Store satisfies it; a store without it reports the raw state.
type settledStatusReader interface {
	SpawnStatusSettled(instanceID string, r store.SettleRead) (reported, raw string, launchStartedAtMillis int64, err error)
}

// Status returns the current state column for the given claude_instance_id,
// with launch_started_at on a pending row when s provides the narrow read.
// Missing rows surface store.ErrSpawnNotFound; the CLI translates that to
// the canonical err_name envelope. It reads with no settle time; the Client
// applies its [state] settle_ms.
func Status(s StatusStore, instanceID string) (StatusResult, error) {
	return statusSettled(s, store.SettleRead{}, instanceID)
}

// statusSettled is Status with the reader's settle time and clock settle
// (b.3ie). With a settle time, and a store that provides the settled read,
// the state is the settled state (store.SettleRead's derivation, in the
// read's own statement); launch_started_at still keys on the raw state. With
// no settle time it is Status exactly as before b.3ie. It never waits.
func statusSettled(s StatusStore, settle store.SettleRead, instanceID string) (StatusResult, error) {
	if sr, ok := s.(settledStatusReader); ok && settle.SettleMs > 0 {
		reported, raw, launchMillis, err := sr.SpawnStatusSettled(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, LaunchStartedAt: launchStartedAt(raw, launchMillis)}, nil
	}
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
// With [state] settle_ms set, the state is the settled state: a hook-written
// change shows only once it has held that long, the last settled state until
// then; terminal states and command-driven changes show at once. The read
// never waits.
//
// CLI: agent-director status
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for claudeInstanceID.
//
// Nondeterminism: none with no settle time; with one, the state depends on
// the clock.
func (c *Client) Status(claudeInstanceID string) (StatusResult, error) {
	if err := c.checkClosed(); err != nil {
		return StatusResult{}, err
	}
	return statusSettled(c.st, c.settleRead(), claudeInstanceID)
}

// Compile-time assertion that *store.Store provides the optional narrow read
// Status uses, so the production wiring keeps showing the launch start, and
// the settled read the Client's status uses with a settle time.
var (
	_ statusReader        = (*store.Store)(nil)
	_ settledStatusReader = (*store.Store)(nil)
)
