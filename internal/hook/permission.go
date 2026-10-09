package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/google/uuid"
)

// AGENT_DIRECTOR_RELAY_MODE env-var values per SRD §6.5. The hook
// reads this from the process env (NOT the DB) so a DB-unreachable
// failure still surfaces the right boundary.
const (
	EnvRelayMode = "AGENT_DIRECTOR_RELAY_MODE"
	RelayModeOn  = "on"
	RelayModeOff = "off"
)

// permissionPayload is the leniently-typed shape the hook payload
// takes for PermissionRequest events. tool_name + tool_input are
// preserved verbatim into the row so a future MCP audit can see what
// was being asked; tool_use_id is recorded with them (b.146 rule 2).
type permissionPayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
}

// relayReserve is the time a relay hook keeps between its ack's commit and
// its kill instant, to write its answer and exit (b.146 rule 4): it never
// acks with less left, and a reader's settle instant for its request is its
// kill instant plus the same reserve.
const relayReserve = 2 * time.Second

// relayDenyLead is how long before its last ack instant the relay hook stops
// polling to make its timeout deny: the longest that deny's own wait for the
// store's write lock can be.
const relayDenyLead = 1 * time.Second

// relayClock is one relay hook's timing (b.146 rule 4): its clock and its
// kill instant, its start plus the timeout spawn wrote for it.
type relayClock struct {
	now    func() time.Time
	killAt time.Time
}

// newRelayClock is the relay hook's timing from hc: the kill instant is start
// plus hc.RelayTimeout, or plus cfg's effective relay window (the value spawn
// writes as the hook's timeout) when hc.RelayTimeout is not positive.
func newRelayClock(hc HandleConfig, start time.Time) relayClock {
	timeout := hc.RelayTimeout
	if timeout <= 0 {
		// A loaded config's window is at most config.MaxRelayTimeoutSeconds
		// (b.8q2), so the conversion does not overflow.
		timeout = time.Duration(hc.Cfg.EffectiveTimeoutSeconds()) * time.Second
	}
	return relayClock{now: hc.clock(), killAt: start.Add(timeout)}
}

// ackBy is the last instant a commit leaves the reserve before the kill.
func (c relayClock) ackBy() time.Time { return c.killAt.Add(-relayReserve) }

// waitLeft is how long a write may still wait for the store's write lock and
// commit by ackBy; zero or negative when it may not.
func (c relayClock) waitLeft() time.Duration { return c.ackBy().Sub(c.now()) }

// pollEnd is the poll's deadline: ackBy less the lead the timeout deny may
// wait.
func (c relayClock) pollEnd() time.Time { return c.ackBy().Add(-relayDenyLead) }

// settledAt is the request's settle instant readers fall back to when they
// cannot check the hook: its kill instant plus the reserve.
func (c relayClock) settledAt() time.Time { return c.killAt.Add(relayReserve) }

// mintRequestToken generates a cryptographically-random UUIDv4 string in
// standard 8-4-4-4-12 hex form (RFC 4122 §4.4) using crypto/rand. The token
// is minted once per runRelay invocation and closed over for the full relay
// lifecycle: insert → poll → ack or timeout deny. Distinct concurrent
// invocations of runRelay each receive their own unique token, ensuring that
// per-request isolation is enforced at the DB row level.
//
// On failure (crypto/rand unavailable) the caller must write a fail-closed
// deny envelope and return without touching the store.
func mintRequestToken() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// RelayStore is the surface the relay flow needs (b.146 step 2): the
// polling loop's read, the first write (the request and check_permission in
// one transaction), the ack and the timeout deny, each write with a bounded
// wait for the store's write lock. *store.Store satisfies it. A HookStore
// that does not gets a fail-closed deny for every relayed PermissionRequest,
// with nothing written.
//
// The ack and the timeout deny take check, their precondition, which the
// store runs inside the write's transaction once the write lock is taken and
// before the statement; its error rolls the write back and is returned as it
// is. runRelay passes its parent check (b.146 problem 7).
type RelayStore interface {
	PollStore
	InsertRelayRequest(instanceID string, gate store.HookGate, req store.RelayRequest, cap int, maxWait time.Duration) (store.UpsertOutcome, store.HookApplied, error)
	AckRelayDecision(instanceID, requestToken string, at time.Time, maxWait time.Duration, check func() error) (decision, reason string, acked bool, err error)
	DenyRelayTimeout(instanceID, requestToken string, at time.Time, maxWait time.Duration, check func() error) (denied bool, err error)
}

// emitResume emits one ad.resume.observed event immediately after the
// decision envelope has been flushed to stdout. It is called once per
// runRelay invocation that reaches a stdout write of an acked answer
// (SR-A-2.7).
//
// elapsed_ms_from_row_open is computed from createdAt to now, createdAt
// being the request's created_at from the polling loop's last read (zero,
// and elapsed_ms large, when no read returned it); the emit must not block
// the relay return (SR-A-3.2 fail-open).
//
// verdict must be one of the canonical lowercase values: "allow",
// "deny", "timeout".
func emitResume(ctx context.Context, instanceID, requestToken, verdict string, createdAt time.Time) {
	_ = trail.Emit(ctx, "ad.resume.observed", map[string]any{
		"claude_instance_id":       instanceID,
		"request_token":            requestToken,
		"verdict":                  verdict,
		"elapsed_ms_from_row_open": time.Since(createdAt).Milliseconds(),
		"source":                   "ad_polling",
	})
}

// relayInput is what Handle hands runRelay about the hook it handles: the
// row's id, the hook's gate and parent (captured at its start), and from the
// payload the agent_id, the transcript path with its presence on disk, and
// the raw payload.
type relayInput struct {
	instanceID        string
	gate              store.HookGate
	parent            parentIdentity
	agentID           string
	transcriptPath    string
	transcriptPresent bool
	raw               json.RawMessage
}

// relayRun is one relay hook's state once its request is recorded.
type relayRun struct {
	ctx        context.Context
	stdout     io.Writer
	st         RelayStore
	hc         HandleConfig
	clock      relayClock
	logger     *log.Logger
	instanceID string
	token      string
	parent     parentIdentity
}

// runRelay is the relay-mode branch invoked from Handle for a
// PermissionRequest event with AGENT_DIRECTOR_RELAY_MODE=on (SRD §6.2, §6.4;
// b.146 step 2). It owns the whole relayed request:
//
//  1. Its first write (b.146 rule 1): the request and the move to
//     check_permission in ONE gated transaction (store InsertRelayRequest),
//     carrying the hook's identity (hc.Self: pid, start time, pid
//     namespace), the payload's tool_use_id and agent_id, and its settle
//     instant (rules 2, 14). Its wait for the store's write lock is cut at
//     the hook's own deadline less the reserve (problem 1), and is zero when
//     that has passed. When the lock is not taken in time, or the write
//     fails, the hook writes today's fail-closed deny (the tool is denied, no
//     dialog appears): nothing was recorded. When the gate does not hold (the row is
//     no longer this process's, SR-22.9) nothing is recorded, nothing is
//     written to stdout (Claude Code asks as it would with no relay) and
//     onIgnored is called once with the store's reason (decision A8).
//  2. The poll (Poll), until its deadline.
//  3. A decision read is acked, then written (deliver): the hook commits
//     delivered_at in one statement that returns the decision it acked
//     (rule 3), its lock wait cut so a commit leaves the reserve before the
//     kill (rule 4), and only then writes that decision to stdout. Inside the
//     ack's transaction, once the write lock is taken and before the
//     statement, it checks that its parent is still the Claude Code that
//     started it (problem 7), so a Claude Code that died while the hook
//     waited for the lock gets no ack.
//  4. At the poll's deadline, the timeout deny (timeoutDeny): one guarded
//     statement records deny, reason timeout and delivered_at together, only
//     while the request is undecided (rule 3), with the same parent check
//     inside its transaction and under the same cut; a verdict that landed
//     first is acked and written instead.
//
// It never writes an answer that is not acked (rule 3): a failed or cut ack,
// read or timeout deny, a parent that changed, or no time left ends the hook
// with no answer (an empty stdout), so Claude Code's own dialog appears and
// the request falls back. Claude Code reads a hook's JSON on any exit code,
// so the ack must come first.
//
// fields is the ad.hook.fired trail map owned by Handle; runRelay sets
// request_token and upsert_outcome in it. The single emit happens via
// Handle's defer (SR-A-2.1: one emit per verb invocation).
func runRelay(ctx context.Context, stdout io.Writer, hs HookStore, hc HandleConfig, clock relayClock, logger *log.Logger, in relayInput, fields map[string]any, onIgnored func(reason string)) {
	instanceID := in.instanceID
	failClosed := func(why string, args ...any) {
		logf(logger, "relay: fail-closed deny (instance=%s): "+why, append([]any{instanceID}, args...)...)
		_, _ = fmt.Fprintln(stdout, EncodeDecision(EventNamePermissionRequest, "deny", ""))
	}
	st, ok := hs.(RelayStore)
	if !ok {
		failClosed("the store has no relay writes")
		return
	}

	var pp permissionPayload
	if err := json.Unmarshal(in.raw, &pp); err != nil {
		// Continue with what was read, but log so a post-mortem can see why
		// an audit row is blank.
		logf(logger, "relay: unmarshal payload (instance=%s): %v", instanceID, err)
	}
	toolInput := string(pp.ToolInput)
	if toolInput == "" {
		toolInput = "null"
	}

	// Mint a per-request UUIDv4 token via crypto/rand. Fail closed on any
	// error — a missing token means we can't key the DB row correctly, so
	// we deny immediately without touching the store.
	requestToken, err := mintRequestToken()
	if err != nil {
		failClosed("mint token: %v", err)
		return
	}
	// Populate request_token into the trail fields immediately after minting
	// so early-exit paths (insert failure) still carry it.
	fields["request_token"] = requestToken

	cap := hc.Cfg.PermissionRequestCap
	if cap < 0 {
		// A negative cap silently falls back to the default (1000) rather
		// than surfacing a config error at runtime. Cap == 0 is intentional
		// (operator opt-in to unbounded growth) and must NOT be collapsed to
		// 1000 here.
		cap = 1000
	}

	// The first write's wait for the store's write lock is cut at the hook's
	// own deadline less the reserve (b.146 problem 1). With nothing left it
	// still goes ahead if the lock is free at once: the gate then decides as
	// for any hook (a hook of another process stays silent), and a request
	// recorded this late is never acked, so it falls back to Claude Code's
	// dialog when the hook exits.
	wait := max(clock.waitLeft(), 0)
	req := store.RelayRequest{
		RequestToken:      requestToken,
		ToolName:          pp.ToolName,
		ToolInput:         toolInput,
		ToolUseID:         pp.ToolUseID,
		AgentID:           in.agentID,
		Hook:              hc.self(),
		SettledAt:         clock.settledAt(),
		TranscriptPath:    in.transcriptPath,
		TranscriptPresent: in.transcriptPresent,
	}
	outcome, applied, err := st.InsertRelayRequest(instanceID, in.gate, req, cap, wait)
	fields["upsert_outcome"] = string(outcome)
	if err != nil {
		failClosed("token=%s: first write: %v", requestToken, err)
		return
	}
	if !applied.Applied {
		// The gate did not hold (SR-22.9): no request, no decision on
		// stdout. A row gone since gives no reason and no ad.hook.ignored
		// (decision A2).
		logf(logger, "relay: request not recorded (instance=%s, token=%s): hook not applied (%s)", instanceID, requestToken, applied.Reason)
		if applied.Reason != "" && onIgnored != nil {
			onIgnored(applied.Reason)
		}
		return
	}

	// CASE B: relay is DB-poll-based — no outbound HTTP shim exists today.
	// Emit ad.relay_attempt.completed with degenerate fields so the SR-A-2.3
	// trail shape is established and the §11 replay harness has a real event
	// to assert against. Future outbound-network work (a real caller shim)
	// should replace target_endpoint / outcome with real values via the
	// `agent-director trail-emit relay-attempt` sub-verb (for external
	// processes); in-process trail.Emit is used here because the hook IS an
	// AD process and the sub-verb wrapper is unnecessary overhead.
	// Fail-open per SRD §3.2: emit error must not block the relay attempt.
	_ = trail.Emit(ctx, "ad.relay_attempt.completed", map[string]any{
		"claude_instance_id": instanceID,
		"request_token":      requestToken,
		"target_endpoint":    "db_poll",
		"outcome":            "db_relay_active",
		"bytes_sent":         0,
		"bytes_received":     0,
		"source":             "relay_hook",
	})

	r := &relayRun{ctx: ctx, stdout: stdout, st: st, hc: hc, clock: clock, logger: logger,
		instanceID: instanceID, token: requestToken, parent: in.parent}
	pollClock := hc.Clock
	if pollClock == nil {
		pollClock = DefaultPollClock()
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	res := Poll(ctx, st, pollClock, clock.now, clock.pollEnd(), hc.Cfg, instanceID, requestToken, rng)
	switch {
	case res.Decision != "":
		r.deliver(res.CreatedAt)
	case res.TimedOut:
		r.timeoutDeny(res.CreatedAt)
	default:
		// ctx cancelled, the request deleted, or the read-retry budget
		// spent: no answer (rule 3), so Claude Code's dialog appears.
		r.silent("poll ended without a decision: %s", res.Why)
	}
}

// silent ends the relay hook with no answer on stdout (b.146 rule 3), so
// Claude Code shows its own dialog and the request falls back. It logs why.
func (r *relayRun) silent(why string, args ...any) {
	logf(r.logger, "relay: exit with no answer (instance=%s, token=%s): "+why,
		append([]any{r.instanceID, r.token}, args...)...)
}

// errParentChanged is the parent check's failure (b.146 problem 7): the
// hook's parent is no longer the Claude Code that started it.
var errParentChanged = errors.New("its parent is no longer the Claude Code that started it")

// parentIsStarter reports whether the hook's parent is still the Claude Code
// that started it (b.146 problem 7): the hook's parent pid is still the one
// captured at its start, and that pid runs with the start time captured
// then. A parent that exits reparents the hook, so its parent pid changes.
// It reads the parent pid and one start time, through hc.ParentPID and
// hc.ParentProc; either unwired, or a start time not read at the start, is
// false.
func (r *relayRun) parentIsStarter() bool {
	if r.hc.ParentPID == nil || r.hc.ParentProc == nil || r.parent.pid <= 0 || r.parent.start == "" {
		return false
	}
	if r.hc.ParentPID() != r.parent.pid {
		return false
	}
	start, alive, known := r.hc.ParentProc.StartTime(r.parent.pid)
	return known && alive && start == r.parent.start
}

// parentCheck is the precondition of the hook's ack and timeout deny
// (b.146 problem 7), which the store runs inside the write's transaction once
// the write lock is taken and before the statement: errParentChanged when the
// parent is no longer the hook's starting Claude Code (parentIsStarter), so
// the write rolls back and the hook exits with no answer.
func (r *relayRun) parentCheck() error {
	if !r.parentIsStarter() {
		return errParentChanged
	}
	return nil
}

// ackWait is the wait an ack or timeout deny may take for the store's write
// lock: what is left before ackBy. It reports false, and the caller exits
// with no answer, when no time is left before the reserve.
func (r *relayRun) ackWait() (time.Duration, bool) {
	wait := r.clock.waitLeft()
	if wait <= 0 {
		r.silent("less than the %s reserve left before its kill", relayReserve)
		return 0, false
	}
	return wait, true
}

// deliver acks the decision the poll read and writes it (b.146 rules 3, 4;
// problem 7): one statement commits delivered_at and returns the decision
// it acked, which alone is written to stdout; the parent check runs inside
// its transaction (parentCheck). A failed or cut ack, a parent that changed,
// or an ack that matched nothing, ends with no answer.
func (r *relayRun) deliver(createdAt time.Time) {
	wait, ok := r.ackWait()
	if !ok {
		return
	}
	decision, reason, acked, err := r.st.AckRelayDecision(r.instanceID, r.token, r.clock.now(), wait, r.parentCheck)
	if err != nil {
		r.silent("ack: %v", err)
		return
	}
	if !acked {
		r.silent("ack: no decided, unacked request to ack")
		return
	}
	logf(r.logger, "relay: %s for %s token=%s (%s)", decision, r.instanceID, r.token, reason)
	_, _ = fmt.Fprintln(r.stdout, EncodeDecision(EventNamePermissionRequest, decision, reason))
	emitResume(r.ctx, r.instanceID, r.token, decision, createdAt)
}

// timeoutDeny is the relay hook's answer at its poll deadline (b.146
// rule 3): one guarded statement records deny, reason timeout and its own
// ack, only while the request is undecided, with the parent check inside its
// transaction (parentCheck); then the deny is written. A verdict that landed
// first is acked and written instead (deliver). A failed or cut deny, or a
// parent that changed, ends with no answer.
func (r *relayRun) timeoutDeny(createdAt time.Time) {
	wait, ok := r.ackWait()
	if !ok {
		return
	}
	denied, err := r.st.DenyRelayTimeout(r.instanceID, r.token, r.clock.now(), wait, r.parentCheck)
	if err != nil {
		r.silent("timeout deny: %v", err)
		return
	}
	if !denied {
		r.deliver(createdAt)
		return
	}
	logf(r.logger, "relay: timeout deny for %s token=%s", r.instanceID, r.token)
	_, _ = fmt.Fprintln(r.stdout, EncodeDecision(EventNamePermissionRequest, "deny", ""))
	emitResume(r.ctx, r.instanceID, r.token, "timeout", createdAt)
}

// Compile-time assertion that *store.Store satisfies RelayStore. Keeps the
// production wiring honest if the store or interface grows.
var _ RelayStore = (*store.Store)(nil)
