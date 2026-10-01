package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PauseTmux is the narrow tmux surface Pause needs (SRD Appendix F.3): the
// lookup, the pane listing and the keys sent to one pane by its pane id.
// TmuxClient, *tmux.Client and tmuxfix.Recorder satisfy it. Every method
// takes the row's socket (SR-3.3) and reports a failure as *TmuxCallError.
type PauseTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
	// SendKeysPane types text literally into the pane paneID on socket and
	// then, only if that succeeded and pressEnter is set, sends Enter; the
	// failed call is named on the *TmuxCallError (text send or Enter send).
	SendKeysPane(socket, paneID, text string, pressEnter bool) error
}

// PauseStore is the narrow store surface Pause needs (Appendix F.3): the
// row read, the state-only read the wait polls, the adoption write of SR-3.6
// (a lost create reply's server and pane identity, applied only if the row
// still has the snapshot Pause examined) and this store's id, which every
// label the lookup accepts ends with (SR-3.4). The adoption is the only
// write pause makes. *store.Store satisfies it; tests use the shared
// killStore fixture, a wrapper over a real store that can fail the state
// read or the adoption write.
type PauseStore interface {
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// GetSpawnState reads only the row's state, for the wait.
	GetSpawnState(instanceID string) (string, error)
	// AdoptIdentityIfUnchanged records a found launch identity when the
	// row is still as examined (SR-3.6).
	AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// The production types satisfy Pause's interfaces.
var (
	_ PauseStore = (*store.Store)(nil)
	_ PauseTmux  = TmuxClient(nil)
)

// PauseParams is the typed parameter shape for the pause verb.
type PauseParams struct {
	// ClaudeInstanceID identifies the Spawn to pause gracefully.
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

// Pause politely shuts down a live Spawn by typing `/exit` into the agent's
// own pane of the row's current launch, by pane id, then Enter, and waiting
// up to timeoutSeconds for the row to reach `ended` (SRD SR-7.2, SR-7.3,
// SR-3.6, SR-3.7, SR-13.2). Behavior:
//
//   - Unknown id: ErrSpawnNotFound from the store.
//   - State ended or missing: no-op success, with no tmux call; the desired
//     post-condition is already met.
//   - State pending, working, ask_user or check_permission:
//     ErrSpawnNotPausable, with no tmux call. `/exit` is never typed in these
//     states, where the slash would be read as input text.
//   - State waiting: the row's socket (SR-3.3; a resolution refusal is
//     ErrTmuxNotAvailable) and one lookup by the row's current label, then
//     as below.
//
// The lookup's outcomes on a waiting row:
//
//   - Ours: one pane listing, then `/exit` and Enter to the agent's pane (the
//     entry with the row's recorded pane id and pid, wherever it now is),
//     then the wait. For a row that records no server identity or no pane (a
//     lost create reply), what the lookup and the pane whose @ad_pane names
//     the row's launch token show is used for this call and written once,
//     guarded on the row as read; the write's outcome never changes the
//     result (SR-3.6). No agent's pane: ErrTmuxSessionConflict ("the agent's
//     pane was not found"), nothing sent.
//   - Leftover: ErrTmuxSessionConflict ("not this launch's session"),
//     nothing sent.
//   - Gone (another agent-director store's sessions included):
//     ErrTmuxSendKeys ("the row's session is not there"), `/exit` not sent.
//   - Can't tell, tmux unavailable, and a pane listing that fails other than
//     by showing no server: the single-row verbs' shared mapping
//     (ErrTmuxNotAvailable, ErrTmuxSessionConflict "conflicting labels",
//     ErrTmuxUnresponsive), nothing sent. A listing that shows no server is
//     Gone.
//   - A failed text or Enter call: a timeout is ErrTmuxUnresponsive saying
//     the keys may have been delivered, with no further call; any other
//     failure makes one follow-up lookup, whose Gone or Leftover gives
//     ErrTmuxSendKeys and whose other outcomes give ErrTmuxUnresponsive, or
//     ErrTmuxNotAvailable for a different server or tmux unavailable
//     (SR-7.3). Once the text call went through, every description says the
//     text may be typed but not submitted, never that nothing was sent.
//
// Every tmux call uses the row's socket and every action targets a pane id.
// The calls are at most one lookup, one pane listing, the text call, the
// Enter call and, only after an action failure other than a timeout, one
// follow-up lookup (SR-13.2); the wait is not counted. On any error the wait
// does not start.
//
// The wait, once `/exit` and Enter went through: the row's state is polled
// at pausePollInterval until it is `ended` (nil) or timeoutSeconds elapse on
// the real clock (ErrPauseTimeout); ctx.Done() during the wait returns
// ctx.Err() so the caller's cancel-on-signal handler can cut a long wait
// short. Pause never writes the row's state; the adoption is its only store
// write.
//
// Pause is one-shot: it returns when the row reaches `ended`, when the
// timeout expires, or when ctx is cancelled. There is no incremental
// progress callback; callers wanting that poll `status` themselves.
//
// Trail (SR-3.16, SR-14): Pause writes at most one ad.provenance.disagree
// record per distinct reason per call (verb pause, source ad_send_keys),
// fail-open, none in the normal case and none on a path that makes no
// lookup; Client.Pause writes the same records through the same path. The
// records are written once the tmux phase has decided, before the wait, so
// the wait's outcome neither adds nor removes one. Each record's action is
// what the call typed, as for send-keys: keys_sent (`/exit` and Enter both
// went through), text_sent (the `/exit` call timed out, or it went through
// and the Enter call failed or timed out) or nothing_sent. Pause writes no
// other trail event.
//
// pc is the start-time reader that judges the lookup's server (SR-3.3) and
// an adopted pane (SR-3.6).
func Pause(ctx context.Context, s PauseStore, t PauseTmux, pc ProcChecker, timeoutSeconds int, params PauseParams) (PauseResult, error) {
	r := &pauseRun{s: s, caller: callerIdentity()}
	delivered, err := r.exit(t, pc, params)
	// The tmux phase has decided: write its ad.provenance.disagree records
	// now, before the wait. Source ad_send_keys, not a pause source: pause
	// writes the adoption like send-keys (SR-3.6) and runs the lookup, so
	// SR-3.16's logging rule applies; SR-3.16's verdict table and SR-7.2 give
	// pause send-keys' cells (SR-7 covers both verbs), and SR-14's closed
	// source list has no pause source, so pause writes the keys-sending
	// source with verb pause and no call event of its own. Exported Pause is
	// the path Client.Pause shares, so each reason is written once per call.
	r.emitDisagree("pause", params.ClaudeInstanceID, r.caller)
	if err != nil || !delivered {
		return PauseResult{}, err
	}
	return PauseResult{}, waitEnded(ctx, s, timeoutSeconds, params.ClaudeInstanceID)
}

// pauseRun is one Pause call's tmux phase: the keys verbs' shared phase
// (keysRun), with the pane verbs' Leftover refusal, whose found facts (the
// socket, the first lookup's Result, a failed listing's Result, whether the
// adoption write applied, the session concerned, the send and the follow-up
// lookup) the call keeps for its trail.
type pauseRun struct {
	keysRun
	s PauseStore
	// caller is the invoking process's identity, collected once per call
	// (callerIdentity) for the call's ad.provenance.disagree records.
	caller caller
}

// exit is pause's row read, state rules and tmux phase. It reports whether
// `/exit` and Enter went through, so the wait is due; a finished row is
// (false, nil). Every refusal before the lookup makes no tmux call.
func (r *pauseRun) exit(t PauseTmux, pc ProcChecker, params PauseParams) (bool, error) {
	row, err := r.s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return false, err
	}

	switch row.State {
	case store.StateEnded, store.StateMissing:
		return false, nil
	case store.StateWaiting:
		// on to the tmux phase
	default:
		return false, fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotPausable, params.ClaudeInstanceID, row.State)
	}

	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return false, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	r.keysRun = newKeysRun(t, pc, row, r.s.StoreID(), socket, r.s)
	if err := r.deliver("/exit"); err != nil {
		return false, err
	}
	return true, nil
}

// waitEnded is pause's wait after a delivered `/exit` (SR-7.2, SR-17): it
// polls the row's state at pausePollInterval, sleeping through pauseSleep,
// until the row is `ended` (nil), the real-clock deadline timeoutSeconds
// away passes (ErrPauseTimeout) or ctx is done (ctx.Err()).
func waitEnded(ctx context.Context, s PauseStore, timeoutSeconds int, instanceID string) error {
	timeout := time.Duration(timeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)

	for {
		// Check ctx first so a caller who cancelled right after
		// sending /exit exits promptly without one extra sleep.
		if err := ctx.Err(); err != nil {
			return err
		}

		state, err := s.GetSpawnState(instanceID)
		if err != nil {
			return err
		}
		if state == store.StateEnded {
			return nil
		}

		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w: spawn %s did not reach ended within %s",
				ErrPauseTimeout, instanceID, timeout)
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

// Pause politely shuts down a waiting Spawn by sending `/exit` and Enter to
// the agent's own pane of the row's current launch, by pane id, and polling
// until the row reaches ended, or until the configured timeout
// (pause.timeout_seconds in config.toml) elapses. Terminal states
// (ended/missing) are treated as no-op success. Pause is one-shot — no
// incremental progress callback; ctx cancellation short-circuits the poll.
//
// CLI: agent-director pause
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotPausable]: the row's state is not waiting.
//   - [ErrPauseTimeout]: the Spawn did not reach ended within the timeout.
//   - [ErrTmuxSendKeys]: the row's tmux session is not there; `/exit` was
//     not sent or, when the Enter call failed after it, may be typed but not
//     submitted.
//   - [ErrTmuxSessionConflict]: the agent's pane was not found, a session an
//     earlier launch left behind is there, or tmux holds conflicting labels;
//     nothing was sent.
//   - [ErrTmuxUnresponsive]: tmux did not answer, or gave a reply that could
//     not be recognised; when the `/exit` or Enter call timed out, `/exit`
//     may have been delivered, and when the Enter call failed after it,
//     `/exit` may be typed but not submitted.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on.
//
// Nondeterminism: none.
func (c *Client) Pause(ctx context.Context, params PauseParams) (PauseResult, error) {
	if err := c.checkClosed(); err != nil {
		return PauseResult{}, err
	}
	return Pause(ctx, c.st, c.tmuxClient, c.procChecker, c.cfg.Pause.TimeoutSeconds, params)
}
