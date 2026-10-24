package store

import (
	"math"
	"time"
)

// paneColumns is the column fragment a read selects, after the other
// columns, to fill a row's timeout override and committed pane baseline, in
// paneScan.dest's order. Each column is selected bare and decoded by
// paneScan.apply, so no stored value fails a read.
const paneColumns = `idle_screen_timeout_seconds, pane_sha256, pane_since_ms, pane_epoch`

// maxTimeoutSeconds is the largest whole number of seconds a time.Duration
// holds; a stored override above it reads as none.
const maxTimeoutSeconds = math.MaxInt64 / int64(time.Second)

// paneScan receives one row's paneColumns.
type paneScan struct {
	override, sha256, since, epoch any
}

func (p *paneScan) dest() []any {
	return []any{&p.override, &p.sha256, &p.since, &p.epoch}
}

// apply decodes the scanned columns into sp: an override is a positive
// whole-second integer, a baseline a nonempty text fingerprint with a
// positive start; anything else, NULL included, reads as none.
func (p *paneScan) apply(sp *Spawn) {
	if v, ok := p.override.(int64); ok && v > 0 && v <= maxTimeoutSeconds {
		sp.IdleScreenTimeoutSeconds = int(v)
	}
	sp.PaneSHA256, _ = p.sha256.(string)
	if v, ok := p.since.(int64); ok && v > 0 {
		sp.PaneSinceMillis = v
	}
	sp.PaneEpoch = -1
	if v, ok := p.epoch.(int64); ok && v >= 0 {
		sp.PaneEpoch = v
	}
}

// PaneSample is one read's capture of a row's own pane: the SHA-256 of the
// pane text, or "" when the capture failed or cannot be trusted.
type PaneSample struct {
	SHA256 string
}

// paneBaseline is a row's committed pane baseline in its current permission
// epoch: the last committed fingerprint and the start of its unchanged
// period. The zero value is none.
type paneBaseline struct {
	sha256      string
	sinceMillis int64
}

// committedPaneBaseline is row's stored baseline when it belongs to the row's
// current permission epoch. One from an earlier turn or launch is none, so it
// can never supply evidence to the current one.
func committedPaneBaseline(row Spawn) paneBaseline {
	if row.PaneSHA256 == "" || row.PaneSinceMillis <= 0 || row.PaneEpoch != row.PermissionEpoch {
		return paneBaseline{}
	}
	return paneBaseline{sha256: row.PaneSHA256, sinceMillis: row.PaneSinceMillis}
}

// IdleScreenApplies reports whether idle_screen can ever apply to a row of
// agent: Claude and Codex. Cursor, whose healthy quiet work can leave its pane
// unchanged, never is. This is the sampling and eligibility rule; the API's
// availability projection separately preserves compatibility for an absent
// agent without allowing its pane to be sampled.
func IdleScreenApplies(agent string) bool {
	return agent == AgentClaude || agent == AgentCodex
}

// PaneObservable reports whether the row's pane is observed for idle_screen:
// a raw working row whose agent IdleScreenApplies.
func (sp Spawn) PaneObservable() bool {
	return sp.State == StateWorking && IdleScreenApplies(sp.Agent)
}

// IdleScreenCurrent reports whether sp's stored observation is its pane's
// current idle_screen finding: sp is observed (PaneObservable) and the
// observation, committed in sp's current permission epoch, has an idle_screen
// candidate. On an observed row only a pane observation commits one in that
// epoch, so it is that observation's finding for this interval: an entry
// settling in, a displayed episode, or a move in from a displayed
// needs_intelligence, which applies only to an observation an earlier release
// stored, since no read enters needs_intelligence. A display an earlier
// interval left (a hook-ended needs_intelligence, an earlier turn's
// idle_screen) is not one.
func (sp Spawn) IdleScreenCurrent() bool {
	o, ok := decodeDisplayObservation(sp.DisplayObservation)
	return ok && sp.PaneObservable() && o.PermissionEpoch == sp.PermissionEpoch && o.Candidate == StateIdleScreen
}

// planPane judges sample against row's committed baseline at now: the
// baseline to store and, once its unchanged period has reached timeout, the
// idle_screen target with the completed minutes of that period. A failed
// sample, or a row that is not observed, resets the baseline; an identical
// sample keeps its start; a changed one starts a new period now.
func planPane(row Spawn, sample PaneSample, now time.Time, timeout time.Duration) (paneBaseline, DisplayDetail) {
	if sample.SHA256 == "" || !row.PaneObservable() {
		return paneBaseline{}, DisplayDetail{}
	}
	current := committedPaneBaseline(row)
	if current.sha256 != sample.SHA256 {
		return paneBaseline{sha256: sample.SHA256, sinceMillis: now.UnixMilli()}, DisplayDetail{}
	}
	unchanged := now.Sub(time.UnixMilli(current.sinceMillis))
	if unchanged < timeout {
		return current, DisplayDetail{}
	}
	return current, DisplayDetail{State: StateIdleScreen, UnchangedMinutes: int64(unchanged / time.Minute)}
}

// paneBaselineArgs is baseline as the bound pane_sha256, pane_since_ms and
// pane_epoch of a row in epoch, all NULL for none.
func paneBaselineArgs(baseline paneBaseline, epoch int64) []any {
	if baseline == (paneBaseline{}) {
		return []any{nil, nil, nil}
	}
	return []any{baseline.sha256, baseline.sinceMillis, epoch}
}
