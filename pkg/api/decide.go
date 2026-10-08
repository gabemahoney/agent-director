package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// ErrRelayModeOff is returned by decide() when the target Spawn's
// `relay_mode` column is anything other than "on". Per SRD §6.2 the
// decide verb only makes sense for Spawns the operator has opted
// into relay mode for.
var ErrRelayModeOff = errors.New("ErrRelayModeOff")

// ErrInvalidDecision is returned by decide() when --decision is
// neither "allow" nor "deny". SRD §6.3 pins the two-valued surface.
var ErrInvalidDecision = errors.New("ErrInvalidDecision")

// ErrMissingRequestToken is returned by Decide when RequestToken is empty.
// The token uniquely identifies the permission-request row to decide; without
// it the call is rejected at the API layer regardless of CLI gating.
var ErrMissingRequestToken = errors.New("ErrMissingRequestToken")

// ErrRelayFallenBack is returned by Decide when the target permission
// request's record is still open past its relay window and its relay hook can
// no longer answer it: the hook had neither delivered a verdict nor recorded
// its timeout deny when Decide last read the record, by when a live hook is
// presumed to have done one or the other (see below). Recording a verdict
// would write success into a void, so the record's decision stays NULL (no
// information is lost). The message conveys "too late — answer at the pane":
// the recourse is to answer at the pane directly, with send-keys. The message
// states no release time (b.ah6): by the time Decide returns this error, the
// send-keys relay guard has already released on this request's account. If
// send-keys still refuses with ErrSendKeysWhileRelayed, another of the Spawn's
// requests holds it and the refusal names that request; answer that one with
// decide, as that error says.
//
// Decide reads stored records, not Claude Code's screen, and a record can be
// left open after its permission dialog closed (b.t6e): the dialog was
// answered at the pane after the relay hook was killed, or closed by a timeout
// deny the hook returned without recording it. A pane answer to such a
// request is typed into Claude's prompt as a user message. So Decide returns
// this error only while the Spawn is still shown sitting on the request
// alone as of its last read: the Spawn is still in check_permission, no
// request of the Spawn was recorded after this one, and no other request of
// it is open (fallenBackUnshown). Otherwise it refuses with
// ErrNoOpenPermissionRequest, whose message advises no pane answer. What the
// records cannot show: while this request's record is open the store holds
// the agent's moves to working, so a dialog closed either way looks like one
// still on screen until a hook moves the Spawn out of check_permission (Stop,
// AskUserQuestion) or records a later request. Nor do they show a later
// request whose PermissionRequest hook has moved the Spawn to
// check_permission but whose record is not yet stored: if this request's
// record is stale and Decide reads in that gap, it returns this error, and
// keys sent at the pane before the later request is recorded can answer that
// request's dialog.
//
// Decide refuses from window - RelayKillSafetyMargin
// (RelayRequestUndeliverable), but a live relay hook may still reach its poll
// deadline after that and deny the request, which closes the dialog and moves
// the row to working, where a pane answer would be typed into Claude's prompt
// as a user message (b.pzy). Decide therefore reads the record last at
// created_at plus the window plus RelayKillSafetyMargin plus created_at's
// storage resolution (1 s), the instant the send-keys relay guard releases
// on the request's account: by then a live hook is presumed to have recorded
// its timeout deny (its poll deadline is counted from the stored created_at,
// b.z6g), and one that never reached its deadline to have been killed by
// Claude Code, whose per-hook timeout started before the record was stored
// (see deliverability.go). A refusal before that instant waits until it (at
// most twice the margin plus the resolution, 3 s) and reads the request
// again. A request decided meanwhile, such as by the hook's timeout deny, is
// ErrAlreadyDecided, and only a request whose record is still open is
// ErrRelayFallenBack. Callers detect it with errors.Is.
var ErrRelayFallenBack = errors.New("ErrRelayFallenBack")

// DecideStore is the narrow store surface Decide needs: the row read, the
// deliverability-guarded write, the follow-up read of the request a refusal
// names, and the read of every permission request of the Spawn that decides
// whether a fallen-back request is still the one the Spawn is shown sitting on
// (b.t6e). *store.Store satisfies it.
type DecideStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	DecidePermissionRequestIfDeliverable(instanceID, requestToken, decision, reason string, writerProcess string, cutoff time.Time) (bool, error)
	GetPermissionRequest(instanceID, requestToken string) (PermissionRow, error)
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
}

// DecideParams is the typed parameter shape for the decide verb.
// Decision is either "allow" or "deny"; Reason is accepted for wire
// compatibility but is currently discarded — the canonical
// DecisionReasonOperator is persisted regardless of its value.
type DecideParams struct {
	// ClaudeInstanceID identifies the Spawn whose open permission request is
	// being decided.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// RequestToken is the UUIDv4 token minted by runRelay for the specific
	// permission request being decided. Required; empty string is rejected at
	// the API layer (ErrMissingRequestToken) as well as at the CLI layer.
	RequestToken string `json:"request_token"`
	// Decision is the orchestrator's verdict: "allow" or "deny".
	Decision string `json:"decision"`
	// Reason is currently discarded on deny; the canonical DecisionReasonOperator
	// is persisted regardless. Reserved for future schema additions.
	Reason string `json:"reason"`
}

// DecideResult is the typed return shape. Empty today; reserved so a
// future field (e.g. echoing the recorded reason) can be added without
// breaking the wire shape.
type DecideResult struct{}

// Decide writes the orchestrator's allow/deny verdict to the open
// permission_requests row identified by the (claude_instance_id, request_token)
// pair (SRD §6.2). request_token is required — it uniquely identifies the
// specific row minted by runRelay for this request. Behavior:
//
//   - Empty request_token → ErrMissingRequestToken (validated at this layer,
//     not just at the CLI).
//   - Unknown id → ErrSpawnNotFound from the store.
//   - Spawn's relay_mode != "on" → ErrRelayModeOff. decide is a
//     no-op outside relay mode; the verb refuses rather than write
//     a row Claude will never look at.
//   - Invalid decision string → ErrInvalidDecision.
//   - Single-statement UPDATE writes (decision, decision_reason, decided_at)
//     guarded by `decision IS NULL AND request_token = ? AND created_at > cutoff`.
//     The cutoff is the deliverability boundary from the shared single-authority
//     signal (RelayDeliverabilityCutoff / RelayRequestUndeliverable), so the
//     deliverability check and the write are one atomic statement (SR-3.4):
//     there is no interval in which success is returned but the relay window has
//     already closed.
//   - RowsAffected==0 is three-way ambiguous and disambiguated via a follow-up
//     SELECT: already-decided (ErrAlreadyDecided) wins for decided rows;
//     open-but-undeliverable (ErrRelayFallenBack) applies ONLY to open rows; no
//     row → ErrNoOpenPermissionRequest.
//   - An open row refused before its relay hook has settled (now earlier
//     than created_at + window + RelayKillSafetyMargin + created_at's 1 s
//     storage resolution, at most 3 s away) may still be denied by its relay
//     hook at the hook's poll deadline. Decide sleeps until that instant and
//     repeats the follow-up SELECT as of it, so ErrRelayFallenBack is
//     returned only for a row still open then; one decided meanwhile is
//     ErrAlreadyDecided (b.pzy). No other path sleeps.
//   - A row still open then has fallen back. It is ErrRelayFallenBack only
//     while the Spawn is still shown sitting on it alone: decide reads the
//     Spawn and all its requests again, and the Spawn must still be in
//     check_permission, with no request recorded after this one and no other
//     request open. Otherwise it is ErrNoOpenPermissionRequest, with no pane
//     advice, since the request's dialog may have closed and a pane answer
//     would then be typed into Claude's prompt (b.t6e).
//   - params.Reason is currently discarded; DecisionReasonOperator is always
//     written for deny decisions regardless of its value.
//
// effectiveWindow is the resolved relay window (obtained by the caller via
// Epic 1's accessor and consumed only through the shared deliverability
// function); now is the injected clock so the deliverability verdict is
// deterministic and testable. The sleep above is time.Sleep.
func Decide(s DecideStore, effectiveWindow time.Duration, now time.Time, params DecideParams) (DecideResult, error) {
	return decide(s, effectiveWindow, now, time.Sleep, params)
}

// decide is Decide with the sleep of the wait for a fallen-back request's
// relay hook injected: Decide passes time.Sleep and Client.Decide the
// Client's own sleep, so a test can stand in for the hook during the wait.
func decide(s DecideStore, effectiveWindow time.Duration, now time.Time, sleep func(time.Duration), params DecideParams) (DecideResult, error) {
	if params.RequestToken == "" {
		return DecideResult{}, fmt.Errorf("%w: request_token is required", ErrMissingRequestToken)
	}
	if params.Decision != "allow" && params.Decision != "deny" {
		return DecideResult{}, fmt.Errorf("%w: %q", ErrInvalidDecision, params.Decision)
	}

	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return DecideResult{}, err
	}
	if row.RelayMode != "on" {
		return DecideResult{}, fmt.Errorf("%w: spawn %s relay_mode=%q",
			ErrRelayModeOff, params.ClaudeInstanceID, row.RelayMode)
	}

	// Use the canonical operator reason for deny; empty string for allow.
	// decision_reason in the DB is always one of the store.DecisionReason*
	// constants for operator-originated decisions.
	dbReason := ""
	if params.Decision == "deny" {
		dbReason = store.DecisionReasonOperator
	}

	// Deliverability boundary from the single authority (SR-4.4). The cutoff is
	// computed here and passed into the guarded UPDATE so the boundary +
	// safety-margin logic is never restated in SQL.
	cutoff := RelayDeliverabilityCutoff(now, effectiveWindow)
	updated, err := s.DecidePermissionRequestIfDeliverable(params.ClaudeInstanceID, params.RequestToken, params.Decision, dbReason, store.WriterProcessDecide, cutoff)
	if err != nil {
		return DecideResult{}, err
	}
	if updated {
		return DecideResult{}, nil
	}
	return DecideResult{}, decideRefusal(s, effectiveWindow, now, sleep, params)
}

// decideRefusal names a decide whose guarded UPDATE affected no row: the row
// is absent, already decided, or open but undeliverable. It disambiguates via
// a follow-up SELECT. Precedence (PM-pinned): ErrAlreadyDecided wins for
// decided rows; ErrRelayFallenBack applies ONLY to open rows. The race window
// is benign: a concurrent decide that landed since our UPDATE surfaces as
// ErrAlreadyDecided; a row that fell out of the window surfaces as
// ErrRelayFallenBack.
//
// An open, undeliverable row whose relay hook has not yet settled
// (relayHookSettledAt) may still be denied by its live relay hook at the
// hook's poll deadline, which closes the permission dialog; "answer at the
// pane" would then type into Claude's prompt (b.pzy). So for such a row
// decideRefusal sleeps until that instant (at most twice
// RelayKillSafetyMargin plus createdAtResolution, 3 s) and repeats the SELECT
// as of it: whatever decided the row meanwhile wins as ErrAlreadyDecided, and
// a row still open has fallen back, its hook presumed dead and its send-keys
// guard already released. fallenBackRefusal names that one:
// ErrRelayFallenBack, whose message states no release time (b.ah6), only
// while the Spawn is still shown sitting on it alone, otherwise
// ErrNoOpenPermissionRequest (b.t6e).
func decideRefusal(s DecideStore, effectiveWindow time.Duration, now time.Time, sleep func(time.Duration), params DecideParams) error {
	pr, err := s.GetPermissionRequest(params.ClaudeInstanceID, params.RequestToken)
	if err == nil && pr.Decision == "" && RelayRequestUndeliverable(pr.CreatedAt, effectiveWindow, now) {
		if settled := relayHookSettledAt(pr.CreatedAt, effectiveWindow); now.Before(settled) {
			sleep(settled.Sub(now))
			now = settled
			pr, err = s.GetPermissionRequest(params.ClaudeInstanceID, params.RequestToken)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID)
	}
	if err != nil {
		return err
	}
	if pr.Decision != "" {
		return alreadyDecidedError(params.ClaudeInstanceID, pr)
	}
	// Open row that the guarded UPDATE refused: the only reason a
	// token-matched, decision-NULL row is skipped is the deliverability
	// predicate, and the wait above has brought now to or past its relay
	// hook's settling. Re-confirm via the shared definition of a fallen-back
	// request, the one the send_keys guard applies (no second inline time
	// comparison), and name the fallen-back refusal.
	if relayRequestFallenBack(pr, effectiveWindow, now) {
		return fallenBackRefusal(s, params, pr)
	}
	// Unreachable in practice — the row exists, decision is NULL, is within the
	// window, yet UPDATE didn't affect it. The only way to land here is a SQL
	// driver oddity; surface as the more conservative ErrNoOpenPermissionRequest.
	return fmt.Errorf("%w: %s (UPDATE no-op against open, deliverable row)",
		store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID)
}

// fallenBackRefusal names decide's refusal of pr, a request decideRefusal
// found fallen back (relayRequestFallenBack). It reads the Spawn, then every
// one of its requests, the last read of pr included: pr removed meanwhile
// (with its Spawn) is ErrNoOpenPermissionRequest and pr decided meanwhile is
// ErrAlreadyDecided, as in decideRefusal. A pr still open is
// ErrRelayFallenBack ("answer at the pane") only while the Spawn is still
// shown sitting on it alone (fallenBackUnshown); otherwise it is
// ErrNoOpenPermissionRequest, whose message says why, that the request's
// permission dialog cannot be shown to be on screen, and not to answer it at
// the pane: a pane answer to a dialog that has closed is typed into Claude's
// prompt as a user message (b.t6e).
func fallenBackRefusal(s DecideStore, params DecideParams, pr PermissionRow) error {
	id := params.ClaudeInstanceID
	sp, err := s.GetSpawn(id)
	if errors.Is(err, store.ErrSpawnNotFound) {
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, id)
	}
	if err != nil {
		return err
	}
	rows, err := s.PermissionRequestsForSpawn(id)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rows, func(r PermissionRow) bool { return r.RequestID == pr.RequestID })
	if i < 0 {
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, id)
	}
	if pr = rows[i]; pr.Decision != "" {
		return alreadyDecidedError(id, pr)
	}
	if why := fallenBackUnshown(sp, rows, pr); why != "" {
		return fmt.Errorf("%w: %s request %s is not shown to be awaiting an answer: its record is still open and its relay hook can no longer answer it, but %s, so its permission dialog cannot be shown to be on screen; do not answer it at the pane",
			store.ErrNoOpenPermissionRequest, id, params.RequestToken, why)
	}
	return fmt.Errorf("%w: %s request %s fell back — too late; its record is still open and its relay hook can no longer answer it; answer at the pane with send-keys",
		ErrRelayFallenBack, id, params.RequestToken)
}

// fallenBackUnshown reports why the Spawn sp is not shown to be sitting on
// pr, an open request that has fallen back, alone; "" when it is. rows is
// every permission request of sp, pr included, read after sp. Nothing stored
// shows Claude Code's screen, so sp counts as sitting on pr alone only while
// nothing stored has happened on sp since pr's relay hook moved it to
// check_permission and recorded pr (b.t6e):
//
//   - sp is still in check_permission. Any other state was written after pr
//     by a hook (Stop, AskUserQuestion, SessionStart, SessionEnd) or by a
//     later launch, once Claude Code had moved on from pr's dialog.
//   - No request of sp was recorded after pr (a higher request id). A later
//     request is a later PermissionRequest hook, perhaps the agent's next one
//     after pr's dialog closed, and sp's check_permission may be that
//     request's, not pr's. A subagent's request recorded while pr's dialog
//     is up looks the same, so it is refused alike. The cap eviction of
//     decided requests never removes sp's newest request while sp has an
//     open one (store's UpsertOpenPermissionRequestResult), so a later
//     request stays visible here for as long as pr is open.
//   - No other request of sp is open. Claude Code shows the oldest pending
//     dialog first, so an older open request's dialog, if it is still up, is
//     the one a pane answer would reach.
//
// What it cannot show:
//
//   - A stale record (b.omt). While pr is open the store holds the agent's
//     moves to working (hook_writes.go's working hold), so a dialog answered
//     at the pane, or closed by a timeout deny the relay hook returned
//     without recording it, changes nothing it reads until a later hook
//     moves sp out of check_permission or records a later request.
//   - A stale record also fails the third check for every later fallen-back
//     request of sp, even one whose dialog really is on screen, for as long
//     as the stale record is open. That is a stall, not a mistyped answer:
//     each is refused with ErrNoOpenPermissionRequest, which advises no pane
//     answer.
//   - A later request in the gap before it is recorded. Its
//     PermissionRequest hook moves sp to check_permission (internal/hook's
//     applyOrdinaryHook) before its relay records the request (runRelay's
//     UpsertOpenPermissionRequestResult). If pr is stale, sp had left
//     check_permission since (to waiting, say), and Decide's reads fall
//     between the later request's state write and its record, all three
//     checks pass and Decide returns ErrRelayFallenBack. The send-keys guard
//     has released on pr's account and holds on the later request's only
//     once it is recorded, so keys the caller sends before then can answer
//     the later request's dialog. No stored field tells this apart: nothing
//     records when sp entered check_permission or which request moved it
//     there.
func fallenBackUnshown(sp Spawn, rows []PermissionRow, pr PermissionRow) string {
	if sp.State != store.StateCheckPermission {
		return fmt.Sprintf("the spawn is in state %s", sp.State)
	}
	for _, r := range rows {
		if r.RequestID > pr.RequestID {
			return fmt.Sprintf("the spawn recorded request %s after it", r.RequestToken)
		}
	}
	for _, r := range rows {
		if r.RequestID != pr.RequestID && r.Decision == "" {
			return fmt.Sprintf("the spawn's request %s is open too", r.RequestToken)
		}
	}
	return ""
}

// alreadyDecidedError is decide's ErrAlreadyDecided for the decided row pr.
// It names the recorded decision_reason when there is one, so a request the
// relay hook denied when its window ran out (DecisionReasonTimeout) reads
// differently from a caller's verdict.
func alreadyDecidedError(instanceID string, pr PermissionRow) error {
	if pr.DecisionReason == "" {
		return fmt.Errorf("%w: %s already decided as %q", store.ErrAlreadyDecided, instanceID, pr.Decision)
	}
	return fmt.Errorf("%w: %s already decided as %q (decision_reason %q)",
		store.ErrAlreadyDecided, instanceID, pr.Decision, pr.DecisionReason)
}

// decideOutcome maps a Decide error to its canonical outcome string for
// ad.decide.called. nil → "ok"; known sentinels → their err_name;
// unrecognized errors → "ErrInternal". The function uses errors.Is so
// %w-wrapped errors are matched correctly.
//
// errnames.Classify cannot be used here: pkg/api/errnames imports pkg/api for
// its sentinel variables, so importing errnames from pkg/api would create an
// unresolvable import cycle. This local switch is the canonical pattern for
// decide-path outcome strings.
func decideOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, ErrMissingRequestToken):
		return "ErrMissingRequestToken"
	case errors.Is(err, ErrInvalidDecision):
		return "ErrInvalidDecision"
	case errors.Is(err, ErrRelayModeOff):
		return "ErrRelayModeOff"
	case errors.Is(err, ErrRelayFallenBack):
		return "ErrRelayFallenBack"
	case errors.Is(err, store.ErrSpawnNotFound):
		return "ErrSpawnNotFound"
	case errors.Is(err, store.ErrAlreadyDecided):
		return "ErrAlreadyDecided"
	case errors.Is(err, store.ErrNoOpenPermissionRequest):
		return "ErrNoOpenPermissionRequest"
	case errors.Is(err, store.ErrAmbiguousRequest):
		return "ErrAmbiguousRequest"
	default:
		return "ErrInternal"
	}
}

// Decide writes the orchestrator's allow or deny verdict on the open
// PermissionRequest for the identified Spawn. Only callable on Spawns with
// relay_mode=on. The underlying UPDATE is race-free (first-call-wins guarded
// by `decision IS NULL`); a concurrent call by a second orchestrator returns
// [ErrAlreadyDecided].
//
// CLI: agent-director decide
//
// Errors:
//   - [ErrMissingRequestToken]: RequestToken is empty.
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrRelayModeOff]: the Spawn's relay_mode is not "on".
//   - [ErrNoOpenPermissionRequest]: no undecided permission request exists,
//     or the request's record is still open past its relay window but the
//     Spawn is not shown to be sitting on it alone: it has left
//     check_permission, recorded a later request, or has another request
//     open. Its permission dialog may have closed; do not answer it at the
//     pane.
//   - [ErrAlreadyDecided]: a verdict is already recorded: a concurrent
//     caller's, or the relay hook's fail-closed deny (decision_reason
//     "timeout"), such as when the request's window ran out, which the hook
//     normally returns to Claude Code as the request's answer.
//   - [ErrRelayFallenBack]: the request's record is still open past its relay
//     window, its relay hook can no longer answer it, and the Spawn is still
//     shown sitting on it alone; answer at the pane instead, with SendKeys.
//     The SendKeys relay guard has already released on this request's
//     account; see [ErrRelayFallenBack].
//   - [ErrInvalidDecision]: Decision is not "allow" or "deny".
//
// A call refused between 1 s before the request's window ends and 2 s after
// it first waits until 2 s after it (at most 3 s), because the relay hook
// may still deny the request in that time; see [ErrRelayFallenBack]. Up to
// then the request may also hold the SendKeys relay guard, whose refusal,
// [ErrSendKeysWhileRelayed], names a request holding it and advises this
// call, so the caller never times that span itself.
//
// Nondeterminism: none.
func (c *Client) Decide(params DecideParams) (DecideResult, error) {
	if err := c.checkClosed(); err != nil {
		return DecideResult{}, err
	}

	// Collect caller identity once at entry — must come from inside AD, not
	// from caller-asserted params (SR-A-2.4).
	callerID := callerIdentity()

	var callErr error
	defer func() {
		// Emit exactly one ad.decide.called per invocation, on every return
		// path including ErrAlreadyDecided no-ops (SR-A-2.4). Fail-open per
		// SR-A-3.2: a trail-emit failure is silently discarded.
		_ = trail.Emit(context.Background(), "ad.decide.called", map[string]any{
			"claude_instance_id":        params.ClaudeInstanceID,
			"request_token":             params.RequestToken,
			"submitted_decision":        params.Decision,
			"submitted_decision_reason": params.Reason,
			"outcome":                   decideOutcome(callErr),
			"caller_process":            callerID.process,
			"caller_pid":                callerID.pid,
			"caller_hostname":           callerID.hostname,
			"caller_user":               callerID.user,
			"source":                    "ad_decide",
		})
	}()

	var result DecideResult
	// Effective relay window is resolved here via Epic 1's accessor (the single
	// source for the non-positive→default fallback); the clock is the
	// Client's own (c.now), so the deliverability verdict is deterministic at
	// the API boundary and tests can step it. The wait for a fallen-back
	// request's relay hook sleeps through the Client's own sleep.
	effectiveWindow := time.Duration(c.cfg.Relay.EffectiveTimeoutSeconds()) * time.Second
	result, callErr = decide(c.st, effectiveWindow, c.now(), c.sleep, params)
	return result, callErr
}
