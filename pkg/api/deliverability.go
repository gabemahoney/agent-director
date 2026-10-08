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
// The Decide contract consults this to refuse recording a success for a
// request whose delivery is already unsafe (fail-early). The send_keys guard
// does NOT use this function; it uses the guard-release sibling below.
func RelayRequestUndeliverable(createdAt time.Time, effectiveWindow time.Duration, now time.Time) bool {
	return !createdAt.After(RelayDeliverabilityCutoff(now, effectiveWindow))
}

// Deliberate asymmetry at the delivery-window boundary (SR-4.2, SR-4.4).
//
// Decide and the send_keys guard sit on opposite sides of the same race — a
// keystroke inserted into the pane while the relay poller might still emit a
// decision — and each must fail toward safety, which points in OPPOSITE
// directions:
//
//   - Decide must not record a success for a request whose delivery is about
//     to fail, so it refuses EARLY: at elapsed >= window - margin
//     (RelayDeliverabilityCutoff / RelayRequestUndeliverable). Refusing a
//     hair too early is safe — the caller simply learns the relay is unsafe.
//
//   - The send_keys guard must not release while the poller could still be
//     alive and emit, so it releases LATE: at elapsed >= window + margin +
//     createdAtResolution (RelayGuardReleaseCutoff /
//     RelayRequestGuardReleasable; relayGuardHold), the instant a relay hook
//     is presumed settled (relayHookSettledAt, below). Releasing at window -
//     margin would free the guard while a live poller can still emit — the
//     exact keystroke/envelope race the guard exists to prevent. Holding a
//     hair too long is safe — the operator waits marginally longer to recover
//     a genuinely-dead relay.
//
// Both boundaries live in THIS file and share the SAME RelayKillSafetyMargin
// constant, so SR-4.4's "single time-based authority" holds: one file, one
// margin, one place to change the boundary — just applied with the sign that
// makes each caller fail safe.
//
// Why the guard's release is late enough (b.z6g). Every instant here is
// counted from the stored created_at, which keeps whole seconds: it is the
// second in which the relay hook inserted the request, so the insert came
// less than createdAtResolution after it, and Claude Code started the hook
// before the insert. A relay hook still alive at the end of the window ends
// in one of two ways:
//
//   - At its poll deadline, created_at + window: internal/hook's Poll counts
//     the window from the stored created_at too. It then records its timeout
//     deny, moves the row to working and returns the deny to Claude Code,
//     which closes the dialog. The guard's release leaves that timeout path
//     margin + createdAtResolution (2 s) to complete in.
//   - At Claude Code's kill, its per-hook timeout of the same window, armed
//     when Claude Code started the hook: by created_at + window +
//     createdAtResolution, plus however late the kill comes, which the margin
//     covers. A killed hook's output is discarded, so it closes no dialog.
//
// A request decided in its window has its verdict recorded before
// created_at + window - margin (Decide's boundary), so its hook has had more
// than twice the margin plus createdAtResolution (3 s) to read the verdict
// and return it, which closes the dialog. So at the guard's release a live
// hook has returned its verdict or deny, or is dead, unless both its delivery
// of its verdict or timeout deny ended more than margin + createdAtResolution
// past created_at + window (its deny and working writes wait for the store's
// write lock, each up to the store busy timeout, or the process stalled) and
// Claude Code killed it more than the margin late. That residual race is not
// closed here: nothing stored shows a hook stuck delivering.
//
// From Decide's boundary to the guard's release (window - margin to window +
// margin + createdAtResolution) Decide refuses but the relay poller may still
// be alive: at its poll deadline it denies the request, which closes Claude
// Code's permission dialog and moves the row to working, where the send_keys
// guard no longer applies. Decide therefore does not answer a refusal inside
// that span at once: it waits until relayHookSettledAt, the guard's release
// on the request's account, and reads the request again, so
// ErrRelayFallenBack ("answer at the pane") is returned only for a request
// whose row is still open once its poller is presumed no longer able to
// answer it (b.pzy).
//
// The span is agent-director's to absorb, not the caller's to time (b.ah6):
// no runtime caller-facing text (error messages, manifest Descriptions)
// states either boundary or the margin. A send_keys refused while the open
// request holds the guard names an open request (namedBefore) and is told to
// answer it with decide, and decide's wait ends as the guard releases on
// that request's account, so a pane answer that follows its
// ErrRelayFallenBack is not refused on that request's account, nor on
// account of a decided request of the same Spawn (relayRequestFallenBack,
// b.ceq).

// RelayGuardReleaseCutoff returns the created_at cutoff instant separating rows
// whose delivery window has provably elapsed (guard may release) from rows that
// might still be delivered (guard holds on their account, but for
// evaluateRelayGuard's decided-row exception, b.ceq) at time now. A row is
// guard-releasable iff its created_at is at or before this cutoff. The cutoff
// is now less relayGuardHold (the effective relay window PLUS
// RelayKillSafetyMargin plus createdAtResolution) — i.e. the created_at whose
// relayHookSettledAt lands exactly at now. This is the deliberate mirror of
// RelayDeliverabilityCutoff (which subtracts the margin); see the asymmetry
// note above.
//
// It shares the single-authority contract of RelayDeliverabilityCutoff: any
// caller applying this boundary MUST obtain the cutoff here rather than
// restating the window +/- margin arithmetic elsewhere.
func RelayGuardReleaseCutoff(now time.Time, effectiveWindow time.Duration) time.Time {
	return now.Add(-relayGuardHold(effectiveWindow))
}

// relayGuardHold is how long after a request's created_at its relay hook may
// still answer it (see the asymmetry note above): the effective relay window
// plus RelayKillSafetyMargin plus createdAtResolution. The send_keys relay
// guard holds that long on the request's account (RelayGuardReleaseCutoff),
// and Decide waits until then before it names a refusal (relayHookSettledAt),
// so the arithmetic is stated once and the two cannot drift apart.
func relayGuardHold(effectiveWindow time.Duration) time.Duration {
	return effectiveWindow + RelayKillSafetyMargin + createdAtResolution
}

// createdAtResolution is the storage resolution of a permission request's
// created_at. The column defaults to SQLite's CURRENT_TIMESTAMP
// (internal/store/schema.go), which keeps whole seconds only, so a stored
// created_at can be up to this much earlier than the instant the relay hook
// inserted the row. Claude Code arms the hook's kill, its per-hook timeout of
// the relay window, when it starts the hook, before that insert, so counted
// from created_at the kill can come up to this much later than created_at +
// window, besides the kill's own lateness RelayKillSafetyMargin covers.
// relayGuardHold adds it, so the send_keys guard and Decide's wait outlast
// that kill; the hook's own poll deadline is counted from the stored
// created_at (internal/hook's Poll, b.z6g), so for its timeout deny it is
// slack. The deliverability boundary above does not add it.
const createdAtResolution = 1 * time.Second

// relayHookSettledAt returns the instant by which a relay hook is presumed to
// have answered a request created at createdAt if it ever will: created_at
// plus relayGuardHold. By then a hook still alive at its poll deadline is
// presumed to have recorded its timeout deny, and a hook that never reached
// it to have been killed by Claude Code (see the asymmetry note above), so a
// row still open then is presumed to have no live poller left to answer it.
// It is also the instant from which RelayRequestGuardReleasable holds for
// the request. Decide waits until this instant before it names a refusal it
// made earlier; from its own boundary that is at most twice
// RelayKillSafetyMargin plus createdAtResolution (3 s).
func relayHookSettledAt(createdAt time.Time, effectiveWindow time.Duration) time.Time {
	return createdAt.Add(relayGuardHold(effectiveWindow))
}

// relayRequestFallenBack reports whether permission request pr has fallen back
// at now: its record is still open (no decision) at or after its
// relayHookSettledAt, so its relay hook is presumed dead and only a pane
// answer can close its dialog. It is the one definition of "fallen back":
// Decide returns ErrRelayFallenBack for exactly such a request (after its
// wait), and the send_keys guard stops holding on account of a decided
// request once another request of the same Spawn has fallen back (b.ceq). A
// decided request has not fallen back. Like its siblings it is a pure
// function of the row, the resolved effective relay window and the injected
// now.
func relayRequestFallenBack(pr PermissionRow, effectiveWindow time.Duration, now time.Time) bool {
	return pr.Decision == "" && !now.Before(relayHookSettledAt(pr.CreatedAt, effectiveWindow))
}

// RelayRequestGuardReleasable is the single authority (SR-4.4) answering, for a
// permission-request row, whether the send_keys relay guard may RELEASE on that
// row's account — i.e. whether the delivering poller is provably dead so a
// pane-side keystroke can no longer race a decision write. It is the fail-late
// mirror of RelayRequestUndeliverable: a row is guard-releasable once its
// created_at is at or before the guard-release cutoff (now - (window + margin
// + createdAtResolution)), that is from its relayHookSettledAt on.
//
// Like its sibling this is a pure function of the row's created_at, the
// resolved effective relay window, and the injected now; it performs no I/O.
// The send_keys guard MUST consult this (not RelayRequestUndeliverable) so it
// holds through the full window plus the safety margin and created_at's
// resolution.
func RelayRequestGuardReleasable(createdAt time.Time, effectiveWindow time.Duration, now time.Time) bool {
	return !createdAt.After(RelayGuardReleaseCutoff(now, effectiveWindow))
}
