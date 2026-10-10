package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pieces of b.146 rules 7, 8 and 13 that touch the pane:
// the pane's hash, the named keys, the shape of a send-keys call (plain or a
// pane answer) and the pane answer's sender. None of them reads what the pane
// says: the hash is byte equality, the named keys are a lookup table, and the
// claimed verdict (as) is stored, never checked against the key.

// paneSHA256 is the SHA-256 of pane's bytes, lowercase hex: read-pane's
// pane_sha256, and what send-keys and record-pane-answer compare
// expect_pane_sha256 with.
func paneSHA256(pane string) string {
	sum := sha256.Sum256([]byte(pane))
	return hex.EncodeToString(sum[:])
}

// parsePaneSHA256 reads a caller's expect_pane_sha256: 64 hex digits, either
// case, returned lowercase; ok false for anything else.
func parsePaneSHA256(h string) (string, bool) {
	if len(h) != sha256.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", false
	}
	return strings.ToLower(h), true
}

// paneKeyNames is the lookup table of the keys send-keys sends by name (b.146
// rule 8, decision 7 A): each name a caller passes, to the tmux key name sent.
// The caller picks the key; agent-director never picks one.
var paneKeyNames = map[string]string{
	"Escape": "Escape",
	"Enter":  "Enter",
	"Up":     "Up",
	"Down":   "Down",
	"Tab":    "Tab",
}

// paneKey is the one key a send-keys call with key sends: a named key, sent
// by its tmux key name (name), or a single character, typed literally (char).
type paneKey struct {
	name string
	char string
}

// parsePaneKey reads a caller's key: a name of paneKeyNames, or exactly one
// character that is not a control character; ok false for anything else (a
// longer text, an empty one, invalid UTF-8, a control character).
func parsePaneKey(k string) (paneKey, bool) {
	if name, ok := paneKeyNames[k]; ok {
		return paneKey{name: name}, true
	}
	if !utf8.ValidString(k) || utf8.RuneCountInString(k) != 1 {
		return paneKey{}, false
	}
	if r, _ := utf8.DecodeRuneInString(k); unicode.IsControl(r) {
		return paneKey{}, false
	}
	return paneKey{char: k}, true
}

// sendKeysPlan is what one send-keys call sends, from its params
// (planSendKeys).
type sendKeysPlan struct {
	// answer marks a pane answer (a request token given, b.146 rule 8).
	answer bool
	// key is the one key to send; nil to type text.
	key *paneKey
	// text is the text to type (CR bytes stripped), and enter whether Enter
	// follows it.
	text  string
	enter bool
	// hash is expect_pane_sha256, lowercase; "" for none.
	hash string
	// nLines is the number of trailing pane lines the hash is over.
	nLines int
}

// The values of a pane answer's as and of record-pane-answer's as.
const (
	paneAsAllow   = "allow"
	paneAsDeny    = "deny"
	paneAsUnknown = "unknown"
)

// planSendKeys checks params' shape and returns what the call sends, or
// ErrInvalidFlags before anything is read, sent or written (b.146 rule 8,
// decision 7 A):
//
//   - A pane answer (request_token): as (allow or deny), key and
//     expect_pane_sha256 are required, and text must be empty: it sends
//     exactly one key and never Enter (no_enter changes nothing).
//   - Plain: as is refused; key (a named key or one character) is exclusive
//     with a non-empty text and sends that key alone, with no Enter; text is
//     typed with Enter after it unless no_enter; no_enter with neither text
//     nor key sends nothing and is refused; expect_pane_sha256 is optional.
//   - expect_pane_sha256 is 64 hex digits; n_lines is not negative, 0
//     meaning DefaultReadPaneLines.
func planSendKeys(p SendKeysParams) (sendKeysPlan, error) {
	bad := func(format string, args ...any) (sendKeysPlan, error) {
		return sendKeysPlan{}, fmt.Errorf("%w: send-keys: "+format+"; nothing was sent", append([]any{ErrInvalidFlags}, args...)...)
	}
	plan := sendKeysPlan{answer: p.RequestToken != "", text: strings.ReplaceAll(p.Text, "\r", ""), nLines: p.NLines}
	if p.NLines < 0 {
		return bad("n_lines %d must not be negative", p.NLines)
	}
	if plan.nLines == 0 {
		plan.nLines = DefaultReadPaneLines
	}
	if p.ExpectPaneSHA256 != "" {
		h, ok := parsePaneSHA256(p.ExpectPaneSHA256)
		if !ok {
			return bad("expect_pane_sha256 %q is not a SHA-256 in hex (64 hex digits)", p.ExpectPaneSHA256)
		}
		plan.hash = h
	}
	if p.Key != "" {
		k, ok := parsePaneKey(p.Key)
		if !ok {
			return bad("key %q is neither a named key (Escape, Enter, Up, Down, Tab) nor one character", p.Key)
		}
		plan.key = &k
	}
	if plan.answer {
		switch {
		case p.As != paneAsAllow && p.As != paneAsDeny:
			return bad("a pane answer (request_token) needs as allow or deny, got %q", p.As)
		case plan.key == nil:
			return bad("a pane answer (request_token) needs key, the one key it sends")
		case plan.hash == "":
			return bad("a pane answer (request_token) needs expect_pane_sha256, the pane_sha256 of the read-pane its answer was chosen from")
		case p.Text != "":
			return bad("a pane answer (request_token) sends exactly one key: text must be empty")
		}
		return plan, nil
	}
	switch {
	case p.As != "":
		return bad("as applies only to a pane answer (request_token)")
	case plan.key != nil && p.Text != "":
		return bad("key and a non-empty text are exclusive")
	case plan.key == nil && p.NoEnter && plan.text == "":
		return bad("no_enter with no text and no key sends nothing")
	}
	plan.enter = plan.key == nil && !p.NoEnter
	return plan, nil
}

// paneIntentReserve is the time added to the tmux action timeout and the
// pipe-close wait in paneIntentHold: a pane answer's sender writes sent, or
// releases its intent, right after its one key call returns.
const paneIntentReserve = 2 * time.Second

// paneIntentHold is how long a pane answer's intent whose sender cannot be
// checked (another or unreadable pid namespace, or no identity recorded)
// counts as in progress after it was written (b.146 problem 2, rule 14's time
// fallback): the longest its one key call can take (the [tmux] action timeout
// plus the pipe-close wait) plus paneIntentReserve.
func paneIntentHold(t config.Tmux) time.Duration {
	return t.EffectiveActionTimeout() + t.EffectivePipeCloseWait() + paneIntentReserve
}

// selfIdentity reads this process's own identity, which a pane answer
// records as its sender (b.146 rule 8): its pid, its start time through pc
// and its pid namespace through ns. When either cannot be read it returns the
// zero identity, which records none: a reader then judges the intent by its
// time (paneIntentHold).
func selfIdentity(pc ProcChecker, ns func() (string, bool)) ProcessIdentity {
	if pc == nil || ns == nil {
		return ProcessIdentity{}
	}
	pid := os.Getpid()
	start, alive, known := pc.StartTime(pid)
	space, nsKnown := ns()
	if !alive || !known || start == "" || !nsKnown {
		return ProcessIdentity{}
	}
	return ProcessIdentity{PID: pid, Starttime: start, PIDNamespace: space}
}

// paneChangedError is ErrPaneChanged with its err_details (b.146 rule 7): the
// pane's last nLines lines, captured with ANSI stripped, no longer hash to
// the caller's expect_pane_sha256. what says what the call did not do. It
// never carries the new hash.
func paneChangedError(instanceID, token string, nLines int, what string) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: %s: the agent's pane (last %d lines) no longer matches expect_pane_sha256: it changed since it was read; read-pane again before any retry; %s",
			ErrPaneChanged, instanceID, nLines, what),
		Details: PaneChangedDetails{NLines: nLines, RequestToken: token},
	}
}

// capturePaneHash captures the agent's pane paneID on socket as read-pane
// gives it by default (the last nLines lines, ANSI stripped) and returns its
// SHA-256 (paneSHA256), or the capture's error as t gave it.
func capturePaneHash(t interface {
	CapturePaneID(socket, paneID string, nLines int, ansi bool) (string, error)
}, socket, paneID string, nLines int) (string, error) {
	pane, err := t.CapturePaneID(socket, paneID, nLines, false)
	if err != nil {
		return "", err
	}
	return paneSHA256(tmux.StripANSI(pane)), nil
}
