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

// ErrRelayFallenBack is returned by Decide when the target permission request
// has fallen back (b.146 rule 5): its relay hook has not acked a verdict, no
// pane answer is recorded on it through agent-director, and the hook is gone:
// provably (its process checked in the caller's pid namespace: no such
// process, another start time, or a zombie), or, when its process cannot be
// checked, past its settle instant (its kill instant plus a 2 s reserve).
// Decide checks the hook process before it reads the request's record, so a
// hook that acked and exited in between is never reported fallen back. It
// returns this at once, not after the request's relay window (decision 1 A).
// Nothing is recorded as the request's decision: the caller's verdict is
// stored as the request's attempted_decision and attempted_at, shown and
// never acted on. No answer from the relay reached Claude Code, so only an
// answer at the pane can close the request; agent-director cannot know
// whether something outside it (a person at tmux) already answered it.
//
// A request recorded before schema v7 (no settle instant on record, its relay
// hook's identity and ack not recorded) falls back by time, as before the
// upgrade: it is still open past its relay window plus RelayKillSafetyMargin
// plus created_at's 1 s storage resolution (relayHookSettledAt). Decide
// refuses such a request from window - RelayKillSafetyMargin on
// (RelayRequestUndeliverable), but its relay hook may still deny it at its
// poll deadline after that (b.pzy), so a refusal before the settle instant
// first waits until it (at most twice the margin plus the resolution, 3 s)
// and reads the request again: one decided meanwhile is ErrAlreadyDecided.
// One still open is ErrRelayFallenBack only while the Spawn is still shown
// sitting on it alone (fallenBackUnshown, b.t6e), otherwise
// ErrNoOpenPermissionRequest, which advises no pane answer. By the time
// Decide returns ErrRelayFallenBack for such a request, the send-keys relay
// guard has already released on this request's account. If send-keys still
// refuses with ErrSendKeysWhileRelayed, another of the Spawn's requests holds
// it and the refusal names that request; answer that one with decide, as
// that error says. For a request recorded from schema v7 on, the send-keys
// relay guard still holds on its account until its relay window ends (b.146
// step 2b rewrites the guard).
//
// Callers detect it with errors.Is.
var ErrRelayFallenBack = errors.New("ErrRelayFallenBack")

// DecideStore is the narrow store surface Decide needs: the Spawn and request
// reads, the guarded verdict write, the write of a refused verdict, and the
// read of every permission request of the Spawn that decides whether a
// fallen-back request recorded before schema v7 is still the one the Spawn is
// shown sitting on (b.t6e). Each read has a bounded variant (…Within), which
// decide uses under a max_wait_ms bound and for its wait for the ack.
// *store.Store satisfies it.
type DecideStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	GetSpawnWithin(instanceID string, maxWait time.Duration) (Spawn, error)
	GetPermissionRequest(instanceID, requestToken string) (PermissionRow, error)
	GetPermissionRequestWithin(instanceID, requestToken string, maxWait time.Duration) (PermissionRow, error)
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	PermissionRequestsForSpawnWithin(instanceID string, maxWait time.Duration) ([]PermissionRow, error)
	DecideRelayRequest(instanceID, requestToken, decision, reason, writerProcess string, legacyCutoff time.Time, maxWait time.Duration) (bool, error)
	RecordRefusedDecision(instanceID, requestToken, decision string, at, hookGoneAt time.Time, maxWait time.Duration) error
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
	// MaxWaitMs is the call's bound in milliseconds (b.146 decision 9 B),
	// counted from the call's start: the reads before the verdict, and the
	// verdict write, wait for the store (its one connection, when another
	// call of this process holds it, and its locks) at most what is left of
	// it (none when nothing is left: the write then goes ahead only if the
	// lock is free at once), and when one is not taken in that time Decide
	// returns ErrStoreBusy having recorded nothing. The wait of at most 1 s
	// for the relay hook's ack ends at the bound too, with not_confirmed. A
	// caller with a deadline passes at most its deadline minus 1 s. nil: no
	// bound, the store's waits apply as before. Negative: ErrInvalidFlags.
	MaxWaitMs *int64 `json:"max_wait_ms,omitempty"`
}

// DecideResult is decide's result once its verdict is recorded (b.146
// decision 1 A, rule 16): the request's delivery facts, read at the end of
// decide's wait of at most 1 s for the relay hook's ack. Delivery is
// delivered (the hook acked the verdict before writing it to Claude Code) or
// not_confirmed (no ack yet: ask get-permission, at the latest at
// confirm_by). A request whose hook has fallen back is refused with
// ErrRelayFallenBack instead.
type DecideResult struct {
	RequestDelivery
}

// decideAckWait is the longest decide waits, after its verdict commits, for
// the relay hook's ack, by its own clock, its reads' lock waits included
// (b.146 rule 16). A live hook reads a verdict within one poll sleep
// (poll_base_ms plus jitter, 200 ms at most by default).
const decideAckWait = 1 * time.Second

// decideAckPoll is decide's sleep between two reads of the request while it
// waits for the ack: the relay polling loop's floor (internal/hook's
// pollFloor).
const decideAckPoll = 50 * time.Millisecond

// Decide writes the orchestrator's allow/deny verdict on the open
// permission_requests row identified by the (claude_instance_id,
// request_token) pair (SRD §6.2; b.146 rules 6, 15, 16 and decisions 1 A and
// 9 B), with RelayView v (its clock, start-time reader, pid namespace and
// relay window) and time.Sleep for its waits. Behavior:
//
//   - Empty request_token → ErrMissingRequestToken; a decision other than
//     allow or deny → ErrInvalidDecision; a negative max_wait_ms →
//     ErrInvalidFlags.
//   - Unknown id → ErrSpawnNotFound from the store. relay_mode != "on" →
//     ErrRelayModeOff.
//   - Spawn ended or missing → nothing is recorded: a request of a finished
//     Spawn is closed (b.146 rule 12). A decided request is
//     ErrAlreadyDecided (recordedRefusal); an open or absent one
//     ErrNoOpenPermissionRequest, which advises no pane answer
//     (finishedRowRefusal).
//   - The request is read by the check-before-read rule (b.146 rule 5): read,
//     judge its relay hook process, read again. Absent →
//     ErrNoOpenPermissionRequest; decided (first call wins) →
//     ErrAlreadyDecided; closed by find-missing's mark before its relay hook
//     acked the verdict recorded on it (b.146 rule 12, the Spawn resumed
//     since or not) → ErrNoOpenPermissionRequest, which advises no pane
//     answer, never ErrRelayFallenBack (recordedRefusal). A request the mark
//     denied because it was undecided stays ErrAlreadyDecided with
//     decision_reason find_missing, as before.
//   - Under a max_wait_ms bound every read before the verdict commits waits
//     for the store at most what is left of the bound; a read cut by it is
//     ErrStoreBusy, with nothing recorded.
//   - Fallen back (no ack, no pane answer recorded, hook gone or past its
//     settle instant) → ErrRelayFallenBack at once. The verdict is stored as
//     attempted_decision / attempted_at, with hook_gone_at when it is not yet
//     set (decide always writes it, b.146 rule 8), in a write whose lock wait
//     is bounded like the verdict's; a write that is cut or fails records
//     nothing and the refusal stands.
//   - Otherwise one guarded statement records the verdict (store
//     DecideRelayRequest: undecided, not acked, no pane answer, Spawn live).
//     Its wait for the write lock is bounded by what is left of max_wait_ms;
//     a lock not taken in time is ErrStoreBusy, with nothing recorded.
//     Without max_wait_ms it waits the store's busy timeout, and a lock not
//     taken in that time is an unnamed store error (ErrInternal), as before.
//   - Once it commits, Decide waits at most 1 s (and never past the bound)
//     for the hook's ack, reading the request every 50 ms with each read's
//     lock wait cut at what is left, and returns delivered or not_confirmed
//     with the request's delivery facts. It never returns ErrStoreBusy after
//     the commit.
//   - A request recorded before schema v7 is decided as before the upgrade,
//     by time (decidePreV7): see ErrRelayFallenBack. No ack is awaited, since
//     a relay hook from before v7 never writes one.
//   - params.Reason is currently discarded; DecisionReasonOperator is always
//     written for deny decisions regardless of its value.
func Decide(s DecideStore, v RelayView, params DecideParams) (DecideResult, error) {
	return decide(s, v, time.Sleep, params)
}

// decide is Decide with its sleep injected: Decide passes time.Sleep and
// Client.Decide the Client's own sleep, so a test runs decide's waits on its
// own clock.
func decide(s DecideStore, v RelayView, sleep func(time.Duration), params DecideParams) (DecideResult, error) {
	if params.RequestToken == "" {
		return DecideResult{}, fmt.Errorf("%w: request_token is required", ErrMissingRequestToken)
	}
	if params.Decision != "allow" && params.Decision != "deny" {
		return DecideResult{}, fmt.Errorf("%w: %q", ErrInvalidDecision, params.Decision)
	}
	if params.MaxWaitMs != nil && *params.MaxWaitMs < 0 {
		return DecideResult{}, fmt.Errorf("%w: decide: max_wait_ms %d must not be negative", ErrInvalidFlags, *params.MaxWaitMs)
	}
	c := newDecideCall(s, v, sleep, params)

	row, err := c.getSpawn()
	if err != nil {
		return DecideResult{}, err
	}
	if row.RelayMode != "on" {
		return DecideResult{}, fmt.Errorf("%w: spawn %s relay_mode=%q",
			ErrRelayModeOff, params.ClaudeInstanceID, row.RelayMode)
	}
	if finishedState(row.State) {
		return DecideResult{}, c.finishedRowRefusal(row.State)
	}

	pr, verdict, err := readOneJudged(c.readRequest, c.j)
	if errors.Is(err, sql.ErrNoRows) {
		return DecideResult{}, fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID)
	}
	if err != nil {
		return DecideResult{}, err
	}
	if pr.Decision != "" || pr.Closed() {
		return DecideResult{}, recordedRefusal(params, pr)
	}
	if pr.PreV7() {
		return c.decidePreV7(pr)
	}
	if c.j.delivery(pr, verdict, c.j.now()).Delivery == DeliveryFallenBack {
		return DecideResult{}, c.refuseFallenBack(pr)
	}
	written, err := s.DecideRelayRequest(params.ClaudeInstanceID, params.RequestToken, params.Decision,
		c.dbReason(), store.WriterProcessDecide, time.Time{}, c.lockWait())
	if err != nil {
		return DecideResult{}, c.writeError(err)
	}
	if !written {
		return DecideResult{}, c.notWritten()
	}
	return c.awaitAck(pr), nil
}

// decideCall is one decide call's state: its store, judge, sleep, params and
// bound.
type decideCall struct {
	s     DecideStore
	j     relayJudge
	sleep func(time.Duration)
	p     DecideParams
	// bounded reports a max_wait_ms bound; deadline is the call's start plus
	// it.
	bounded  bool
	deadline time.Time
}

// newDecideCall starts the call's clock: the bound is counted from here.
func newDecideCall(s DecideStore, v RelayView, sleep func(time.Duration), p DecideParams) *decideCall {
	c := &decideCall{s: s, j: newRelayJudge(v), sleep: sleep, p: p}
	if p.MaxWaitMs != nil {
		c.bounded = true
		c.deadline = c.j.now().Add(time.Duration(*p.MaxWaitMs) * time.Millisecond)
	}
	return c
}

// readRequest reads the call's request; under a bound, waiting for the store
// at most what is left of it (lockWait), a read cut by it being an error
// wrapping ErrStoreBusy.
func (c *decideCall) readRequest() (PermissionRow, error) {
	if c.bounded {
		return c.s.GetPermissionRequestWithin(c.p.ClaudeInstanceID, c.p.RequestToken, c.lockWait())
	}
	return c.s.GetPermissionRequest(c.p.ClaudeInstanceID, c.p.RequestToken)
}

// getSpawn reads the call's Spawn, bounded as readRequest.
func (c *decideCall) getSpawn() (Spawn, error) {
	if c.bounded {
		return c.s.GetSpawnWithin(c.p.ClaudeInstanceID, c.lockWait())
	}
	return c.s.GetSpawn(c.p.ClaudeInstanceID)
}

// readAllRequests reads every request of the call's Spawn, bounded as
// readRequest.
func (c *decideCall) readAllRequests() ([]PermissionRow, error) {
	if c.bounded {
		return c.s.PermissionRequestsForSpawnWithin(c.p.ClaudeInstanceID, c.lockWait())
	}
	return c.s.PermissionRequestsForSpawn(c.p.ClaudeInstanceID)
}

// dbReason is the decision_reason recorded with the verdict: the canonical
// operator reason for deny, none for allow.
func (c *decideCall) dbReason() string {
	if c.p.Decision == "deny" {
		return store.DecisionReasonOperator
	}
	return ""
}

// lockWait is a store call's wait, for the store's connection and its write
// lock: what is left of the bound, never below zero, or the store's own
// waits without one.
func (c *decideCall) lockWait() time.Duration {
	if !c.bounded {
		return store.DefaultLockWait
	}
	return max(c.deadline.Sub(c.j.now()), 0)
}

// writeError names a failed verdict write. A lock not taken in time is
// ErrStoreBusy under a bound (decision 9 B). Without a bound it is reported
// as an unnamed store error, ErrInternal, as an unbounded write's lock
// timeout was before decision 9 B, so a caller that passes no bound sees no
// new error name (b.146 problem 5 (a)).
func (c *decideCall) writeError(err error) error {
	if errors.Is(err, store.ErrStoreBusy) && !c.bounded {
		return fmt.Errorf("decide: the store's write lock was not free within its busy timeout: %v", err)
	}
	return err
}

// refuseFallenBack is decide's refusal of pr, a request recorded from schema
// v7 on that has fallen back: ErrRelayFallenBack. Before returning it stores
// the verdict as attempted (RecordRefusedDecision), with hook_gone_at when
// pr has none yet (a writing verb always writes it, b.146 rule 8), in a write
// whose lock wait is bounded like the verdict's. The refusal stands whatever
// that write does: a write cut by the bound or failing records nothing
// (store-side, one transaction) and changes no answer.
func (c *decideCall) refuseFallenBack(pr PermissionRow) error {
	now := c.j.now()
	var goneAt time.Time
	if pr.HookGoneAt.IsZero() {
		goneAt = now
	}
	_ = c.s.RecordRefusedDecision(c.p.ClaudeInstanceID, c.p.RequestToken, c.p.Decision, now, goneAt, c.lockWait())
	return fmt.Errorf("%w: %s request %s fell back: its relay hook is gone and acked no verdict, and no pane answer is recorded on it through agent-director; nothing was recorded as its decision (the verdict is kept as attempted_decision); only an answer at the pane can close it",
		ErrRelayFallenBack, c.p.ClaudeInstanceID, c.p.RequestToken)
}

// notWritten names a verdict write that matched no request, from one read of
// the request (and of the Spawn): absent → ErrNoOpenPermissionRequest;
// decided or closed meanwhile → recordedRefusal; its Spawn finished meanwhile
// → the closed-request refusal (b.146 rule 12); a pane answer recorded
// meanwhile (b.146 step 2b) → ErrNoOpenPermissionRequest. Nothing was
// recorded.
func (c *decideCall) notWritten() error {
	id := c.p.ClaudeInstanceID
	pr, err := c.readRequest()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, id)
	case err != nil:
		return err
	case pr.Decision != "" || pr.Closed():
		return recordedRefusal(c.p, pr)
	}
	sp, err := c.getSpawn()
	switch {
	case errors.Is(err, store.ErrSpawnNotFound):
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, id)
	case err != nil:
		return err
	case finishedState(sp.State):
		return closedRequestError(c.p, sp.State)
	case pr.PaneAnswer != store.PaneAnswerNone:
		return fmt.Errorf("%w: %s request %s has a pane answer recorded (pane_answer %q); nothing was recorded",
			store.ErrNoOpenPermissionRequest, id, c.p.RequestToken, pr.PaneAnswer)
	}
	// Unreachable in practice — the request exists, is undecided and its
	// Spawn is live, yet the write did not apply. Surface the conservative
	// ErrNoOpenPermissionRequest.
	return fmt.Errorf("%w: %s (verdict write no-op against an open request)",
		store.ErrNoOpenPermissionRequest, id)
}

// awaitAck is decide's wait for the relay hook's ack once its verdict has
// committed (b.146 rule 16): at most decideAckWait, ending at the bound when
// that comes first, measured on the call's own clock. It reads the request
// every decideAckPoll, each read's lock wait cut at what is left, judging the
// hook before each read (the check-before-read rule) from the identity pr
// carries, and stops at the first read that finds the ack. It returns the
// last read's delivery facts, delivered when acked and not_confirmed
// otherwise: once its verdict has committed decide never reports fallen_back
// or a store error (a failed read keeps the facts of the previous one).
func (c *decideCall) awaitAck(pr PermissionRow) DecideResult {
	end := c.j.now().Add(decideAckWait)
	if c.bounded && c.deadline.Before(end) {
		end = c.deadline
	}
	var verdict hookVerdict
	for {
		verdict = c.j.hook(pr)
		left := max(end.Sub(c.j.now()), 0)
		if read, err := c.s.GetPermissionRequestWithin(c.p.ClaudeInstanceID, c.p.RequestToken, left); err == nil {
			pr = read
			if !pr.DeliveredAt.IsZero() {
				break
			}
		}
		now := c.j.now()
		if !now.Before(end) {
			break
		}
		c.sleep(min(decideAckPoll, end.Sub(now)))
	}
	d := c.j.delivery(pr, verdict, c.j.now())
	if d.Delivery != DeliveryDelivered {
		d.Delivery = DeliveryNotConfirmed
	}
	return DecideResult{RequestDelivery: d}
}

// decidePreV7 decides pr, a request recorded before schema v7, as before the
// upgrade (b.146 rule 5's compatibility clause): by time. Its verdict write
// is guarded by the deliverability cutoff (RelayDeliverabilityCutoff, from
// the single authority, SR-4.4) besides the other guards, with its lock wait
// bounded as any verdict write's. A written verdict returns at once, with
// the request's delivery facts (not_confirmed until its relay hook is
// presumed settled): a relay hook from before v7 writes no ack to wait for.
// A refused one is named by refusePreV7.
func (c *decideCall) decidePreV7(pr PermissionRow) (DecideResult, error) {
	cutoff := RelayDeliverabilityCutoff(c.j.now(), c.j.view.Window)
	written, err := c.s.DecideRelayRequest(c.p.ClaudeInstanceID, c.p.RequestToken, c.p.Decision,
		c.dbReason(), store.WriterProcessDecide, cutoff, c.lockWait())
	if err != nil {
		return DecideResult{}, c.writeError(err)
	}
	if !written {
		return DecideResult{}, c.refusePreV7()
	}
	if read, err := c.s.GetPermissionRequestWithin(c.p.ClaudeInstanceID, c.p.RequestToken, c.lockWait()); err == nil {
		pr = read
	}
	return DecideResult{RequestDelivery: c.j.delivery(pr, hookCantTell, c.j.now())}, nil
}

// refusePreV7 names a refused verdict write on a request recorded before
// schema v7, as decide named it before the upgrade: the request is absent,
// already decided, or open but undeliverable. It disambiguates via a
// follow-up read. Precedence (PM-pinned): ErrAlreadyDecided wins for decided
// rows; ErrRelayFallenBack applies ONLY to open rows.
//
// An open, undeliverable row whose relay hook has not yet settled
// (relayHookSettledAt) may still be denied by its live relay hook at the
// hook's poll deadline, which closes the permission dialog; "answer at the
// pane" would then type into Claude's prompt (b.pzy). So for such a row
// refusePreV7 sleeps until that instant (at most twice RelayKillSafetyMargin
// plus createdAtResolution, 3 s) and repeats the read as of it: whatever
// decided the row meanwhile wins as ErrAlreadyDecided, and a row still open
// has fallen back, its hook presumed dead. Under a max_wait_ms bound that
// ends before that instant it does not sleep: it returns ErrStoreBusy, having
// recorded nothing. fallenBackRefusal names a fallen-back row:
// ErrRelayFallenBack only while the Spawn is still shown sitting on it alone,
// otherwise ErrNoOpenPermissionRequest (b.t6e). An ErrRelayFallenBack stores
// the verdict as attempted, as for any request.
func (c *decideCall) refusePreV7() error {
	window := c.j.view.Window
	now := c.j.now()
	pr, err := c.readRequest()
	if err == nil && pr.Decision == "" && RelayRequestUndeliverable(pr.CreatedAt, window, now) {
		if settled := relayHookSettledAt(pr.CreatedAt, window); now.Before(settled) {
			if c.bounded && c.deadline.Before(settled) {
				return fmt.Errorf("%w: %s request %s was recorded before this release and its relay hook may still answer it until %s, after the call's max_wait_ms bound; nothing was recorded",
					store.ErrStoreBusy, c.p.ClaudeInstanceID, c.p.RequestToken, settled.UTC().Format(time.RFC3339))
			}
			c.sleep(settled.Sub(now))
			now = settled
			pr, err = c.readRequest()
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, c.p.ClaudeInstanceID)
	}
	if err != nil {
		return err
	}
	if pr.Decision != "" || pr.Closed() {
		return recordedRefusal(c.p, pr)
	}
	// Open row that the guarded write refused: skipped by the deliverability
	// predicate, or because its Spawn ended or went missing after decide read
	// it (b.146 rule 12). Re-confirm via the shared definition of a fallen-back
	// request (no second inline time comparison) and name the refusal;
	// fallenBackRefusal reads the Spawn again, and a finished one is not shown
	// to be sitting on the request.
	if relayRequestFallenBack(pr, window, now) {
		err := c.fallenBackRefusal(pr)
		if errors.Is(err, ErrRelayFallenBack) {
			_ = c.s.RecordRefusedDecision(c.p.ClaudeInstanceID, c.p.RequestToken, c.p.Decision, c.j.now(), time.Time{}, c.lockWait())
		}
		return err
	}
	return c.notWritten()
}

// finishedState reports whether state is a finished row's, ended or missing:
// the row's agent is gone, or judged gone, so a request of it is closed
// (b.146 rule 12).
func finishedState(state string) bool {
	return state == store.StateEnded || state == store.StateMissing
}

// finishedRowRefusal is decide's refusal for a request of a Spawn that is
// ended or missing (b.146 rule 12): such a request is closed, so decide
// records nothing. It reads the request once and names the refusal with an
// existing error name: no such request is ErrNoOpenPermissionRequest, as for
// a live Spawn; a decided one is ErrAlreadyDecided (find-missing's mark
// denies a missing Spawn's undecided requests, decision_reason find_missing),
// except one the mark closed before its relay hook acked its verdict, which
// is ErrNoOpenPermissionRequest (recordedRefusal); an open one is
// ErrNoOpenPermissionRequest (closedRequestError). Neither advises a pane
// answer.
func (c *decideCall) finishedRowRefusal(state string) error {
	pr, err := c.readRequest()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, c.p.ClaudeInstanceID)
	case err != nil:
		return err
	case pr.Decision != "" || pr.Closed():
		return recordedRefusal(c.p, pr)
	}
	return closedRequestError(c.p, state)
}

// closedRequestError is decide's ErrNoOpenPermissionRequest for an undecided
// request whose Spawn is in state, ended or missing (b.146 rule 12): the
// request is closed with its agent, nothing was recorded, and it is not to be
// answered at the pane.
func closedRequestError(params DecideParams, state string) error {
	return fmt.Errorf("%w: %s request %s is closed: the spawn is %s, so nothing was recorded; do not answer it at the pane",
		store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID, params.RequestToken, state)
}

// recordedRefusal is decide's refusal of pr, a request with a verdict
// recorded on it or closed by find-missing's mark (b.146 rule 12):
//
//   - Closed by the mark before its relay hook acked it, with a verdict the
//     mark did not write (one recorded before the mark: decision_reason is not
//     find_missing): ErrNoOpenPermissionRequest. The request is closed with
//     the agent the mark judged gone, whose Spawn may have been resumed since,
//     so neither "already decided" nor "fallen back" would tell the caller
//     what happened to it; nothing was recorded and it is not to be answered
//     at the pane. This also covers a closed request with no verdict, which
//     the mark never leaves, so decide never reports a closed request fallen
//     back.
//   - Otherwise ErrAlreadyDecided (first call wins), a request the mark
//     denied because it was undecided (decision_reason find_missing) and a
//     closed request a live relay hook acked since included.
func recordedRefusal(params DecideParams, pr PermissionRow) error {
	if pr.Closed() && pr.DeliveredAt.IsZero() && pr.DecisionReason != store.DecisionReasonFindMissing {
		return fmt.Errorf("%w: %s request %s is closed: find-missing marked the spawn missing before the request's relay hook delivered a verdict, so nothing was recorded; do not answer it at the pane",
			store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID, params.RequestToken)
	}
	return alreadyDecidedError(params.ClaudeInstanceID, pr)
}

// fallenBackRefusal names decide's refusal of pr, a request recorded before
// schema v7 that refusePreV7 found fallen back by time
// (relayRequestFallenBack). It reads the Spawn, then every one of its
// requests, the last read of pr included: pr removed meanwhile (with its
// Spawn) is ErrNoOpenPermissionRequest and pr decided meanwhile is
// recordedRefusal's. A pr still open is ErrRelayFallenBack ("answer at the
// pane") only while the Spawn is still shown sitting on it alone
// (fallenBackUnshown); otherwise it is ErrNoOpenPermissionRequest, whose
// message says why, that the request's permission dialog cannot be shown to
// be on screen, and not to answer it at the pane: a pane answer to a dialog
// that has closed is typed into Claude's prompt as a user message (b.t6e).
// Under a max_wait_ms bound its reads are bounded as decide's others.
func (c *decideCall) fallenBackRefusal(pr PermissionRow) error {
	params := c.p
	id := params.ClaudeInstanceID
	sp, err := c.getSpawn()
	if errors.Is(err, store.ErrSpawnNotFound) {
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, id)
	}
	if err != nil {
		return err
	}
	rows, err := c.readAllRequests()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rows, func(r PermissionRow) bool { return r.RequestID == pr.RequestID })
	if i < 0 {
		return fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, id)
	}
	if pr = rows[i]; pr.Decision != "" || pr.Closed() {
		return recordedRefusal(params, pr)
	}
	if why := fallenBackUnshown(sp, rows, pr); why != "" {
		return fmt.Errorf("%w: %s request %s is not shown to be awaiting an answer: its record is still open and its relay hook can no longer answer it, but %s, so its permission dialog cannot be shown to be on screen; do not answer it at the pane",
			store.ErrNoOpenPermissionRequest, id, params.RequestToken, why)
	}
	// By now the send-keys relay guard has released on this request's
	// account (refusePreV7 waited until relayHookSettledAt), so the advice
	// names send-keys, as before the upgrade.
	return fmt.Errorf("%w: %s request %s fell back — too late; its record is still open and its relay hook can no longer answer it; answer at the pane with send-keys",
		ErrRelayFallenBack, id, params.RequestToken)
}

// fallenBackUnshown reports why the Spawn sp is not shown to be sitting on
// pr, an open request recorded before schema v7 that has fallen back by
// time, alone; "" when it is. rows is every permission request of sp, pr
// included, read after sp. Nothing stored shows Claude Code's screen, and a
// relay hook from before v7 records neither its identity nor an ack, so sp
// counts as sitting on pr alone only while nothing stored has happened on sp
// since pr's relay hook moved it to check_permission and recorded pr (b.t6e):
//
//   - sp is still in check_permission. Any other state was written after pr
//     by a hook (Stop, AskUserQuestion, SessionStart, SessionEnd) or by a
//     later launch, once Claude Code had moved on from pr's dialog.
//   - No request of sp was recorded after pr (a higher request id). A later
//     request is a later PermissionRequest hook, perhaps the agent's next one
//     after pr's dialog closed, and sp's check_permission may be that
//     request's, not pr's. The cap eviction never removes sp's newest request
//     while sp has one that still awaits an answer (store's
//     evictClosedRequests), so a later request stays visible here for as
//     long as pr is open.
//   - No other request of sp is open. Claude Code shows the oldest pending
//     dialog first, so an older open request's dialog, if it is still up, is
//     the one a pane answer would reach.
//
// What it cannot show: a stale record (b.omt). While pr is open the store
// holds the agent's moves to working, so a dialog answered at the pane, or
// closed by a timeout deny the relay hook returned without recording it,
// changes nothing it reads until a later hook moves sp out of
// check_permission or records a later request.
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
	case errors.Is(err, ErrInvalidFlags):
		return "ErrInvalidFlags"
	case errors.Is(err, ErrRelayModeOff):
		return "ErrRelayModeOff"
	case errors.Is(err, ErrRelayFallenBack):
		return "ErrRelayFallenBack"
	case errors.Is(err, ErrStoreBusy):
		return "ErrStoreBusy"
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
// PermissionRequest for the identified Spawn and reports its delivery
// (b.146 decision 1 A): after its verdict commits it waits at most 1 s for
// the relay hook's ack and returns delivered, or not_confirmed (ask
// GetPermission, at the latest at confirm_by). Only callable on Spawns with
// relay_mode=on. The underlying write is race-free (first-call-wins guarded
// by `decision IS NULL`); a concurrent call by a second orchestrator returns
// [ErrAlreadyDecided]. With params.MaxWaitMs the whole call is bounded:
// see [ErrStoreBusy].
//
// CLI: agent-director decide
//
// Errors:
//   - [ErrMissingRequestToken]: RequestToken is empty.
//   - [ErrInvalidFlags]: MaxWaitMs is negative.
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrRelayModeOff]: the Spawn's relay_mode is not "on".
//   - [ErrNoOpenPermissionRequest]: no such request exists, or the request is
//     closed: its Spawn is ended or missing, or find-missing marked the Spawn
//     missing before the request's relay hook delivered the verdict recorded
//     on it (the Spawn resumed since or not); nothing is recorded. Or a
//     request recorded before this release is still open past its relay
//     window but the Spawn is not shown to be sitting on it alone. Do not
//     answer it at the pane.
//   - [ErrAlreadyDecided]: a verdict is already recorded: a concurrent
//     caller's, or the relay hook's fail-closed deny (decision_reason
//     "timeout") at its deadline, or find-missing's deny when it marked the
//     Spawn missing (decision_reason "find_missing").
//   - [ErrRelayFallenBack]: the request's relay hook is gone (or cannot be
//     checked and is past its settle instant) and acked no verdict, and no
//     pane answer is recorded on it through agent-director: only an answer
//     at the pane can close it. Returned at once; the verdict is kept as the
//     request's attempted_decision.
//   - [ErrStoreBusy]: MaxWaitMs was reached before the verdict was recorded;
//     nothing was recorded, so retry. The wait it cut was for the store's
//     write lock, or for its connection while another call of this process
//     held it.
//   - [ErrInvalidDecision]: Decision is not "allow" or "deny".
//
// Nondeterminism: the delivery facts depend on the clock and on whether the
// relay hook acks within the wait.
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
	// The relay view is the Client's own: its clock (c.now), start-time
	// reader, pid namespace and the configured effective relay window
	// (EffectiveTimeoutSeconds, the single source for the non-positive →
	// default fallback). decide's waits sleep through the Client's own sleep.
	result, callErr = decide(c.st, c.relayView(), c.sleep, params)
	return result, callErr
}
