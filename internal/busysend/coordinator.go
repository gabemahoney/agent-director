package busysend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// Store is the inbox's guarded transactions the coordinator drives;
// *store.Store implements it. Each call is one transaction bounded by its
// context; an error that store.CommitUnconfirmed reports means its commit may
// or may not have landed, which authorizes nothing.
type Store interface {
	ClaimBusySendHandover(context.Context, store.BusySendClaimRequest) (store.BusySendClaim, error)
	CommitBusySendIssue(context.Context, store.BusySendIssueRequest) (store.BusySendIssue, error)
	RecordBusySendCompletion(context.Context, store.BusySendCompletion) (store.BusySendCompletionResult, error)
	ReleaseBusySendClaims(context.Context, store.BusySendReleaseRequest) (store.BusySendRelease, error)
	RecordBusySendWait(context.Context, store.BusySendWaitRequest) (bool, error)
	ReleaseAbandonedBusySendForce(ctx context.Context, instanceID string, at time.Time) (store.BusySendForceRelease, error)
}

var _ Store = (*store.Store)(nil)

// Budgets are an invocation's original finite phases (SR-3.2), fixed as
// absolute deadlines when Deliver starts, before the route's eligibility, and
// never renewed per prompt or operation. Issue bounds eligibility, the claim,
// the route's authority check and every issue commit, and the hand-over
// records it as its issue deadline: the issue commit's guard judges it once,
// inside its transaction, and a confirmed commit's one call follows. Job
// bounds the route's calls and is the hand-over's job deadline. Cleanup is
// the one finite allowance after Job for recording what the route observed,
// releasing the never-issued remainder and recording the wait; it is 0 when
// that work must also end by Job. Every phase is cut by the caller's own
// context.
type Budgets struct {
	Issue, Job, Cleanup time.Duration
}

// Coordinator runs finite delivery invocations for one route. It holds no
// state between invocations: durable custody is the store's.
type Coordinator struct {
	Store   Store
	Route   Route
	Budgets Budgets
	// Now is the invocation's clock (nil: time.Now): it fixes the phases'
	// deadlines, stamps the claim, completion, release and wait records, and
	// is read again inside each issue commit's transaction, so one clock
	// decides every deadline.
	Now func() time.Time
}

// OutcomeKind is how an invocation ended.
type OutcomeKind string

const (
	// OutcomeIssued: at least one prompt was issued and its completion
	// recorded.
	OutcomeIssued OutcomeKind = "issued"
	// OutcomeNoIssue: nothing was issued; Wait names why when it is known.
	OutcomeNoIssue OutcomeKind = "no_issue"
	// OutcomeStale: the event's or the job's authority is gone (another
	// Launch, session, connection or owner); nothing was issued by it.
	OutcomeStale OutcomeKind = "stale"
	// OutcomeUncertain: a commit or a completion record whose outcome is
	// unknown; the invocation stopped without issuing anything further, and
	// durable custody stays for a later eligible event or Launch End.
	OutcomeUncertain OutcomeKind = "uncertain"
)

// Outcome is an invocation's result: what it issued and why it stopped.
type Outcome struct {
	Kind       OutcomeKind
	Wait       store.BusySendWaitReason
	HandoverID string
	// Issued is the prompts issued by this invocation, in order, with what
	// their route observed.
	Issued []Disposition
	Detail string
}

func (c *Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Deliver runs one finite invocation for ev (SR-2.2 to SR-2.6, SR-3.2). It
// fixes the invocation's phases first (Budgets). As the agent's eligible
// event, it then finishes the agent's force whose call never reached its end
// (store.ReleaseAbandonedBusySendForce; SR-3.2, SR-4.3), whatever the route
// then decides: that returns only never-issued Messages to Held, which follow
// the ordinary waits, so a waiting route still delivers nothing. It then reads
// the route's eligibility; when that forbids an attempt it records the wait
// and returns.
// Otherwise, while the issue phase lasts, it claims the eligible prefix at its
// cutoff (later admissions wait for a later event), and for each frozen
// prompt in order: asks the route's final authority, commits the prompt's
// emission intent and its issue revalidation in one transaction that also
// records the issuer (CommitBusySendIssue), makes the route's one call, and
// records what the route observed. It moves to the next prompt only once this
// one's order is secured, the route can issue again within this invocation
// and the issue phase allows; a stalled, partial or uncertain prompt keeps its
// order barrier. Its never-issued remainder is released to Held at the end,
// and a refused step records its wait. Nothing is retried: an uncertain
// commit or output, or a phase that ends, stops the invocation with durable
// custody left for a later eligible event or Launch End.
func (c *Coordinator) Deliver(ctx context.Context, ev Event) (Outcome, error) {
	b := c.Budgets
	if b.Issue <= 0 || b.Job < b.Issue || b.Cleanup < 0 {
		return Outcome{}, fmt.Errorf("busysend: budgets need a positive issue phase within the job and a cleanup of 0 or more")
	}
	start := c.now()
	issue, cancelIssue := context.WithTimeout(ctx, b.Issue)
	defer cancelIssue()
	job, cancelJob := context.WithTimeout(ctx, b.Job)
	defer cancelJob()
	cleanup, cancelCleanup := context.WithTimeout(ctx, b.Job+b.Cleanup)
	defer cancelCleanup()
	inv := &invocation{c: c, ev: ev, start: start, issueDeadline: start.Add(b.Issue), issue: issue, job: job, cleanup: cleanup}
	// A row gone or with no published binding has no force to finish; the
	// route's own reading then decides the event as before.
	_, err := c.Store.ReleaseAbandonedBusySendForce(issue, ev.InstanceID, start)
	if err != nil && !errors.Is(err, store.ErrSpawnNotFound) && !errors.Is(err, store.ErrBusySendNoBinding) {
		return Outcome{}, err
	}
	return inv.deliver()
}

// invocation is one Deliver call with its phases, fixed when it started: the
// issue deadline on the coordinator's clock, and each phase's context.
type invocation struct {
	c                    *Coordinator
	ev                   Event
	start, issueDeadline time.Time
	issue, job, cleanup  context.Context
}

// issueOver reports whether the issue phase has ended, by its context or by
// the coordinator's clock.
func (inv *invocation) issueOver() bool {
	return inv.issue.Err() != nil || inv.c.now().After(inv.issueDeadline)
}

func (inv *invocation) deliver() (Outcome, error) {
	c, ev := inv.c, inv.ev
	elig, err := c.Route.Eligibility(inv.issue, ev)
	if err != nil {
		return Outcome{}, err
	}
	if elig.Wait != "" {
		return inv.noIssue(elig.Destination.LaunchID, elig.Wait, "")
	}
	if inv.issueOver() {
		return Outcome{Kind: OutcomeNoIssue, Detail: "the invocation's issue phase ended before its claim"}, nil
	}
	claim, err := c.Store.ClaimBusySendHandover(inv.issue, store.BusySendClaimRequest{InstanceID: ev.InstanceID,
		Destination: elig.Destination, Boundary: ev.Boundary, RaceExposed: elig.RaceExposed,
		IssueBudget: c.Budgets.Issue, JobBudget: c.Budgets.Job, At: inv.start})
	switch {
	case store.CommitUnconfirmed(err):
		// A claim that landed expires unissued and a later event releases it.
		return Outcome{Kind: OutcomeUncertain, Detail: "the claim's commit is unconfirmed"}, nil
	case err != nil:
		return Outcome{}, err
	}
	switch claim.Outcome {
	case store.BusySendClaimMade:
	case store.BusySendClaimEmpty:
		return Outcome{Kind: OutcomeNoIssue}, nil
	case store.BusySendClaimIneligible:
		if claim.Wait == "" {
			return Outcome{Kind: OutcomeStale, Detail: claim.Detail}, nil
		}
		return inv.noIssue(elig.Destination.LaunchID, claim.Wait, claim.Detail)
	default:
		return inv.noIssue(elig.Destination.LaunchID, claim.Wait, claim.Detail)
	}
	return inv.run(claim.Handover)
}

// run takes a claimed hand-over's prompts through the issue commit, the
// route's call and completion, then releases what it did not issue.
func (inv *invocation) run(h *store.BusySendHandover) (Outcome, error) {
	c := inv.c
	out := Outcome{Kind: OutcomeNoIssue, HandoverID: h.HandoverID}
	revision, wait, release := h.Revision, store.BusySendWaitReason(""), true
	for k := h.PromptCursor; k < len(h.Prompts); k++ {
		if inv.issueOver() {
			wait = store.BusySendWaitReadinessUnknown
			break
		}
		p := prepared(h, k)
		auth, err := c.Route.Authorize(inv.issue, p)
		if err != nil {
			return inv.finish(h, revision, release, wait, out, err)
		}
		if auth.Wait != "" {
			wait = auth.Wait
			break
		}
		ref := store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: revision}
		issue, err := c.Store.CommitBusySendIssue(inv.issue, store.BusySendIssueRequest{Ref: ref, Issuer: auth.Issuer, Now: c.now})
		if stop, kind, guardWait := refused(err); stop {
			// A guard refusal (the issue deadline included, judged by the
			// guard on c.now inside the transaction) or a rolled-back commit
			// wrote nothing, so the prompt is still only claimed and the job
			// still holds revision unless the refusal says it does not: the
			// release returns it to Held. An unconfirmed commit, a context
			// that ended once COMMIT started included, may have landed, so it
			// grants no output, releases nothing and Launch End resolves any
			// intent it left.
			out.Kind, wait, release = mergeKind(out.Kind, kind), guardWait, kind != OutcomeUncertain && !ownerGone(err)
			return inv.finish(h, revision, release, wait, out, unexpected(err))
		}
		// A confirmed commit is the issue authority (SR-2.4: revalidated
		// immediately before issue): its one call follows under the job
		// budget, even if the issue phase ended while the commit returned.
		revision = issue.Revision
		d := c.Route.Issue(inv.job, clonePrepared(p))
		out.Issued, out.Kind = append(out.Issued, d), OutcomeIssued
		ref.Revision = revision
		res, err := c.Store.RecordBusySendCompletion(inv.cleanup, store.BusySendCompletion{Ref: ref, Output: d.Output,
			Order: d.Order, UnprovenBoundary: d.UnprovenBoundary, At: c.now()})
		if err != nil {
			// The output stands with its completion unrecorded: the prompt
			// keeps its order barrier and nothing is retried.
			out.Kind, out.Detail = OutcomeUncertain, fmt.Sprintf("recording prompt %d's completion: %v", k, err)
			return out, unexpected(err)
		}
		if res.Closed {
			return out, nil
		}
		if res.Revision == 0 {
			// Another step took the hand-over meanwhile: this job goes no
			// further and releases nothing.
			release = false
			break
		}
		revision = res.Revision
		if !res.OrderSecured {
			wait = store.BusySendWaitHandoverInProgress
			break
		}
		if !d.More {
			break
		}
	}
	return inv.finish(h, revision, release, wait, out, nil)
}

// finish releases the hand-over's never-issued remainder when the job still
// holds it, records the wait that stopped it, and returns out with cause, all
// within the cleanup phase.
func (inv *invocation) finish(h *store.BusySendHandover, revision int64, release bool,
	wait store.BusySendWaitReason, out Outcome, cause error) (Outcome, error) {
	c := inv.c
	out.Wait = wait
	recorded := false
	if release {
		r, err := c.Store.ReleaseBusySendClaims(inv.cleanup, store.BusySendReleaseRequest{HandoverID: h.HandoverID,
			Revision: revision, Wait: wait, At: c.now()})
		var guard *store.BusySendGuardError
		switch {
		case errors.As(err, &guard):
		case err != nil && cause == nil:
			cause = err
		case err == nil && r.Closed:
			// The release recorded the wait on the Messages it returned.
			recorded = true
		}
	}
	if wait != "" && !recorded {
		if _, err := c.Store.RecordBusySendWait(inv.cleanup, store.BusySendWaitRequest{InstanceID: inv.ev.InstanceID,
			LaunchID: h.Destination.LaunchID, Reason: wait, At: c.now()}); err != nil && cause == nil {
			cause = err
		}
	}
	if cause != nil {
		return out, cause
	}
	return out, nil
}

// noIssue records, within the cleanup phase, why an event issued nothing and
// returns that outcome.
func (inv *invocation) noIssue(launchID string, wait store.BusySendWaitReason, detail string) (Outcome, error) {
	out := Outcome{Kind: OutcomeNoIssue, Wait: wait, Detail: detail}
	if wait == "" || launchID == "" {
		return out, nil
	}
	_, err := inv.c.Store.RecordBusySendWait(inv.cleanup, store.BusySendWaitRequest{InstanceID: inv.ev.InstanceID,
		LaunchID: launchID, Reason: wait, At: inv.c.now()})
	return out, err
}

// refused classifies a job step's error: whether the invocation must stop,
// the outcome it stops with, and the wait to record.
func refused(err error) (bool, OutcomeKind, store.BusySendWaitReason) {
	var guard *store.BusySendGuardError
	switch {
	case err == nil:
		return false, "", ""
	case store.CommitUnconfirmed(err):
		return true, OutcomeUncertain, ""
	case errors.As(err, &guard):
		if guard.Wait == "" {
			return true, OutcomeStale, ""
		}
		return true, OutcomeNoIssue, guard.Wait
	}
	return true, OutcomeNoIssue, ""
}

// ownerGone reports a guard refusal that says the job no longer holds its
// hand-over, so it may release nothing.
func ownerGone(err error) bool {
	var guard *store.BusySendGuardError
	return errors.As(err, &guard) && guard.Failure == store.BusySendGuardOwner
}

// unexpected is err unless it is a guard refusal or an unconfirmed commit,
// which are outcomes, not failures.
func unexpected(err error) error {
	var guard *store.BusySendGuardError
	if errors.As(err, &guard) || store.CommitUnconfirmed(err) {
		return nil
	}
	return err
}

// mergeKind keeps OutcomeIssued once a prompt was issued.
func mergeKind(current, next OutcomeKind) OutcomeKind {
	if current == OutcomeIssued && next != OutcomeUncertain {
		return current
	}
	return next
}

func prepared(h *store.BusySendHandover, k int) Prepared {
	p := h.Prompts[k]
	return Prepared{HandoverID: h.HandoverID, PromptIndex: k, PromptCount: len(h.Prompts), Destination: h.Destination,
		Body: p.Body, RenderedBytes: p.RenderedBytes, IssueDeadline: h.IssueDeadline}
}

// clonePrepared gives the route its own copy, so the prepared prompt stays
// immutable whatever the route does with its bytes.
func clonePrepared(p Prepared) Prepared {
	p.Body = append([]byte(nil), p.Body...)
	return p
}
