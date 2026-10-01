package api

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// unusableNameConsequence ends every unusable-name description (SR-1.4): the
// name cannot be used, removing the row is a human's decision with the
// pointer to the README's "Operator actions" (SR-18.17), and no tmux call was
// made.
const unusableNameConsequence = "the name cannot be used, so removing the row is a human's decision, " + operatorActionsPointer + "; no tmux call was made"

// unusableNameError is the verb-agnostic refusal of a row whose recorded
// tmux session name must never be passed to tmux (SR-3.2): nil for a usable
// name, otherwise an ErrInternal-class error, matching no catalogued sentinel
// under errors.Is (SR-1.3, SR-1.5), with the SR-1.4 description of the
// guard's kind. It decides only through tmux.Unusable (the first fault in the
// order empty, control character, rewritten wins) and, for the rewritten
// kind, names which character through tmux.RewrittenIn; it re-implements no
// character rule. The name is quoted in Go quoted-string form, so every
// control character and invalid byte is written out as an escape.
//
//   - empty: "the recorded tmux session name is empty";
//   - control character: the quoted name and "the recorded tmux session name
//     contains a control character";
//   - rewritten: the quoted name and "the recorded tmux session name contains
//     a character tmux stores differently", naming which: '.', ':' or bytes
//     that are not valid UTF-8 (each one the name holds).
//
// Each then says the name cannot be used, so removing the row is a human's
// decision, points to "Operator actions", and says no tmux call was made. No
// description names a session-ending command, another row's id or the
// opt-in. Every verb that would look a row up calls it on the recorded name
// before the socket and any tmux call: kill of a live row (SR-6.1) and of a
// finished row with the opt-in, read-pane, send-keys after its state and
// relay guards, pause on a waiting row, resume first in its step 2, and
// reuse after its live-row collision.
func unusableNameError(name string) error {
	switch tmux.Unusable(name) {
	case tmux.UnusableEmpty:
		return errors.New("the recorded tmux session name is empty; " + unusableNameConsequence)
	case tmux.UnusableControl:
		return errors.New("tmux session " + strconv.Quote(name) + ": the recorded tmux session name contains a control character; " + unusableNameConsequence)
	case tmux.UnusableRewritten:
		return errors.New("tmux session " + strconv.Quote(name) + ": the recorded tmux session name contains a character tmux stores differently (" +
			rewrittenWhich(tmux.RewrittenIn(name)) + "); " + unusableNameConsequence)
	}
	return nil
}

// rewrittenWhich names the rewritten characters r records, in the order
// '.', ':', invalid UTF-8, joined as a list ("'.' and ':'").
func rewrittenWhich(r tmux.Rewritten) string {
	var which []string
	if r.Dot {
		which = append(which, "'.'")
	}
	if r.Colon {
		which = append(which, "':'")
	}
	if r.InvalidUTF8 {
		which = append(which, "bytes that are not valid UTF-8")
	}
	switch len(which) {
	case 0:
		return ""
	case 1:
		return which[0]
	}
	return strings.Join(which[:len(which)-1], ", ") + " and " + which[len(which)-1]
}
