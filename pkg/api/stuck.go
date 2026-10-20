package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// StuckPermissionUnanswered identifies a current unanswered permission fallback.
const StuckPermissionUnanswered = store.StuckPermissionUnanswered

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
}

type permissionStuckStore interface {
	PermissionRequestsForSpawnWithin(string, time.Duration) ([]PermissionRow, error)
	ObservePermissionStuck(Spawn, []store.PermissionSample, time.Time, time.Duration, store.SettleRead) (store.StuckDisplay, error)
}

var (
	_ permissionStuckStore = (*store.Store)(nil)
	_ permissionStuckStore = (*store.CursorMissingReads)(nil)
)

// permissionDisplay shares one all-agent evaluation across status/get/list.
// Sampling and persistence are best effort; an uncommitted observation returns
// the coherent display from the original row snapshot, including its detail.
func permissionDisplay(s any, v RelayView, settle store.SettleRead, row Spawn) store.StuckDisplay {
	display, _ := permissionObservation(s, v, settle, row)
	return display
}

// judgedRequestSet is one check-before-read read of every request of a row,
// with its hook verdicts, which list reuses for the row's open requests.
type judgedRequestSet struct {
	rows     []PermissionRow
	verdicts map[int64]hookVerdict
}

// permissionObservation is permissionDisplay, also returning the requests it
// read; nil when it read none.
func permissionObservation(s any, v RelayView, settle store.SettleRead, row Spawn) (store.StuckDisplay, *judgedRequestSet) {
	previous := row.CommittedStuckDisplay()
	r, ok := s.(permissionStuckStore)
	if !ok || finishedState(row.State) || row.State == store.StatePending ||
		(row.State != store.StateCheckPermission && row.StuckObservation == "") {
		return previous, nil
	}
	j := newRelayJudge(v)
	requests, verdicts, err := readManyJudged(func() ([]PermissionRow, error) {
		return r.PermissionRequestsForSpawnWithin(row.InstanceID, readerLockWait)
	}, j)
	if err != nil {
		return previous, nil
	}
	judged := &judgedRequestSet{rows: requests, verdicts: verdicts}
	now := j.now()
	samples := make([]store.PermissionSample, 0, len(requests))
	for _, pr := range requests {
		verdict := verdicts[pr.RequestID]
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
		return previous, judged
	}
	return display, judged
}

func stuckInfo(d store.StuckDisplay) StuckInfo {
	if d.State != store.StateStuck {
		return StuckInfo{}
	}
	return StuckInfo{StuckReason: d.Reason, Since: time.UnixMilli(d.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z"), RequestToken: d.RequestToken, ToolName: d.ToolName}
}
