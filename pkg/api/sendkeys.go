package api

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// SendKeysStore is the narrow store surface SendKeys needs (SRD Appendix
// F.3): the row read, the relay guard's permission-request read, the
// adoption write of SR-3.6 (a lost create reply's server and pane identity,
// applied only if the row still has the snapshot SendKeys examined) and this
// store's id, which every label the lookup accepts ends with (SR-3.4; WD
// 2026-09-29 STORE). The adoption is the only write send-keys makes.
// *store.Store satisfies it.
//
// PermissionRequestsForSpawn returns ALL of the Spawn's permission_requests
// rows (decided and undecided) so the relay-guard release can evaluate
// deliverability across every row, decided or not (SR-4.2); evaluateRelayGuard
// describes the one exception, a decided row once another request of the
// Spawn has fallen back.
type SendKeysStore interface {
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// PermissionRequestsForSpawn reads every permission request of the row.
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	// AdoptIdentityIfUnchanged records a found launch identity when the
	// row is still as examined (SR-3.6).
	AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// SendKeysTmux is the narrow tmux surface SendKeys needs (Appendix F.3): the
// lookup, the pane listing and the keys sent to one pane by its pane id.
// TmuxClient, *tmux.Client and tmuxfix.Recorder satisfy it. Every method
// takes the row's socket (SR-3.3) and reports a failure as *TmuxCallError.
type SendKeysTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
	// SendKeysPane types text literally into the pane paneID on socket and
	// then, only if that succeeded and pressEnter is set, sends Enter; the
	// failed call is named on the *TmuxCallError (text send or Enter send).
	SendKeysPane(socket, paneID, text string, pressEnter bool) error
}

// The production types satisfy SendKeys' interfaces.
var (
	_ SendKeysStore = (*store.Store)(nil)
	_ SendKeysTmux  = TmuxClient(nil)
)

// SendKeysParams is the typed parameter shape for the send-keys verb.
// JSON tags use snake_case so MCP clients can decode into the struct
// directly via the dispatcher's decodeParams helper.
type SendKeysParams struct {
	// ClaudeInstanceID identifies the Spawn whose pane will receive the text.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// Text is the string to deliver to the agent's own pane. CR bytes (0x0D) are
	// stripped before delivery; LF bytes (0x0A) are preserved as input newlines.
	// A single Enter is always appended to submit the composed buffer. Empty
	// text types nothing, so the call sends Enter only: it submits what is
	// already typed, such as a text a failed send-keys left unsubmitted.
	Text string `json:"text"`
	// AllowPending also allows a pending row: a launch (spawn, reuse or
	// resume) whose agent has not reported in yet. Keys are delivered only to
	// a session started by the row's current launch. ended and missing rows
	// are still rejected (SR-18.14).
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
//     Spawn's permission-request rows holds the guard: its relay hook is not
//     yet presumed settled (relayHookSettledAt) and it is open, or decided
//     while no other request of the Spawn has fallen back (or there are zero
//     rows). The send was refused with ErrSendKeysWhileRelayed.
//   - guardReleased — relay_mode=on + check_permission and no row holds the
//     guard: every open row's relay hook is presumed settled, and every
//     decided row's too unless another request of the Spawn has fallen back.
//     The guard released and keys were delivered. This is the audited
//     recovery of a fallen-back relay.
//   - guardError — relay_mode=on + check_permission but the store read that
//     the guard needs (PermissionRequestsForSpawn) failed, so deliverability
//     could not be evaluated. Distinct from guardNotApplicable so a store
//     failure on a relayed Spawn is not misrecorded as an ordinary send. The
//     send fails with the underlying store error.
const (
	guardNotApplicable = "not-applicable"
	guardHeld          = "held"
	guardReleased      = "released"
	guardError         = "error"
)

// sendKeysGuard is the internal result of evaluating the relay guard: the
// guard-evaluation outcome string (one of guardNotApplicable/guardHeld/
// guardReleased/guardError), whether the send should be refused and, when the
// guard is held, the request token the refusal names (holding; "" when the
// Spawn has zero request rows, so no request is recorded to name) and whether
// that request is decided (holdingDecided: its verdict is recorded, so the
// refusal advises a later send-keys rather than decide, b.ceq).
type sendKeysGuard struct {
	eval           string
	refuse         bool
	holding        string
	holdingDecided bool
}

// SendKeys is the verb-handler entry point for `agent-director send-keys`
// (SRD SR-7.1, SR-7.2, SR-7.3, SR-3.6, SR-3.7, SR-13.2, SR-22.7, SR-22.8).
// It types text into the agent's own pane of the row's current launch, by
// pane id, then submits it with Enter:
//
//   - `\r` (CR, 0x0D) bytes in Text are STRIPPED before invoking tmux. CR
//     submits the buffer at the position it appears, which would split
//     one logical message into multiple submissions. The fix is per the
//     research note: delete CR bytes from the input.
//   - `\n` (LF, 0x0A) bytes are PRESERVED — Claude's input handler treats
//     LF as "insert newline in input box", not as a submit. Multi-line
//     prompts compose as one message.
//   - The text is typed literally, then a single Enter is sent to the same
//     pane as a separate call, only if the text call succeeded. That is the
//     single submit.
//   - Empty text types nothing, so the call is an Enter-only send, on a
//     pending row too with AllowPending: it submits a line already typed,
//     such as a text a failed send-keys left unsubmitted, and on an empty
//     Claude Code input it submits nothing.
//
// State precondition: the row must be in a live interactive state (waiting,
// working, ask_user or check_permission). A finished row (ended or missing)
// is refused with ErrSpawnNotInteractive, with or without AllowPending, and
// with no tmux call; so is a pending row (a launch whose agent has not
// reported in yet) without AllowPending. With AllowPending, a pending row
// whose launch start or launch token is absent or unreadable is refused with
// ErrSpawnNotInteractive before any tmux call: without them no session can be
// shown to belong to the current launch (SR-7.1, SR-22.8). Otherwise the keys
// are sent only when the lookup finds the current launch's session by its
// label and launch token and the agent's pane is found; a leftover found for
// a pending row is refused with ErrSpawnNotInteractive (Leftover, below). A
// row that turns live between the read and the send still gets the keys in
// the same pane; agent-director does not guarantee that the prompt the
// caller saw is still showing (SR-22.7).
//
// Relay-mode guard (time-bounded): when relay_mode=on AND
// state=check_permission, the permission relay normally owns the answer, so
// SendKeys refuses with ErrSendKeysWhileRelayed to keep a pane-side keystroke
// from racing the relay's decide() write. The refusal is NOT unconditional:
// Claude Code kills the relay hook at its per-hook timeout, after which the
// poller can no longer deliver a decision and the guard is pure denial of
// service. The guard therefore consults the shared guard-release signal
// (RelayRequestGuardReleasable, SR-4.4) across ALL of the Spawn's
// permission_requests rows — each row's window measured from its own
// created_at, decided or not (SR-4.2: a row decided in-window still has a
// live poller about to deliver it), with the one exception below. It refuses
// while ANY row holds the guard and RELEASES only once none does; a row holds
// until its relay hook is presumed settled, its window plus the safety margin
// plus created_at's 1 s resolution after its created_at (relayHookSettledAt,
// the instant Decide's wait ends — the deliberate fail-late mirror of
// Decide's fail-early refusal at window - margin; same authority, asymmetric
// margin, both in deliverability.go). The one exception is a decided row once
// another request of the Spawn has fallen back (the request Decide refuses with
// ErrRelayFallenBack): that request's open record keeps the Spawn in
// check_permission and only a pane answer closes its dialog, so the decided
// row no longer holds (b.ceq; see evaluateRelayGuard for the trade-off this
// accepts). A relay-on check_permission Spawn with
// zero rows keeps refusing: with no row there is no signal and no authority
// to release, and the state is a real mid-insert transient. The refusal's message names the request
// holding the guard (an open one in preference to a decided one, then the
// oldest; none with zero rows) and states no release time (b.ah6). For an
// open request it advises answering it with decide: Decide absorbs the span
// between its own refusal and the guard's release, and returns
// ErrRelayFallenBack only once the guard has released on that request's
// account, and on account of every decided request of the Spawn. For a
// decided request, named only when no open request holds, it says the
// verdict is recorded and its relay hook may still be delivering it, and
// advises retrying send-keys later (b.ceq): decide on it would return
// ErrAlreadyDecided. With zero rows it advises decide once get lists the
// request.
//
// After the state and relay guards, a row whose recorded name is unusable
// (SR-3.2) is ErrInternal with no tmux call; a pending row with no launch
// start or token has already been refused by the state guard.
//
// Then the row's socket (SR-3.3; a resolution refusal is
// ErrTmuxNotAvailable) and one lookup by the row's current label:
//
//   - Ours: one pane listing, then the text and Enter to the agent's pane
//     (the entry with the row's recorded pane id and pid, wherever it now
//     is). For a row that records no server identity or no pane (a lost
//     create reply), what the lookup and the pane whose @ad_pane names the
//     row's launch token show is used for this call and written once,
//     guarded on the row as read; the write's outcome never changes the
//     result (SR-3.6). No agent's pane: ErrTmuxSessionConflict ("the agent's
//     pane was not found"), nothing sent.
//   - Leftover: on a pending row ErrSpawnNotInteractive ("not this launch's
//     session"), otherwise ErrTmuxSessionConflict; nothing sent.
//   - Gone (another agent-director store's sessions included):
//     ErrTmuxSendKeys ("the row's session is not there"), nothing sent.
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
//     Where the text may be in the pane, an ErrTmuxUnresponsive names the
//     next step in place of "retry later", since a literal retry would type
//     the text again after the unsubmitted copy (b.9o4): after a timed-out
//     text call "read-pane; if the text is typed, send-keys with empty text,
//     otherwise the same send-keys", and after a failed or timed-out Enter
//     "send-keys with empty text submits it". A text call that failed other
//     than by timing out typed nothing, and its refusals still end "retry
//     later".
//
// Every tmux call uses the row's socket and every action targets a pane id.
// The calls are at most one lookup, one pane listing, the text call, the
// Enter call and, only after an action failure other than a timeout, one
// follow-up lookup (SR-13.2). SendKeys never writes
// the row's state; the adoption is its only store write.
//
// Trail (SR-7.4, SR-14): SendKeys writes at most one ad.provenance.disagree
// record per distinct reason per call (source ad_send_keys), fail-open, and
// none in the normal case; Client.SendKeys writes the same records through
// the same path, plus its one ad.send_keys.called. Each record's action is
// what the call typed: keys_sent (the text and Enter both went through),
// text_sent (the text call timed out, or the text went through and the Enter
// call failed or timed out) or nothing_sent (no keys call, or the text call
// failed other than by timing out).
//
// pc is the start-time reader that judges the lookup's server (SR-3.3) and
// an adopted pane (SR-3.6). effectiveWindow is the resolved relay window
// (obtained by the caller via Epic 1's accessor and consumed only through the
// shared deliverability function); now is the injected clock so the release
// verdict is deterministic and testable.
func SendKeys(s SendKeysStore, t SendKeysTmux, pc ProcChecker, effectiveWindow time.Duration, now time.Time, params SendKeysParams) (SendKeysResult, error) {
	_, res, err := sendKeys(s, t, pc, effectiveWindow, now, params)
	return res, err
}

// sendKeysFacts is what one send-keys call read, found and did, kept by
// sendKeys so Client.SendKeys and the trail (ad.send_keys.called,
// ad.provenance.disagree) read them without re-deriving them (SR-7.4, SR-14).
type sendKeysFacts struct {
	// RowState is the stored state of the row read; "" when no row was read.
	RowState string
	// Guard is the relay guard's evaluation (guardNotApplicable when the
	// guard did not run or did not apply).
	Guard string
	// keysFacts is what the tmux phase found and did: the socket, the first
	// lookup, a failed listing, the adoption, the session concerned, the
	// send and the follow-up (zero when the phase did not run).
	keysFacts
	// Caller is the invoking process's identity, collected once per call on
	// the path Client.SendKeys and SendKeys share (callerIdentity).
	Caller caller
}

// sendKeysRun is one SendKeys call: the keys verbs' tmux phase (keysRun)
// with send-keys' pending-row Leftover refusal, and the facts the call keeps.
type sendKeysRun struct {
	keysRun
	s     SendKeysStore
	facts sendKeysFacts
}

// sendKeys is the inner implementation shared by the pure SendKeys entry point
// and Client.SendKeys. It collects the caller identity once, runs the call,
// writes the call's ad.provenance.disagree records (keysRun.emitDisagree)
// and returns the call's facts alongside the result/error so the Client
// wrapper can record them on the ad.send_keys.called trail event without
// re-deriving them.
func sendKeys(s SendKeysStore, t SendKeysTmux, pc ProcChecker, effectiveWindow time.Duration, now time.Time, params SendKeysParams) (sendKeysFacts, SendKeysResult, error) {
	r := &sendKeysRun{s: s, facts: sendKeysFacts{Guard: guardNotApplicable, Caller: callerIdentity()}}
	err := r.run(t, pc, effectiveWindow, now, params)
	r.emitDisagree("send-keys", params.ClaudeInstanceID, r.facts.Caller)
	return r.facts, SendKeysResult{}, err
}

// run is the send-keys flow; the returned error is SendKeys'.
func (r *sendKeysRun) run(t SendKeysTmux, pc ProcChecker, effectiveWindow time.Duration, now time.Time, params SendKeysParams) error {
	row, err := r.s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return err
	}
	r.facts.RowState = row.State

	if err := sendKeysStateGuard(row, params); err != nil {
		return err
	}

	guard, err := evaluateRelayGuard(r.s, effectiveWindow, now, row, params.ClaudeInstanceID)
	r.facts.Guard = guard.eval
	if err != nil {
		return err
	}
	if guard.refuse {
		return relayGuardRefusal(params.ClaudeInstanceID, guard.holding, guard.holdingDecided)
	}

	if err := unusableNameError(row.TmuxSessionName); err != nil {
		return fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}

	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	r.keysRun = newKeysRun(t, pc, row, r.s.StoreID(), socket, r.s)
	r.next = sendKeysNext
	if row.State == store.StatePending {
		r.leftover = func(leftovers []tmux.Session) error {
			return pendingLeftoverError(row.ClaudeInstanceID, leftovers)
		}
	}
	err = r.deliver(strings.ReplaceAll(params.Text, "\r", ""))
	r.facts.keysFacts = r.found
	return err
}

// sendKeysStateGuard is send-keys' state guard (SR-7.1, SR-22.8): a live
// interactive row passes; a pending row passes only with AllowPending and
// only when its launch start and launch token are recorded (the store's
// decoded values: 0 launch start, or no well-formed token, is absent),
// otherwise pendingNoLaunchError; every other state, finished rows included,
// is ErrSpawnNotInteractive naming the state. It makes no tmux call.
func sendKeysStateGuard(row Spawn, params SendKeysParams) error {
	if params.AllowPending && row.State == store.StatePending {
		if row.LaunchStartedAtMillis == 0 || row.Identity.Token == "" {
			return pendingNoLaunchError(row.ClaudeInstanceID)
		}
		return nil
	}
	if !isInteractiveState(row.State) {
		return fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotInteractive, params.ClaudeInstanceID, row.State)
	}
	return nil
}

// evaluateRelayGuard decides whether the time-bounded relay guard refuses the
// send. It returns guardNotApplicable when the guard does not apply
// (relay_mode != on or state != check_permission — the ordinary send path).
// Otherwise it consults the guard-release signal across every one of the
// Spawn's permission_requests rows and returns guardHeld (refuse) while any row
// holds the guard (and in the zero-rows state), or guardReleased (deliver)
// once none does. If the store read fails it returns guardError with the
// underlying error (the send fails). A held guard carries the token of the
// holding row its refusal names (namedBefore picks it when several hold), or
// none with zero rows, and whether that row is decided, which selects the
// refusal's wording (relayGuardRefusal).
//
// A row holds until its relay hook is presumed settled (relayHookSettledAt:
// its window plus RelayKillSafetyMargin plus created_at's 1 s resolution
// after its created_at), open or decided: an open row's verdict may still be
// recorded by decide and delivered, or its live poller may still record and
// return its timeout deny, and a decided row's live poller may still be about
// to deliver its verdict. The guard releases LATE — at elapsed >= window +
// margin + resolution, when Decide's wait ends too — so it does not free
// while a live poller could still emit a decision on such a row, but for the
// residual race deliverability.go describes (a hook whose delivery of its
// verdict or timeout deny outlasts that slack and whose kill comes late).
// That is the deliberate mirror of Decide's fail-early refusal; both
// boundaries and the shared margin live in deliverability.go (SR-4.4). An
// open row thus holds until it has fallen back.
//
// One exception (b.ceq): a decided row stops holding once another request of
// the Spawn has fallen back (relayRequestFallenBack: still open after its
// relay hook settled, which Decide reports as ErrRelayFallenBack). That
// request's open record holds the Spawn in check_permission (the store holds
// the agent's move to working while any request is open), so without the
// exception the decided row would hold until its own relay hook is presumed
// settled, delivered or not, and a send-keys retried later, as its refusal
// advises, would be refused alike for that long.
//
// The exception rests on an assumption, accepted as its trade-off: Claude
// Code shows the oldest pending permission dialog first. The fallen-back
// request was recorded before every row still in its window, so under that
// assumption its dialog is the one on screen, only a pane answer closes it,
// and that answer is what the release lets through. The exception gives up
// the span between a decide and Claude Code acting on the decided request's
// hook output: up to one poll sleep of its live poller (poll_base_ms plus
// jitter up to poll_jitter_ms; internal/hook's Poll) before it reads the
// verdict, plus the hook writing that output and exiting. The assumption
// fails when a fallen-back row's dialog is no longer on screen (its record
// left open after the dialog closed, b.t6e): keys sent in that span can then
// land in the decided request's still-pending dialog. Open rows in their
// windows keep holding, so a pane answer never overtakes a verdict decide can
// still record.
//
// All time arithmetic lives in deliverability.go (RelayRequestGuardReleasable,
// relayRequestFallenBack); this function performs no independent
// elapsed-vs-timeout computation.
func evaluateRelayGuard(s SendKeysStore, effectiveWindow time.Duration, now time.Time, row Spawn, instanceID string) (sendKeysGuard, error) {
	if !(row.RelayMode == "on" && row.State == store.StateCheckPermission) {
		return sendKeysGuard{eval: guardNotApplicable, refuse: false}, nil
	}

	rows, err := s.PermissionRequestsForSpawn(instanceID)
	if err != nil {
		// Store failure on a relayed Spawn: record guardError (distinct from the
		// ordinary-send guardNotApplicable) and fail the send with the error.
		return sendKeysGuard{eval: guardError, refuse: false}, err
	}

	// Zero rows: no signal, no authority to release — keep refusing (PM-pinned).
	if len(rows) == 0 {
		return sendKeysGuard{eval: guardHeld, refuse: true}, nil
	}

	// Refuse while ANY row holds; release only when none does. Each row's
	// window is measured from its own created_at by the shared guard-release
	// authority, decided or not, except that a decided row no longer holds
	// once another request of the Spawn has fallen back (b.ceq).
	fallenBack := slices.ContainsFunc(rows, func(pr PermissionRow) bool {
		return relayRequestFallenBack(pr, effectiveWindow, now)
	})
	var named *PermissionRow
	for i := range rows {
		pr := &rows[i]
		if RelayRequestGuardReleasable(pr.CreatedAt, effectiveWindow, now) {
			continue
		}
		if fallenBack && pr.Decision != "" {
			continue
		}
		if named == nil || namedBefore(*pr, *named) {
			named = pr
		}
	}
	if named == nil {
		return sendKeysGuard{eval: guardReleased, refuse: false}, nil
	}
	return sendKeysGuard{eval: guardHeld, refuse: true, holding: named.RequestToken, holdingDecided: named.Decision != ""}, nil
}

// namedBefore reports whether holding row a, rather than b, is the request
// the ErrSendKeysWhileRelayed refusal names: an open (undecided) row before a
// decided one, as the open one is what decide can still answer, then the
// older by created_at, then the lower request id, so the choice does not
// depend on the order the store returned the rows in.
func namedBefore(a, b PermissionRow) bool {
	if aOpen, bOpen := a.Decision == "", b.Decision == ""; aOpen != bOpen {
		return aOpen
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.RequestID < b.RequestID
}

// relayGuardRefusal is the ErrSendKeysWhileRelayed refusal for a held relay
// guard on instanceID. Every refusal of that name is built here. It states no
// release time or margin (b.ah6), and has three forms:
//
//   - An open holding request (decided false): it names the request's token
//     and advises answering it with decide.
//   - A decided holding request (decided true): it names the request's token,
//     says its verdict is recorded and its relay hook may still be delivering
//     it, and advises retrying send-keys later (b.ceq); decide on it would
//     return ErrAlreadyDecided. Such a request is named only when no open
//     request holds the guard (namedBefore) and none of the Spawn's requests
//     has fallen back (evaluateRelayGuard), so the refusal stands until the
//     Spawn leaves check_permission (normally once the verdict is delivered),
//     another request of the Spawn falls back, or the named request's relay
//     hook is presumed settled (relayHookSettledAt), whichever is first.
//   - No token (zero request rows): it says the request is not yet recorded
//     and to answer it with decide once get lists it.
func relayGuardRefusal(instanceID, holding string, decided bool) error {
	switch {
	case holding == "":
		return fmt.Errorf(
			"%w: spawn %s is awaiting a relayed permission decision whose request is not yet recorded; answer it with decide once get lists it",
			ErrSendKeysWhileRelayed, instanceID)
	case decided:
		return fmt.Errorf(
			"%w: spawn %s: the relayed permission verdict on request %s is recorded and its relay hook may still be delivering it; retry send-keys later",
			ErrSendKeysWhileRelayed, instanceID, holding)
	}
	return fmt.Errorf(
		"%w: spawn %s is awaiting a relayed permission decision on request %s; answer it with decide",
		ErrSendKeysWhileRelayed, instanceID, holding)
}

// isInteractiveState returns true iff the supplied state value belongs to
// the set of live conversational states send-keys drives without
// AllowPending. pending is excluded: it means a launch (spawn, reuse or
// resume) is in progress until its agent reports in, and send-keys reaches
// such a launch only with AllowPending (sendKeysStateGuard).
func isInteractiveState(state string) bool {
	switch state {
	case store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission:
		return true
	}
	return false
}

// SendKeys sends text into the agent's own pane: the row's pane, found by the
// row's label on its recorded socket and targeted by pane id. CR bytes
// (0x0D) are stripped before delivery to prevent premature submission; LF
// bytes (0x0A) are preserved as composed newlines in Claude's input box. A
// single Enter is always appended to submit the composed buffer; empty text
// sends that Enter only.
//
// After the state and relay refusals, a row whose recorded tmux session name
// cannot be used (it is empty, contains a control character, or contains a
// character tmux stores differently) gets ErrInternal (an error matching no
// catalogued sentinel) with no tmux call; removing the row is a human's
// decision (see "Operator actions" in the agent-director README).
//
// CLI: agent-director send-keys
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotInteractive]: the row is finished or otherwise not in a
//     live interactive state, or pending without AllowPending, or pending
//     with no launch start or launch token recorded, or pending and the
//     lookup found only a session an earlier launch left behind; nothing was
//     sent.
//   - [ErrSendKeysWhileRelayed]: relay_mode is on, state is check_permission
//     and the relay guard holds: at least one of the Spawn's permission
//     requests holds the relay guard, or the Spawn has zero request rows;
//     nothing was sent. The message names the holding
//     request (an open one in preference to a decided one, then the oldest).
//     For an open request it advises answering it with Decide. For a
//     decided one, named only when no open request holds, it says the
//     verdict is recorded and its relay hook may still be delivering it, and
//     advises retrying SendKeys later: there is nothing left to answer, and
//     Decide on it would return [ErrAlreadyDecided]. With zero rows the
//     request is still being recorded, the message names none and advises
//     Decide once Get lists it. The refusal is time-bounded: the guard
//     releases once every request's relay hook is presumed to have answered
//     or died, letting the operator recover the wedged Spawn through this
//     sanctioned surface. A request Decide has refused with
//     [ErrRelayFallenBack] no longer holds it, and while that request stays
//     open no decided request of the Spawn does either; only an open request
//     that has not fallen back can.
//   - [ErrTmuxSendKeys]: the row's tmux session is not there.
//   - [ErrTmuxSessionConflict]: the agent's pane was not found, a session an
//     earlier launch left behind is there on a live row, or tmux holds
//     conflicting labels; nothing was sent.
//   - [ErrTmuxUnresponsive]: tmux did not answer, or gave a reply that could
//     not be recognised; after a text or Enter call the keys may have been
//     delivered. Where the text may be typed, the description names the
//     next step (read-pane, and send-keys with empty text, which sends Enter
//     only) in place of a retry that would type the text twice.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on.
//
// Nondeterminism: none.
func (c *Client) SendKeys(params SendKeysParams) (SendKeysResult, error) {
	if err := c.checkClosed(); err != nil {
		return SendKeysResult{}, err
	}

	// Effective relay window is resolved here via Epic 1's accessor (the single
	// source for the non-positive→default fallback); the clock is the
	// Client's own (c.now), so the guard-release verdict is deterministic at
	// the API boundary and tests can step it. The start-time reader is the
	// Client's too. The caller identity is collected inside agent-director,
	// never from caller-asserted params (SR-5.2), once per call by sendKeys.
	effectiveWindow := time.Duration(c.cfg.Relay.EffectiveTimeoutSeconds()) * time.Second
	facts, result, err := sendKeys(c.st, c.tmuxClient, c.procChecker, effectiveWindow, c.now(), params)
	// Exactly one ad.send_keys.called per call past the closed check, on
	// every outcome (SR-7.4, SR-5.2), fail-open.
	emitSendKeysCalled(params, facts, err)
	return result, err
}
