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

// paneEvidencePresentSQL keeps a held turn start from skipping invalidation
// when its turn id is absent (or repeated) but pane evidence still exists.
const paneEvidencePresentSQL = `(pane_sha256 IS NOT NULL OR pane_since_ms IS NOT NULL OR pane_epoch IS NOT NULL
    OR CASE WHEN json_valid(display_observation) THEN
      json_extract(display_observation, '$.state') = 'idle_screen'
      OR json_extract(display_observation, '$.candidate') = 'idle_screen' ELSE 0 END)`

// invalidatePaneSet is the SET fragment every turn-start or pane-adoption
// write must use. It discards the old baseline and idle_screen candidate or
// display in the same statement, without changing permission ownership or
// raw-state settling. It preserves ordinary hook observation invalidation
// when the raw value changes. newStateSQL is the writer's raw-state expression,
// never input; adoption and held turns pass state to keep other observations.
// The writer must drain display exits and refresh families in its transaction.
func invalidatePaneSet(newStateSQL string) string {
	return `pane_sha256 = NULL, pane_since_ms = NULL, pane_epoch = NULL,
        display_exit = CASE WHEN ` + committedDisplayStateSQL + ` = 'idle_screen'
          THEN json_object('prior_state', 'idle_screen', 'new_state', ` + newStateSQL + `, 'at_ms', ` + nowMsSQL + `)
          ELSE display_exit END,
        display_evidence = CASE WHEN ` + committedDisplayStateSQL + ` = 'idle_screen' THEN NULL ELSE display_evidence END,
        display_observation = CASE WHEN json_valid(display_observation) THEN
            CASE WHEN json_extract(display_observation, '$.state') = 'idle_screen' THEN NULL
              WHEN json_extract(display_observation, '$.candidate') = 'idle_screen' OR state <> ` + newStateSQL + ` THEN
              CASE WHEN ` + committedDisplayStateSQL + ` = 'needs_intelligence'
                THEN json_set(display_observation, '$.candidate', '', '$.candidate_since_ms', 0) END
              ELSE display_observation END
          ELSE CASE WHEN state = ` + newStateSQL + ` THEN display_observation END END,
        observation_version = observation_version + 1`
}

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
// pane text, or "" when the capture failed or cannot be trusted, with Reason
// then the SR-4.5 reason it failed (unreadable when unset or invalid).
type PaneSample struct {
	SHA256 string
	Reason string
}

// failure is why sample established no display, "" for a successful sample.
func (p PaneSample) failure() string {
	switch {
	case p.SHA256 != "":
		return ""
	case ValidConfirmationReason(p.Reason):
		return p.Reason
	}
	return ReasonUnreadable
}

// paneBaseline is a row's committed pane baseline in its current permission
// epoch: the last committed fingerprint and the start of its unchanged
// period. The zero value is none.
type paneBaseline struct {
	sha256      string
	sinceMillis int64
}

// committedPaneBaseline is row's stored baseline when it belongs to the row's
// current permission epoch. Raw-state changes invalidate it by epoch; turn
// starts and pane adoptions clear it through invalidatePaneSet even when the
// raw state stays the same.
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
