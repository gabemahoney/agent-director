package api

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
)

// InputProofStore is the persistence-only seam for E4's shared read/sweep.
// It exposes no transport, signal, enqueue or launch capability.
type InputProofStore interface {
	inputdelivery.Persistence
	StoreID() string
}

// InputProofResult carries exact supporting facts, without a public display
// policy. Zero confirmation timestamps remain unknown, including imports.
type InputProofResult struct {
	// Identity is the caller's captured launch and process identity.
	Identity InputDeliveryIdentity
	// LaunchRevision is the retained launch revision observed by this pass.
	LaunchRevision int64
	// Attempt is the retained current attempt and any exact proof; nil means none.
	Attempt *InputDeliveryAttempt
	// CapabilityConfirmation is retained capability evidence, not submission proof.
	CapabilityConfirmation inputdelivery.Evidence
}

// ReconcileInputProof makes one pass for the caller's captured row. It never
// sleeps, retries for evidence, starts a request, or performs any delivery.
// Caller cancellation bounds SQLite admission; the internal ceiling prevents
// an unbounded call when a shared read supplies no deadline.
// Shared proof-only observation must reuse this seam.
func ReconcileInputProof(ctx context.Context, s InputProofStore, row Spawn) (InputProofResult, error) {
	result, _, err := reconcileInputProof(ctx, s, row)
	return result, err
}

// The invocation orchestrator also needs the commit outcome, kept separate
// from the stable proof facts returned by the exported persistence-only seam.
func reconcileInputProof(ctx context.Context, s InputProofStore, row Spawn) (result InputProofResult, committed bool, resultErr error) {
	if row.Identity.Token == "" {
		return result, committed, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	g := deliveryGuard(row, s.StoreID(), false)
	g.ProofOnly = true
	session, err := (inputdelivery.Native{Store: s, Context: ctx, Guard: g}).OpenExisting(g.Identity)
	if err != nil {
		return result, false, err
	}
	defer func() {
		if err := session.Close(); err != nil && resultErr == nil {
			resultErr = err
			committed = false
		}
	}()
	a, err := session.Load()
	if err != nil {
		return result, false, err
	}
	result.Identity = g.Identity
	if a != nil && a.Phase != "submitted" && a.EnterAttempted {
		proof, err := session.Confirm(a)
		if err != nil {
			return result, false, err
		}
		if proof != nil {
			a.Proof, a.Phase = proof, "submitted"
			if err := session.Save(a); err != nil {
				return result, false, err
			}
			committed = true
		}
	}
	result.Attempt = a
	if facts, ok := session.(interface {
		LaunchEvidence() (int64, inputdelivery.Evidence)
	}); ok {
		result.LaunchRevision, result.CapabilityConfirmation = facts.LaunchEvidence()
	}
	return result, committed, nil
}
