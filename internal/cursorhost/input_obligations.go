package cursorhost

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// record belongs to the controller. The writer receives an immutable started
// value and publishes only whether its one issue commit was confirmed.
type inputObligation struct {
	record  store.CursorInput
	issued  atomic.Bool
	retired atomic.Bool
	final   bool // controller captured a terminal disposition; not a durability claim
}

// Terminal observations carry no dispatch authority. The controller captures
// their exact CAS and deadline at the event, then writes them after its required
// proof flush and before reporting/recovering admission. They are never replayed
// after an uncertain result, nor reconstructed from storage.
type inputDisposition struct {
	obligation *inputObligation
	old, next  store.CursorInput
	until      time.Time
}

func (d inputDisposition) write(ctx context.Context, s *store.Store) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.old.State == "" {
		return s.CaptureCursorInput(ctx, d.next, d.until, nil)
	}
	return s.AdvanceCursorInput(ctx, d.old, d.next, d.until, nil)
}

func (o *inputObligation) snapshot() store.CursorInput {
	v := o.record
	if v.State == store.CursorInputStarted && o.issued.Load() {
		v.State = store.CursorInputIssued
	}
	return v
}
func inputEvidence(rep *reporter, v terminalInput, generation uint64, state, detail string) store.CursorInput {
	binding := rep.binding
	if v.sessionID != "" {
		binding.SessionID = v.sessionID
	}
	target := ""
	if state == store.CursorInputAdmitted {
		target = v.sessionID
	}
	path := ""
	if rep.loaded {
		path = rep.path
	}
	return store.CursorInput{Owner: store.CursorOwner{StoreID: rep.store.StoreID(), Runtime: binding},
		Generation: generation, SubmissionID: v.submission, Order: v.order, Epoch: v.epoch,
		Text: v.text, CapturedAt: v.at, LoadedPath: path, State: state, TargetSession: target, Detail: detail}
}

// Issue is not provider receipt. A timeout/unknown commit leaves durable
// started/issued evidence and never permits another write of this prompt.
func (o *inputObligation) issue(rep reporter, started store.CursorInput, current func() bool) error {
	live := func() error {
		// Expiry remains an uncertain issue outcome even if a control also
		// arrived. Only positive control retirement means known unwritten.
		if !time.Now().Before(started.Deadline) {
			return context.DeadlineExceeded
		}
		if o.retired.Load() || !current() {
			return errPromptRetired
		}
		return nil
	}
	if err := live(); err != nil {
		return err
	}
	check := func(row store.Spawn) error {
		if err := live(); err != nil {
			return err
		}
		if err := VerifyRuntime(rep.binding, false); err != nil {
			return err
		}
		if err := VerifyPane(rep.store, row, rep.cfg); err != nil {
			return err
		}
		return live()
	}
	issued := started
	issued.State = store.CursorInputIssued
	if err := rep.store.AdvanceCursorInput(context.Background(), started, issued, started.Deadline, check); err != nil {
		// The store can return after the control check's deadline. Preserve
		// that timeout rather than allowing a nested retirement to recover
		// the transport after its original issue bound has passed.
		if errors.Is(err, errPromptRetired) && !time.Now().Before(started.Deadline) {
			err = context.DeadlineExceeded
		}
		return fmt.Errorf("Cursor prompt issue unconfirmed: %w", err)
	}
	o.issued.Store(true)
	return live()
}
