package api

import (
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The two Displayed States: a current unanswered permission fallback, which a
// human or orchestrating agent must answer, and a working Claude or Codex row
// whose sampled pane fingerprints stayed identical for its idle_screen
// timeout. Both are report-only; command guards act on the raw state.
const (
	// StateNeedsIntelligence is needs_intelligence: a permission fallback is
	// still unanswered.
	StateNeedsIntelligence = store.StateNeedsIntelligence
	// StateIdleScreen is idle_screen: a working row's pane stopped changing
	// for its idle_screen timeout.
	StateIdleScreen = store.StateIdleScreen
)

// The idle_screen_availability values report agent capability, with available
// retained for compatibility when a custom store supplies no agent.
// Unavailable is a capability outcome, never a state.
const (
	// IdleScreenAvailable: idle_screen can apply to the row's agent (Claude,
	// Codex), or the store supplies no agent. Only known supported agents
	// may be sampled.
	IdleScreenAvailable = "available"
	// IdleScreenUnavailable: idle_screen never applies to the row's agent
	// (Cursor), which is never sampled.
	IdleScreenUnavailable = "unavailable"
)

// idleScreenAvailability preserves empty-agent compatibility independently of
// pane eligibility: store.IdleScreenApplies still admits only Claude and Codex.
func idleScreenAvailability(agent string) string {
	if agent == "" || store.IdleScreenApplies(agent) {
		return IdleScreenAvailable
	}
	return IdleScreenUnavailable
}

// StateDetail is shared by get and status: the detail of the Displayed State
// shown, every field omitted while the committed display is ordinary. Since
// uses exactly UTC ms.
type StateDetail struct {
	// Since is actual committed display entry in RFC3339 UTC milliseconds.
	Since string `json:"since,omitempty"`
	// RequestToken selects the earliest qualifying fallback, then lexical
	// token; only while needs_intelligence.
	RequestToken string `json:"request_token,omitempty"`
	// ToolName is the selected request tool; only while needs_intelligence.
	ToolName string `json:"tool_name,omitempty"`
	// UnchangedMinutes is the completed minutes, rounded down, of the
	// committed unchanged observation period; only while idle_screen.
	UnchangedMinutes *int64 `json:"unchanged_minutes,omitempty"`
}

// The observers also return the families their commit refreshed, by instance
// id under the reader's SettleRead, so a committed exit reports its display,
// detail and families from that one commit, with no later read to fail.
type needsIntelligenceStore interface {
	PermissionRequestsForSpawn(string) ([]PermissionRow, error)
	PermissionRequestsForSpawnWithin(string, time.Duration) ([]PermissionRow, error)
	ObserveNeedsIntelligence(Spawn, []store.PermissionSample, time.Time, time.Duration, store.SettleRead) (store.DisplayDetail, map[string]string, error)
}

type idleScreenStore interface {
	ObserveIdleScreen(Spawn, store.PaneSample, time.Time, time.Duration, store.SettleRead) (store.DisplayDetail, map[string]string, error)
}

var (
	_ needsIntelligenceStore = (*store.Store)(nil)
	_ needsIntelligenceStore = (*store.CursorMissingReads)(nil)
	_ idleScreenStore        = (*store.Store)(nil)
	_ idleScreenStore        = (*store.CursorMissingReads)(nil)
)

// displayed is status's evaluation through the one all-agent dispatch status,
// get and list share (observeDisplay), with the row's family. Sampling and
// persistence are best effort; an uncommitted observation returns the
// display and family of the original row snapshot, detail included.
func displayed(s any, v RelayView, settle store.SettleRead, row Spawn) (store.DisplayDetail, string) {
	display, families, _, _ := observeDisplay(s, v, settle, row, permissionRead{})
	return display, familyOf(row, families)
}

// familyOf is row's family as an observation's commit refreshed it, else as
// row's own snapshot read it.
func familyOf(row Spawn, families map[string]string) string {
	if family, ok := families[row.InstanceID]; ok {
		return family
	}
	return row.FamilyState
}

// observeDisplay returns the display, the families its commit refreshed (nil
// when none), the permission requests it observed (nil when none) and
// whether its write found the store busy. A read
// whose earlier write found the store busy (read.storeBusy) samples, reads and
// writes nothing more and answers the committed display, pane rows included.
// Otherwise a working Claude or Codex row is observed by its pane. A reader
// that samples none (the exported verb functions) keeps such a row's current
// idle_screen finding (IdleScreenCurrent) as committed, so it never settles
// out an episode, entry or move a sampling reader still sees. Every other row,
// such a row with no current finding included, is observed by its permission
// requests (those in read when the reader gives them), which also settles out
// a display of either state left by an earlier interval.
func observeDisplay(s any, v RelayView, settle store.SettleRead, row Spawn, read permissionRead) (store.DisplayDetail, map[string]string, *judgedRequestSet, bool) {
	if read.storeBusy {
		return row.CommittedDisplay(), nil, read.requests, true
	}
	if row.PaneObservable() {
		if r, ok := s.(idleScreenStore); ok && v.samplePane != nil {
			display, families, busy := paneObservation(r, v, settle, row)
			return display, families, nil, busy
		}
		if row.IdleScreenCurrent() {
			return row.CommittedDisplay(), nil, nil, false
		}
	}
	return permissionObservation(s, v, settle, row, read)
}

// judgedRequestSet is one check-before-read read of every request of a row,
// with its hook verdicts, which get and list reuse for the row's request
// lists and its permission observation.
type judgedRequestSet struct {
	rows     []PermissionRow
	verdicts map[int64]hookVerdict
}

// permissionRead is what a reader gives a row's observation, so the
// observation repeats no store work the read has done. requests are the
// row's requests as the reader's own check-before-read read took them, still
// as stored; nil makes the observation read them itself, without waiting.
// storeBusy is set when a nonwaiting write of the same read has just found
// the store busy (ErrStoreBusy): the observation's write most likely could not
// commit either (a busy refusal, such as at the connection admission, can be
// momentary), so it answers the committed display without attempting one and
// the next read writes. Under a held write lock a get makes one write attempt
// in all; a list makes one per observed row and no hook_gone_at write after a
// busy one.
type permissionRead struct {
	requests  *judgedRequestSet
	storeBusy bool
}

// permissionObservation observes row by its permission requests, also
// returning the families its commit refreshed, the requests it observed (nil
// when none) and whether its write found the store busy.
func permissionObservation(s any, v RelayView, settle store.SettleRead, row Spawn, read permissionRead) (store.DisplayDetail, map[string]string, *judgedRequestSet, bool) {
	previous := row.CommittedDisplay()
	r, ok := s.(needsIntelligenceStore)
	if !ok || finishedState(row.State) || row.State == store.StatePending ||
		(row.State != store.StateCheckPermission && row.DisplayObservation == "") {
		return previous, nil, read.requests, false
	}
	j := newRelayJudge(v)
	judged := read.requests
	if judged == nil {
		var err error
		judged, err = readJudgedRequests(func(id string) ([]PermissionRow, error) {
			return r.PermissionRequestsForSpawnWithin(id, readerLockWait)
		}, j, row.InstanceID)
		if err != nil {
			return previous, nil, nil, false
		}
	}
	now := j.now()
	samples := make([]store.PermissionSample, 0, len(judged.rows))
	for _, pr := range judged.rows {
		verdict := judged.verdicts[pr.RequestID]
		d := j.delivery(pr, verdict, now)
		sample := store.PermissionSample{Request: pr}
		if d.Delivery == DeliveryFallenBack {
			sample.FallbackAt, sample.Basis = store.FallbackClock(row.Agent, row.RelayMode, pr, d.fallbackObservation, now)
		}
		samples = append(samples, sample)
	}
	timeout := v.NeedsIntelligenceTimeout
	if timeout <= 0 {
		timeout = config.DefaultNeedsIntelligenceTimeoutSeconds * time.Second
	}
	display, families, err := r.ObserveNeedsIntelligence(row, samples, now, timeout, settle)
	if err != nil {
		return previous, nil, judged, errors.Is(err, store.ErrStoreBusy)
	}
	return display, families, judged, false
}

// paneObservation observes a working Claude or Codex row by one sample of its
// own pane, against the row's own timeout override or else the reader's
// configured timeout. A failed capture is a sample that resets the baseline;
// an uncommitted sample returns the committed display. It also returns the
// families its commit refreshed and whether its write found the store busy.
func paneObservation(r idleScreenStore, v RelayView, settle store.SettleRead, row Spawn) (store.DisplayDetail, map[string]string, bool) {
	sample := v.samplePane(row)
	timeout := v.idleScreenTimeout
	if row.IdleScreenTimeoutSeconds > 0 {
		timeout = time.Duration(row.IdleScreenTimeoutSeconds) * time.Second
	} else if timeout <= 0 {
		timeout = config.DefaultIdleScreenTimeoutSeconds * time.Second
	}
	display, families, err := r.ObserveIdleScreen(row, sample, relayJudge{view: v}.now(), timeout, settle)
	if err != nil {
		return row.CommittedDisplay(), nil, errors.Is(err, store.ErrStoreBusy)
	}
	return display, families, false
}

// paneSampler is the samplePane of one read's RelayView. Once a socket's tmux
// stops answering a row's lookup or pane listing (tmux.Result.StopsSocket, as
// a sweep stops), every later row of the read on that socket gets no sample,
// the reset a failed capture persists, without another tmux call: an
// unresponsive server costs a list one lookup or listing timeout per socket,
// not one per row (a capture timeout does not stop the socket). Like the read, it is not for concurrent use.
func (c *Client) paneSampler() func(Spawn) store.PaneSample {
	stopped := map[string]bool{}
	return func(row Spawn) store.PaneSample { return c.samplePane(row, stopped) }
}

// samplePane fingerprints row's own pane for its idle_screen observation: the
// agent's pane of the row's current launch, found as
// read-pane finds it and captured as read-pane's default read
// (DefaultReadPaneLines, ANSI stripped), so the fingerprint is the
// pane_sha256 such a read returns. Only that pane counts: a session an earlier
// launch left behind, a gone or unreadable session, a pane not found and a
// failed capture are no sample, and so is a row on a socket in stopped, which
// a lookup or pane listing that stops its socket joins. It writes nothing to
// the store and reads only the fingerprint, never what the pane says.
func (c *Client) samplePane(row Spawn, stopped map[string]bool) store.PaneSample {
	if unusableNameError(row.TmuxSessionName) != nil {
		return store.PaneSample{}
	}
	socket, err := rowSocket(row.Identity.Socket)
	if err != nil || stopped[socket] {
		return store.PaneSample{}
	}
	r := &paneRun{t: c.tmuxClient, pc: c.procChecker, row: row, storeID: c.st.StoreID(), socket: socket,
		gone: tmux.ErrTmuxCaptureFailed, nothing: nothingRead}
	launch := r.launchFor(row.Identity)
	res := tmux.Lookup(c.tmuxClient, c.procChecker, launch, "")
	if res.Verdict != tmux.Ours {
		stopped[socket] = res.StopsSocket()
		return store.PaneSample{}
	}
	paneID, _, err := r.ours(res, launch)
	if err != nil {
		stopped[socket] = r.listing.StopsSocket()
		return store.PaneSample{}
	}
	pane, err := c.tmuxClient.CapturePaneID(socket, paneID, DefaultReadPaneLines, false)
	if err != nil {
		return store.PaneSample{}
	}
	return store.PaneSample{SHA256: paneSHA256(tmux.StripANSI(pane))}
}

// stateDetail is d's detail as status and get report it: only the fields of
// the Displayed State shown, none for an ordinary state.
func stateDetail(d store.DisplayDetail) StateDetail {
	var detail StateDetail
	switch d.State {
	case StateNeedsIntelligence:
		detail = StateDetail{RequestToken: d.RequestToken, ToolName: d.ToolName}
	case StateIdleScreen:
		minutes := d.UnchangedMinutes
		detail = StateDetail{UnchangedMinutes: &minutes}
	default:
		return StateDetail{}
	}
	detail.Since = time.UnixMilli(d.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z")
	return detail
}
