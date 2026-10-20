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

// permissionDisplay shares one all-agent evaluation across status/get/list.
// Sampling and persistence are best effort; an uncommitted observation returns
// the coherent display from the original row snapshot, including its detail.
func permissionDisplay(s any, v RelayView, settle store.SettleRead, row Spawn) store.StuckDisplay {
	previous := row.CommittedStuckDisplay()
	r, ok := s.(permissionStuckStore)
	if !ok || finishedState(row.State) || row.State == store.StatePending ||
		(row.State != store.StateCheckPermission && row.StuckObservation == "") {
		return previous
	}
	j := newRelayJudge(v)
	requests, verdicts, err := readManyJudged(func() ([]PermissionRow, error) { return r.PermissionRequestsForSpawnWithin(row.InstanceID, readerLockWait) }, j)
	if err != nil {
		return previous
	}
	now := j.now()
	samples := make([]store.PermissionSample, 0, len(requests))
	for _, pr := range requests {
		verdict := verdicts[pr.RequestID]
		d := j.delivery(pr, verdict, now)
		sample := store.PermissionSample{Request: pr}
		if d.Delivery == DeliveryFallenBack {
			switch {
			case !pr.FallbackAt.IsZero():
				sample.FallbackAt, sample.Basis = pr.FallbackAt, pr.FallbackBasis
			case row.Agent == store.AgentCursor && row.RelayMode == "off":
				sample.FallbackAt, sample.Basis = pr.AdmittedAt, store.FallbackAdmission
				// Historical admission has second precision but is still an
				// established admission fact, independent of read time.
				if sample.FallbackAt.IsZero() {
					sample.FallbackAt = pr.CreatedAt
				}
			case row.Agent == store.AgentCursor || verdict == hookCantTell || pr.PreV7():
				sample.FallbackAt, sample.Basis = d.ConfirmBy, store.FallbackDeadline
			case verdict == hookGone:
				sample.FallbackAt, sample.Basis = pr.HookGoneAt, store.FallbackProcess
				if sample.FallbackAt.IsZero() {
					sample.FallbackAt = now
				}
			}
		}
		if (pr.PreV7() && pr.CreatedAt.IsZero()) || (!sample.FallbackAt.IsZero() && (sample.FallbackAt.UnixMilli() <= 0 || sample.FallbackAt.Year() > 9999)) {
			sample.FallbackAt, sample.Basis = time.Time{}, ""
		}
		samples = append(samples, sample)
	}
	timeout := v.PermissionUnansweredTimeout
	if timeout <= 0 {
		timeout = config.DefaultPermissionUnansweredTimeoutSeconds * time.Second
	}
	display, err := r.ObservePermissionStuck(row, samples, now, timeout, settle)
	if err != nil {
		return previous
	}
	return display
}

func stuckInfo(d store.StuckDisplay) StuckInfo {
	if d.State != store.StateStuck {
		return StuckInfo{}
	}
	return StuckInfo{StuckReason: d.Reason, Since: time.UnixMilli(d.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z"), RequestToken: d.RequestToken, ToolName: d.ToolName}
}
