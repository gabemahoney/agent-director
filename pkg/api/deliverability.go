package api

import (
	"time"
)

// RelayKillSafetyMargin is the epsilon subtracted from the effective relay
// window when deciding whether a permission request is still deliverable
// (SR-3.4). A row whose age is within this margin of the deadline is treated
// as already-undeliverable so Decide never records a success for a request
// Claude Code is about to — or has just — killed.
//
// Rationale: the per-hook kill is deterministic at the configured window
// (Epic 1 emits relay.timeout_seconds as the per-hook `timeout`, default
// 86400s; the poll loop's own kill boundary was measured at 599.6–600.07s
// against a nominal 600s window in the b.kk3 timing study). The ~0.4s
// observed undershoot plus the absence of any millisecond-level clock
// agreement between the AD process, the hook process, and Claude Code means
// the honest deliverability boundary is the deadline minus a small margin,
// not the deadline itself. One second comfortably covers the measured
// undershoot and sub-second cross-process clock skew without materially
// shrinking a 24h (or even a 600s) window.
const RelayKillSafetyMargin = 1 * time.Second

// RelayDeliverabilityCutoff returns the created_at cutoff instant that
// separates deliverable from undeliverable permission requests at time now: a
// row is deliverable iff its created_at is strictly after this cutoff. The
// cutoff is now less the effective relay window plus RelayKillSafetyMargin
// (equivalently, the created_at whose deliverability deadline lands exactly at
// now).
//
// It is the single authority (SR-4.4) for the boundary value — any caller that
// must apply the boundary in SQL (e.g. a `created_at > ?` WHERE term) MUST
// obtain the cutoff from this function and pass it in as a parameter, so the
// boundary + safety-margin logic is never restated in SQL or anywhere else.
//
// effectiveWindow is the already-resolved relay window; callers MUST obtain it
// from config.Relay.EffectiveTimeoutSeconds (Epic 1's accessor) — this function
// deliberately takes the resolved value rather than the config type so it
// stays free of internal-package references and never restates the
// non-positive → DefaultRelayTimeoutSeconds fallback rule.
func RelayDeliverabilityCutoff(now time.Time, effectiveWindow time.Duration) time.Time {
	return now.Add(-(effectiveWindow - RelayKillSafetyMargin))
}

// RelayRequestUndeliverable is the single authority (SR-4.4) answering, for a
// permission-request row, whether the relay window that would deliver its
// decision has elapsed. It is a pure function of stored row state plus config
// plus an injected clock (SR-2.3): the row's created_at, the effective relay
// window (resolved via Epic 1's accessor by the caller), and the
// caller-supplied now. The verdict is computable without the dead hook ever
// writing anything (SR-2.2).
//
// The determination is strictly TIME-based. The corrected liveness model is
// time-only: a native permission dialog's on-screen visibility carries no
// information about whether the relay hook that would deliver a decision is
// still alive, so nothing dialog- or state-derived may enter this function.
// A row is undeliverable once its created_at is at or before the cutoff
// (created_at > cutoff means deliverable), matching the SQL predicate a
// guarded write applies.
//
// This function performs no I/O and no DB writes; it only reads its arguments.
// Both this Epic's Decide contract and Epic t1.kk3.up's send_keys guard
// release MUST consult this same function rather than re-deriving the boundary.
func RelayRequestUndeliverable(createdAt time.Time, effectiveWindow time.Duration, now time.Time) bool {
	return !createdAt.After(RelayDeliverabilityCutoff(now, effectiveWindow))
}
