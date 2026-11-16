// Package busysend is the provider-neutral inbox coordinator (SR-2, SR-3.1,
// SR-3.2; M1, M2 P-C4.3): one finite, event-owned invocation turns a
// conversation's Held Messages into prompts through internal/store's guarded
// claim, issue, completion and release transactions, and a
// provider route issues each prompt to its captured destination. Every
// route (Claude's hooks now; later Send Modes, force and the Codex and Cursor
// routes) implements Route and reuses Coordinator; none builds a second inbox,
// queue or delivery engine.
//
// Imports go one way: a provider adapter depends on this package and on
// internal/store; the store's transactions and its pure prompt packing have no
// provider dependency. Nothing here runs from a read, a Lookup, sweep, store
// open, retention or elapsed time, and nothing starts a goroutine, timer or
// retry loop: an invocation runs only when a route's own eligible event (or an
// explicit delivery command) calls Deliver, and ends within its context and
// its original budgets.
package busysend

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// Event is one eligible event for an agent: an owned callback from that same
// agent's current Launch (startup or readiness, turn end, tool boundary,
// permission resolution or provider-input completion; SR-3.1), already
// authenticated by its route. Boundary selects the claim's prefix: turn_end
// claims every Held Message, tool_boundary the Steer Turn Messages ahead of
// the first Wait Turn Message (store.BusySendClaimRequest).
type Event struct {
	InstanceID string
	Boundary   store.BusySendBoundary
}

// Eligibility is a route's authoritative reading of its own boundary at an
// event (SR-3.3). It is advisory: the store's guards decide, comparing every
// captured fact again.
type Eligibility struct {
	// Wait is the structured cause that forbids an attempt now (a running
	// turn, a Dialog, compaction, earlier AD-owned input still unresolved,
	// unknown readiness or an unavailable route); "" when the boundary
	// authorizes one.
	Wait store.BusySendWaitReason
	// Destination is the Launch, provider session, route identity, connection
	// generation and turn the event authenticated. With Wait set, its
	// LaunchID still names the Launch whose waiting Messages record the cause.
	Destination store.BusySendDestination
	// RaceExposed is SR-3.3a's exposure: the boundary cannot exclude a
	// competing turn start or sibling continuation.
	RaceExposed bool
}

// Prepared is one immutable prompt of a frozen hand-over, ready for its route:
// the body rendered from the Messages' retained text with their separators
// (store.RenderBusySendPromptBody), its complete measured size, and the
// captured destination it may go to and nowhere else.
type Prepared struct {
	HandoverID    string
	PromptIndex   int
	PromptCount   int
	Destination   store.BusySendDestination
	Body          []byte
	RenderedBytes int64
	IssueDeadline time.Time
}

// Authority is a route's last look at its boundary before the store's issue
// commit: Wait is a cause that now forbids output ("" when none), and Issuer
// is the process that will write the prompt, which the issue commit records
// among the Launch's owned processes (nil when the route issues from
// no process of its own).
type Authority struct {
	Wait   store.BusySendWaitReason
	Issuer *store.BusySendIssuer
}

// Disposition is what a route observed of its one call for a prompt, bound to
// that prompt by the coordinator. Output separates complete output, proven no
// output (the route's qualified not-submitted), partial output and
// uncertainty; preparation, serialization, a hook's start or a wake-file touch
// is never complete. Order is qualified order evidence observed with complete
// output ("" for none), and UnprovenBoundary marks the route's qualified
// handed_over_not_proven boundary (Claude's complete keyless emission). More
// says the route can issue the frozen group's next prompt within this same
// invocation once this one's order is secured.
type Disposition struct {
	Output           store.BusySendOutput
	Order            store.BusySendOrderKind
	UnprovenBoundary bool
	More             bool
}

// Route is a provider's delivery route. A route never redirects a prepared
// prompt to a newer session, issues before the coordinator's verified issue
// commit, retries an ambiguous write, answers a permission, cancels
// a turn or types into a pane.
type Route interface {
	// Eligibility reads the route's authoritative facts for ev's boundary.
	Eligibility(ctx context.Context, ev Event) (Eligibility, error)
	// Measure returns the complete rendered size of a prompt with body, the
	// route's wrapper included (store.BusySendPromptMeasure's contract). It
	// is pure: the claim runs it inside its store transaction. The limits are
	// the capability table's; a route keeps none of its own.
	Measure(body []byte) int64
	// Authorize is the route's final authority check for p, before the
	// store's issue commit.
	Authorize(ctx context.Context, p Prepared) (Authority, error)
	// Issue makes the one irreversible call: p's complete payload to
	// p.Destination, and reports what it observed. It is called at most once
	// per prompt, only after the store committed p's intent and issue
	// revalidation (store.CommitBusySendIssue).
	Issue(ctx context.Context, p Prepared) Disposition
}
