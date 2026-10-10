package api

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// RecordPaneAnswerStore is the narrow store surface RecordPaneAnswer needs
// (b.146 rule 13): the request read by its token, the row read, the read of
// every permission request of the row, the hook_gone_at write, the outside
// record and this store's id, which every label the lookup accepts ends with.
// *store.Store satisfies it.
type RecordPaneAnswerStore interface {
	// GetPermissionRequestByToken reads the request; an unknown token is
	// ErrPermissionRequestNotFound.
	GetPermissionRequestByToken(requestToken string) (PermissionRow, error)
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// PermissionRequestsForSpawn reads every permission request of the row.
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	// RecordHookGone records hook_gone_at on the requests with requestIDs
	// that have none yet and returns each one's stored value.
	RecordHookGone(at time.Time, maxWait time.Duration, requestIDs ...int64) (map[int64]time.Time, error)
	// RecordPaneOutside runs check inside one write transaction, then records
	// the request answered outside agent-director (store.RecordPaneOutside).
	RecordPaneOutside(instanceID, requestToken, claim string, maxWait time.Duration, check PaneCheck) (bool, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// The production store satisfies RecordPaneAnswerStore.
var _ RecordPaneAnswerStore = (*store.Store)(nil)

// RecordPaneAnswerParams is the typed parameter shape for the
// record-pane-answer verb (b.146 rule 13, decision 6 C).
type RecordPaneAnswerParams struct {
	// RequestToken is the token of the permission request answered outside
	// agent-director. Required. It names the request across every Spawn.
	RequestToken string `json:"request_token"`
	// As is the caller's claim of how it was answered: allow, deny, or
	// unknown when the caller did not see the answer. Required. It is stored,
	// never checked.
	As string `json:"as"`
	// ExpectPaneSHA256 is the pane_sha256 read-pane returned for the pane the
	// caller looked at; the agent's pane is captured again with the same
	// NLines (ANSI stripped) and compared byte for byte. Required.
	ExpectPaneSHA256 string `json:"expect_pane_sha256"`
	// NLines is the number of trailing pane lines ExpectPaneSHA256 is over:
	// read-pane's n_lines. 0 falls back to DefaultReadPaneLines (25).
	NLines int `json:"n_lines"`
}

// RecordPaneAnswerResult is the request as record-pane-answer recorded it.
type RecordPaneAnswerResult struct {
	// RequestToken is the request's token (echoed back).
	RequestToken string `json:"request_token"`
	// PaneAnswer is outside.
	PaneAnswer string `json:"pane_answer"`
	// PaneAs is the claim recorded: allow, deny or unknown.
	PaneAs string `json:"pane_as"`
	// Decision is the claim when it is allow or deny; null for unknown.
	Decision *string `json:"decision"`
	// DecisionReason is pane_outside.
	DecisionReason string `json:"decision_reason"`
}

// RecordPaneAnswerEnv is what RecordPaneAnswer judges the request with: the
// relay view (start-time reader, pid namespace, clock, relay window; the
// start-time reader also judges the lookup's server) and how long a pane
// answer's intent whose sender cannot be checked counts as in progress.
type RecordPaneAnswerEnv struct {
	// Relay judges the request's relay hook and a pane answer's sender.
	Relay RelayView
	// IntentHold is paneIntentHold's value.
	IntentHold time.Duration
}

// paneClaimMinAge is how long a request's relay hook must have been gone
// (from its hook_gone_at, or its confirm_by when that is earlier and the hook
// is judged by time: claimNotBefore) before record-pane-answer accepts a
// record for it (b.146 rule 13, decision 5): Claude Code draws a permission
// dialog only after the hook is gone, so a pane read in between may not show
// it yet.
const paneClaimMinAge = 2 * time.Second

// nothingRecorded is record-pane-answer's sentence for what a refusal did
// not do.
const nothingRecorded paneNothing = "nothing was recorded"

// RecordPaneAnswer is the verb-handler entry point for `agent-director
// record-pane-answer` (b.146 rule 13, decision 6 C): it records that a
// permission request that fell back was answered outside agent-director (by
// a person at tmux, for example), so plain send-keys is no longer refused on
// its account. It types nothing.
//
//   - Params: RequestToken, As (allow, deny or unknown) and ExpectPaneSHA256
//     (64 hex digits) are required, NLines not negative: else
//     ErrInvalidFlags, nothing read.
//   - The request is read by its token (ErrPermissionRequestNotFound), then
//     its row (ErrSpawnNotFound); a request of an ended or missing row is
//     closed (b.146 rule 12): ErrNoOpenPermissionRequest.
//   - The row's requests are read by the check-before-read rule and judged
//     (rule 14). Refused, in order: the request acked, closed or already
//     answered at the pane (ErrAlreadyDecided or ErrNoOpenPermissionRequest);
//     its relay hook may still answer it (ErrClaimTooSoon, not_before null
//     while the hook is seen running, its confirm_by plus 2 s when the hook
//     cannot be checked);
//     a pane answer on it still being sent (ErrPaneAnswerInProgress); its hook
//     not yet recorded gone (hook_gone_at is written now, waiting the store's
//     busy timeout, fail-open) or gone less than 2 s ago (ErrClaimTooSoon,
//     not_before hook_gone_at plus 2 s; for a hook that cannot be checked, or
//     a request recorded before schema v7, gone from its confirm_by at the
//     latest: not_before confirm_by plus 2 s when that is earlier).
//   - The row's unusable recorded name is ErrInternal; its socket, then one
//     lookup by its current label: Ours, one pane listing and the agent's
//     pane (a lost create reply's pane taken for this call only, never
//     written); Leftover ErrTmuxSessionConflict; Gone ErrTmuxCaptureFailed;
//     Can't tell the single-row verbs' shared mapping; nothing recorded.
//   - Under the store's write lock (RecordPaneOutside), the row and its
//     requests are read and judged again, the refusals above applied again,
//     the pane captured (NLines lines, ANSI stripped) and its SHA-256
//     compared with ExpectPaneSHA256 (ErrPaneChanged when it differs), and
//     the request recorded: pane_answer outside, pane_as As, decision As (null
//     for unknown), decision_reason pane_outside. A lock not taken within the
//     store's busy timeout is ErrStoreBusy, nothing recorded.
//
// A failed capture maps as read-pane's does, with "nothing was recorded".
// The tmux calls are at most one lookup, one pane listing, one capture and,
// after a capture failure other than a timeout, one follow-up lookup.
func RecordPaneAnswer(s RecordPaneAnswerStore, t ReadPaneTmux, env RecordPaneAnswerEnv, params RecordPaneAnswerParams) (RecordPaneAnswerResult, error) {
	hash, err := checkRecordPaneAnswerParams(params)
	if err != nil {
		return RecordPaneAnswerResult{}, err
	}
	nLines := params.NLines
	if nLines == 0 {
		nLines = DefaultReadPaneLines
	}
	pr, err := s.GetPermissionRequestByToken(params.RequestToken)
	if err != nil {
		return RecordPaneAnswerResult{}, err
	}
	row, err := s.GetSpawn(pr.ClaudeInstanceID)
	if err != nil {
		return RecordPaneAnswerResult{}, err
	}
	j := newRelayJudge(env.Relay)
	reqs, err := judgedRequests(func() ([]PermissionRow, error) {
		return s.PermissionRequestsForSpawn(row.ClaudeInstanceID)
	}, j, row)
	if err != nil {
		return RecordPaneAnswerResult{}, err
	}
	// A writing verb records hook_gone_at on the requests it finds fallen back
	// with none yet (b.146 rule 8); for the named one that starts its 2 s.
	reqs.recordFallenBackGone(s, store.DefaultLockWait)
	if err := reqs.claimRefusal(params.RequestToken, env.IntentHold); err != nil {
		return RecordPaneAnswerResult{}, err
	}

	if err := unusableNameError(row.TmuxSessionName); err != nil {
		return RecordPaneAnswerResult{}, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return RecordPaneAnswerResult{}, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	r := &recordPaneRun{paneRun{
		t: t, pc: env.Relay.Procs, row: row, storeID: s.StoreID(), socket: socket,
		gone: tmux.ErrTmuxCaptureFailed, nothing: nothingRecorded,
	}}
	paneID, launch, err := r.target()
	if err != nil {
		return RecordPaneAnswerResult{}, err
	}

	written, err := s.RecordPaneOutside(row.ClaudeInstanceID, params.RequestToken, params.As, store.DefaultLockWait,
		func(sp Spawn, rows []PermissionRow) error {
			if err := lockedRequests(j, sp, rows).claimRefusal(params.RequestToken, env.IntentHold); err != nil {
				return err
			}
			return r.paneMatches(t, paneID, launch, nLines, hash, params.RequestToken)
		})
	if err != nil {
		return RecordPaneAnswerResult{}, err
	}
	if !written {
		return RecordPaneAnswerResult{}, fmt.Errorf("%w: %s request %s no longer awaits an answer; %s",
			store.ErrNoOpenPermissionRequest, row.ClaudeInstanceID, params.RequestToken, nothingRecorded)
	}
	res := RecordPaneAnswerResult{
		RequestToken:   params.RequestToken,
		PaneAnswer:     store.PaneAnswerOutside,
		PaneAs:         params.As,
		DecisionReason: store.DecisionReasonPaneOutside,
	}
	if params.As != paneAsUnknown {
		res.Decision = nullableString(params.As)
	}
	return res, nil
}

// checkRecordPaneAnswerParams checks record-pane-answer's params and returns
// the expected hash, lowercase, or ErrInvalidFlags.
func checkRecordPaneAnswerParams(p RecordPaneAnswerParams) (string, error) {
	bad := func(format string, args ...any) (string, error) {
		return "", fmt.Errorf("%w: record-pane-answer: "+format+"; %s", append(append([]any{ErrInvalidFlags}, args...), nothingRecorded)...)
	}
	if p.RequestToken == "" {
		return bad("request_token is required")
	}
	switch p.As {
	case paneAsAllow, paneAsDeny, paneAsUnknown:
	default:
		return bad("as must be allow, deny or unknown, got %q", p.As)
	}
	if p.NLines < 0 {
		return bad("n_lines %d must not be negative", p.NLines)
	}
	h, ok := parsePaneSHA256(p.ExpectPaneSHA256)
	if !ok {
		return bad("expect_pane_sha256 %q is required, a SHA-256 in hex (64 hex digits)", p.ExpectPaneSHA256)
	}
	return h, nil
}

// claimRefusal is record-pane-answer's refusal of the request with token
// (b.146 rule 13), nil when a record may be accepted: not one of the row's
// requests, ErrNoOpenPermissionRequest; not open and fallen back,
// notFallenBackRefusal's, except while its relay hook may still answer it
// (not_confirmed), ErrClaimTooSoon (hookMayAnswerNotBefore's not_before:
// null while the hook is seen running, its confirm_by plus paneClaimMinAge
// when it cannot be checked); a pane answer on it still being sent,
// ErrPaneAnswerInProgress; its hook gone less than paneClaimMinAge ago,
// ErrClaimTooSoon with claimNotBefore's not_before (hook_gone_at plus
// paneClaimMinAge, or confirm_by plus paneClaimMinAge when that is earlier
// and the hook is judged by time).
func (r spawnRequests) claimRefusal(token string, hold time.Duration) error {
	id := r.sp.ClaudeInstanceID
	what := string(nothingRecorded)
	pr, ok := r.find(token)
	if !ok {
		return fmt.Errorf("%w: %s has no permission request %s; %s", store.ErrNoOpenPermissionRequest, id, token, what)
	}
	d := r.delivery(pr)
	if !r.closed(pr) && d.Delivery == DeliveryNotConfirmed {
		notBefore := hookMayAnswerNotBefore(d)
		return claimTooSoonError(id, pr, d, notBefore, notBefore != nil)
	}
	if err := r.notFallenBackRefusal(pr, what); err != nil {
		return err
	}
	if err := r.inProgressRefusal(pr, hold, what); err != nil {
		return err
	}
	if notBefore, byConfirm := r.claimNotBefore(pr, d); r.now.Before(notBefore) {
		return claimTooSoonError(id, pr, d, &notBefore, byConfirm)
	}
	return nil
}

// claimNotBefore is the earliest time record-pane-answer accepts a record for
// pr, fallen back with delivery d: paneClaimMinAge after its relay hook is
// gone. That counts from pr's hook_gone_at (now when none is on record: its
// write failed), or from d's confirm_by when the hook is judged by time (it
// cannot be checked, or pr was recorded before schema v7) and confirm_by is
// not later: such a hook is gone by confirm_by at the latest, whenever a
// reader first found it gone, so a retry at hookMayAnswerNotBefore's
// not_before is accepted. byConfirm reports that it counts from confirm_by.
// hook_gone_at keeps its meaning; it is not moved.
func (r spawnRequests) claimNotBefore(pr PermissionRow, d RequestDelivery) (notBefore time.Time, byConfirm bool) {
	goneAt := pr.HookGoneAt
	if goneAt.IsZero() {
		goneAt = r.now
	}
	if r.hookJudgedByTime(pr) && !goneAt.Before(d.ConfirmBy) {
		return d.ConfirmBy.Add(paneClaimMinAge), true
	}
	return goneAt.Add(paneClaimMinAge), false
}

// hookJudgedByTime reports whether pr's relay hook is judged by its
// confirm_by rather than by a process check (b.146 rules 5 and 14): pr was
// recorded before schema v7, or its hook cannot be checked.
func (r spawnRequests) hookJudgedByTime(pr PermissionRow) bool {
	return pr.PreV7() || r.verdicts[pr.RequestID] == hookCantTell
}

// hookMayAnswerNotBefore is ErrClaimTooSoon's not_before for a request whose
// relay hook may still answer it (delivery d not_confirmed): nil while the
// hook is seen running, since no time can be named for a live process's end;
// otherwise (the hook cannot be checked) its confirm_by plus paneClaimMinAge:
// such a hook is gone by confirm_by at the latest, and claimNotBefore counts
// its paneClaimMinAge from there once it has fallen back, so a retry at this
// time is not refused as too soon.
func hookMayAnswerNotBefore(d RequestDelivery) *time.Time {
	if d.HookAlive != nil && *d.HookAlive {
		return nil
	}
	notBefore := d.ConfirmBy.Add(paneClaimMinAge)
	return &notBefore
}

// claimTooSoonError is ErrClaimTooSoon for pr, with its err_details: d's
// hook_alive and hook_gone_at, and notBefore (nil while the relay hook is
// seen running). byConfirm says notBefore is d's confirm_by plus
// paneClaimMinAge for a hook that cannot be checked, whether it may still
// answer the request or has fallen back since: both read the same, so a retry
// just before notBefore gets the same refusal. Otherwise the hook has been
// gone less than paneClaimMinAge.
func claimTooSoonError(instanceID string, pr PermissionRow, d RequestDelivery, notBefore *time.Time, byConfirm bool) error {
	var why string
	switch {
	case notBefore == nil:
		why = "its relay hook runs and may still answer it"
	case byConfirm:
		why = fmt.Sprintf("its relay hook cannot be checked and may still answer it until its confirm_by %s, and Claude Code may not have drawn its dialog until %s after that (not before %s)",
			d.ConfirmBy.UTC().Format(time.RFC3339Nano), paneClaimMinAge, notBefore.UTC().Format(time.RFC3339Nano))
	default:
		why = fmt.Sprintf("its relay hook has been gone less than %s (not before %s): Claude Code may not have drawn its dialog yet",
			paneClaimMinAge, notBefore.UTC().Format(time.RFC3339Nano))
	}
	return &DetailedError{
		Err: fmt.Errorf("%w: %s request %s: %s; %s", ErrClaimTooSoon, instanceID, pr.RequestToken, why, nothingRecorded),
		Details: ClaimTooSoonDetails{
			RequestToken: pr.RequestToken,
			HookAlive:    d.HookAlive,
			HookGoneAt:   nullableTime(pr.HookGoneAt),
			NotBefore:    notBefore,
		},
	}
}

// recordPaneRun is one RecordPaneAnswer call's tmux phase: the pane verbs'
// shared run with read-pane's gone sentinel, "nothing was recorded", and no
// adoption write.
type recordPaneRun struct {
	paneRun
}

// target makes the lookup and, on Ours, the pane listing, and returns the
// agent's pane id with the launch view a capture failure's follow-up lookup
// uses, or the verb error, with nothing recorded. Only the current launch's
// session is read: a Leftover is ErrTmuxSessionConflict, as send-keys'
// refusal on a live row.
func (r *recordPaneRun) target() (string, tmux.Launch, error) {
	launch := r.launchFor(r.row.Identity)
	res := tmux.Lookup(r.t, r.pc, launch, r.row.TmuxSessionName)
	switch res.Verdict {
	case tmux.Ours:
		return r.ours(res, launch)
	case tmux.Leftover:
		return "", launch, paneLeftoverError(r.refusal(""), res.Leftovers, false)
	case tmux.Gone:
		return "", launch, r.goneError()
	}
	return "", launch, cantTellError(res, r.cantTellRefusal(tmux.CallLookup))
}

// paneMatches captures the agent's pane paneID (nLines lines, ANSI
// stripped) and compares its SHA-256 with hash: ErrPaneChanged when they
// differ. A failed capture maps as read-pane's does.
func (r *recordPaneRun) paneMatches(t ReadPaneTmux, paneID string, launch tmux.Launch, nLines int, hash, token string) error {
	got, err := capturePaneHash(t, r.socket, paneID, nLines)
	if err != nil {
		_, verr := paneActionFailureError(err, paneActionFailure{
			Call:    tmux.CallCapture,
			Gone:    r.gone,
			Pane:    r.refusal(r.row.TmuxSessionName),
			Refusal: r.cantTellRefusal(tmux.CallCapture),
		}, t, r.pc, launch)
		return verr
	}
	if got != hash {
		return paneChangedError(r.row.ClaudeInstanceID, token, nLines, string(nothingRecorded))
	}
	return nil
}

// RecordPaneAnswer records that a permission request which fell back was
// answered outside agent-director (b.146 rule 13): pane_answer outside,
// pane_as and decision the caller's claim As (decision null for unknown),
// decision_reason pane_outside. It types nothing. Plain send-keys is then no
// longer refused on the request's account. Pass As unknown unless the answer
// was seen: the claim is stored, never checked.
//
// It is accepted only when the request has fallen back (its relay hook gone
// and no verdict acked), no pane answer through agent-director on it is
// still being sent, its hook has been gone at least 2 s (Claude Code draws
// the dialog only after the hook ends), and the agent's pane still has the
// hash the caller read (read-pane's pane_sha256 with the same NLines).
//
// CLI: agent-director record-pane-answer
//
// Errors:
//   - [ErrInvalidFlags]: RequestToken missing, As not allow, deny or
//     unknown, ExpectPaneSHA256 missing or not 64 hex digits, or NLines
//     negative.
//   - [ErrPermissionRequestNotFound]: no request has the token.
//   - [ErrSpawnNotFound]: the request's row is gone.
//   - [ErrNoOpenPermissionRequest]: the request is closed (its row ended or
//     missing, or find-missing closed it); nothing was recorded.
//   - [ErrAlreadyDecided]: the request was acked, or already answered at the
//     pane; nothing was recorded.
//   - [ErrClaimTooSoon]: the request's relay hook may still answer it, or has
//     been gone less than 2 s; nothing was recorded. err_details:
//     [ClaimTooSoonDetails] (retry at not_before; null only while the hook is
//     seen running).
//   - [ErrPaneAnswerInProgress]: a pane answer through send-keys on the
//     request is still being sent; nothing was recorded. err_details:
//     [PaneAnswerInProgressDetails].
//   - [ErrPaneChanged]: the pane no longer has ExpectPaneSHA256; nothing was
//     recorded. err_details: [PaneChangedDetails].
//   - [ErrStoreBusy]: the store's write lock was not taken within its busy
//     timeout; nothing was recorded.
//   - [ErrTmuxCaptureFailed]: the row's tmux session is not there.
//   - [ErrTmuxSessionConflict]: the agent's pane was not found, a session an
//     earlier launch left behind is there, or tmux holds conflicting labels.
//   - [ErrTmuxUnresponsive]: tmux did not answer usably; nothing was
//     recorded.
//   - [ErrTmuxNotAvailable]: tmux could not be run, its socket is not
//     accessible to this user, or this is not the tmux server the agent was
//     launched on.
//
// Nondeterminism: the outcome depends on the clock and on whether the
// request's relay hook, and a pane answer's sender, runs.
func (c *Client) RecordPaneAnswer(params RecordPaneAnswerParams) (RecordPaneAnswerResult, error) {
	if err := c.checkClosed(); err != nil {
		return RecordPaneAnswerResult{}, err
	}
	env := c.sendKeysEnv()
	return RecordPaneAnswer(c.st, c.tmuxClient, RecordPaneAnswerEnv{Relay: env.Relay, IntentHold: env.IntentHold}, params)
}
