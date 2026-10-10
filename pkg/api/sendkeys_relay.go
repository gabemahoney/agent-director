package api

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// This file holds b.146 rule 7, when send-keys is refused on account of the
// Spawn's permission requests, with the facts and refusals send-keys,
// record-pane-answer and decide share: whether a request's relay hook may
// still answer it, whether it has fallen back, whether a pane answer on it is
// still being sent (problem 2), ErrRelayFallenBack's err_details (rule 15),
// and step 2c's hold of plain send-keys while a request is not proven gone
// (ErrDialogMaybeOpen). Every judgement rests on the store, on Claude Code's
// hooks as the store recorded them, and on process checks; none reads the
// pane.

// spawnRequests is one judged read of every permission request of a Spawn
// (b.146 rules 5, 7, 12 and 14): the Spawn as read, its requests, each relay
// hook's verdict and the time they are judged at.
type spawnRequests struct {
	j        relayJudge
	sp       Spawn
	rows     []PermissionRow
	verdicts map[int64]hookVerdict
	now      time.Time
}

// judgedRequests reads every request of sp through read by the
// check-before-read rule (readManyJudged: read, judge every relay hook, read
// again), and sorts them oldest first (created_at, then request id).
func judgedRequests(read func() ([]PermissionRow, error), j relayJudge, sp Spawn) (spawnRequests, error) {
	rows, verdicts, err := readManyJudged(read, j)
	if err != nil {
		return spawnRequests{}, err
	}
	return newSpawnRequests(j, sp, rows, verdicts), nil
}

// lockedRequests judges rows read inside a pane write's transaction
// (store.PaneCheck). While the write lock is held no relay hook can ack and no
// other writer can change a request, so judging the hooks after the read is
// as safe there as the check-before-read rule is elsewhere.
func lockedRequests(j relayJudge, sp Spawn, rows []PermissionRow) spawnRequests {
	verdicts := make(map[int64]hookVerdict, len(rows))
	for _, pr := range rows {
		verdicts[pr.RequestID] = j.hook(pr)
	}
	return newSpawnRequests(j, sp, rows, verdicts)
}

// newSpawnRequests sorts rows oldest first and stamps the judgement's time.
func newSpawnRequests(j relayJudge, sp Spawn, rows []PermissionRow, verdicts map[int64]hookVerdict) spawnRequests {
	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b PermissionRow) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.RequestID, b.RequestID)
	})
	return spawnRequests{j: j, sp: sp, rows: rows, verdicts: verdicts, now: j.now()}
}

// delivery is pr's delivery facts at the judgement's time.
func (r spawnRequests) delivery(pr PermissionRow) RequestDelivery {
	return r.j.delivery(pr, r.verdicts[pr.RequestID], r.now)
}

// closed reports whether pr is closed: its Spawn is ended or missing (b.146
// rule 12), a close of the Spawn's requests closed it (closed_at:
// find-missing's mark, the ended transition or resume's move), or a completed
// pane answer did.
func (r spawnRequests) closed(pr PermissionRow) bool {
	return finishedState(r.sp.State) || pr.Closed() || pr.PaneAnswered()
}

// awaits reports whether pr still awaits an answer (b.146 rule 9) on a live
// Spawn.
func (r spawnRequests) awaits(pr PermissionRow) bool {
	return !finishedState(r.sp.State) && pr.AwaitsAnswer()
}

// hookMayAnswer reports whether pr's relay hook may still answer it (b.146
// rule 7, rows 1 and 3): pr is not closed, and its hook process is seen
// running (an acked request's hook included: it is writing its verdict to
// Claude Code), or pr is not_confirmed (no ack, and its hook cannot be
// checked and confirm_by has not passed; a request recorded before schema v7,
// by its relay window). A closed request's hook answers nothing that reaches
// the agent's dialog, so it holds nothing.
func (r spawnRequests) hookMayAnswer(pr PermissionRow) bool {
	if r.closed(pr) {
		return false
	}
	return r.verdicts[pr.RequestID] == hookAlive || r.delivery(pr).Delivery == DeliveryNotConfirmed
}

// fallenBack reports whether pr is open and fallen back (b.146 rule 5): not
// acked, no completed pane answer (pane_answer none or intent), its hook gone
// (or past confirm_by when it cannot be checked), and not closed.
func (r spawnRequests) fallenBack(pr PermissionRow) bool {
	return !r.closed(pr) && r.delivery(pr).Delivery == DeliveryFallenBack
}

// liveHolder returns the request ErrSendKeysWhileRelayed names: of those whose
// relay hook may still answer (hookMayAnswer), one still awaiting an answer
// in preference to an acked one, then the oldest.
func (r spawnRequests) liveHolder() (PermissionRow, bool) {
	var held *PermissionRow
	for i := range r.rows {
		pr := &r.rows[i]
		if !r.hookMayAnswer(*pr) {
			continue
		}
		if held == nil || (r.awaits(*pr) && !r.awaits(*held)) {
			held = pr
		}
	}
	if held == nil {
		return PermissionRow{}, false
	}
	return *held, true
}

// oldestFallenBack returns the oldest open request that has fallen back.
func (r spawnRequests) oldestFallenBack() (PermissionRow, bool) {
	for _, pr := range r.rows {
		if r.fallenBack(pr) {
			return pr, true
		}
	}
	return PermissionRow{}, false
}

// unproven returns every request that Claude Code has not proven gone (b.146
// step 2c), oldest first.
func (r spawnRequests) unproven() []PermissionRow {
	var out []PermissionRow
	for _, pr := range r.rows {
		if !pr.ProvenGone() {
			out = append(out, pr)
		}
	}
	return out
}

// find returns the request with token.
func (r spawnRequests) find(token string) (PermissionRow, bool) {
	for _, pr := range r.rows {
		if pr.RequestToken == token {
			return pr, true
		}
	}
	return PermissionRow{}, false
}

// sendKeysRefusal is b.146 rule 7 for a send-keys call with params on the
// Spawn, in its table's order; nil when the call may go on to the pane (and,
// with expect_pane_sha256, to its hash check). hold is how long an intent
// whose sender cannot be checked counts as in progress (paneIntentHold).
//
//  1. Any relay hook of the Spawn may still answer its request
//     (hookMayAnswer): ErrSendKeysWhileRelayed, plain or with a token.
//  2. Plain (no token), any open request has fallen back with pane_answer
//     none or intent: ErrRelayFallenBack with err_details, naming the oldest.
//  3. Plain with no expect_pane_sha256, any request of the Spawn is not
//     proven gone (b.146 step 2c): ErrDialogMaybeOpen with err_details,
//     naming the oldest. With expect_pane_sha256 the call is not held here:
//     its hash check, against the pane captured before the keys are sent,
//     decides it (ErrPaneChanged on a mismatch).
//  4. With a token T: no such request of the Spawn is
//     ErrNoOpenPermissionRequest; T not fallen back (acked, closed, answered
//     at the pane) is notFallenBackRefusal's; T's pane answer still being
//     sent (paneInProgress) is ErrPaneAnswerInProgress.
//
// Nothing is written here: a caller that refuses with ErrRelayFallenBack
// records hook_gone_at through recordFallenBackGone.
func (r spawnRequests) sendKeysRefusal(params SendKeysParams, hold time.Duration) error {
	id := r.sp.ClaudeInstanceID
	if pr, ok := r.liveHolder(); ok {
		return relayGuardRefusal(id, pr.RequestToken, pr.Decision != "")
	}
	if params.RequestToken == "" {
		if pr, ok := r.oldestFallenBack(); ok {
			return relayFallenBackError(id, pr.RequestToken, "nothing was sent", r.fallenBackDetails(pr))
		}
		if unproven := r.unproven(); len(unproven) > 0 && params.ExpectPaneSHA256 == "" {
			return dialogMaybeOpenError(id, r.dialogMaybeOpenDetails(unproven))
		}
		return nil
	}
	pr, ok := r.find(params.RequestToken)
	if !ok {
		return fmt.Errorf("%w: %s has no permission request %s; nothing was sent",
			store.ErrNoOpenPermissionRequest, id, params.RequestToken)
	}
	if err := r.notFallenBackRefusal(pr, "nothing was sent"); err != nil {
		return err
	}
	return r.inProgressRefusal(pr, hold, "nothing was sent")
}

// notFallenBackRefusal names a request that is not open and fallen back, for
// a call that answers it at the pane or records it answered (b.146 rule 7,
// row 4); nil for one that is. what says what the call did not do.
//
//   - Closed with its finished Spawn (b.146 rule 12): ErrNoOpenPermissionRequest.
//   - Closed by a close of the Spawn's requests (closed_at; the Spawn resumed
//     since): recordedRefusal's (ErrAlreadyDecided for find-missing's deny,
//     otherwise ErrNoOpenPermissionRequest).
//   - Answered at the pane (sent, outside or tool_ran): ErrAlreadyDecided.
//   - Acked (delivered): ErrAlreadyDecided.
//   - Its relay hook may still answer it (not_confirmed):
//     ErrSendKeysWhileRelayed.
func (r spawnRequests) notFallenBackRefusal(pr PermissionRow, what string) error {
	id := r.sp.ClaudeInstanceID
	params := DecideParams{ClaudeInstanceID: id, RequestToken: pr.RequestToken}
	switch {
	case r.fallenBack(pr):
		return nil
	case finishedState(r.sp.State):
		return fmt.Errorf("%w: %s request %s is closed: the spawn is %s; %s",
			store.ErrNoOpenPermissionRequest, id, pr.RequestToken, r.sp.State, what)
	case pr.Closed():
		return fmt.Errorf("%w; %s", recordedRefusal(params, pr), what)
	case pr.PaneAnswered():
		return paneAnsweredError(id, pr, what)
	case r.delivery(pr).Delivery == DeliveryDelivered:
		return fmt.Errorf("%w; its relay hook delivered it; %s", alreadyDecidedError(id, pr), what)
	}
	return relayGuardRefusal(id, pr.RequestToken, pr.Decision != "")
}

// paneInProgress reports whether pr carries a pane answer through send-keys
// that is still being sent (b.146 problem 2): pane_answer intent, its intent
// not released, and its sender process seen running (rule 14's check), or,
// when the sender cannot be checked (or recorded no identity), the intent
// younger than hold. It also returns whether the sender runs (true, or nil
// when it cannot be checked) and, when it cannot be checked, the instant the
// intent stops counting.
func (r spawnRequests) paneInProgress(pr PermissionRow, hold time.Duration) (bool, *bool, *time.Time) {
	if pr.PaneAnswer != store.PaneAnswerIntent || pr.PaneIntentAt.IsZero() {
		return false, nil, nil
	}
	switch r.j.sender(pr) {
	case hookAlive:
		alive := true
		return true, &alive, nil
	case hookGone:
		return false, nil, nil
	}
	until := pr.PaneIntentAt.Add(hold)
	if r.now.Before(until) {
		return true, nil, &until
	}
	return false, nil, nil
}

// inProgressRefusal is ErrPaneAnswerInProgress with its err_details while
// pr's pane answer is still being sent (paneInProgress); nil otherwise.
func (r spawnRequests) inProgressRefusal(pr PermissionRow, hold time.Duration, what string) error {
	busy, alive, until := r.paneInProgress(pr, hold)
	if !busy {
		return nil
	}
	return &DetailedError{
		Err: fmt.Errorf("%w: %s request %s: a pane answer through agent-director (pane_as %q) is still being sent, its intent written at %s; %s",
			ErrPaneAnswerInProgress, r.sp.ClaudeInstanceID, pr.RequestToken, pr.PaneAs,
			pr.PaneIntentAt.UTC().Format(time.RFC3339Nano), what),
		Details: PaneAnswerInProgressDetails{
			RequestToken: pr.RequestToken,
			PaneAs:       pr.PaneAs,
			PaneIntentAt: pr.PaneIntentAt,
			SenderAlive:  alive,
			NotBefore:    until,
		},
	}
}

// fallenBackDetails is ErrRelayFallenBack's err_details for pr (b.146 rule
// 15): its fields with its delivery facts, the Spawn's state, and every other
// request of the Spawn that still awaits an answer, oldest first.
func (r spawnRequests) fallenBackDetails(pr PermissionRow) RelayFallenBackDetails {
	d := RelayFallenBackDetails{
		PermissionRequestInfo: permissionRequestInfo(pr, r.delivery(pr)),
		State:                 r.sp.State,
		OpenRequests:          []OpenRequestFacts{},
	}
	for _, o := range r.rows {
		if o.RequestID == pr.RequestID || !r.awaits(o) {
			continue
		}
		od := r.delivery(o)
		d.OpenRequests = append(d.OpenRequests, OpenRequestFacts{
			RequestToken: o.RequestToken,
			ToolName:     o.ToolName,
			RequestedAt:  o.CreatedAt,
			Delivery:     od.Delivery,
			HookAlive:    od.HookAlive,
			PaneAnswer:   od.PaneAnswer,
		})
	}
	return d
}

// dialogMaybeOpenDetails is ErrDialogMaybeOpen's err_details (b.146 step 2c)
// for unproven, the Spawn's requests not proven gone, oldest first (at least
// one): the oldest with its fields and delivery facts, the Spawn's state, and
// the others, oldest first.
func (r spawnRequests) dialogMaybeOpenDetails(unproven []PermissionRow) DialogMaybeOpenDetails {
	d := DialogMaybeOpenDetails{
		PermissionRequestInfo: permissionRequestInfo(unproven[0], r.delivery(unproven[0])),
		State:                 r.sp.State,
		UnprovenRequests:      make([]PermissionRequestInfo, 0, len(unproven)-1),
	}
	for _, pr := range unproven[1:] {
		d.UnprovenRequests = append(d.UnprovenRequests, permissionRequestInfo(pr, r.delivery(pr)))
	}
	return d
}

// dialogMaybeOpenError is ErrDialogMaybeOpen for spawn instanceID, with d as
// its err_details (b.146 step 2c): the request d names is not proven gone by
// any hook of Claude Code, so its permission dialog may still be on the pane.
// The description states facts only: the request, what agent-director's
// records say of it, since when, and that nothing was sent.
func dialogMaybeOpenError(instanceID string, d DialogMaybeOpenDetails) error {
	since := "it still awaits an answer"
	if d.UnprovenSince != nil {
		since = "its record has read closed since " + d.UnprovenSince.UTC().Format(time.RFC3339Nano)
	}
	return &DetailedError{
		Err: fmt.Errorf("%w: %s request %s is not proven gone (no PostToolUse with its tool_use_id, no end of the main agent's turn after it for a request with no agent_id, and no end of its agent is recorded), so its permission dialog may still be on the pane; agent-director's records give delivery %s and pane_answer %s, and %s; %d other request(s) of the spawn are not proven gone (err_details); nothing was sent",
			ErrDialogMaybeOpen, instanceID, d.RequestToken, d.Delivery, d.PaneAnswer, since, len(d.UnprovenRequests)),
		Details: d,
	}
}

// recordFallenBackGone records hook_gone_at, at the judgement's time and
// waiting at most maxWait for the write lock, on every open request of the
// Spawn found fallen back with none yet (a writing verb always records it,
// b.146 rule 8), and keeps the stored value on r's rows, so facts built from r
// afterwards carry it. It is fail-open (a store that does not record it, a
// lock not taken in time, or a failed write leaves the rows as read).
func (r spawnRequests) recordFallenBackGone(s any, maxWait time.Duration) {
	rec, ok := s.(hookGoneRecorder)
	if !ok {
		return
	}
	var ids []int64
	for _, pr := range r.rows {
		if r.fallenBack(pr) && pr.HookGoneAt.IsZero() {
			ids = append(ids, pr.RequestID)
		}
	}
	if len(ids) == 0 {
		return
	}
	stored, err := rec.RecordHookGone(r.now, maxWait, ids...)
	if err != nil {
		return
	}
	for i := range r.rows {
		if t, ok := stored[r.rows[i].RequestID]; ok && !t.IsZero() {
			r.rows[i].HookGoneAt = t
		}
	}
}

// relayFallenBackError is ErrRelayFallenBack for request token of spawn
// instanceID, with details as its err_details (b.146 rule 15; decision 10 A):
// the request's relay hook is gone and acked no verdict, and no pane answer
// is recorded on it through agent-director, so no answer from the relay
// reached the agent; what says what the refused call did not do. It says
// nothing of what the pane shows.
func relayFallenBackError(instanceID, token, what string, details RelayFallenBackDetails) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: %s request %s fell back: its relay hook is gone and acked no verdict, and no pane answer is recorded on it through agent-director (err_details give its facts and the spawn's other open requests); %s; answer it at the pane with send-keys and its request_token, or, if it was answered outside agent-director, close it with record-pane-answer",
			ErrRelayFallenBack, instanceID, token, what),
		Details: details,
	}
}

// relayGuardRefusal is the ErrSendKeysWhileRelayed refusal for request
// holding of spawn instanceID, whose relay hook may still answer it (b.146
// rule 7). Every refusal of that name is built here. It states no release time
// or margin (b.ah6), and has two forms:
//
//   - An undecided request (decided false): it advises answering it with
//     decide.
//   - A request with a recorded verdict (decided true): it says the verdict is
//     recorded and its relay hook may still be delivering it, and advises
//     retrying send-keys later (b.ceq); decide on it would return
//     ErrAlreadyDecided.
func relayGuardRefusal(instanceID, holding string, decided bool) error {
	if decided {
		return fmt.Errorf(
			"%w: spawn %s: the relayed permission verdict on request %s is recorded and its relay hook may still be delivering it; retry send-keys later",
			ErrSendKeysWhileRelayed, instanceID, holding)
	}
	return fmt.Errorf(
		"%w: spawn %s is awaiting a relayed permission decision on request %s; answer it with decide",
		ErrSendKeysWhileRelayed, instanceID, holding)
}
