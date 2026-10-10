package api

import (
	"errors"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// SendKeysStore is the narrow store surface SendKeys needs (SRD Appendix
// F.3): the row read; the relay guard's read of every permission request of
// the row (b.146 rule 7); the adoption write of SR-3.6 (a lost create reply's
// server and pane identity, applied only if the row still has the snapshot
// SendKeys examined); hook_gone_at, which a writing verb records on a request
// it finds fallen back (b.146 rule 8); a pane answer's three writes (its
// intent, sent, and the release of its intent; b.146 rule 8); and this
// store's id, which every label the lookup accepts ends with (SR-3.4; WD
// 2026-09-29 STORE). *store.Store satisfies it.
type SendKeysStore interface {
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// PermissionRequestsForSpawn reads every permission request of the row.
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	// AdoptIdentityIfUnchanged records a found launch identity when the
	// row is still as examined (SR-3.6).
	AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error)
	// RecordHookGone records hook_gone_at on the requests with requestIDs
	// that have none yet and returns each one's stored value.
	RecordHookGone(at time.Time, maxWait time.Duration, requestIDs ...int64) (map[int64]time.Time, error)
	// RecordPaneIntent runs check inside one write transaction, then records
	// a pane answer's intent on the request, stamped with now read inside
	// that transaction, and returns the intent as recorded
	// (store.RecordPaneIntent).
	RecordPaneIntent(instanceID, requestToken, as string, sender ProcessIdentity, now func() time.Time, maxWait time.Duration, check PaneCheck) (PaneIntent, bool, error)
	// RecordPaneSent records sent on the request still carrying intent
	// (store.RecordPaneSent).
	RecordPaneSent(instanceID, requestToken string, intent PaneIntent, maxWait time.Duration) (bool, error)
	// ReleasePaneIntent ends intent's claim on the request
	// (store.ReleasePaneIntent).
	ReleasePaneIntent(instanceID, requestToken string, intent PaneIntent, maxWait time.Duration) (bool, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// SendKeysTmux is the narrow tmux surface SendKeys needs (Appendix F.3): the
// lookup, the pane listing, the keys sent to one pane by its pane id (a
// text, or one named key) and the capture of that pane, which
// expect_pane_sha256 is compared with. TmuxClient, *tmux.Client and
// tmuxfix.Recorder satisfy it. Every method takes the row's socket (SR-3.3)
// and reports a failure as *TmuxCallError.
type SendKeysTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
	// SendKeysPane types text literally into the pane paneID on socket and
	// then, only if that succeeded and pressEnter is set, sends Enter; the
	// failed call is named on the *TmuxCallError (text send or Enter send).
	SendKeysPane(socket, paneID, text string, pressEnter bool) error
	// SendKeyPane sends one key, by its tmux key name and never typed
	// literally, to the pane paneID on socket.
	SendKeyPane(socket, paneID, key string) error
	// CapturePaneID returns the last nLines lines of the pane paneID on
	// socket; ansi keeps the escape sequences (tmux's -e flag).
	CapturePaneID(socket, paneID string, nLines int, ansi bool) (string, error)
}

// The production types satisfy SendKeys' interfaces.
var (
	_ SendKeysStore = (*store.Store)(nil)
	_ SendKeysTmux  = TmuxClient(nil)
)

// SendKeysParams is the typed parameter shape for the send-keys verb.
// JSON tags use snake_case so MCP clients can decode into the struct
// directly via the dispatcher's decodeParams helper.
//
// A call is plain (no RequestToken) or a pane answer (RequestToken set; b.146
// rule 8, decision 7 A). A pane answer needs As, Key and ExpectPaneSHA256,
// takes no Text, and sends exactly one key with no Enter after it.
type SendKeysParams struct {
	// ClaudeInstanceID identifies the Spawn whose pane will receive the keys.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// Text is the string to deliver to the agent's own pane on a plain call.
	// CR bytes (0x0D) are stripped before delivery; LF bytes (0x0A) are
	// preserved as input newlines. A single Enter is appended to submit the
	// composed buffer unless NoEnter. Empty text types nothing, so the call
	// sends Enter only: it submits what is already typed, such as a text a
	// failed send-keys left unsubmitted. Exclusive with Key; must be empty on
	// a pane answer.
	Text string `json:"text"`
	// AllowPending also allows a pending row: a launch (spawn, reuse or
	// resume) whose agent has not reported in yet. Keys are delivered only to
	// a session started by the row's current launch. ended and missing rows
	// are still rejected (SR-18.14).
	AllowPending bool `json:"allow_pending"`
	// NoEnter types Text with no Enter after it (plain calls; a pane answer
	// never presses Enter).
	NoEnter bool `json:"no_enter"`
	// Key is one key to send instead of a text, never followed by Enter: a
	// named key (Escape, Enter, Up, Down, Tab), sent by name, or a single
	// character, typed literally. Required on a pane answer.
	Key string `json:"key"`
	// ExpectPaneSHA256 is the pane_sha256 read-pane returned for the pane the
	// caller looked at: before typing, send-keys captures the pane with the
	// same NLines (ANSI stripped, as read-pane by default), and refuses with
	// ErrPaneChanged, sending nothing, when its SHA-256 differs. Required on a
	// pane answer, optional on a plain call.
	ExpectPaneSHA256 string `json:"expect_pane_sha256"`
	// NLines is the number of trailing pane lines ExpectPaneSHA256 is over:
	// read-pane's n_lines. 0 falls back to DefaultReadPaneLines (25).
	NLines int `json:"n_lines"`
	// RequestToken makes the call a pane answer to that permission request of
	// the Spawn (b.146 rule 8): accepted only once the request has fallen
	// back, while no relay hook of the Spawn may still answer and no other
	// pane answer on it is still being sent.
	RequestToken string `json:"request_token"`
	// As is a pane answer's claimed verdict, allow or deny, recorded as the
	// request's decision once its key is sent. It is the caller's claim:
	// agent-director never checks it against the key.
	As string `json:"as"`
}

// SendKeysResult is the typed return shape. Empty struct today; reserved
// so future fields (e.g. truncated_count, dropped_cr_count) can be added
// without breaking the wire shape.
type SendKeysResult struct{}

// SendKeysEnv is what SendKeys judges and records the relay with besides its
// store and tmux surface (b.146 rules 7, 8 and 14). The Client builds it from
// its own readers, clock and configuration.
type SendKeysEnv struct {
	// Relay judges the Spawn's relay hooks and a pane answer's sender: its
	// start-time reader (which also judges the lookup's server and an adopted
	// pane, SR-3.3, SR-3.6), its own pid namespace, its clock and the relay
	// window.
	Relay RelayView
	// Self reads this process's identity, which a pane answer records as its
	// sender; nil records none.
	Self func() ProcessIdentity
	// IntentHold is how long a pane answer's intent whose sender cannot be
	// checked counts as in progress (paneIntentHold).
	IntentHold time.Duration
}

// self is e.Self's identity, or the zero identity when it is nil.
func (e SendKeysEnv) self() ProcessIdentity {
	if e.Self == nil {
		return ProcessIdentity{}
	}
	return e.Self()
}

// send-keys guard-evaluation outcomes, carried on the ad.send_keys.called
// trail event so a send on a relayed Spawn is distinguishable from ordinary
// sends and from guard refusals (SR-5.2). The values are:
//
//   - guardNotApplicable — the relay guard did not apply (relay_mode != on);
//     this is the ordinary send path.
//   - guardHeld — relay_mode=on and b.146 rule 7 refused the send
//     (ErrSendKeysWhileRelayed, ErrRelayFallenBack, ErrDialogMaybeOpen,
//     ErrPaneAnswerInProgress, or a pane answer's request not open:
//     ErrAlreadyDecided, ErrNoOpenPermissionRequest).
//   - guardReleased — relay_mode=on and rule 7 let the send through: no
//     relay hook of the Spawn may still answer, and no open request has
//     fallen back and every request is proven gone or the call carries
//     expect_pane_sha256 (plain), or the named request has fallen back (pane
//     answer).
//   - guardError — relay_mode=on but the read the guard needs failed, so it
//     could not be evaluated. The send fails with the underlying store error.
const (
	guardNotApplicable = "not-applicable"
	guardHeld          = "held"
	guardReleased      = "released"
	guardError         = "error"
)

// sendKeysGuard is the internal result of evaluating the relay guard: the
// guard-evaluation outcome string (guardNotApplicable, guardHeld,
// guardReleased or guardError) and, when held, the refusal.
type sendKeysGuard struct {
	eval    string
	refusal error
}

// SendKeys is the verb-handler entry point for `agent-director send-keys`
// (SRD SR-7.1, SR-7.2, SR-7.3, SR-3.6, SR-3.7, SR-13.2, SR-22.7, SR-22.8;
// b.146 rules 7 and 8). It types into the agent's own pane of the row's
// current launch, by pane id.
//
// The params' shape is checked first (planSendKeys): a refusal is
// ErrInvalidFlags with nothing read, sent or written. What a call sends:
//
//   - Plain, text: `\r` (CR, 0x0D) bytes are STRIPPED (CR would submit the
//     buffer mid-message); `\n` (LF, 0x0A) bytes are PRESERVED (Claude's
//     input treats LF as a newline in the input box). The text is typed
//     literally, then a single Enter is sent to the same pane as a separate
//     call, only if the text call succeeded, unless no_enter. Empty text
//     types nothing, so the call is an Enter-only send, on a pending row too
//     with AllowPending: it submits a line already typed, such as a text a
//     failed send-keys left unsubmitted, and on an empty Claude Code input it
//     submits nothing.
//   - Plain, key: that one key alone (a named key by name, or one character
//     typed literally), no Enter.
//   - A pane answer (request_token; b.146 rule 8): exactly one key, never
//     followed by Enter. See below.
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
// the same pane; without expect_pane_sha256 agent-director does not guarantee
// that the prompt the caller saw is still showing (SR-22.7).
//
// Relay guard (b.146 rule 7; evaluateRelayGuard): on a row with relay_mode
// on, in any live state, the row's permission requests are read by the
// check-before-read rule (each relay hook judged by pid, start time and pid
// namespace, rule 14), and the call is refused, with no tmux call, in this
// order:
//
//  1. Any relay hook of the row may still answer its request (its process
//     runs; or, when it cannot be checked, its request is open and before
//     its confirm_by): ErrSendKeysWhileRelayed, plain or pane answer, naming
//     that request.
//  2. Plain, any open request has fallen back (pane_answer none or intent):
//     ErrRelayFallenBack with err_details (RelayFallenBackDetails), naming
//     the oldest. Only a call that names the request it answers can type on
//     such a row.
//  3. Plain without expect_pane_sha256, any request of the row is not proven
//     gone (b.146 step 2c: no PostToolUse with its tool_use_id, no main-agent
//     Stop or idle-prompt Notification after it for a request with no
//     agent_id, and no end of the row's agent recorded), however
//     agent-director's own records say it closed: ErrDialogMaybeOpen with
//     err_details (DialogMaybeOpenDetails), naming the oldest. A plain call
//     with expect_pane_sha256 is not held by this rule: its hash check below
//     decides it (it means a person or LLM judged that exact screen). A row
//     with no permission request is never held.
//  4. A pane answer whose request is no request of the row:
//     ErrNoOpenPermissionRequest; one not fallen back (acked, closed with its
//     row or by find-missing, already answered at the pane):
//     ErrAlreadyDecided or ErrNoOpenPermissionRequest; one whose earlier pane
//     answer is still being sent (its intent's sender runs, or cannot be
//     checked and the intent is younger than SendKeysEnv.IntentHold):
//     ErrPaneAnswerInProgress with err_details.
//
// A refusal that finds a request fallen back records its hook_gone_at when it
// has none (a writing verb always does, b.146 rule 8), waiting for the
// store's write lock as any write does; that write is fail-open.
//
// After the state and relay guards, a row whose recorded name is unusable
// (SR-3.2) is ErrInternal with no tmux call; a pending row with no launch
// start or token has already been refused by the state guard.
//
// Then the row's socket (SR-3.3; a resolution refusal is
// ErrTmuxNotAvailable) and one lookup by the row's current label:
//
//   - Ours: one pane listing; the agent's pane is the entry with the row's
//     recorded pane id and pid, wherever it now is. For a row that records
//     no server identity or no pane (a lost create reply), what the lookup
//     and the pane whose @ad_pane names the row's launch token show is used
//     for this call and written once, guarded on the row as read; the write's
//     outcome never changes the result (SR-3.6). No agent's pane:
//     ErrTmuxSessionConflict ("the agent's pane was not found"), nothing sent.
//   - Leftover: on a pending row ErrSpawnNotInteractive ("not this launch's
//     session"), otherwise ErrTmuxSessionConflict; nothing sent.
//   - Gone (another agent-director store's sessions included):
//     ErrTmuxSendKeys ("the row's session is not there"), nothing sent.
//   - Can't tell, tmux unavailable, and a pane listing that fails other than
//     by showing no server: the single-row verbs' shared mapping
//     (ErrTmuxNotAvailable, ErrTmuxSessionConflict "conflicting labels",
//     ErrTmuxUnresponsive), nothing sent. A listing that shows no server is
//     Gone.
//
// With expect_pane_sha256 the agent's pane is then captured as read-pane
// gives it by default (the last n_lines lines, ANSI stripped) and its SHA-256
// compared byte for byte with the caller's; a difference is ErrPaneChanged
// (with err_details, never the new hash), nothing sent. A failed capture maps
// as read-pane's does, with the gone error ErrTmuxSendKeys, nothing sent.
//
// A pane answer (b.146 rule 8) then, under the store's write lock, in one
// transaction: re-reads the row and its requests and applies the state and
// relay guards again, captures and compares the pane, and records the intent
// (pane_answer intent, pane_as, its sender's pid, start time and pid
// namespace from SendKeysEnv.Self, pane_intent_at read on the call's clock
// after the capture, and hook_gone_at when none); it commits before the key is
// sent. It sends exactly one key, then records pane_answer sent, decision its
// claimed verdict and decision_reason pane, unless the request was closed
// meanwhile (the close wins; the call still succeeds, the key was sent). When
// the key send fails, or that last write does, it releases its intent, a
// failed release tried again a bounded number of times (releaseIntent; the
// request stays pane_answer intent, the key may have been typed, and a retry
// with a fresh hash is accepted at once). A lock not taken within the store's
// busy timeout is ErrStoreBusy, nothing sent or recorded; a failed last write
// after a sent key is ErrInternal, saying the key was sent, with err_details
// PaneKeySentDetails (key_sent true).
//
// A failed text, Enter or key call: a timeout is ErrTmuxUnresponsive saying
// the keys may have been delivered, with no further call; any other failure
// makes one follow-up lookup, whose Gone or Leftover gives ErrTmuxSendKeys and
// whose other outcomes give ErrTmuxUnresponsive, or ErrTmuxNotAvailable for a
// different server or tmux unavailable (SR-7.3). Once the text call went
// through, every description says the text may be typed but not submitted,
// never that nothing was sent. Where the text may be in the pane after a plain
// text call with Enter, an ErrTmuxUnresponsive names the next step in place of
// "retry later", since a literal retry would type the text again after the
// unsubmitted copy (b.9o4): after a timed-out text call "read-pane; if the
// text is typed, send-keys with empty text, otherwise the same send-keys", and
// after a failed or timed-out Enter "send-keys with empty text submits it". A
// text or key call that failed other than by timing out typed nothing, and its
// refusals still end "retry later"; so do a key's and a no_enter text's.
//
// Every tmux call uses the row's socket and every action targets a pane id.
// The calls are at most one lookup, one pane listing, one capture, the text
// or key call, the Enter call and, only after an action failure other than a
// timeout, one follow-up lookup (SR-13.2). SendKeys never writes the row's
// state.
//
// Trail (SR-7.4, SR-14): SendKeys writes at most one ad.provenance.disagree
// record per distinct reason per call (source ad_send_keys), fail-open, and
// none in the normal case; Client.SendKeys writes the same records through
// the same path, plus its one ad.send_keys.called. Each record's action is
// what the call typed: keys_sent (the text and Enter, or the key, went
// through), text_sent (the text or key call timed out, or the text went
// through and the Enter call failed or timed out) or nothing_sent (no keys
// call, or the text or key call failed other than by timing out). A pane
// answer's sent write emits ad.row_mutation.committed (writer send_keys).
//
// env carries the relay judge (its start-time reader judges the lookup's
// server, SR-3.3, and an adopted pane, SR-3.6, too), the sender reader and
// the intent hold.
func SendKeys(s SendKeysStore, t SendKeysTmux, env SendKeysEnv, params SendKeysParams) (SendKeysResult, error) {
	_, res, err := sendKeys(s, t, env, params)
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
// with send-keys' pending-row Leftover refusal, its own tmux surface (the
// named key and the capture), its environment and the facts the call keeps.
type sendKeysRun struct {
	keysRun
	s     SendKeysStore
	t     SendKeysTmux
	env   SendKeysEnv
	j     relayJudge
	facts sendKeysFacts
}

// sendKeys is the inner implementation shared by the pure SendKeys entry point
// and Client.SendKeys. It collects the caller identity once, runs the call,
// writes the call's ad.provenance.disagree records (keysRun.emitDisagree)
// and returns the call's facts alongside the result/error so the Client
// wrapper can record them on the ad.send_keys.called trail event without
// re-deriving them.
func sendKeys(s SendKeysStore, t SendKeysTmux, env SendKeysEnv, params SendKeysParams) (sendKeysFacts, SendKeysResult, error) {
	r := &sendKeysRun{s: s, t: t, env: env, j: newRelayJudge(env.Relay),
		facts: sendKeysFacts{Guard: guardNotApplicable, Caller: callerIdentity()}}
	err := r.run(params)
	r.emitDisagree("send-keys", params.ClaudeInstanceID, r.facts.Caller)
	return r.facts, SendKeysResult{}, err
}

// run is the send-keys flow; the returned error is SendKeys'.
func (r *sendKeysRun) run(params SendKeysParams) error {
	plan, err := planSendKeys(params)
	if err != nil {
		return err
	}
	row, err := r.s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return err
	}
	r.facts.RowState = row.State

	if err := sendKeysStateGuard(row, params); err != nil {
		return err
	}

	guard, err := evaluateRelayGuard(r.s, r.j, r.env.IntentHold, row, params)
	r.facts.Guard = guard.eval
	if err != nil {
		return err
	}
	if guard.refusal != nil {
		return guard.refusal
	}

	if err := unusableNameError(row.TmuxSessionName); err != nil {
		return fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}

	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	r.keysRun = newKeysRun(r.t, r.env.Relay.Procs, row, r.s.StoreID(), socket, r.s)
	if plan.enter {
		// A literal retry after a text call with Enter would type the text
		// again after its unsubmitted copy (b.9o4); without Enter, or for a
		// key, "retry later" stands.
		r.next = sendKeysNext
	}
	if row.State == store.StatePending {
		r.leftover = func(leftovers []tmux.Session) error {
			return pendingLeftoverError(row.ClaudeInstanceID, leftovers)
		}
	}
	err = r.send(plan, params)
	r.facts.keysFacts = r.found
	return err
}

// send is the tmux phase: the agent's pane, then the pane answer
// (paneAnswer), or the hash check when asked (paneMatches) and the plain
// keys.
func (r *sendKeysRun) send(plan sendKeysPlan, params SendKeysParams) error {
	paneID, launch, err := r.targetPane()
	if err != nil {
		return err
	}
	if plan.answer {
		return r.paneAnswer(paneID, launch, plan, params)
	}
	if plan.hash != "" {
		if err := r.paneMatches(paneID, launch, plan, ""); err != nil {
			return err
		}
	}
	if plan.key != nil {
		return r.sendKey(paneID, launch, *plan.key)
	}
	return r.typeText(paneID, launch, plan.text, plan.enter)
}

// paneMatches captures the agent's pane paneID as read-pane gives it by
// default (plan.nLines lines, ANSI stripped) and compares its SHA-256 with
// plan.hash, byte equality only (b.146 rules 7 and 8): ErrPaneChanged when
// they differ, naming token when one is given. A failed capture is mapped as
// read-pane's is (paneActionFailureError, not in Keys mode), with send-keys'
// gone error and "nothing was sent"; its follow-up is kept for the trail.
func (r *sendKeysRun) paneMatches(paneID string, launch tmux.Launch, plan sendKeysPlan, token string) error {
	got, err := capturePaneHash(r.t, r.socket, paneID, plan.nLines)
	if err != nil {
		fu, verr := paneActionFailureError(err, paneActionFailure{
			Call:    tmux.CallCapture,
			Gone:    r.gone,
			Pane:    r.refusal(r.row.TmuxSessionName),
			Refusal: r.cantTellRefusal(tmux.CallCapture),
		}, r.t, r.pc, launch)
		r.found.FollowUp = fu
		return verr
	}
	if got != plan.hash {
		return paneChangedError(r.row.ClaudeInstanceID, token, plan.nLines, string(nothingSent))
	}
	return nil
}

// sendKey sends the one key k to the agent's pane paneID: a named key by its
// tmux key name (SendKeyPane), a character typed literally with no Enter
// (SendKeysPane). A failed call is mapped in the "keys may have reached the
// pane" mode (sendFailed), its retry sentence "retry later".
func (r *sendKeysRun) sendKey(paneID string, launch tmux.Launch, k paneKey) error {
	r.found.Sent = true
	call, err := tmux.CallSendKey, error(nil)
	if k.name != "" {
		err = r.t.SendKeyPane(r.socket, paneID, k.name)
	} else {
		call = tmux.CallSendText
		err = r.t.SendKeysPane(r.socket, paneID, k.char, false)
	}
	if err != nil {
		r.found.SendErr = err
		return r.sendFailed(err, call, launch)
	}
	return nil
}

// paneAnswer is a pane answer on the agent's pane paneID (b.146 rule 8):
//
//  1. Under the store's write lock (RecordPaneIntent, waiting the store's busy
//     timeout): the row and its requests are read again, the state guard and
//     rule 7 applied again (lockedRequests), the pane captured and compared
//     with the caller's hash (paneMatches), and the intent recorded: as, the
//     sender's identity (SendKeysEnv.Self) and the time, read on the call's
//     clock only then, so an intent whose sender cannot be checked counts as
//     in progress from its commit, not from before the wait for the lock
//     (b.146 problem 2). It commits before any key is sent.
//  2. The one key (sendKey), never followed by Enter.
//  3. sent, decision as and decision_reason pane (RecordPaneSent), matched by
//     the intent as recorded.
//
// When step 2 fails, or step 3 does, the intent is released (releaseIntent,
// fail-open, a failed release tried again a bounded number of times). A
// step-3 failure is an unnamed error (ErrInternal) saying the key was sent,
// with err_details (paneKeySentError), so it never reads as a call that sent
// nothing and invites a blind retry. A step 3 that matches nothing (the
// request was closed meanwhile, as by its tool's PostToolUse or a close of
// its Spawn's requests) changes nothing and the call succeeds: the key was
// sent.
func (r *sendKeysRun) paneAnswer(paneID string, launch tmux.Launch, plan sendKeysPlan, params SendKeysParams) error {
	id, token := r.row.ClaudeInstanceID, params.RequestToken
	intent, written, err := r.s.RecordPaneIntent(id, token, params.As, r.env.self(), r.j.now, store.DefaultLockWait,
		func(sp Spawn, rows []PermissionRow) error {
			if err := sendKeysStateGuard(sp, params); err != nil {
				return err
			}
			if err := lockedRequests(r.j, sp, rows).sendKeysRefusal(params, r.env.IntentHold); err != nil {
				return err
			}
			return r.paneMatches(paneID, launch, plan, token)
		})
	if err != nil {
		return err
	}
	if !written {
		return fmt.Errorf("%w: %s request %s no longer awaits an answer; nothing was sent",
			store.ErrNoOpenPermissionRequest, id, token)
	}
	if err := r.sendKey(paneID, launch, *plan.key); err != nil {
		r.releaseIntent(id, token, intent)
		return err
	}
	if _, err := r.s.RecordPaneSent(id, token, intent, store.DefaultLockWait); err != nil {
		return paneKeySentError(id, token, err, r.releaseIntent(id, token, intent))
	}
	return nil
}

// paneReleaseAttempts is the most times releaseIntent tries to release a pane
// answer's intent, and paneReleaseBudget the most all its attempts wait for
// the store (its connection and write lock), in all: the store's default busy
// timeout, so a release never holds its call much past one ordinary wait.
const (
	paneReleaseAttempts = 3
	paneReleaseBudget   = 10 * time.Second
)

// releaseIntent ends intent's claim on the request (store ReleasePaneIntent)
// when a pane answer's call ends without recording sent (b.146 rule 8;
// problem 2), and reports whether it did (or the request no longer carries
// the intent). A sender that outlives its call (an MCP server, a Go caller)
// would otherwise keep the request's pane answer in progress
// (ErrPaneAnswerInProgress) until it exits, so a failed release is tried
// again: at most paneReleaseAttempts times in all, each waiting for the store
// at most what is left of paneReleaseBudget on the call's clock (with nothing
// left, a last attempt goes ahead only if the lock is free at once). It never
// lets the intent lapse by its age: while its sender runs the intent holds,
// since its key may be on its way (control c_livesender). Fail-open: a
// release that never succeeds leaves the request as it is.
func (r *sendKeysRun) releaseIntent(id, token string, intent PaneIntent) bool {
	end := r.j.now().Add(paneReleaseBudget)
	for attempt := 1; ; attempt++ {
		if _, err := r.s.ReleasePaneIntent(id, token, intent, max(end.Sub(r.j.now()), 0)); err == nil {
			return true
		}
		if attempt == paneReleaseAttempts || !r.j.now().Before(end) {
			return false
		}
	}
}

// paneKeySentError is a pane answer's error once its key was sent but its
// sent write failed with err (b.146 rule 8, step 3): an unnamed error
// (ErrInternal; err is formatted, never wrapped, so no catalogued name such
// as ErrStoreBusy reads it as nothing recorded and safe to retry) whose
// err_details (PaneKeySentDetails, key_sent true) tell it from a call that
// sent nothing. released is whether the call's intent was released: when it
// was not, a pane answer or a record on the request reads in progress until
// this process exits.
func paneKeySentError(id, token string, err error, released bool) error {
	intent := "its intent is released"
	if !released {
		intent = "its intent could not be released, so it reads in progress (ErrPaneAnswerInProgress) until this process exits"
	}
	return &DetailedError{
		Err: fmt.Errorf("send-keys: %s request %s: the key was sent (err_details.key_sent), but recording it failed (%v); the request still reads pane_answer intent and %s: do not send the key again unread; read-pane, then close it with record-pane-answer if the pane shows it answered",
			id, token, err, intent),
		Details: PaneKeySentDetails{KeySent: true, RequestToken: token},
	}
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

// evaluateRelayGuard is send-keys' relay guard (b.146 rule 7). It returns
// guardNotApplicable when relay_mode is not on (no relay hook records a
// request on such a row). Otherwise it reads every permission request of the
// row by the check-before-read rule (judgedRequests: each relay hook judged by
// pid, start time and pid namespace through j, rule 14), in whatever live
// state the row is, and applies rule 7 (spawnRequests.sendKeysRefusal):
// guardHeld with the refusal, or guardReleased; rule 7 includes b.146 step
// 2c's hold of a plain call without a pane hash while a request is not proven
// gone (ErrDialogMaybeOpen). A refusal that finds a request
// fallen back first records hook_gone_at on every such request with none
// (recordFallenBackGone, waiting the store's busy timeout, fail-open), so its
// err_details carry it. A failed read is guardError with the store's error.
//
// A request of an ended or missing row is closed (b.146 rule 12): the guard
// runs only on a row the state guard let through, which is live.
func evaluateRelayGuard(s SendKeysStore, j relayJudge, hold time.Duration, row Spawn, params SendKeysParams) (sendKeysGuard, error) {
	if row.RelayMode != "on" {
		return sendKeysGuard{eval: guardNotApplicable}, nil
	}
	reqs, err := judgedRequests(func() ([]PermissionRow, error) {
		return s.PermissionRequestsForSpawn(row.ClaudeInstanceID)
	}, j, row)
	if err != nil {
		return sendKeysGuard{eval: guardError}, err
	}
	refusal := reqs.sendKeysRefusal(params, hold)
	if errors.Is(refusal, ErrRelayFallenBack) {
		reqs.recordFallenBackGone(s, store.DefaultLockWait)
		refusal = reqs.sendKeysRefusal(params, hold)
	}
	if refusal != nil {
		return sendKeysGuard{eval: guardHeld, refusal: refusal}, nil
	}
	return sendKeysGuard{eval: guardReleased}, nil
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

// SendKeys sends keys into the agent's own pane: the row's pane, found by the
// row's label on its recorded socket and targeted by pane id.
//
// A plain call types Text (CR bytes (0x0D) stripped to prevent premature
// submission; LF bytes (0x0A) preserved as composed newlines in Claude's
// input box) and then one Enter, unless NoEnter (empty text with Enter sends
// that Enter only); or, with Key, that one key alone. With ExpectPaneSHA256
// it first checks that the pane still has the hash the caller read. Without
// it, a plain call on a relayed Spawn is refused while any of its permission
// requests is not proven gone by Claude Code's own hooks (ErrDialogMaybeOpen;
// b.146 step 2c); ExpectPaneSHA256 means a person or LLM judged that exact
// screen, so never pass it from an automatic flow.
//
// A pane answer (RequestToken, As, Key and ExpectPaneSHA256; b.146 rule 8)
// answers that permission request at the pane once it has fallen back: under
// the store's write lock it checks the guards and the pane's hash and records
// its intent, then sends exactly one key, never Enter, then records
// pane_answer sent with decision As (the caller's claim, never checked) and
// decision_reason pane. When that last write fails after the key was sent the
// error is ErrInternal (an error matching no catalogued sentinel) with
// err_details [PaneKeySentDetails] (key_sent true): the key reached the pane,
// so read it before anything else is sent.
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
//   - [ErrInvalidFlags]: the params' shape is refused (a pane answer without
//     As allow or deny, Key or ExpectPaneSHA256, or with Text; As without
//     RequestToken; Key with Text; NoEnter with neither; a Key that is not a
//     named key or one character; a malformed hash; a negative NLines);
//     nothing was read or sent.
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotInteractive]: the row is finished or otherwise not in a
//     live interactive state, or pending without AllowPending, or pending
//     with no launch start or launch token recorded, or pending and the
//     lookup found only a session an earlier launch left behind; nothing was
//     sent.
//   - [ErrSendKeysWhileRelayed]: relay_mode is on and a relay hook of the
//     Spawn may still answer its request (its process runs, or cannot be
//     checked and its request is open before its confirm_by); nothing was
//     sent. The message names that request and advises Decide (undecided)
//     or a later retry (verdict recorded).
//   - [ErrRelayFallenBack]: a plain call while a request of the Spawn has
//     fallen back with no pane answer recorded through agent-director
//     (pane_answer none or intent); nothing was sent. Its err_details
//     ([RelayFallenBackDetails]) name the request and the Spawn's other open
//     requests. Answer it with a pane answer, or close it with
//     RecordPaneAnswer.
//   - [ErrDialogMaybeOpen]: a plain call without ExpectPaneSHA256 while a
//     request of the Spawn is not proven gone by Claude Code's hooks (its
//     tool's PostToolUse, the main agent's end of turn after it, or the
//     agent's end), whatever agent-director's records say of it; nothing was
//     sent. Its err_details ([DialogMaybeOpenDetails]) name the request,
//     how agent-director's records say it closed, since when, and the
//     Spawn's other such requests. A plain call whose ExpectPaneSHA256
//     matches the pane is not refused with it.
//   - [ErrNoOpenPermissionRequest]: a pane answer whose request is not one
//     of the Spawn's, or is closed with its row or by find-missing; nothing
//     was sent.
//   - [ErrAlreadyDecided]: a pane answer whose request was acked, or already
//     answered at the pane; nothing was sent.
//   - [ErrPaneAnswerInProgress]: a pane answer while another pane answer on
//     that request is still being sent; nothing was sent. err_details:
//     [PaneAnswerInProgressDetails].
//   - [ErrPaneChanged]: ExpectPaneSHA256 is not the hash of the pane as
//     captured now; nothing was sent. err_details: [PaneChangedDetails],
//     never the new hash.
//   - [ErrStoreBusy]: a pane answer's intent could not take the store's
//     write lock within its busy timeout; nothing was sent or recorded.
//   - [ErrTmuxSendKeys]: the row's tmux session is not there.
//   - [ErrTmuxSessionConflict]: the agent's pane was not found, a session an
//     earlier launch left behind is there on a live row, or tmux holds
//     conflicting labels; nothing was sent.
//   - [ErrTmuxUnresponsive]: tmux did not answer, or gave a reply that could
//     not be recognised; after a text, Enter or key call the keys may have
//     been delivered. Where the text may be typed, the description names the
//     next step (read-pane, and send-keys with empty text, which sends Enter
//     only) in place of a retry that would type the text twice.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on.
//
// Nondeterminism: the relay guard depends on the clock and on whether each
// relay hook, and a pane answer's sender, runs.
func (c *Client) SendKeys(params SendKeysParams) (SendKeysResult, error) {
	if err := c.checkClosed(); err != nil {
		return SendKeysResult{}, err
	}

	// The relay view is the Client's own: its clock (c.now), start-time
	// reader, pid namespace and the configured effective relay window. A pane
	// answer's sender is this process (c.paneSelf), and an intent whose
	// sender cannot be checked counts as in progress for the configured tmux
	// action timeout plus the pipe-close wait plus 2 s. The caller identity
	// is collected inside agent-director, never from caller-asserted params
	// (SR-5.2), once per call by sendKeys.
	facts, result, err := sendKeys(c.st, c.tmuxClient, c.sendKeysEnv(), params)
	// Exactly one ad.send_keys.called per call past the closed check, on
	// every outcome (SR-7.4, SR-5.2), fail-open.
	emitSendKeysCalled(params, facts, err)
	return result, err
}

// sendKeysEnv is the SendKeysEnv of the Client's send-keys and
// record-pane-answer calls.
func (c *Client) sendKeysEnv() SendKeysEnv {
	return SendKeysEnv{
		Relay:      c.relayView(),
		Self:       c.paneSelf,
		IntentHold: paneIntentHold(c.cfg.Tmux),
	}
}
