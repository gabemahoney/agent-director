package api

import (
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// claudePacedTyping selects paced typing (typePaced) for a Claude row's
// fallback text longer than inputdelivery.ChunkBytes (b.vr5). On macOS a
// Claude pane loses the start of a longer burst typed in one call, which
// reaches it in reads of about 1 KiB; each paced piece arrives in one read.
// Linux needs none. Tests set it with SetClaudePacedTypingForTest.
var claudePacedTyping = runtime.GOOS == "darwin"

// pacedPiecePause follows each paced piece, and pacedEnterPause the last
// piece's pause before its Enter: the pacing the macOS check delivered whole.
const (
	pacedPiecePause = 100 * time.Millisecond
	pacedEnterPause = 500 * time.Millisecond
)

// pacedTextMaxBytes is the longest text paced typing takes, so that even after
// pending Claude's readiness wait a send is meant to end inside a 30 s client
// timeout instead of being killed while it types. Its 64 pieces pause about
// 7 s in all, and before each later piece and the Enter come the rechecks
// (pacedStop): a row reread, the relay guard's reads, and an input-authority
// recheck of two store reads, a process check, a tmux lookup and a pane
// listing.
const pacedTextMaxBytes = 32 << 10

// pacedReadPane is paced typing's next step once the text may be in the
// input, in part or unsubmitted.
const pacedReadPane = "read-pane before sending again, and do not resend the text blindly"

// pacedCancelledReason is why paced typing stops when the call's context is
// cancelled or reaches its deadline.
const pacedCancelledReason = "the call was cancelled or reached its deadline"

// pacedText reports whether send types text for a row of agent in paced
// pieces (typePaced): on macOS, a Claude row's text longer than
// inputdelivery.ChunkBytes.
func pacedText(agent, text string) bool {
	return claudePacedTyping && agent == store.AgentClaude && len(text) > inputdelivery.ChunkBytes
}

// pacedTextTooLong is run's not_started refusal of text that send would type
// in paced pieces and that is longer than pacedTextMaxBytes, made before input
// admission, recordManualInput (and so any durable manual-input record) and
// any tmux input; "" for any other text.
func pacedTextTooLong(agent, text string) string {
	if !pacedText(agent, text) || len(text) <= pacedTextMaxBytes {
		return ""
	}
	return fmt.Sprintf("macOS paced Claude typing takes at most %d bytes of text; put the prompt in a file and send a short instruction to read it", pacedTextMaxBytes)
}

// typePaced types text literally into the agent's pane paneID in pieces of
// at most inputdelivery.ChunkBytes bytes (inputdelivery.Split: rune-aligned,
// and cut at that size where text is not UTF-8), pausing pacedPiecePause
// after each, then, when enter is set, pacedEnterPause more and one Enter
// (b.vr5). send runs pending Claude's readiness wait, input admission and
// recordManualInput before it, as before typeText; run has refused text longer
// than pacedTextMaxBytes.
//
// The first piece follows a context check and beforeWrite (with an input
// session, the full admission recheck and, with a pane hash, the hash compared
// before the first write only). Each later piece and the Enter follow a
// context check, the row and relay-guard reread (pacedRefusal) and, with an
// input session, an input-authority recheck (checkInputTarget with
// checkInputIdentity: launch, session, process and pane, not the row's epoch,
// version or state). Later pieces skip the full admission recheck so that a
// working row's own hooks, which move its version or state during the seconds
// the pieces take, do not stop a send that pacedRefusal still permits;
// pacedRefusal carries state, dialog and permission changes. Pauses go through
// sleepInput, and the context is checked before each later piece and the
// Enter because an injected Sleep ignores it.
//
// A stop before the first piece types nothing: not_started, or beforeWrite's
// own error (ErrPaneChanged with a pane hash). A first piece that fails other
// than by timing out maps as typeText's failed text call (sendFailed). A
// timed-out first piece may have been typed, and a failed later piece leaves
// the pieces before it typed; those and a stop before a later piece or the
// Enter (cancellation, the call's deadline, pacedRefusal or changed input
// authority) are ErrSubmissionUnconfirmed with outcome
// partial_input_unconfirmed, phase text_attempted and the bytes of the pieces
// attempted, with no further piece, no Enter and never a next step that would
// type the text again. A failed Enter maps as typeText's failed Enter after
// its text. r.found carries the attempted facts the error, trail and cleanup
// read; any durable record recordManualInput wrote stays as written (none on
// an unsupported launch with no earlier attempt).
//
// The pieces of concurrent calls to one row can interleave in its input, so
// on macOS long Claude text must go to one row one call at a time.
func (r *sendKeysRun) typePaced(paneID string, launch tmux.Launch, text string, enter bool, params SendKeysParams) error {
	if r.env.Context != nil && r.env.Context.Err() != nil {
		return deliveryNotStarted(r.observedRow(), "paced Claude typing did not begin because "+pacedCancelledReason+"; nothing was typed")
	}
	if r.beforeWrite != nil {
		if err := r.beforeWrite(); err != nil {
			return err
		}
	}
	for i, piece := range inputdelivery.Split(text) {
		if i > 0 {
			if row, why := r.pacedStop(params); why != "" {
				return r.pacedUnconfirmed(row, "paced Claude typing stopped before the rest of the text because "+why+", so part of it may be in the input; "+pacedReadPane)
			}
		}
		r.found.Sent = true
		r.found.BytesAttempted += len(piece)
		if err := r.t.SendKeysPane(r.socket, paneID, piece, false); err != nil {
			var ce *tmux.CallError
			timedOut := errors.As(err, &ce) && ce.Failure == tmux.FailTimeout
			switch {
			case i > 0:
				return r.pacedUnconfirmed(r.observedRow(), "paced Claude typing failed after part of the text was typed, so part of it may be in the input; "+pacedReadPane+": "+err.Error())
			case timedOut:
				return r.pacedUnconfirmed(r.observedRow(), "paced Claude typing timed out on the text's first piece, so part of it may be in the input; "+pacedReadPane+": "+err.Error())
			}
			r.found.SendErr = err
			return r.sendFailed(err, tmux.CallSendText, launch)
		}
		r.found.TextSent = true
		r.sleepInput(pacedPiecePause)
	}
	if !enter {
		return nil
	}
	r.sleepInput(pacedEnterPause)
	if row, why := r.pacedStop(params); why != "" {
		return r.pacedUnconfirmed(row, "paced Claude typing sent no Enter after the text because "+why+", so the text may be in the input unsubmitted; "+pacedReadPane)
	}
	r.found.EnterAttempted = true
	if err := r.t.SendKeyPane(r.socket, paneID, "Enter"); err != nil {
		// The named Enter is the same tmux command as SendKeysPane's Enter
		// after its text, so it fails as that Enter does (enterFailedAfterText).
		var ce *tmux.CallError
		if errors.As(err, &ce) && ce.Call == tmux.CallSendKey {
			enterErr := *ce
			enterErr.Call = tmux.CallSendEnter
			err = &enterErr
		}
		r.found.SendErr = err
		return r.sendFailed(err, tmux.CallSendText, launch)
	}
	return nil
}

// pacedStop is the check before each paced piece after the first and before
// paced typing's Enter: the call's context, then the row and relay-guard
// reread (pacedRefusal), then, with an input session, input authority
// (checkInputTarget with checkInputIdentity) immediately before the write.
// It returns why input must stop, "" when it may go on, with the row to
// report: pacedRefusal's for its own stops, else observedRow. A recheck that
// stops after the context ended reports the cancellation or deadline, since
// the context's end can fail its reads, keeping the row that recheck reported.
func (r *sendKeysRun) pacedStop(params SendKeysParams) (Spawn, string) {
	cancelled := func() bool { return r.env.Context != nil && r.env.Context.Err() != nil }
	if cancelled() {
		return r.observedRow(), pacedCancelledReason
	}
	row, why := r.pacedRefusal(params)
	if why == "" && r.input != nil && r.checkInputTarget(r.input, checkInputIdentity) != nil {
		row, why = r.observedRow(), "input authority changed"
	}
	if why != "" && cancelled() {
		why = pacedCancelledReason
	}
	return row, why
}

// pacedRefusal rereads the row before each paced piece after the first and
// before paced typing's Enter, and runs the relay guard (evaluateRelayGuard) on
// it again, as native input revalidates before every write. That narrows the
// window in which a piece or an Enter the pacing delayed can reach a dialog
// opened meanwhile; it cannot close it. It returns why input must stop, ""
// when it may go on, with the row to report: the reread row, or observedRow
// when the read failed. The launch must be unchanged (life number and token),
// the state still pass the state guard and not have become check_permission or
// ask_user since the call observed the row (a send begun on such a row goes
// on, as on Linux), and the guard not refuse. It makes no tmux call.
func (r *sendKeysRun) pacedRefusal(params SendKeysParams) (Spawn, string) {
	row, err := r.s.GetSpawnWithin(r.row.InstanceID, time.Second)
	if err != nil {
		return r.observedRow(), "the row could not be reread"
	}
	if row.LifeNumber != r.row.LifeNumber || row.Identity.Token != r.row.Identity.Token {
		return row, "the launch changed"
	}
	dialog := row.State == store.StateCheckPermission || row.State == store.StateAskUser
	if sendKeysStateGuard(row, params) != nil || (dialog && row.State != r.observedRow().State) {
		return row, "the row's state is now " + row.State
	}
	guard, err := evaluateRelayGuard(r.s, r.j, r.env.IntentHold, row, params)
	if err != nil || guard.refusal != nil {
		return row, "the permission or dialog guard no longer permits input"
	}
	return row, ""
}

// pacedUnconfirmed is paced typing's error once the text may be in the input,
// in part or unsubmitted: ErrSubmissionUnconfirmed built from r.found's
// attempted facts (outcome partial_input_unconfirmed, phase text_attempted,
// the bytes of the piece calls made), reporting row's state, kept as the
// call's send error. No follow-up lookup is made, as pasteText makes none.
func (r *sendKeysRun) pacedUnconfirmed(row Spawn, reason string) error {
	a := r.found.deliveryAttempt()
	r.found.SendErr = deliveryError(row, a, unresolvedOutcome(a), reason)
	return r.found.SendErr
}

// observedRow is the row as this call last observed it: r.row (the admitted
// row once input admission ran, else the row read at the start) with pending
// Claude readiness's last row state (terminalState) once that wait ran. It is
// the row recordManualInput's and paced typing's refusals report.
func (r *sendKeysRun) observedRow() Spawn {
	row := r.row
	if r.terminal != nil {
		row.State = r.terminalState
	}
	return row
}
