package api

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// PasteKeysTmux is the optional paste transport for ordinary Codex fallback
// input. Existing SendKeysTmux implementations remain source-compatible, but
// must implement this capability to deliver that input. Manual operations and
// native delivery keep using SendKeysPane/SendKeyPane.
type PasteKeysTmux interface {
	// PasteKeysPane preserves literal text and LF in one paste, followed by
	// exactly one Enter only after the paste succeeds. It never retries text
	// or Enter, and reports errors as *TmuxCallError (text or Enter send).
	PasteKeysPane(socket, paneID, text string) error
}

// codexPasteRefusal deliberately does not reproduce the composer's parser.
// Conservatively refuse a dollar token at the insertion cursor, including
// trailing horizontal whitespace (completion can retain same-line affinity)
// and shell spellings that some versions exclude. A newline ends that affinity;
// earlier tokens and dollar signs embedded in a non-dollar token remain literal.
// No size cutoff is used, even though some versions represent large pastes with
// placeholders.
func codexPasteRefusal(text string) string {
	const advice = "; put the prompt in a file and send a short instruction to read it"
	if !utf8.ValidString(text) {
		return "Codex fallback paste requires valid UTF-8" + advice
	}
	for _, char := range text {
		if unicode.IsControl(char) && char != '\n' {
			return "Codex fallback paste cannot safely deliver a control character" + advice
		}
	}
	text = strings.TrimRightFunc(text, func(char rune) bool {
		return unicode.IsSpace(char) && char != '\n' && char != '\u2028' && char != '\u2029'
	})
	last := 0
	for index, char := range text {
		if unicode.IsSpace(char) {
			last = index + utf8.RuneLen(char)
		}
	}
	if strings.HasPrefix(text[last:], "$") {
		return "a final dollar token may open the Codex skill picker and consume Enter" + advice
	}
	return ""
}

func (r *sendKeysRun) pasteText(paneID, text string) error {
	r.found.Sent = true
	if err := r.paste.PasteKeysPane(r.socket, paneID, text); err != nil {
		// A fallback transport error cannot prove what the composer received.
		// In particular, the legacy manual-path advice to send another Enter
		// is unsafe here. Keep the existing structured uncertainty contract.
		a := &inputdelivery.Attempt{Phase: "text_attempted", BytesAttempted: len(text)}
		var call *tmux.CallError
		if !errors.As(err, &call) || call.Call != tmux.CallSendText {
			a.Phase, a.EnterAttempted = "enter_attempted", true
		}
		r.found.SendErr = deliveryError(r.row, a, unresolvedOutcome(a), "Codex fallback paste transport failed; inspect get or status and escalate if submission remains unknown: "+err.Error())
		return r.found.SendErr
	}
	return nil
}
