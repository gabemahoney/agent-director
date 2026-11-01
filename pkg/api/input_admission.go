package api

import (
	"context"
	"reflect"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
)

// admitInput makes one final preparation pass after target discovery. The
// snapshot read here must survive all guard/target checks and SQLite admission;
// there is no retry or refresh after intent, a failed commit or physical input.
func (r *keysRun) admitInput(ctx context.Context, session InputDeliverySession, s interface {
	GetSpawn(string) (Spawn, error)
}, validate func(Spawn) error) error {
	owned := r.row
	owned.Identity = r.adoption.Identity
	id := deliveryIdentity(owned, r.storeID)
	admitter, ok := session.(interface {
		Admit(inputdelivery.Guard, inputdelivery.Identity) error
	})
	if !ok {
		if err := bindInput(session, id, r.adoption.Applied); err != nil {
			return deliveryNotStarted(r.row, "input launch binding is stale or unavailable")
		}
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var current Spawn
	var err error
	if reader, ok := s.(interface {
		GetSpawnWithin(string, time.Duration) (Spawn, error)
	}); ok {
		budget := inputReadyTimeout
		if ctx != nil {
			if end, ok := ctx.Deadline(); ok {
				budget = min(budget, max(time.Until(end), 0))
			}
		}
		current, err = reader.GetSpawnWithin(r.row.InstanceID, budget)
	} else {
		current, err = s.GetSpawn(r.row.InstanceID)
	}
	if err != nil {
		return err
	}
	if !r.sameInputPreparation(current) {
		return deliveryNotStarted(r.row, "input preparation changed launch or authority")
	}
	if err := validate(current); err != nil {
		return err
	}
	// targetPane just established the physical target. The dispatch guard
	// rechecks that exact target after any waited intent/binding commit and
	// before each action; another lookup here would not make either atomic.
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err := admitter.Admit(deliveryGuard(current, r.storeID, false), id); err != nil {
		return deliveryNotStarted(r.row, "input launch binding is stale or unavailable")
	}
	r.row = current
	return nil
}

// Only optional adoption, liveness metadata and the initial same-process
// report-in may change during preparation. An established session, permission
// interval, turn, launch or target is never replaced. Compare the remaining
// row too so newly added authority fields are conservative by default.
func (r *keysRun) sameInputPreparation(current Spawn) bool {
	expected := r.row
	if r.adoption.Applied {
		expected.Identity = r.adoption.Identity
		expected.RowVersion++
		expected.Snapshot.RowVersion++
	}
	if current.RowVersion < expected.RowVersion {
		return false
	}
	initialReport := expected.SessionID == "" && current.SessionID != ""
	if current.RowVersion != expected.RowVersion && !initialReport {
		if (expected.State != store.StatePending && expected.State != store.StateWaiting) ||
			(current.LivenessNote == expected.LivenessNote && current.LivenessUnverifiedSince == expected.LivenessUnverifiedSince) {
			return false
		}
	}
	if initialReport {
		id := r.adoption.Identity
		if (expected.State != store.StatePending && expected.State != store.StateWaiting) ||
			current.State != store.StateWaiting || expected.Agent == store.AgentCursor ||
			id.PaneStarttime == "" || current.PID != id.PanePID || current.ProcStarttime != id.PaneStarttime ||
			(expected.PID != 0 && expected.PID != current.PID) ||
			(expected.ProcStarttime != "" && expected.ProcStarttime != current.ProcStarttime) ||
			expected.EndReportedAtMillis != 0 {
			return false
		}
		if expected.State == store.StatePending {
			expected.PermissionEpoch++
			expected.ObservationVersion++
			expected.StateChangedMillis = current.StateChangedMillis
		}
		expected.State = store.StateWaiting
		expected.SessionID, expected.Snapshot.SessionID = current.SessionID, current.SessionID
		expected.PID, expected.Snapshot.PID = current.PID, current.PID
		expected.ProcStarttime, expected.Snapshot.ProcStarttime = current.ProcStarttime, current.ProcStarttime
		expected.JSONLPath, expected.LastSeenAt = current.JSONLPath, current.LastSeenAt
		expected.LaunchStartedAtMillis, expected.IdleSince = 0, ""
		expected.CodexStartup, expected.ClaudeCapacity = current.CodexStartup, nil
	}
	expected.RowVersion, expected.Snapshot.RowVersion = current.RowVersion, current.Snapshot.RowVersion
	expected.LivenessNote, expected.LivenessUnverifiedSince = current.LivenessNote, current.LivenessUnverifiedSince
	// Main-field evidence is freshness metadata a concurrent read or event may
	// record; it grants no authority, so it never decides admission.
	expected.StateEvidence, expected.SettledStateEvidence, expected.ProcessEvidence = current.StateEvidence, current.SettledStateEvidence, current.ProcessEvidence
	expected.DisplayEvidence = current.DisplayEvidence
	return reflect.DeepEqual(expected.RawFields(), current.RawFields())
}
