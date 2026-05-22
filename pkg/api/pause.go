package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PauseTmux is the narrow tmux surface Pause needs. *tmux.Client
// satisfies it; tests pass the same recording fake the send-keys
// tests use. pause issues exactly one SendKeys call carrying `/exit`
// with pressEnter=true — the tmux client owns the literal-text-then-
// real-Enter split internally.
type PauseTmux interface {
	SendKeys(name, text string, pressEnter bool) error
}

// PauseStore is the narrow store surface Pause needs: a row lookup for
// the initial dispatch and a state-only lookup for the polling loop.
// *store.Store satisfies both methods; tests fake the surface so the
// polling cadence can be driven without touching SQLite.
type PauseStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	GetSpawnState(instanceID string) (string, error)
}

// PauseParams is the typed parameter shape for the pause verb.
type PauseParams struct {
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// PauseResult is the typed return shape — empty today. Reserved so
// future fields (e.g. exit_method, elapsed_ms) can be added without
// breaking the wire shape.
type PauseResult struct{}

// pausePollInterval is the cadence of the polling loop. Held as a
// package var so tests can shorten it. Production keeps the SRD-§9
// "small interval, bounded total wait" guidance — 200ms is small
// enough that a 30s timeout still has ~150 polls, big enough that
// SQLite isn't whipped pointlessly during normal shutdown.
var pausePollInterval = 200 * time.Millisecond

// pauseSleep is the sleep callable used inside the polling loop.
// Held as a package var so tests can swap in a fast or instant variant
// without spinning real wall-clock time. Production uses time.Sleep.
var pauseSleep = time.Sleep

// Pause politely shuts down a live Spawn by sending `/exit` to its
// pane and waiting up to timeoutSeconds for the row to transition
// to `ended`. Behavior (SRD §9):
//
//   - Unknown id → ErrSpawnNotFound surfaces from the store.
//   - State == ended / missing → no-op success: the desired
//     post-condition is already met.
//   - State == waiting → emit `/exit` + Enter via two tmux send-keys
//     calls (matching the send-keys verb's submit pattern), then poll
//     the row's state at pausePollInterval until either it becomes
//     `ended` (return nil) or cfg.TimeoutSeconds elapses
//     (ErrPauseTimeout).
//   - State ∈ {pending, working, ask_user, check_permission} →
//     ErrSpawnNotPausable. The verb does NOT emit /exit in these
//     states because the slash would be interpreted as input text and
//     the caller would silently get the wrong outcome.
//   - ctx.Done() during the polling loop → ctx.Err() so the caller's
//     cancel-on-signal handler can short-circuit a long wait.
//
// Pause is one-shot: it returns when the row reaches `ended`, when the
// timeout expires, or when ctx is cancelled. There is no incremental
// progress callback — callers wanting that should poll `status`
// themselves and skip pause.
func Pause(ctx context.Context, s PauseStore, t PauseTmux, timeoutSeconds int, params PauseParams) (PauseResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return PauseResult{}, err
	}

	switch row.State {
	case store.StateEnded, store.StateMissing:
		return PauseResult{}, nil
	case store.StateWaiting:
		// fall through to the /exit + poll path
	default:
		return PauseResult{}, fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotPausable, params.ClaudeInstanceID, row.State)
	}

	// /exit + submit — the tmux client owns the literal-text-then-
	// real-Enter split internally (see (*tmux.Client).SendKeys).
	if err := t.SendKeys(row.TmuxSessionName, "/exit", true); err != nil {
		return PauseResult{}, err
	}

	timeout := time.Duration(timeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)

	for {
		// Check ctx first so a caller who cancelled right after
		// sending /exit exits promptly without one extra sleep.
		if err := ctx.Err(); err != nil {
			return PauseResult{}, err
		}

		state, err := s.GetSpawnState(params.ClaudeInstanceID)
		if err != nil {
			return PauseResult{}, err
		}
		if state == store.StateEnded {
			return PauseResult{}, nil
		}

		if !time.Now().Before(deadline) {
			return PauseResult{}, fmt.Errorf("%w: spawn %s did not reach ended within %s",
				ErrPauseTimeout, params.ClaudeInstanceID, timeout)
		}

		// Sleep at the polling cadence, but never past the deadline —
		// the loop should evaluate the final state check at the
		// deadline boundary, not deadline + pollInterval.
		sleep := pausePollInterval
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		pauseSleep(sleep)
	}
}

// Pause politely shuts down a waiting Spawn by sending `/exit` and waiting up
// to pause.timeout_seconds for the row to reach `ended`. ctx is the first
// argument per SRD §12 (preserving CLI ctx semantics).
func (c *Client) Pause(ctx context.Context, params PauseParams) (PauseResult, error) {
	if err := c.checkClosed(); err != nil {
		return PauseResult{}, err
	}
	return Pause(ctx, c.st, c.tmuxClient, c.cfg.Pause.TimeoutSeconds, params)
}
