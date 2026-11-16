package busysend

import (
	"context"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// ForcePromptReader is the read a route's forced wake drives a force with:
// the agent's running force and the frozen prompt it may issue now
// (store.ReadBusySendForcePrompt). *store.Store implements it.
type ForcePromptReader interface {
	ReadBusySendForcePrompt(ctx context.Context, instanceID string) (store.BusySendForcePrompt, error)
}

var _ ForcePromptReader = (*store.Store)(nil)

// DeliverForcePrompt runs one forced wake's finite invocation for ev's agent
// (SR-3.4, SR-4.3, SR-3.2): it issues at most one prompt of the agent's
// running force, the frozen prompt the force may issue now, and never creates
// a force, chooses a new group, adds a Message or releases a claim; the
// force's call owns its claims and their return at its end. Each phase's end
// is fixed once: the coordinator's Budgets from the invocation's start,
// clipped to the force's original recorded deadlines, on the coordinator's
// clock. The issue phase ends by the hand-over's issue deadline, and the
// route's call and the completion record both by its job deadline (the
// call's recorded deadline): the wake's cleanup allowance never extends past
// it.
// What remains of each is measured after the prompt's read, so neither the
// read nor a wake near the active phase's end renews a budget. Within them
// it asks the
// route's final authority, commits the prompt's intent and its force
// revalidation in one transaction (store.CommitBusySendIssue with the force's
// authority), makes the route's one call and records what it observed. A
// later prompt passes only once the one before it is admitted with its order
// secured, and only while the call runs; a repeated or coalesced wake finds
// the prompt already issued and issues nothing. Nothing is retried. The Store
// must also be a ForcePromptReader.
func (c *Coordinator) DeliverForcePrompt(ctx context.Context, ev Event) (Outcome, error) {
	b := c.Budgets
	if b.Issue <= 0 || b.Job < b.Issue || b.Cleanup < 0 {
		return Outcome{}, fmt.Errorf("busysend: budgets need a positive issue phase within the job and a cleanup of 0 or more")
	}
	reader, ok := c.Store.(ForcePromptReader)
	if !ok {
		return Outcome{}, fmt.Errorf("busysend: the store cannot read a force's prompt")
	}
	start := c.now()
	read, cancelRead := context.WithTimeout(ctx, b.Issue)
	defer cancelRead()
	fp, err := reader.ReadBusySendForcePrompt(read, ev.InstanceID)
	if err != nil {
		return Outcome{}, err
	}
	if fp.Handover == nil || fp.Next < 0 {
		return Outcome{Kind: OutcomeNoIssue, Detail: "the agent's force has no prompt to issue now"}, nil
	}
	h := fp.Handover
	issueEnd := earliest(start.Add(b.Issue), h.IssueDeadline)
	jobEnd := earliest(start.Add(b.Job), h.JobDeadline)
	cleanupEnd := earliest(start.Add(b.Job+b.Cleanup), h.JobDeadline)
	now := c.now()
	if !now.Before(issueEnd) {
		return Outcome{Kind: OutcomeNoIssue, HandoverID: h.HandoverID, Detail: "the force's issue phase has ended"}, nil
	}
	issue, cancelIssue := context.WithTimeout(ctx, issueEnd.Sub(now))
	defer cancelIssue()
	job, cancelJob := context.WithTimeout(ctx, jobEnd.Sub(now))
	defer cancelJob()
	// The completion record may use the wake's cleanup allowance only within
	// the force's recorded deadline, never after it.
	cleanup, cancelCleanup := context.WithTimeout(ctx, cleanupEnd.Sub(now))
	defer cancelCleanup()

	k := fp.Next
	p := prepared(h, k)
	out := Outcome{Kind: OutcomeNoIssue, HandoverID: h.HandoverID}
	auth, err := c.Route.Authorize(issue, p)
	if err != nil {
		return out, err
	}
	if auth.Wait != "" {
		out.Wait = auth.Wait
		return out, nil
	}
	if issue.Err() != nil || c.now().After(issueEnd) {
		out.Detail = "the force's issue phase ended before its commit"
		return out, nil
	}
	ref := store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: h.Revision}
	force := fp.Force
	committed, err := c.Store.CommitBusySendIssue(issue, store.BusySendIssueRequest{Ref: ref, Force: &force, Issuer: auth.Issuer, Now: c.now})
	if stop, kind, wait := refused(err); stop {
		// A refusal wrote nothing and an unconfirmed commit grants no output;
		// either way the force's call, not this wake, settles its claims.
		out.Kind, out.Wait = kind, wait
		return out, unexpected(err)
	}
	d := c.Route.Issue(job, clonePrepared(p))
	out.Issued, out.Kind = append(out.Issued, d), OutcomeIssued
	ref.Revision = committed.Revision
	if _, err := c.Store.RecordBusySendCompletion(cleanup, store.BusySendCompletion{Ref: ref, Output: d.Output,
		Order: d.Order, UnprovenBoundary: d.UnprovenBoundary, At: c.now()}); err != nil {
		out.Kind, out.Detail = OutcomeUncertain, fmt.Sprintf("recording force prompt %d's completion: %v", k, err)
		return out, unexpected(err)
	}
	return out, nil
}

// earliest is the earlier of a and b.
func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
