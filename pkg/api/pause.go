package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PauseTmux is the narrow tmux surface Pause needs (SRD Appendix F.3): the
// lookup, the pane listing, the line-clearing key and the keys sent to one
// pane by its pane id. TmuxClient, *tmux.Client and tmuxfix.Recorder satisfy
// it. Every method takes the row's socket (SR-3.3) and reports a failure as
// *TmuxCallError.
type PauseTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
	// SendKeyPane sends one key, by its tmux key name and never typed
	// literally, to the pane paneID on socket (key send): pause's C-u.
	SendKeyPane(socket, paneID, key string) error
	// SendKeysPane types text literally into the pane paneID on socket and
	// then, only if that succeeded and pressEnter is set, sends Enter; the
	// failed call is named on the *TmuxCallError (text send or Enter send).
	SendKeysPane(socket, paneID, text string, pressEnter bool) error
}

// PauseStore is the narrow store surface Pause needs (Appendix F.3): the
// row read, the state-only read the wait polls, the relay guard's read of
// every permission request of the row and its hook_gone_at write on a request
// it finds fallen back (send-keys' own, b.146 rules 7 and 8; b.zdz), the
// adoption write of SR-3.6 (a lost create reply's server and pane identity,
// applied only if the row still has the snapshot Pause examined) and this
// store's id, which every label the lookup accepts ends with (SR-3.4). The
// adoption and hook_gone_at are the only writes pause makes. *store.Store
// satisfies it; tests use the shared killStore fixture, a wrapper over a real
// store that can fail the state read, the requests read or the adoption
// write.
type PauseStore interface {
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// GetSpawnState reads only the row's state, for the wait.
	GetSpawnState(instanceID string) (string, error)
	// PermissionRequestsForSpawn reads every permission request of the row.
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	// RecordHookGone records hook_gone_at on the requests with requestIDs
	// that have none yet and returns each one's stored value.
	RecordHookGone(at time.Time, maxWait time.Duration, requestIDs ...int64) (map[int64]time.Time, error)
	// AdoptIdentityIfUnchanged records a found launch identity when the
	// row is still as examined (SR-3.6).
	AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// PauseEnv is what Pause judges the relay with besides its store and tmux
// surface (b.zdz): the relay view and intent hold a plain send-keys gets
// (SendKeysEnv), so pause's relay guard is evaluated exactly as send-keys'
// is. The Client builds it from its own readers, clock and configuration.
type PauseEnv struct {
	// Relay judges the Spawn's relay hooks: its start-time reader (which also
	// judges the lookup's server and an adopted pane, SR-3.3, SR-3.6), its own
	// pid namespace, its clock and the relay window.
	Relay RelayView
	// IntentHold is how long a pane answer's intent whose sender cannot be
	// checked counts as in progress (paneIntentHold), given to the relay
	// guard as send-keys gives it.
	IntentHold time.Duration
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

// Pause politely shuts down a live Spawn by clearing the input line of the
// agent's own pane of the row's current launch (C-u: it clears a one-line
// leftover, not a multi-line input), by pane id, then typing `/exit` and
// Enter, and waiting up to timeoutSeconds for the row to reach `ended` (SRD
// SR-7.2, SR-7.3, SR-3.6, SR-3.7, SR-13.2; b.9o4). Behavior:
//
//   - Unknown id: ErrSpawnNotFound from the store.
//   - State ended or missing: no-op success, with no tmux call; the desired
//     post-condition is already met.
//   - State pending, working, ask_user or check_permission:
//     ErrSpawnNotPausable, with no tmux call. `/exit` is never typed in these
//     states, where the slash would be read as input text.
//   - State waiting: the relay guard (below); then a recorded name that is
//     unusable (SR-3.2) is ErrInternal, with no tmux call and no wait;
//     otherwise the row's socket (SR-3.3; a resolution refusal is
//     ErrTmuxNotAvailable) and one lookup by the row's current label, then as
//     below.
//
// Relay guard (b.zdz): pause types C-u, `/exit` and Enter as a plain
// send-keys without expect_pane_sha256 would, so on a waiting row with
// relay_mode on it is held as one. Send-keys' own relay guard (relayGuard
// with no request token and no pane hash; b.146 rule 7 and step 2c) reads
// the row's permission requests by the check-before-read rule and refuses,
// with no tmux call and no wait, in send-keys' order, with its descriptions
// and its err_details:
//
//  1. A relay hook of the row may still answer its request:
//     ErrSendKeysWhileRelayed, naming that request. On a request whose
//     verdict is recorded it advises "retry later" where send-keys says
//     "retry send-keys later"; its other descriptions are send-keys' own.
//  2. An open request has fallen back with no pane answer recorded:
//     ErrRelayFallenBack (RelayFallenBackDetails), naming the oldest. It
//     first records hook_gone_at on every such request with none (waiting
//     the store's busy timeout, fail-open), so its err_details carry it.
//  3. A request of the row is not proven gone by Claude Code's hooks:
//     ErrDialogMaybeOpen (DialogMaybeOpenDetails), naming the oldest.
//
// A failed read of the requests is the store's error, nothing sent. A
// relay-off row, a row with no permission request and a row whose every
// request is proven gone are not held. Pause takes no pane hash, so it has no
// way past the hold, which lasts until Claude Code's own hooks prove each
// request gone, whatever is done at the pane: a background subagent's request
// answered deny at the pane, for one, gets no PostToolUse, and the main
// agent's Stop does not prove a subagent's request, so it holds pause until
// the agent ends. A caller that has read the pane and answered any dialog on
// it types `/exit` itself, with a send-keys whose expect_pane_sha256 is the
// pane it judged (which the hold lets through), or kills the Spawn.
//
// The lookup's outcomes on a waiting row:
//
//   - Ours: one pane listing, then C-u, `/exit` and Enter to the agent's pane
//     (the entry with the row's recorded pane id and pid, wherever it now
//     is), then the wait. C-u, sent as a key, deletes from the cursor back to
//     the start of its line, not earlier lines of a multi-line input, so a
//     one-line leftover with the cursor at its end (the unsubmitted `/exit` a
//     failed pause left, or a one-line draft) is cleared and the agent gets
//     `/exit` alone, never `/exit/exit`. For a row that records no server
//     identity or no pane (a lost create reply), what the lookup and the pane
//     whose @ad_pane names the row's launch token show is used for this call
//     and written once, guarded on the row as read; the write's outcome never
//     changes the result (SR-3.6). No agent's pane: ErrTmuxSessionConflict
//     ("the agent's pane was not found"), nothing sent.
//   - Leftover: ErrTmuxSessionConflict ("not this launch's session"),
//     nothing sent.
//   - Gone (another agent-director store's sessions included):
//     ErrTmuxSendKeys ("the row's session is not there"), `/exit` not sent.
//   - Can't tell, tmux unavailable, and a pane listing that fails other than
//     by showing no server: the single-row verbs' shared mapping
//     (ErrTmuxNotAvailable, ErrTmuxSessionConflict "conflicting labels",
//     ErrTmuxUnresponsive), nothing sent. A listing that shows no server is
//     Gone.
//   - A failed C-u, text or Enter call: a timeout is ErrTmuxUnresponsive
//     saying the keys may have been delivered, with no further call; any
//     other failure makes one follow-up lookup, whose Gone or Leftover gives
//     ErrTmuxSendKeys and whose other outcomes give ErrTmuxUnresponsive, or
//     ErrTmuxNotAvailable for a different server or tmux unavailable
//     (SR-7.3). Once the text call went through, every description says the
//     text may be typed but not submitted, never that nothing was sent. A
//     failed C-u types nothing and ends the call before `/exit`: it is
//     described as a failed text call is, naming the key send. Every
//     ErrTmuxUnresponsive ends "retry later": a retried pause clears the line
//     before typing, so it submits a clean `/exit` whatever its own failed
//     attempt left typed.
//
// Every tmux call uses the row's socket and every action targets a pane id.
// The calls are at most one lookup, one pane listing, the C-u, the text
// call, the Enter call and, only after an action failure other than a
// timeout, one follow-up lookup (SR-13.2); the wait is not counted. On any
// error the wait does not start.
//
// The wait, once C-u, `/exit` and Enter went through: the row's state is
// polled at pausePollInterval until it is `ended` (nil) or timeoutSeconds
// elapse on the real clock (ErrPauseTimeout); ctx.Done() during the wait
// returns ctx.Err() so the caller's cancel-on-signal handler can cut a long
// wait short. timeoutSeconds is used as given, with no default applied: 0 or
// less times out at the first state check that does not find `ended`.
// Client.Pause passes pause.timeout_seconds through
// config.Pause.EffectiveTimeoutSeconds. Pause never writes the row's state;
// the adoption and the relay guard's hook_gone_at are its only store writes.
//
// Pause is one-shot: it returns when the row reaches `ended`, when the
// timeout expires, or when ctx is cancelled. There is no incremental
// progress callback; callers wanting that poll `status` themselves.
//
// Trail (SR-3.16, SR-14): Pause writes at most one ad.provenance.disagree
// record per distinct reason per call (verb pause, source ad_send_keys),
// fail-open, none in the normal case and none on a path that makes no
// lookup (a relay guard refusal included); Client.Pause writes the same
// records through the same path. The records are written once the tmux phase
// has decided, before the wait, so the wait's outcome neither adds nor
// removes one. Each record's action is
// what the call typed, as for send-keys: keys_sent (`/exit` and Enter both
// went through), text_sent (the `/exit` call timed out, or it went through
// and the Enter call failed or timed out) or nothing_sent (a failed C-u
// included: it types no text). Pause writes no other trail event.
//
// env carries the relay judge (its start-time reader judges the lookup's
// server, SR-3.3, and an adopted pane, SR-3.6, too) and the intent hold.
func Pause(ctx context.Context, s PauseStore, t PauseTmux, env PauseEnv, timeoutSeconds int, params PauseParams) (PauseResult, error) {
	r := &pauseRun{s: s, caller: callerIdentity()}
	delivered, err := r.exit(t, env, params)
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

// exit is pause's row read, state rules, relay guard and tmux phase, which
// clears the agent's input line before typing (keysRun.clear). It reports
// whether C-u, `/exit` and Enter went through, so the wait is due; a finished
// row is (false, nil). Every refusal before the lookup makes no tmux call.
func (r *pauseRun) exit(t PauseTmux, env PauseEnv, params PauseParams) (bool, error) {
	row, err := r.s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return false, err
	}

	switch row.State {
	case store.StateEnded, store.StateMissing:
		return false, nil
	case store.StateWaiting:
		// on to the relay guard
	default:
		return false, fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotPausable, params.ClaudeInstanceID, row.State)
	}

	// C-u, `/exit` and Enter are what a plain send-keys without a pane hash
	// types, so pause is held as one (b.zdz): send-keys' relay guard, with no
	// request token and no expect_pane_sha256, whose refusal on a recorded
	// verdict advises retryLater, as the refused call is pause.
	guard, err := relayGuard(r.s, newRelayJudge(env.Relay), env.IntentHold, row, SendKeysParams{}, retryLater)
	if err != nil {
		return false, err
	}
	if guard.refusal != nil {
		return false, guard.refusal
	}

	if err := unusableNameError(row.TmuxSessionName); err != nil {
		return false, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}

	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return false, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	r.keysRun = newKeysRun(t, env.Relay.Procs, row, r.s.StoreID(), socket, r.s)
	r.clear = t
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

// Pause politely shuts down a waiting Spawn by sending C-u (which deletes the
// agent's input from the cursor back to the start of its line, clearing a
// one-line leftover such as the `/exit` a failed pause left typed), `/exit`
// and Enter to the agent's own pane of the row's current launch, by pane id,
// and polling until the row reaches ended, or until the configured timeout
// (pause.timeout_seconds in config.toml; a missing key or 0 gives the default,
// 30 s) elapses. Terminal states (ended/missing) are treated as no-op success.
// Pause is one-shot — no incremental progress callback; ctx cancellation
// short-circuits the poll.
//
// Pause types what a plain send-keys without ExpectPaneSHA256 types, so on a
// relayed Spawn it is held as one: it is refused, typing nothing (no C-u
// either) and with no wait, whenever such a send-keys would be
// (ErrSendKeysWhileRelayed, ErrRelayFallenBack or ErrDialogMaybeOpen, in that
// order and with the same err_details). It takes no pane hash, so it has no
// way past the hold (ErrDialogMaybeOpen), which lasts until Claude Code's own
// hooks prove each request gone, whatever is done at the pane: a subagent's
// request answered deny at the pane gets no PostToolUse, and the main agent's
// Stop does not prove it, so it holds Pause until the agent ends. Having read
// the pane and answered any dialog on it, type `/exit` with SendKeys and
// ExpectPaneSHA256 of the pane judged (the hold lets it through), or Kill. A
// Spawn with no permission request, or whose every request is proven gone,
// pauses as before.
//
// A waiting row whose recorded tmux session name cannot be used (it is empty,
// contains a control character, or contains a character tmux stores
// differently) gets ErrInternal (an error matching no catalogued sentinel)
// with no tmux call and no wait; removing the row is a human's decision (see
// "Operator actions" in agent-director's docs/operator-actions.md).
//
// CLI: agent-director pause
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotPausable]: the row's state is not waiting.
//   - [ErrSendKeysWhileRelayed]: relay_mode is on and a relay hook of the
//     Spawn may still answer its request (its process runs, or cannot be
//     checked and its request is open before its confirm_by); nothing was
//     sent. The message names that request, and advises answering it with
//     decide, or, when its verdict is recorded, retrying later.
//   - [ErrRelayFallenBack]: a request of the Spawn has fallen back with no
//     pane answer recorded through agent-director (pane_answer none or
//     intent); nothing was sent. Its err_details
//     ([RelayFallenBackDetails]) name the request and the Spawn's other open
//     requests. Answer it with a pane answer (SendKeys with its
//     RequestToken), or close it with RecordPaneAnswer; Pause then stays
//     held (ErrDialogMaybeOpen) until Claude Code proves it gone.
//   - [ErrDialogMaybeOpen]: a request of the Spawn is not proven gone by
//     Claude Code's hooks (its tool's PostToolUse, the main agent's end of
//     turn after it, or the agent's end), whatever agent-director's records
//     say of it; nothing was sent. Its err_details
//     ([DialogMaybeOpenDetails]) name the request, how agent-director's
//     records say it closed, since when, and the Spawn's other such
//     requests. The way past it is above: SendKeys `/exit` with the judged
//     pane's ExpectPaneSHA256, or Kill.
//   - [ErrPauseTimeout]: the Spawn did not reach ended within the timeout.
//   - [ErrTmuxSendKeys]: the row's tmux session is not there; `/exit` was
//     not sent or, when the Enter call failed after it, may be typed but not
//     submitted.
//   - [ErrTmuxSessionConflict]: the agent's pane was not found, a session an
//     earlier launch left behind is there, or tmux holds conflicting labels;
//     nothing was sent.
//   - [ErrTmuxUnresponsive]: tmux did not answer, or gave a reply that could
//     not be recognised; when the C-u, `/exit` or Enter call timed out, the
//     keys may have been delivered, and when the Enter call failed after
//     `/exit`, `/exit` may be typed but not submitted. Retry later: a retried
//     pause clears the one-line `/exit` its failed attempt left before typing
//     `/exit`.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on.
//
// Nondeterminism: the relay guard depends on the clock and on whether each
// relay hook runs.
func (c *Client) Pause(ctx context.Context, params PauseParams) (PauseResult, error) {
	if err := c.checkClosed(); err != nil {
		return PauseResult{}, err
	}
	// The effective pause wait (b.8q2): the configured value when positive,
	// otherwise the default; Load has refused a negative value and one too
	// large for a time.Duration. The exported Pause takes the timeout as
	// given, 0 included. The relay view and intent hold are send-keys' own
	// (sendKeysEnv), so pause's relay guard is judged as send-keys' is.
	env := PauseEnv{Relay: c.relayView(), IntentHold: paneIntentHold(c.cfg.Tmux)}
	return Pause(ctx, c.st, c.tmuxClient, env, c.cfg.Pause.EffectiveTimeoutSeconds(), params)
}
