package api

import (
	"cmp"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The delivery values of a permission request (b.146 rule 15).
const (
	// DeliveryDelivered: the request's relay hook acked a verdict (committed
	// delivered_at) before writing it to Claude Code.
	DeliveryDelivered = "delivered"
	// DeliveryNotConfirmed: no ack yet, and the relay hook may still be
	// alive; it never lasts past confirm_by.
	DeliveryNotConfirmed = "not_confirmed"
	// DeliveryFallenBack: no ack, and the relay hook is gone (or cannot be
	// checked and its settle instant has passed): no answer from the relay
	// reached Claude Code. While pane_answer is none or intent no pane answer
	// is recorded through agent-director. It stays fallen_back once a pane
	// answer closes the request (b.146 step 2b), pane_answer telling how.
	DeliveryFallenBack = "fallen_back"
)

// RelayView is what a reader of permission requests judges their relay hooks
// with (b.146 rules 5 and 14): the start-time reader, the reader's own pid
// namespace, its clock and the effective relay window. The Client builds it
// from its own start-time reader, clock and configuration; a caller of the
// exported verb functions (Get, List, GetPermission, Decide,
// RepairCheckPermission) passes its own.
type RelayView struct {
	// Procs reads a recorded relay hook's start time. nil judges every hook
	// "can't tell".
	Procs ProcChecker
	// PIDNamespace reads the caller's own pid namespace (the target of
	// /proc/self/ns/pid on Linux); a recorded hook is judged only in the same
	// namespace. nil, or an unknown namespace, judges every hook "can't
	// tell".
	PIDNamespace func() (ns string, known bool)
	// Now is the clock delivery is judged on, and decide's wait for the ack
	// is measured on. nil reads time.Now.
	Now func() time.Time
	// Window is the effective relay window (relay.timeout_seconds), by which
	// a request recorded before schema v7, with no settle instant on record,
	// falls back.
	Window time.Duration
}

// RequestDelivery is a permission request's delivery facts (b.146 rule 15),
// with whether Claude Code proved its dialog gone (b.146 step 2c), on every
// request of get's and list's permission_requests and get's
// unproven_requests, on get-permission and on decide's result. They are
// derived from the record and a check of the request's relay hook process
// each time they are read; nothing but hook_gone_at is written for them.
type RequestDelivery struct {
	// Delivery is delivered, not_confirmed or fallen_back (DeliveryDelivered,
	// DeliveryNotConfirmed, DeliveryFallenBack). decide's result is never
	// fallen_back: decide refuses such a request with ErrRelayFallenBack.
	Delivery string `json:"delivery"`
	// ConfirmBy is when not_confirmed ends: the relay hook's kill instant
	// plus a 2 s reserve, by when its hook has acked or is gone. For a
	// request recorded before schema v7 it is created_at plus the relay
	// window plus 2 s.
	ConfirmBy time.Time `json:"confirm_by"`
	// HookAlive is whether the relay hook process runs: true, false, or null
	// when it cannot be checked (another or unreadable pid namespace, an
	// unreadable /proc, or no hook identity on record).
	HookAlive *bool `json:"hook_alive"`
	// HookGoneAt is when a reader first found the request fallen back; null
	// until then.
	HookGoneAt *time.Time `json:"hook_gone_at"`
	// AttemptedDecision is the verdict a decide refused as fallen back tried
	// to record, the latest one; null when none. Shown, never acted on.
	AttemptedDecision *string `json:"attempted_decision"`
	// AttemptedAt is when that refused decide ran; null when none.
	AttemptedAt *time.Time `json:"attempted_at"`
	// ToolUseID is the hook input's tool_use_id; null when none was given.
	ToolUseID *string `json:"tool_use_id"`
	// PaneAnswer is how the request was answered at the pane (b.146 step
	// 2b): none (no pane answer recorded through agent-director: something
	// outside it, such as a person at tmux, may still have answered it),
	// intent (a pane answer through send-keys was started; whether its key
	// was typed is unknown), sent (its key was sent), outside (a caller
	// recorded it answered outside agent-director) or tool_ran (Claude Code
	// reported its tool ran).
	PaneAnswer string `json:"pane_answer"`
	// PaneAs is the verdict a pane answer claims: allow, deny, or unknown (an
	// outside record that did not see the answer); null when none was
	// recorded. It is the caller's claim, stored and never checked.
	PaneAs *string `json:"pane_as"`
	// ProvenGoneAt is when Claude Code proved the request's permission dialog
	// gone (b.146 step 2c); null until then. While it is null, the request
	// holds plain send-keys to its live Spawn (ErrDialogMaybeOpen).
	ProvenGoneAt *time.Time `json:"proven_gone_at"`
	// ProvenGoneHow is how: tool_ran (its tool's PostToolUse or
	// PostToolUseFailure, by tool_use_id), turn_end (the turn of the agent
	// that asked ended after it: the main agent's Stop or idle-prompt
	// Notification proves a request with no agent_id) or agent_gone (its
	// Spawn was marked missing, ended or resumed); null until then.
	ProvenGoneHow *string `json:"proven_gone_how"`
	// UnprovenSince is, for a request not proven gone that no longer awaits
	// an answer in agent-director's own records, when its record stopped
	// awaiting one: its relay hook's ack, its pane answer's or
	// record-pane-answer's write, or the close of its Spawn's requests (a
	// request recorded before this release: its recorded verdict). From then
	// agent-director reads it closed while Claude Code has not shown its
	// dialog gone. Null while it still awaits an answer, and once it is
	// proven gone.
	UnprovenSince *time.Time `json:"unproven_since"`
}

// hookVerdict is a reader's judgement of a request's relay hook (b.146
// rule 14). The zero value is hookCantTell.
type hookVerdict int

const (
	// hookCantTell: the hook cannot be checked; readers fall back to its
	// settle instant.
	hookCantTell hookVerdict = iota
	// hookAlive: the hook runs with its recorded start time, in any state
	// but zombie (stopped or in uninterruptible sleep included).
	hookAlive
	// hookGone: no such process, another start time, or a zombie.
	hookGone
)

// relayJudge is one read's judge of relay hooks: the view and the reader's
// own pid namespace, read once.
type relayJudge struct {
	view    RelayView
	ns      string
	nsKnown bool
}

// newRelayJudge reads the caller's pid namespace through v once.
func newRelayJudge(v RelayView) relayJudge {
	j := relayJudge{view: v}
	if v.PIDNamespace != nil {
		j.ns, j.nsKnown = v.PIDNamespace()
	}
	return j
}

// now reads the view's clock.
func (j relayJudge) now() time.Time {
	if j.view.Now == nil {
		return time.Now()
	}
	return j.view.Now()
}

// hook judges pr's relay hook by rule 14's table (process).
func (j relayJudge) hook(pr PermissionRow) hookVerdict { return j.process(pr.Hook) }

// sender judges the process that recorded pr's pane-answer intent (b.146
// problem 2) by rule 14's table, as a relay hook is judged (process).
func (j relayJudge) sender(pr PermissionRow) hookVerdict { return j.process(pr.PaneSender) }

// process judges the recorded process id by rule 14's table: can't tell when
// its identity is not on record (no pid or start time: a relay hook, and a
// pane answer's sender, records its pid only with its start time and pid
// namespace), when there is no start-time reader, or when the reader's own pid
// namespace is unknown or another than the recorded one (on darwin, which has
// no pid namespaces, both read ""); otherwise one start-time read
// (tmux.JudgeProcess) decides: alive with the recorded start time is alive
// (whatever the state but zombie), gone or a zombie or another start time is
// gone, unreadable is can't tell.
func (j relayJudge) process(id ProcessIdentity) hookVerdict {
	if id.PID <= 0 || id.Starttime == "" || j.view.Procs == nil || !j.nsKnown || j.ns != id.PIDNamespace {
		return hookCantTell
	}
	switch tmux.JudgeProcess(j.view.Procs, tmux.ProcIdentity{PID: id.PID, Starttime: id.Starttime}) {
	case tmux.ProcAlive:
		return hookAlive
	case tmux.ProcGone:
		return hookGone
	}
	return hookCantTell
}

// confirmBy is pr's confirm_by: its recorded settle instant, or, for a
// request recorded before schema v7, the instant its relay hook is presumed
// settled by the relay window (relayHookSettledAt).
func (j relayJudge) confirmBy(pr PermissionRow) time.Time {
	if pr.PreV7() {
		return relayHookSettledAt(pr.CreatedAt, j.view.Window)
	}
	return pr.SettledAt
}

// delivery derives pr's delivery facts at now from the record and v, the
// judgement of its hook taken BEFORE the record was read (rule 5's note: a
// hook found gone writes nothing more, so the record read after is final).
func (j relayJudge) delivery(pr PermissionRow, v hookVerdict, now time.Time) RequestDelivery {
	d := RequestDelivery{
		ConfirmBy:         j.confirmBy(pr),
		HookGoneAt:        nullableTime(pr.HookGoneAt),
		AttemptedDecision: nullableString(pr.AttemptedDecision),
		AttemptedAt:       nullableTime(pr.AttemptedAt),
		ToolUseID:         nullableString(pr.ToolUseID),
		PaneAnswer:        paneAnswerOf(pr),
		PaneAs:            nullableString(pr.PaneAs),
		ProvenGoneAt:      nullableTime(pr.ProvenGoneAt),
		ProvenGoneHow:     nullableString(pr.ProvenGoneHow),
		UnprovenSince:     nullableTime(unprovenSince(pr)),
	}
	switch v {
	case hookAlive:
		alive := true
		d.HookAlive = &alive
	case hookGone:
		alive := false
		d.HookAlive = &alive
	}
	d.Delivery = deliveryOf(pr, v, !now.Before(d.ConfirmBy))
	return d
}

// deliveryOf is rule 5's derivation. settled is whether confirm_by has
// passed.
//
//   - Acked: delivered.
//   - A completed pane answer is recorded (step 2b: sent, outside or
//     tool_ran): fallen_back, its pane_answer telling how it closed. A pane
//     answer is given only to a request that had fallen back, a request
//     recorded before schema v7 included.
//   - Recorded before schema v7 (no ack, no hook identity on record), by
//     time as before the upgrade: until confirm_by not_confirmed; after it,
//     delivered when a verdict was recorded (a relay hook from before v7
//     delivered it with no ack), fallen_back when none was (a close's deny
//     of an undecided request, find-missing's or the ended row's, counts as
//     none: PermissionRow.CloseDenied).
//   - Otherwise by the hook: alive is not_confirmed; gone is fallen_back;
//     can't tell is fallen_back once confirm_by has passed and
//     not_confirmed before.
//
// A request closed with its Spawn (b.146 rule 12; closed_at: find-missing's
// mark, the terminal SessionEnd's move to ended, or resume's move to pending)
// is derived by the same rules, so each value keeps its meaning and never
// goes back: it is delivered only when its relay hook acked (a hook whose
// parent is still alive may yet ack the verdict recorded on it, or the
// close's deny of an undecided one), not_confirmed while that hook may still
// run, and fallen_back once it is gone or past confirm_by: no answer from the
// relay reached the agent. A closed request no longer awaits an answer, so
// get and list do not show it, and decide refuses it as closed
// (ErrNoOpenPermissionRequest, or ErrAlreadyDecided for the mark's deny),
// never as fallen back; for a request recorded before schema v7 a close's
// deny counts as no verdict, as above.
func deliveryOf(pr PermissionRow, v hookVerdict, settled bool) string {
	switch {
	case !pr.DeliveredAt.IsZero():
		return DeliveryDelivered
	case pr.PaneAnswered():
		return DeliveryFallenBack
	case pr.PreV7():
		switch {
		case !settled:
			return DeliveryNotConfirmed
		case pr.Decision != "" && !pr.CloseDenied():
			return DeliveryDelivered
		}
		return DeliveryFallenBack
	case v == hookAlive:
		return DeliveryNotConfirmed
	case v == hookGone, settled:
		return DeliveryFallenBack
	}
	return DeliveryNotConfirmed
}

// unprovenSince is pr's unproven_since (b.146 step 2c): for a request not
// proven gone that no longer awaits an answer in agent-director's own records
// (PermissionRow.AwaitsAnswer), the instant its record stopped awaiting one,
// by the same clauses: its relay hook's ack (delivered_at); a completed pane
// answer's write (decided_at of pane_answer sent, outside or tool_ran); a
// close of its Spawn's requests (closed_at); recorded before schema v7, its
// recorded verdict (decided_at). The zero time while it is proven gone or
// still awaits an answer.
func unprovenSince(pr PermissionRow) time.Time {
	switch {
	case pr.ProvenGone():
		return time.Time{}
	case !pr.DeliveredAt.IsZero():
		return pr.DeliveredAt
	case pr.PaneAnswered():
		return pr.DecidedAt
	case pr.Closed():
		return pr.ClosedAt
	case pr.PreV7() && pr.Decision != "":
		return pr.DecidedAt
	}
	return time.Time{}
}

// readOneJudged reads one request by the check-before-read rule (rule 5's
// note): read for the hook's identity, judge the hook, then read again for
// the final record. Both reads' errors are returned as read gave them.
func readOneJudged(read func() (PermissionRow, error), j relayJudge) (PermissionRow, hookVerdict, error) {
	pr, err := read()
	if err != nil {
		return PermissionRow{}, hookCantTell, err
	}
	v := j.hook(pr)
	pr, err = read()
	return pr, v, err
}

// readManyJudged is readOneJudged for a set of requests: read, judge every
// hook, read again. A request first seen in the second read was recorded
// between the reads; it is judged can't tell (the zero hookVerdict of the
// returned map), so it reads not_confirmed until its settle instant, never
// fallen back on a check taken after its record was read.
func readManyJudged(read func() ([]PermissionRow, error), j relayJudge) ([]PermissionRow, map[int64]hookVerdict, error) {
	first, err := read()
	if err != nil || len(first) == 0 {
		return first, nil, err
	}
	verdicts := make(map[int64]hookVerdict, len(first))
	for _, pr := range first {
		verdicts[pr.RequestID] = j.hook(pr)
	}
	rows, err := read()
	return rows, verdicts, err
}

// hookGoneRecorder is the store write of hook_gone_at (b.146 rules 8, 15).
// *store.Store satisfies it; a store that does not records nothing and its
// readers report the stored value only.
type hookGoneRecorder interface {
	RecordHookGone(at time.Time, maxWait time.Duration, requestIDs ...int64) (map[int64]time.Time, error)
}

// goneSlot is one returned request a reader derived fallen back with no
// hook_gone_at yet, and the delivery facts to fill once it is written.
type goneSlot struct {
	id int64
	d  *RequestDelivery
}

// addGone appends id's slot to slots when d is fallen back with no
// hook_gone_at.
func addGone(slots []goneSlot, id int64, d *RequestDelivery) []goneSlot {
	if d.Delivery == DeliveryFallenBack && d.HookGoneAt == nil {
		slots = append(slots, goneSlot{id: id, d: d})
	}
	return slots
}

// readerLockWait is a reading verb's wait for the write lock: none. get, list
// and get-permission write hook_gone_at only if the lock is free at that
// moment (b.146 problem 4).
const readerLockWait time.Duration = 0

// recordGone writes at as hook_gone_at on every slot's request in one
// transaction through s (when it is a hookGoneRecorder), waiting at most
// maxWait for the write lock, and fills each slot's HookGoneAt with the
// stored value. It is fail-open: a lock not free within maxWait, or a
// failed write, writes nothing and leaves the slots' HookGoneAt null, and the
// read that found them is answered as it is.
func recordGone(s any, at time.Time, maxWait time.Duration, slots []goneSlot) {
	r, ok := s.(hookGoneRecorder)
	if !ok || len(slots) == 0 {
		return
	}
	ids := make([]int64, 0, len(slots))
	for _, sl := range slots {
		ids = append(ids, sl.id)
	}
	stored, err := r.RecordHookGone(at, maxWait, ids...)
	if err != nil {
		return
	}
	for _, sl := range slots {
		if t, ok := stored[sl.id]; ok && !t.IsZero() {
			sl.d.HookGoneAt = &t
		}
	}
}

// paneAnswerOf is pr's pane_answer as the wire gives it: none for a row built
// in memory with no value (the column itself is never empty).
func paneAnswerOf(pr PermissionRow) string {
	if pr.PaneAnswer == "" {
		return store.PaneAnswerNone
	}
	return pr.PaneAnswer
}

// RelayHookGone reports whether pr's relay hook is gone at v's clock by
// rule 5's hook clause (b.146 rules 5 and 14): its process is provably gone in
// v's pid namespace, or it cannot be checked and pr's confirm_by (its settle
// instant; for a request recorded before schema v7, its relay window plus the
// margins) has passed. Call it BEFORE reading the record a decision rests on
// (rule 5's check-before-read note): a gone hook writes nothing more. The
// agent's PostToolUse close of a request whose tool ran judges the request's
// hook with it (cmd/agent-director wires it into the hook verb).
func RelayHookGone(v RelayView, pr PermissionRow) bool {
	j := newRelayJudge(v)
	switch j.hook(pr) {
	case hookGone:
		return true
	case hookAlive:
		return false
	}
	return !j.now().Before(j.confirmBy(pr))
}

// nullableTime maps a zero time to nil (JSON null) and any other to a pointer
// to it.
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// permissionRequestInfo projects pr, with its delivery facts d, as one
// element of get's and list's permission_requests.
func permissionRequestInfo(pr PermissionRow, d RequestDelivery) PermissionRequestInfo {
	return PermissionRequestInfo{
		RequestID:       pr.RequestID,
		RequestToken:    pr.RequestToken,
		ToolName:        pr.ToolName,
		ToolInput:       pr.ToolInput,
		RequestedAt:     pr.CreatedAt,
		Decision:        nullableString(pr.Decision),
		DecisionReason:  nullableString(pr.DecisionReason),
		RequestDelivery: d,
	}
}

// openRequestInfos reads instanceID's open permission requests (those still
// awaiting an answer) through read, by the check-before-read rule, and
// projects them with their delivery facts at j's now. The caller collects
// those fallen back with no hook_gone_at (goneSlotsOf) for its one
// recordGone.
func openRequestInfos(read func(string) ([]PermissionRow, error), j relayJudge, instanceID string) ([]PermissionRequestInfo, error) {
	prs, verdicts, err := readManyJudged(func() ([]PermissionRow, error) { return read(instanceID) }, j)
	if err != nil {
		return nil, err
	}
	now := j.now()
	infos := make([]PermissionRequestInfo, 0, len(prs))
	for _, pr := range prs {
		infos = append(infos, permissionRequestInfo(pr, j.delivery(pr, verdicts[pr.RequestID], now)))
	}
	return infos, nil
}

// spawnRequestInfos reads every permission request of instanceID through
// read, once, by the check-before-read rule, and projects from that one read,
// oldest first (created_at, then request id), with their delivery facts at
// j's now: open, the requests that still await an answer
// (PermissionRow.AwaitsAnswer, as OpenPermissionRequestsForSpawn selects
// them), and unproven, those Claude Code has not proven gone (b.146 step 2c).
// A request in both lists is judged once and carries the same facts in each.
// Both are non-nil. The caller collects those fallen back with no
// hook_gone_at (goneSlotsOf) for its one recordGone.
func spawnRequestInfos(read func(string) ([]PermissionRow, error), j relayJudge, instanceID string) (open, unproven []PermissionRequestInfo, err error) {
	prs, verdicts, err := readManyJudged(func() ([]PermissionRow, error) { return read(instanceID) }, j)
	if err != nil {
		return nil, nil, err
	}
	prs = slices.Clone(prs)
	slices.SortStableFunc(prs, func(a, b PermissionRow) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.RequestID, b.RequestID)
	})
	now := j.now()
	open, unproven = []PermissionRequestInfo{}, []PermissionRequestInfo{}
	for _, pr := range prs {
		info := permissionRequestInfo(pr, j.delivery(pr, verdicts[pr.RequestID], now))
		if pr.AwaitsAnswer() {
			open = append(open, info)
		}
		if !pr.ProvenGone() {
			unproven = append(unproven, info)
		}
	}
	return open, unproven, nil
}

// goneSlotsOf appends to slots those of infos fallen back with no
// hook_gone_at. The slots point into infos, which the caller must not append
// to before recordGone.
func goneSlotsOf(slots []goneSlot, infos []PermissionRequestInfo) []goneSlot {
	for i := range infos {
		slots = addGone(slots, infos[i].RequestID, &infos[i].RequestDelivery)
	}
	return slots
}
