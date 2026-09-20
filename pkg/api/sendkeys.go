package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// SendKeysStore is the narrow store surface SendKeys needs. *store.Store
// satisfies it; tests pass the real store.
//
// PermissionRequestsForSpawn returns ALL of the Spawn's permission_requests
// rows (decided and undecided) so the relay-guard release can evaluate
// deliverability across every row regardless of decision status (SR-4.2).
type SendKeysStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
}

// SendKeysTmux is the narrow tmux surface SendKeys needs. *tmux.Client
// satisfies it; tests pass a recording fake that captures the text +
// press_enter pair without launching real tmux. The tmux client owns
// the literal-text-then-Enter sequencing internally — see
// (*tmux.Client).SendKeys for the wire shape.
type SendKeysTmux interface {
	SendKeys(name, text string, pressEnter bool) error
}

// SendKeysParams is the typed parameter shape for the send-keys verb.
// JSON tags use snake_case so MCP clients can decode into the struct
// directly via the dispatcher's unmarshalSnake helper.
type SendKeysParams struct {
	// ClaudeInstanceID identifies the Spawn whose pane will receive the text.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// Text is the string to deliver to the Spawn's pane. CR bytes (0x0D) are
	// stripped before delivery; LF bytes (0x0A) are preserved as input newlines.
	// A single Enter is always appended to submit the composed buffer.
	Text string `json:"text"`
	// AllowPending relaxes the interactive-state guard to also allow pending
	// Spawns. Use this when you need to pre-load text before the first
	// SessionStart hook fires (e.g. inserting an initial prompt while the TUI
	// is still booting). ended/missing Spawns are still rejected even with
	// this flag set.
	AllowPending bool `json:"allow_pending"`
}

// SendKeysResult is the typed return shape. Empty struct today; reserved
// so future fields (e.g. truncated_count, dropped_cr_count) can be added
// without breaking the JSON wire shape.
type SendKeysResult struct{}

// send-keys guard-evaluation outcomes, carried on the ad.send_keys.called
// trail event so the relay-recovery path is distinguishable from ordinary
// sends and from guard refusals (SR-5.2). The values are:
//
//   - guardNotApplicable — the relay guard did not apply (relay_mode != on,
//     or state != check_permission); this is the ordinary send path.
//   - guardHeld — relay_mode=on + check_permission and at least one of the
//     Spawn's permission-request rows is still within its delivery window
//     (or there are zero rows). The send was refused with
//     ErrSendKeysWhileRelayed.
//   - guardReleased — relay_mode=on + check_permission and every row's
//     window has elapsed; the guard released and keys were delivered. This is
//     the audited recovery of a fallen-back relay.
const (
	guardNotApplicable = "not-applicable"
	guardHeld          = "held"
	guardReleased      = "released"
)

// sendKeysGuard is the internal result of evaluating the relay guard: the
// guard-evaluation outcome string (one of guardNotApplicable/guardHeld/
// guardReleased) plus whether the send should be refused.
type sendKeysGuard struct {
	eval   string
	refuse bool
}

// SendKeys is the verb-handler entry point for `agent-director send-keys`.
// Behavior (SRD §4.3 + reference/send-keys-research.md):
//
//   - `\r` (CR, 0x0D) bytes in Text are STRIPPED before invoking tmux. CR
//     submits the buffer at the position it appears, which would split
//     one logical message into multiple submissions. The fix is per the
//     research note: delete CR bytes from the input.
//   - `\n` (LF, 0x0A) bytes are PRESERVED — Claude's input handler treats
//     LF as "insert newline in input box", not as a submit. Multi-line
//     prompts compose as one message.
//   - A single Enter is always appended via a separate
//     `tmux send-keys -t <name>:0.0 Enter` call after the text. That is
//     the single submit.
//
// State precondition: the Spawn must be in a live, interactive state
// (waiting / working / ask_user / check_permission). pending Spawns have
// not yet booted their TUI; ended / missing Spawns have nothing to type
// into. A non-interactive state surfaces ErrSpawnNotInteractive.
// Set AllowPending=true to also permit pending Spawns (pre-SessionStart
// use case); ended/missing are still rejected.
//
// Relay-mode guard (time-bounded): when relay_mode=on AND
// state=check_permission, the permission relay normally owns the answer, so
// SendKeys refuses with ErrSendKeysWhileRelayed to keep a pane-side keystroke
// from racing the relay's decide() write. The refusal is NOT unconditional:
// Claude Code kills the relay hook at its per-hook timeout, after which the
// poller can no longer deliver a decision and the guard is pure denial of
// service. The guard therefore consults the shared deliverability signal
// (RelayRequestUndeliverable, SR-4.4) across ALL of the Spawn's
// permission_requests rows — each row's window measured from its own
// created_at, regardless of decision status (SR-4.2: a row decided in-window
// still has a live poller about to deliver it). It refuses while ANY row is
// still within its window and RELEASES only once every row's window has
// elapsed. A relay-on check_permission Spawn with zero rows keeps refusing:
// with no row there is no signal and no authority to release, and the state
// is a real mid-insert transient.
//
// effectiveWindow is the resolved relay window (obtained by the caller via
// Epic 1's accessor and consumed only through the shared deliverability
// function); now is the injected clock so the release verdict is deterministic
// and testable.
func SendKeys(s SendKeysStore, tmux SendKeysTmux, effectiveWindow time.Duration, now time.Time, params SendKeysParams) (SendKeysResult, error) {
	_, res, err := sendKeys(s, tmux, effectiveWindow, now, params)
	return res, err
}

// sendKeys is the inner implementation shared by the pure SendKeys entry point
// and Client.SendKeys. It returns the guard-evaluation outcome string
// alongside the result/error so the Client wrapper can record it on the
// ad.send_keys.called trail event without re-deriving it.
func sendKeys(s SendKeysStore, tmux SendKeysTmux, effectiveWindow time.Duration, now time.Time, params SendKeysParams) (string, SendKeysResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return guardNotApplicable, SendKeysResult{}, err
	}

	if !isInteractiveState(row.State) && !(params.AllowPending && row.State == store.StatePending) {
		return guardNotApplicable, SendKeysResult{}, fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotInteractive, params.ClaudeInstanceID, row.State)
	}

	guard, err := evaluateRelayGuard(s, effectiveWindow, now, row, params.ClaudeInstanceID)
	if err != nil {
		return guardNotApplicable, SendKeysResult{}, err
	}
	if guard.refuse {
		return guard.eval, SendKeysResult{}, fmt.Errorf(
			"%w: spawn %s is awaiting a relayed permission decision (guard releases once every request's delivery window elapses)",
			ErrSendKeysWhileRelayed, params.ClaudeInstanceID)
	}

	cleaned := strings.ReplaceAll(params.Text, "\r", "")
	// The tmux client handles the literal-text-then-real-Enter split
	// internally (see (*tmux.Client).SendKeys); the verb hands it the
	// cleaned text plus pressEnter=true.
	if err := tmux.SendKeys(row.TmuxSessionName, cleaned, true); err != nil {
		return guard.eval, SendKeysResult{}, err
	}

	return guard.eval, SendKeysResult{}, nil
}

// evaluateRelayGuard decides whether the time-bounded relay guard refuses the
// send. It returns guardNotApplicable when the guard does not apply
// (relay_mode != on or state != check_permission — the ordinary send path).
// Otherwise it consults the shared deliverability signal across every one of
// the Spawn's permission_requests rows and returns guardHeld (refuse) while any
// row is still within its window (including the zero-rows state), or
// guardReleased (deliver) once every row's window has elapsed.
//
// All time arithmetic lives in RelayRequestUndeliverable (SR-4.4); this
// function performs no independent elapsed-vs-timeout computation.
func evaluateRelayGuard(s SendKeysStore, effectiveWindow time.Duration, now time.Time, row Spawn, instanceID string) (sendKeysGuard, error) {
	if !(row.RelayMode == "on" && row.State == store.StateCheckPermission) {
		return sendKeysGuard{eval: guardNotApplicable, refuse: false}, nil
	}

	rows, err := s.PermissionRequestsForSpawn(instanceID)
	if err != nil {
		return sendKeysGuard{}, err
	}

	// Zero rows: no signal, no authority to release — keep refusing (PM-pinned).
	if len(rows) == 0 {
		return sendKeysGuard{eval: guardHeld, refuse: true}, nil
	}

	// Refuse while ANY row is still within its delivery window; release only
	// when every row's window has elapsed. Each row's window is measured from
	// its own created_at by the shared single-authority signal, regardless of
	// decision status (a row decided in-window still has a live poller).
	for _, pr := range rows {
		if !RelayRequestUndeliverable(pr.CreatedAt, effectiveWindow, now) {
			return sendKeysGuard{eval: guardHeld, refuse: true}, nil
		}
	}
	return sendKeysGuard{eval: guardReleased, refuse: false}, nil
}

// isInteractiveState returns true iff the supplied state value belongs to
// the set of live conversational states send-keys is allowed to drive.
// pending is excluded because the TUI isn't up yet — the first
// SessionStart hook flips pending to waiting, after which the Spawn is
// reachable.
func isInteractiveState(state string) bool {
	switch state {
	case store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission:
		return true
	}
	return false
}

// sendKeysOutcome maps a SendKeys error to its canonical outcome string for
// ad.send_keys.called. nil → "ok"; known sentinels → their err_name;
// unrecognized errors → "ErrInternal". The function uses errors.Is so
// %w-wrapped errors are matched correctly.
//
// As with decideOutcome, errnames.Classify cannot be used here: pkg/api/errnames
// imports pkg/api for its sentinel variables, so importing errnames from pkg/api
// would create an unresolvable import cycle. This local switch is the canonical
// pattern for send-keys-path outcome strings.
func sendKeysOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, ErrSpawnNotInteractive):
		return "ErrSpawnNotInteractive"
	case errors.Is(err, ErrSendKeysWhileRelayed):
		return "ErrSendKeysWhileRelayed"
	case errors.Is(err, store.ErrSpawnNotFound):
		return "ErrSpawnNotFound"
	default:
		return "ErrInternal"
	}
}

// SendKeys sends text into a tracked Spawn's tmux pane. CR bytes (0x0D) are
// stripped before delivery to prevent premature submission; LF bytes (0x0A)
// are preserved as composed newlines in Claude's input box. A single Enter is
// always appended to submit the composed buffer.
//
// CLI: agent-director send-keys
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotInteractive]: the Spawn's state is not one of waiting,
//     working, ask_user, check_permission, or (with AllowPending=true) pending.
//   - [ErrSendKeysWhileRelayed]: relay_mode is on and state is
//     check_permission and at least one of the Spawn's permission requests is
//     still within its relay delivery window (or the Spawn has zero request
//     rows). The refusal is time-bounded: once every request row's window has
//     elapsed the delivering hook is dead and the guard releases, letting the
//     operator recover the wedged Spawn through this sanctioned surface.
//   - ErrTmuxNotAvailable: tmux binary is not on PATH.
//   - ErrTmuxSendKeys: tmux send-keys exited non-zero.
//
// Nondeterminism: none.
func (c *Client) SendKeys(params SendKeysParams) (SendKeysResult, error) {
	if err := c.checkClosed(); err != nil {
		return SendKeysResult{}, err
	}

	// Collect caller identity once at entry — must come from inside AD, not
	// from caller-asserted params (SR-5.2), mirroring Client.Decide.
	callerID := callerIdentity()

	var callErr error
	guardEval := guardNotApplicable
	defer func() {
		// Emit exactly one ad.send_keys.called per invocation, on every return
		// path including error outcomes (SR-5.2). The guard_evaluation field
		// makes the relay-recovery send (guard released) distinguishable from
		// ordinary sends and from guard refusals. Fail-open per SR-A-3.2: a
		// trail-emit failure is silently discarded and never fails the send.
		_ = trail.Emit(context.Background(), "ad.send_keys.called", map[string]any{
			"claude_instance_id": params.ClaudeInstanceID,
			"allow_pending":      params.AllowPending,
			"guard_evaluation":   guardEval,
			"outcome":            sendKeysOutcome(callErr),
			"caller_process":     callerID.process,
			"caller_pid":         callerID.pid,
			"caller_hostname":    callerID.hostname,
			"caller_user":        callerID.user,
			"source":             "ad_send_keys",
		})
	}()

	// Effective relay window is resolved here via Epic 1's accessor (the single
	// source for the non-positive→default fallback); the clock is injected as
	// time.Now() so the guard-release verdict is deterministic at the API
	// boundary, mirroring Client.Decide.
	effectiveWindow := time.Duration(c.cfg.Relay.EffectiveTimeoutSeconds()) * time.Second
	var result SendKeysResult
	guardEval, result, callErr = sendKeys(c.st, c.tmuxClient, effectiveWindow, time.Now(), params)
	return result, callErr
}
