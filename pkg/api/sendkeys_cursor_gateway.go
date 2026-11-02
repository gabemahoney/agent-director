package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/cursortranscript"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// cursorVerdictWait bounds how long a plain Cursor send with Enter watches
// the gateway's transcript for its verdict on that Enter (b.bqd), as the
// pending-Codex check bounds its hook observation.
const cursorVerdictWait = 2 * time.Second

// cursorVerdictReadBytes is how much of the gateway transcript one read takes.
const cursorVerdictReadBytes = 64 * 1024

// typeCursorText types text and then Enter into a Cursor row's gateway pane
// (typeText), and reports the gateway's own verdict on that Enter (b.bqd).
// Just before typing it opens the row's protected gateway transcript
// (row.JSONLPath, read-only) and takes its end and the time on the run's clock
// (r.j.now: time.Now in production, the wall clock the gateway stamps each
// input's consumed_at with; a test's fake clock stamps its stand-in gateway's
// records the same way). After the Enter it reads the records added from
// there for at most cursorVerdictWait (cursorVerdict). A refusal of this
// call's text by the row's launch is gatewayRefusedError; this text accepted
// or held, an input_control (the gateway took the Enter as a control such as
// an exit, which it never admits or refuses as a prompt), no verdict on this
// text in the window, a transcript that cannot be opened or read, and a record
// of another launch keep the send's success: keys sent, not a prompt admitted.
// Nothing is retried.
//
// An Enter-only send (empty text) is watched too: its Enter submits what the
// gateway's input line holds, such as a text an earlier send left unsubmitted,
// and the gateway admits or refuses that as any other prompt. Such a call takes
// the first verdict on input consumed since it began as its own, a refusal
// only when it names its text by digest. An empty line makes no record, so
// such a call succeeds when the window ends.
func (r *sendKeysRun) typeCursorText(paneID string, launch tmux.Launch, text string) error {
	f, err := cursortranscript.OpenRead(r.row.JSONLPath)
	if err != nil {
		return r.typeText(paneID, launch, text, true)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return r.typeText(paneID, launch, text, true)
	}
	since := r.j.now()
	if err = r.typeText(paneID, launch, text, true); err != nil {
		return err
	}
	if reason, refused := r.cursorVerdict(f, st.Size(), since, text); refused {
		return gatewayRefusedError(r.row, len(text), reason)
	}
	return nil
}

// cursorVerdict reads the gateway transcript f from offset, polling every
// submissionPollInterval on the run's clock and sleep until cursorVerdictWait
// has passed, at most cursorVerdictWait/submissionPollInterval times, and
// returns the gateway's verdict on the call's Enter of text, typed from since:
// the recorded reason and true for this call's refusal, false otherwise
// (cursorRecordVerdict). Only complete lines are judged. A read failure
// leaves the verdict unknown and returns false at once.
func (r *sendKeysRun) cursorVerdict(f *os.File, offset int64, since time.Time, text string) (string, bool) {
	digest := ""
	if text != "" {
		digest = cursortranscript.TextSHA256(text)
	}
	deadline := r.j.now().Add(cursorVerdictWait)
	polls := int(cursorVerdictWait / submissionPollInterval)
	buf := make([]byte, cursorVerdictReadBytes)
	var line []byte
	for attempt := 0; ; attempt++ {
		for {
			n, err := f.ReadAt(buf, offset)
			offset += int64(n)
			for data := buf[:n]; len(data) > 0; {
				i := bytes.IndexByte(data, '\n')
				if i < 0 {
					line = append(line, data...)
					break
				}
				line, data = append(line, data[:i]...), data[i+1:]
				if reason, refused, done := r.cursorRecordVerdict(line, text, digest, since); done {
					return reason, refused
				}
				line = line[:0]
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", false
			}
		}
		remaining := deadline.Sub(r.j.now())
		if remaining <= 0 || attempt == polls {
			return "", false
		}
		r.sleepInput(min(submissionPollInterval, remaining))
	}
}

// cursorRecordVerdict judges one complete gateway transcript line for the
// call's Enter of text, whose TextSHA256 is digest (both empty for an
// Enter-only send), typed from since. done ends the watch: a record of another
// launch token (the launch changed), an input_control, or this call's
// accepted, held or refused submission, with refused and the recorded reason
// for a refusal (submission_refused, or held_input_refused for provisional
// input a control or the gateway's stop retired before it was recorded held).
//
// A record whose consumed_at is before since is an earlier input's, written
// late, such as an identical text's refusal after a gateway stall, and is
// passed over (a control the gateway merged into a pending earlier one keeps
// that one's time, which costs the full wait, never a refusal); a record with
// no consumed_at, such as one from a gateway older than this check or the
// combined input capacity overflow, is judged by the rest alone. An accepted
// or held submission is this call's when its text is the call's text, and a
// refusal when its text_sha256 is digest; an Enter-only send takes any
// accepted or held submission and any refusal with a text_sha256. Both
// compare unnormalized: the gateway records the runes typed into its empty
// input line as typed, and send-keys has already stripped CR, while LF, tabs
// and trailing whitespace reach it unchanged. A text the gateway's line editor
// changes (other control characters, or a draft already on the line) matches
// nothing and keeps success.
//
// A refusal without text_sha256 is never claimed, by a text or an Enter-only
// send: one from a gateway older than this check, one retiring input already
// recorded held, the combined input capacity overflow, and accepted input
// refused because the gateway has no session yet. A wrong refusal would invite
// resending a prompt the gateway admitted. A line that does not decode,
// another kind and another text's verdict are passed over.
func (r *sendKeysRun) cursorRecordVerdict(line []byte, text, digest string, since time.Time) (reason string, refused, done bool) {
	var rec struct {
		Kind  string          `json:"kind"`
		Token string          `json:"launch_token"`
		Data  json.RawMessage `json:"data"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return "", false, false
	}
	if rec.Token != r.row.Identity.Token {
		return "", false, true
	}
	var d struct {
		Text       string    `json:"text"`
		TextSHA256 string    `json:"text_sha256"`
		Reason     string    `json:"reason"`
		ConsumedAt time.Time `json:"consumed_at"`
	}
	if json.Unmarshal(rec.Data, &d) != nil || !d.ConsumedAt.IsZero() && d.ConsumedAt.Before(since) {
		return "", false, false
	}
	switch rec.Kind {
	case cursortranscript.KindInputControl:
		return "", false, true
	case cursortranscript.KindSubmissionAccepted, cursortranscript.KindInputHeld:
		return "", false, text == "" || d.Text == text
	case cursortranscript.KindSubmissionRefused, cursortranscript.KindHeldInputRefused:
		return d.Reason, true, d.TextSHA256 != "" && (text == "" || d.TextSHA256 == digest)
	}
	return "", false, false
}

// gatewayRefusedError is send-keys' error when a Cursor row's gateway refused
// the submission the call's Enter made (b.bqd): ErrSubmissionUnconfirmed with
// outcome gateway_refused, the text's bytes, the gateway's recorded reason and
// the row's state as read. The gateway discarded the text, so nothing of it
// reached Cursor.
func gatewayRefusedError(row Spawn, bytesAttempted int, reason string) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: send-keys: %s: gateway_refused (%s); the Cursor gateway refused the submission: nothing reached Cursor and the gateway discarded the text; read-pane and get, then answer or clear what holds the input before sending again, or kill and resume the row",
			ErrSubmissionUnconfirmed, row.InstanceID, reason),
		Details: SubmissionUnconfirmedDetails{Outcome: "gateway_refused", Phase: "enter_attempted", BytesAttempted: bytesAttempted,
			EnterMayHaveBeenSent: true, Reason: reason, KeysSent: true, RowState: row.State},
	}
}
