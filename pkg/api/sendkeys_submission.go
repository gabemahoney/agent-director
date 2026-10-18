package api

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Exported aliases allow native evidence and durable storage to be injected.
type InputDeliveryBackend = inputdelivery.Backend
type InputDeliverySession = inputdelivery.Session
type InputDeliveryIdentity = inputdelivery.Identity
type InputDeliveryAttempt = inputdelivery.Attempt
type InputDeliveryWatermark = inputdelivery.Watermark
type InputDeliveryProof = inputdelivery.Proof

const (
	submissionConfirmTimeout = 12 * time.Second
	submissionPollInterval   = 100 * time.Millisecond
	inputReadyTimeout        = 12 * time.Second
	inputDrainTimeout        = 3 * time.Second
	inputDrainInterval       = 40 * time.Millisecond
	inputDeliveryTimeout     = 60 * time.Second
)

// Default pane input needs no feature state until a native launch or attempt
// exists. Explicit backends own their provisioning contract.
func openInputDelivery(backend InputDeliveryBackend, id InputDeliveryIdentity) (InputDeliverySession, error) {
	if backend == nil {
		return (inputdelivery.Native{}).OpenExisting(id)
	}
	return backend.Open(id)
}

func deliveryIdentity(row Spawn, storeID string) inputdelivery.Identity {
	return inputdelivery.Identity{StoreID: storeID, Token: row.Identity.Token, InstanceID: row.InstanceID, Socket: row.Identity.Socket, PaneID: row.Identity.PaneID, PID: row.Identity.PanePID, Starttime: row.Identity.PaneStarttime, Agent: row.Agent, CWD: row.CWD}
}

// Adapters may opt out when no native contract exists for this launch. Older
// injected adapters already implement that contract through Ready.
func supportsInput(session inputdelivery.Session, id inputdelivery.Identity) bool {
	if supported, ok := session.(interface {
		Supports(inputdelivery.Identity) bool
	}); ok {
		return supported.Supports(id)
	}
	return true
}

func deliveryNotStarted(row Spawn, reason string) error {
	return deliveryError(row, nil, "not_started", reason)
}

func deliveryError(row Spawn, a *inputdelivery.Attempt, outcome, reason string) error {
	d := SubmissionUnconfirmedDetails{Outcome: outcome, Phase: "pre_input", RowState: row.State, Reason: reason}
	if a != nil {
		d.RequestID, d.Phase, d.BytesAttempted, d.EnterMayHaveBeenSent, d.LastQueue = a.RequestID, a.Phase, a.BytesAttempted, a.EnterAttempted, a.LastQueue
		d.KeysSent = a.BytesAttempted > 0 || a.EnterAttempted
	}
	guidance := "unconfirmed input may still submit later; do not automatically replay text or Enter"
	if outcome == "not_started" {
		guidance = "no input was attempted by this request"
	}
	return &DetailedError{Err: fmt.Errorf("%w: send-keys: %s: %s (%s); %s", ErrSubmissionUnconfirmed, row.InstanceID, outcome, reason, guidance), Details: d}
}

func (r *sendKeysRun) sleepInput(d time.Duration) {
	if r.env.Sleep != nil {
		r.env.Sleep(d)
	} else {
		time.Sleep(d)
	}
}

func (r *sendKeysRun) deliverOrdinary(paneID string, launch tmux.Launch, plan sendKeysPlan, params SendKeysParams) error {
	row := r.row
	row.Identity = r.adoption.Identity
	id := deliveryIdentity(row, r.s.StoreID())
	return r.beginOrdinary(id, paneID, plan.text, params)
}

func (r *sendKeysRun) reconcileOrdinary(row Spawn, id inputdelivery.Identity, text, requestID string) (bool, error) {
	previous, err := r.input.Load()
	if err != nil {
		return false, deliveryNotStarted(row, "delivery journal cannot be read")
	}
	if !r.automatic && previous != nil && previous.Phase == "manual_input" && previous.BytesAttempted == 0 && !previous.EnterAttempted {
		// A manual operation on a plain pane is not an automatic attempt and
		// must not permanently disable its established input path.
		previous = nil
	}
	if requestID != "" {
		owned, err := r.input.LoadRequest(requestID)
		if err != nil {
			return false, deliveryNotStarted(row, "request history cannot be read")
		}
		if owned != nil {
			if owned.Payload != text || owned.Identity != id {
				return false, deliveryNotStarted(row, "delivery_request_id already owns different input or identity")
			}
			if owned.Phase == "submitted" && owned.Proof != nil {
				return true, r.useReconciledProof(row, owned)
			}
			if previous == nil || previous.Phase == "submitted" || previous.Phase == "not_started" {
				previous = owned
			}
		}
	} else if previous != nil && previous.Phase == "submitted" && previous.Payload == text && previous.Identity == id && previous.Proof != nil {
		return true, r.useReconciledProof(row, previous)
	}
	if previous != nil && previous.Phase != "submitted" && previous.Phase != "not_started" {
		if previous.EnterAttempted && previous.Identity == id {
			proof, err := r.input.Confirm(previous)
			if err != nil {
				return false, deliveryError(row, previous, unresolvedOutcome(previous), "native transcript evidence is unavailable or ambiguous")
			}
			if proof != nil {
				previous.Proof, previous.Phase = proof, "submitted"
				if err = r.input.Save(previous); err != nil {
					return false, deliveryError(row, previous, "submission_unconfirmed", "late proof could not be persisted")
				}
				if requestID != "" && requestID != previous.RequestID || requestID == "" && text != previous.Payload {
					return false, deliveryNotStarted(row, "earlier request was confirmed; this different request has not started")
				}
				// A retry only reconciles the original request, never sends again.
				return true, r.useReconciledProof(row, previous)
			}
		}
		return false, deliveryError(row, previous, unresolvedOutcome(previous), "an earlier launch attempt remains unresolved; operator reconciliation is required")
	}
	if previous != nil && previous.ManualInterference {
		return false, deliveryNotStarted(row, "manual input changed the launch's composer contract; continue with operator-controlled input")
	}
	return false, nil
}

// After the state, relay and dialog guards pass, passive proof reconciliation
// needs no pane. Proof must still belong to the row's exact launch, including
// any native session mapping.
func (r *sendKeysRun) useReconciledProof(row Spawn, a *inputdelivery.Attempt) error {
	current, err := r.s.GetSpawnWithin(row.InstanceID, time.Second)
	if err != nil {
		return deliveryError(row, a, "submission_unconfirmed", "reconciled launch cannot be reread")
	}
	if current.LifeNumber != row.LifeNumber || deliveryIdentity(current, r.s.StoreID()) != a.Identity ||
		current.SessionID != "" && current.SessionID != a.Proof.SessionID {
		return deliveryError(row, a, "submission_unconfirmed", "launch or native session changed before reconciliation")
	}
	r.deliveryResult = confirmedDelivery(a)
	return nil
}

func (r *sendKeysRun) beginOrdinary(id inputdelivery.Identity, paneID, text string, params SendKeysParams) error {
	row := r.row
	if len(text) > 1<<20 {
		return deliveryNotStarted(row, "ordinary input exceeds 1 MiB")
	}
	if err := r.waitInputReady(id, paneID, params); err != nil {
		return deliveryNotStarted(row, err.Error())
	}
	marks, err := r.input.Watermark(id)
	if err != nil {
		return deliveryNotStarted(row, "transcript watermark unavailable")
	}
	a, err := inputdelivery.NewAttempt(id, text, marks)
	if err != nil {
		return deliveryNotStarted(row, "cannot allocate request identity")
	}
	a.NativeSessionID = row.SessionID
	if params.DeliveryRequestID != "" {
		a.RequestID = params.DeliveryRequestID
	}
	if err = r.input.Save(a); err != nil {
		return deliveryNotStarted(row, "cannot persist attempt before input")
	}
	parts, err := inputdelivery.Chunks(text)
	if err != nil {
		return deliveryNotStarted(row, err.Error())
	}
	deadline := r.j.now().Add(inputDeliveryTimeout)
	if err = r.writeOrdinaryChunks(a, parts, paneID, params, deadline); err != nil {
		return err
	}
	if err = r.revalidateInput(id, paneID, params); err != nil {
		return r.stopDelivery(a, err.Error())
	}
	if !r.j.now().Before(deadline) {
		return r.stopDelivery(a, "overall input deadline exhausted before Enter")
	}
	a.EnterAttempted, a.Phase = true, "enter_attempted"
	if err = r.input.Save(a); err != nil {
		return r.stopDelivery(a, "cannot persist Enter intent")
	}
	// Exactly one Enter; neither transport errors nor missing proof retry it.
	if err = r.t.SendKeyPane(r.socket, paneID, "Enter"); err != nil {
		r.found.SendErr = err
	}
	return r.observeSubmission(a)
}

func (r *sendKeysRun) writeOrdinaryChunks(a *inputdelivery.Attempt, parts []string, paneID string, params SendKeysParams, deadline time.Time) error {
	id := a.Identity
	var err error
	for _, part := range parts {
		if !r.j.now().Before(deadline) {
			return r.stopDelivery(a, "overall input deadline exhausted")
		}
		if err = r.revalidateInput(id, paneID, params); err != nil {
			return r.stopDelivery(a, err.Error())
		}
		if !r.j.now().Before(deadline) {
			return r.stopDelivery(a, "overall input deadline exhausted before write")
		}
		a.Phase = "text_attempted"
		a.BytesAttempted += len(part)
		if err = r.input.Save(a); err != nil {
			return r.stopDelivery(a, "cannot persist text intent")
		}
		r.found.Sent = true
		if err = r.t.SendKeysPane(r.socket, paneID, part, false); err != nil {
			r.found.SendErr = err
			return r.stopDelivery(a, "literal text write failed or timed out")
		}
		if err = r.drainInput(a, paneID, params, deadline); err != nil {
			return r.stopDelivery(a, err.Error())
		}
	}
	return nil
}

func unresolvedOutcome(a *inputdelivery.Attempt) string {
	if a.EnterAttempted {
		return "submission_unconfirmed"
	}
	if a.BytesAttempted > 0 || a.Phase == "manual_input" {
		return "partial_input_unconfirmed"
	}
	return "not_started"
}

func (r *sendKeysRun) stopDelivery(a *inputdelivery.Attempt, reason string) error {
	if a.BytesAttempted == 0 && !a.EnterAttempted {
		a.Phase = "not_started"
	}
	if err := r.input.Save(a); err != nil {
		reason += "; journal update failed"
	}
	return deliveryError(r.row, a, unresolvedOutcome(a), reason)
}

func (r *sendKeysRun) revalidateInput(id inputdelivery.Identity, paneID string, params SendKeysParams) error {
	row, err := r.s.GetSpawnWithin(id.InstanceID, time.Second)
	if err != nil {
		return fmt.Errorf("launch cannot be reread")
	}
	if row.LifeNumber != r.row.LifeNumber || row.Identity.Token != id.Token || row.Identity.Socket != id.Socket {
		return fmt.Errorf("launch identity changed")
	}
	if row.State != store.StatePending && row.State != store.StateWaiting {
		return fmt.Errorf("ordinary composer is not waiting (state=%s)", row.State)
	}
	if row.Identity.PaneID != "" && (row.Identity.PaneID != id.PaneID || row.Identity.PanePID != id.PID || row.Identity.PaneStarttime != id.Starttime) {
		return fmt.Errorf("native process identity changed")
	}
	if id.Starttime == "" || r.pc == nil || tmux.JudgeProcess(r.pc, tmux.ProcIdentity{PID: id.PID, Starttime: id.Starttime}) != tmux.ProcAlive {
		return fmt.Errorf("native process ownership unavailable")
	}
	res := tmux.Lookup(r.t, r.pc, r.launchFor(row.Identity), row.TmuxSessionName)
	if res.Verdict != tmux.Ours {
		return fmt.Errorf("exact launch socket/label no longer verified")
	}
	panes, err := r.t.ListPanes(r.socket)
	if err != nil {
		return fmt.Errorf("pane ownership query failed")
	}
	if _, ok := agentPane(panes, paneID, id.PID); !ok {
		return fmt.Errorf("owned pane changed")
	}
	guard, err := evaluateRelayGuard(r.s, r.j, r.env.IntentHold, row, params)
	if err != nil || guard.refusal != nil {
		return fmt.Errorf("permission or dialog guard no longer permits ordinary input")
	}
	return nil
}

func (r *sendKeysRun) waitInputReady(id inputdelivery.Identity, paneID string, params SendKeysParams) error {
	end := r.j.now().Add(inputReadyTimeout)
	for n := 0; n <= int(inputReadyTimeout/submissionPollInterval); n++ {
		if !r.j.now().Before(end) {
			break
		}
		if err := r.revalidateInput(id, paneID, params); err != nil {
			return err
		}
		ready, err := r.input.Ready(id)
		if err != nil {
			return fmt.Errorf("native readiness is unavailable or unsupported")
		}
		if ready {
			q, err := r.input.Queue(id)
			if err != nil {
				return fmt.Errorf("initial terminal state query failed")
			}
			if q != 0 {
				return fmt.Errorf("terminal input is not initially empty")
			}
			if !r.j.now().Before(end) {
				break
			}
			return nil
		}
		r.sleepInput(submissionPollInterval)
	}
	return fmt.Errorf("fresh version-supported native readiness marker was not observed")
}

func (r *sendKeysRun) drainInput(a *inputdelivery.Attempt, paneID string, params SendKeysParams, overall time.Time) error {
	end := minTime(r.j.now().Add(inputDrainTimeout), overall)
	var zeroAt time.Time
	for n := 0; n <= int(inputDrainTimeout/inputDrainInterval); n++ {
		if !r.j.now().Before(end) {
			break
		}
		if err := r.revalidateInput(a.Identity, paneID, params); err != nil {
			return err
		}
		sampleAt := r.j.now()
		q, err := r.input.Queue(a.Identity)
		if err != nil {
			return fmt.Errorf("terminal queue sample failed")
		}
		if !r.j.now().Before(end) {
			break
		}
		a.LastQueue = &q
		if q == 0 {
			// Bound the gap from the first completed sample to the next call's
			// start; time spent inside Queue cannot supply the spacing itself.
			if !zeroAt.IsZero() && sampleAt.Sub(zeroAt) >= inputDrainInterval {
				return nil
			}
			zeroAt = r.j.now()
		} else {
			zeroAt = time.Time{}
		}
		r.sleepInput(inputDrainInterval)
	}
	return fmt.Errorf("terminal queue did not drain within its bounded deadline")
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (r *sendKeysRun) observeSubmission(a *inputdelivery.Attempt) error {
	end := r.j.now().Add(submissionConfirmTimeout)
	for n := 0; n <= int(submissionConfirmTimeout/submissionPollInterval); n++ {
		if !r.j.now().Before(end) {
			break
		}
		row, err := r.s.GetSpawnWithin(a.Identity.InstanceID, min(time.Second, end.Sub(r.j.now())))
		if err != nil {
			return r.stopDelivery(a, "submission evidence row unavailable")
		}
		if row.Identity.Token != a.Identity.Token || row.LifeNumber != r.row.LifeNumber {
			return r.stopDelivery(a, "launch changed before confirmation")
		}
		if row.SessionID != "" {
			if a.NativeSessionID != "" && a.NativeSessionID != row.SessionID {
				return r.stopDelivery(a, "native session changed before confirmation")
			}
			a.NativeSessionID = row.SessionID
		}
		proof, err := r.input.Confirm(a)
		if err != nil {
			return r.stopDelivery(a, "native transcript evidence is unavailable or ambiguous")
		}
		if proof != nil {
			a.Proof, a.Phase = proof, "submitted"
			if err = r.input.Save(a); err != nil {
				return deliveryError(row, a, "submission_unconfirmed", "positive proof could not be persisted")
			}
			r.deliveryResult = confirmedDelivery(a)
			return nil
		}
		r.sleepInput(min(submissionPollInterval, max(end.Sub(r.j.now()), 0)))
	}
	return r.stopDelivery(a, "no exact launch-bound post-watermark user record within 12 seconds")
}

func confirmedDelivery(a *inputdelivery.Attempt) SendKeysResult {
	return SendKeysResult{RequestID: a.RequestID, Outcome: "submitted", SessionID: a.Proof.SessionID, RecordID: a.Proof.RecordID, RecordSHA256: a.Proof.RawSHA256}
}

func (r *sendKeysRun) recordManualInput() error {
	if err := recordManualDelivery(r.input, deliveryIdentity(r.row, r.s.StoreID())); err != nil {
		return deliveryNotStarted(r.row, err.Error())
	}
	return nil
}

func recordManualDelivery(session inputdelivery.Session, id inputdelivery.Identity) error {
	old, err := session.Load()
	if err != nil {
		return fmt.Errorf("delivery journal cannot be read")
	}
	// Operator recovery never erases an unresolved automatic request.
	if old != nil && old.Phase != "submitted" && old.Phase != "not_started" {
		old.ManualInterference = true
		if err = session.Save(old); err != nil {
			return fmt.Errorf("cannot persist manual input ownership")
		}
		return nil
	}
	if old == nil && !supportsInput(session, id) {
		return nil
	}
	a, err := inputdelivery.NewAttempt(id, "", nil)
	if err != nil {
		return fmt.Errorf("cannot allocate manual operation identity")
	}
	a.Phase = "manual_input"
	a.ManualInterference = true
	if err = session.Save(a); err != nil {
		return fmt.Errorf("cannot persist manual input intent")
	}
	return nil
}
