package api

import (
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// StuckPermissionUnanswered identifies a current unanswered permission fallback.
const StuckPermissionUnanswered = store.StuckPermissionUnanswered

// StuckNoScreenChange identifies a working Claude or Codex row whose sampled
// pane fingerprints stayed identical for its no_screen_change timeout.
const StuckNoScreenChange = store.StuckNoScreenChange

// The no_screen_change_availability values: whether no_screen_change can
// ever apply to a row's agent. Unavailable is a capability outcome, never a
// stuck reason.
const (
	// NoScreenChangeAvailable: no_screen_change can apply to the row's agent
	// (Claude, Codex).
	NoScreenChangeAvailable = "available"
	// NoScreenChangeUnavailable: no_screen_change never applies to the row's
	// agent (Cursor), which is never sampled.
	NoScreenChangeUnavailable = "unavailable"
)

// noScreenChangeAvailability is the capability outcome of a row's agent, by
// the same rule that decides which rows are sampled
// (store.NoScreenChangeApplies).
func noScreenChangeAvailability(agent string) string {
	if store.NoScreenChangeApplies(agent) {
		return NoScreenChangeAvailable
	}
	return NoScreenChangeUnavailable
}

// StuckInfo is shared by get and status. Reason-specific fields are omitted
// together when the committed display is ordinary. Since uses exactly UTC ms.
type StuckInfo struct {
	// StuckReason is present only while displayed stuck.
	StuckReason string `json:"stuck_reason,omitempty"`
	// Since is actual committed display entry in RFC3339 UTC milliseconds.
	Since string `json:"since,omitempty"`
	// RequestToken selects the earliest qualifying fallback, then lexical token.
	RequestToken string `json:"request_token,omitempty"`
	// ToolName is the selected request tool.
	ToolName string `json:"tool_name,omitempty"`
	// UnchangedMinutes is the completed minutes, rounded down, of the
	// committed unchanged observation period; only while displayed
	// no_screen_change.
	UnchangedMinutes *int64 `json:"unchanged_minutes,omitempty"`
}

type permissionStuckStore interface {
	PermissionRequestsForSpawn(string) ([]PermissionRow, error)
	PermissionRequestsForSpawnWithin(string, time.Duration) ([]PermissionRow, error)
	ObservePermissionStuck(Spawn, []store.PermissionSample, time.Time, time.Duration, store.SettleRead) (store.StuckDisplay, error)
}

type paneStuckStore interface {
	ObservePaneStuck(Spawn, store.PaneSample, time.Time, time.Duration, store.SettleRead) (store.StuckDisplay, error)
}

var (
	_ permissionStuckStore = (*store.Store)(nil)
	_ permissionStuckStore = (*store.CursorMissingReads)(nil)
	_ paneStuckStore       = (*store.Store)(nil)
	_ paneStuckStore       = (*store.CursorMissingReads)(nil)
)

// stuckDisplay is status's evaluation through the one all-agent dispatch
// status, get and list share (observeStuck). Sampling and persistence are
// best effort; an uncommitted observation returns the coherent display from
// the original row snapshot, including its detail.
func stuckDisplay(s any, v RelayView, settle store.SettleRead, row Spawn) store.StuckDisplay {
	display, _, _ := observeStuck(s, v, settle, row, permissionRead{})
	return display
}

// observeStuck is stuckDisplay, also returning the permission requests it
// observed (nil when none) and whether its write found the store busy. A read
// whose earlier write found the store busy (read.storeBusy) samples, reads and
// writes nothing more and answers the committed display, pane rows included.
// Otherwise a working Claude or Codex row is observed by its pane. A reader
// that samples none (the exported verb functions) keeps such a row's current
// no_screen_change finding (NoScreenChangeCurrent) as committed, so it never
// settles out an episode, entry or reason change a sampling reader still
// sees. Every other row, such a row with no current finding included, is
// observed by its permission requests (those in read when the reader gives
// them), which also settles out a displayed episode of either reason left by
// an earlier interval.
func observeStuck(s any, v RelayView, settle store.SettleRead, row Spawn, read permissionRead) (store.StuckDisplay, *judgedRequestSet, bool) {
	if read.storeBusy {
		return row.CommittedStuckDisplay(), read.requests, true
	}
	if row.PaneObservable() {
		if r, ok := s.(paneStuckStore); ok && v.samplePane != nil {
			display, busy := paneObservation(r, v, settle, row)
			return display, nil, busy
		}
		if row.NoScreenChangeCurrent() {
			return row.CommittedStuckDisplay(), nil, false
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
// returning the requests it observed (nil when none) and whether its write
// found the store busy.
func permissionObservation(s any, v RelayView, settle store.SettleRead, row Spawn, read permissionRead) (store.StuckDisplay, *judgedRequestSet, bool) {
	previous := row.CommittedStuckDisplay()
	r, ok := s.(permissionStuckStore)
	if !ok || finishedState(row.State) || row.State == store.StatePending ||
		(row.State != store.StateCheckPermission && row.StuckObservation == "") {
		return previous, read.requests, false
	}
	j := newRelayJudge(v)
	judged := read.requests
	if judged == nil {
		var err error
		judged, err = readJudgedRequests(func(id string) ([]PermissionRow, error) {
			return r.PermissionRequestsForSpawnWithin(id, readerLockWait)
		}, j, row.InstanceID)
		if err != nil {
			return previous, nil, false
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
	timeout := v.PermissionUnansweredTimeout
	if timeout <= 0 {
		timeout = config.DefaultPermissionUnansweredTimeoutSeconds * time.Second
	}
	display, err := r.ObservePermissionStuck(row, samples, now, timeout, settle)
	if err != nil {
		return previous, judged, errors.Is(err, store.ErrStoreBusy)
	}
	return display, judged, false
}

// paneObservation observes a working Claude or Codex row by one sample of its
// own pane, against the row's own timeout override or else the reader's
// configured timeout. A failed capture is a sample that resets the baseline;
// an uncommitted sample returns the committed display, and whether its write
// found the store busy.
func paneObservation(r paneStuckStore, v RelayView, settle store.SettleRead, row Spawn) (store.StuckDisplay, bool) {
	sample := v.samplePane(row)
	timeout := v.noScreenChangeTimeout
	if row.NoScreenChangeTimeoutSeconds > 0 {
		timeout = time.Duration(row.NoScreenChangeTimeoutSeconds) * time.Second
	} else if timeout <= 0 {
		timeout = config.DefaultNoScreenChangeTimeoutSeconds * time.Second
	}
	display, err := r.ObservePaneStuck(row, sample, relayJudge{view: v}.now(), timeout, settle)
	if err != nil {
		return row.CommittedStuckDisplay(), errors.Is(err, store.ErrStoreBusy)
	}
	return display, false
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

// samplePane fingerprints row's own pane for its no_screen_change
// observation: the agent's pane of the row's current launch, found as
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

func stuckInfo(d store.StuckDisplay) StuckInfo {
	if d.State != store.StateStuck {
		return StuckInfo{}
	}
	info := StuckInfo{StuckReason: d.Reason, Since: time.UnixMilli(d.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z"), RequestToken: d.RequestToken, ToolName: d.ToolName}
	if d.Reason == store.StuckNoScreenChange {
		minutes := d.UnchangedMinutes
		info.UnchangedMinutes = &minutes
	}
	return info
}
