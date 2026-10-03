package apitest

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// descriptions_pane.go holds the shared description helper's SR-1.4 cases
// that the pane verbs introduce (Epic 11), parameterised by verb so that
// read-pane, send-keys and pause share them: the agent's pane was not found
// (DescPaneNotFound, with its lost-reply variant; a lone leftover whose
// launch token no pane carries uses it too), a pane verb on Leftover
// (DescPaneLeftover, built from kill's Leftover blocks, leftoverCase), and
// the verb's gone error (DescPaneGone); only these say what the verb did
// not do ("nothing was read", "nothing was sent"). A pane verb's tmux
// trouble (a timeout or unrecognised reply of the lookup, the pane listing
// or the action, a different server, conflicting labels, tmux unavailable,
// and the follow-up after a failed action) says "nothing was done", like
// kill, so it uses DescCallTimeout, DescUnrecognisedReply,
// DescDifferentServer, DescConflictingLabels (with NothingWasDone),
// DescSocketPermission and DescTmuxNotRun unchanged. The keys actions
// (send-keys' text and Enter, pause's) add what they may have done and
// each verb's next step (b.9o4): a timed-out text or Enter send
// (DescKeysTimeout), and any class after a failed Enter send
// (DescCase.AfterEnterFailed) or text send (DescCase.AfterTextFailed,
// "nothing was done; retry later"). send-keys' two pending-row refusals are
// DescSendKeysPendingNoLaunch and DescSendKeysPendingLeftover. No pane-verb
// case may give the retired "no pane 0.0" clause or a base-index hint
// (WD 2026-09-29c). The pane verbs' manifest texts are here too: each
// Description's tmux error classes (DescPaneManifest, SR-18.1) and
// send-keys' allow_pending text (DescAllowPending, SR-18.14).

// PaneVerb is a pane verb whose refusals the pane cases check.
type PaneVerb string

// The pane verbs.
const (
	PaneReadPane PaneVerb = "read-pane"
	PaneSendKeys PaneVerb = "send-keys"
	PanePause    PaneVerb = "pause"
)

// nothing returns the sentence v's pane refusals give for what was not
// done: read-pane's "nothing was read", send-keys' and pause's "nothing was
// sent".
func (v PaneVerb) nothing() string {
	switch v {
	case PaneReadPane:
		return "nothing was read"
	case PaneSendKeys, PanePause:
		return "nothing was sent"
	}
	panic("apitest: unknown pane verb " + strconv.Quote(string(v)))
}

// The pane-verb phrases (SR-1.4, SR-3.4, SR-3.6, SR-3.7): the agent's pane
// not found and its lost-reply clause, read-pane's leftover count, and the
// gone error's "the row's session is not there".
const (
	paneNotFound        = "the agent's pane was not found"
	paneNotAdopted      = "the agent's pane was not adopted"
	noPaneCarriesLabel  = "no pane carries this launch's pane label"
	moreThanOneLeftover = "more than one leftover session exists"
	rowSessionNotThere  = "the row's session is not there"
)

// paneMustNot is what no pane-verb description may say: the retired "no
// pane 0.0" clause and any base-index hint (base-index, pane-base-index;
// WD 2026-09-29c).
var paneMustNot = []string{"no pane 0.0", "base-index"}

// PaneNotFound parameterises DescPaneNotFound: the verb; the row's instance
// id; Name, the session carrying a label with the row's id (Ours's session,
// or the lone leftover's when no pane carries its launch token); and
// LostReply, a lost create reply whose pane could not be adopted (SR-3.6).
type PaneNotFound struct {
	Verb       PaneVerb
	InstanceID string
	Name       string
	LostReply  bool
}

// DescPaneNotFound is ErrTmuxSessionConflict for "the agent's pane was not
// found" (pane verbs; SR-1.4, SR-3.7): the instance id; the quoted name;
// "this row's own id"; "the agent's pane was not found"; the verb's sentence
// for what was not done; with LostReply, that the agent's pane was not
// adopted because no pane carries this launch's pane label (which it must
// not say otherwise); the "Operator actions" pointer; and "list
// --tmux-session-name". It never says "not this launch's session" or that
// more than one leftover session exists.
func DescPaneNotFound(p PaneNotFound) DescCase {
	req := []string{p.InstanceID, strconv.Quote(p.Name), thisRowsOwnID, paneNotFound, p.Verb.nothing(), listSessionName}
	mustNot := append(append([]string(nil), paneMustNot...), notThisLaunch, moreThanOneLeftover)
	name := "ErrTmuxSessionConflict, " + string(p.Verb) + ", the agent's pane was not found"
	if p.LostReply {
		name += " after a lost reply"
		req = append(req, paneNotAdopted, noPaneCarriesLabel)
	} else {
		mustNot = append(mustNot, "not adopted", noPaneCarriesLabel)
	}
	return DescCase{Name: name, Require: req, MustNot: mustNot}.PointsToOperatorActions()
}

// PaneLeftover parameterises DescPaneLeftover: the verb; the row's instance
// id; and the leftover sessions found, in the order the description names
// them (lowest $N first; up to three, then their count), whose number is the
// leftover count.
type PaneLeftover struct {
	Verb       PaneVerb
	InstanceID string
	Sessions   []DescSession
}

// DescPaneLeftover is ErrTmuxSessionConflict for a pane verb on Leftover
// (SR-1.4, SR-3.4): kill's Leftover blocks (each session's quoted name and
// tmux id, "this row's own id", "not this launch's session", the "Operator
// actions" pointer, "list --tmux-session-name"; leftoverCase), the instance
// id and the verb's sentence for what was not done. read-pane refuses only
// more than one leftover (it reads a lone leftover's pane), so for it the
// case requires "more than one leftover session exists" and panics on fewer
// than two sessions; the other verbs must not say it. Never "the agent's
// pane was not found".
func DescPaneLeftover(p PaneLeftover) DescCase {
	req := []string{p.InstanceID, p.Verb.nothing()}
	mustNot := append(append([]string(nil), paneMustNot...), paneNotFound)
	if p.Verb == PaneReadPane {
		if len(p.Sessions) < 2 {
			panic("apitest: read-pane refuses Leftover only with more than one leftover session")
		}
		req = append(req, moreThanOneLeftover)
	} else {
		mustNot = append(mustNot, moreThanOneLeftover)
	}
	return leftoverCase("ErrTmuxSessionConflict, "+string(p.Verb)+" on Leftover", p.Sessions, req, mustNot)
}

// PaneGone parameterises DescPaneGone: the verb; the row's instance id; its
// recorded name (quoted when set); and FailedCall, the action call whose
// failure's follow-up lookup was Gone or Leftover ("" after a Gone lookup).
type PaneGone struct {
	Verb       PaneVerb
	InstanceID string
	Name       string
	FailedCall tmux.Call
}

// DescPaneGone is a pane verb's gone error (ErrTmuxCaptureFailed for
// read-pane, ErrTmuxSendKeys for send-keys and pause; SR-7.2, SR-7.3): the
// instance id, "the row's session is not there", the quoted recorded name
// when set, "tmux <call> failed" for FailedCall when set, and the verb's
// sentence for what was not done; never "dead" or "gone". SR-1.4 has no row
// for it; the case keeps its phrases in the helper.
func DescPaneGone(p PaneGone) DescCase {
	req := []string{p.InstanceID, rowSessionNotThere, p.Verb.nothing()}
	if p.Name != "" {
		req = append(req, strconv.Quote(p.Name))
	}
	if p.FailedCall != "" {
		req = append(req, "tmux "+string(p.FailedCall)+" failed")
	}
	return DescCase{
		Name:    "gone error, " + string(p.Verb) + ", the row's session is not there",
		Require: req,
		MustNot: append(append([]string(nil), paneMustNot...), unresponsiveMustNot...),
	}
}

// The keys actions' phrases (send-keys' text and Enter, pause's /exit and
// Enter; SR-1.4 rows "keys action timed out" and "Enter failed after the
// text went through", SR-7.3) and the pending refusals' "no tmux call was
// made" (SR-1.4, SR-7.1).
const (
	keysMayHaveBeenDelivered = "the keys may have been delivered"
	textNotSubmitted         = "the text may be typed but not submitted"
	noTmuxCallMade           = "no tmux call was made"
)

// send-keys' next steps after a keys failure, in place of "retry later"
// (b.9o4): after a timed-out text call, read the pane and send empty text
// (Enter only) if the text is typed; after a failed or timed-out Enter, send
// empty text. pause keeps "retry later" (its retry clears the line first)
// and must not name them.
const (
	sendKeysEmptyText        = "send-keys with empty text"
	sendKeysTextTimeoutNext  = "read-pane; if the text is typed, " + sendKeysEmptyText + ", otherwise the same send-keys"
	sendKeysEmptyTextSubmits = sendKeysEmptyText + " submits it"
)

// keysMustNot is what a description after a keys action that may have typed
// keys must not say: that nothing was done or sent.
var keysMustNot = []string{nothingWasDone, "nothing was sent"}

// DescSendKeysPendingNoLaunch is send-keys' ErrSpawnNotInteractive for a
// pending row, with allow_pending, whose launch start or launch token is not
// recorded (SR-1.4, SR-7.1, SR-22.8): the instance id; that the launch start
// or launch token is not recorded, so agent-director cannot show that a
// session belongs to the current launch; that nothing was sent and no tmux
// call was made; the "Operator actions" pointer. Never "not this launch's
// session".
func DescSendKeysPendingNoLaunch(instanceID string) DescCase {
	return DescCase{
		Name: "ErrSpawnNotInteractive, send-keys, pending row with no launch start or token",
		Require: []string{
			instanceID, "launch start or launch token is not recorded",
			"agent-director cannot show that a session belongs to the current launch",
			PaneSendKeys.nothing(), noTmuxCallMade,
		},
		MustNot: append(append([]string(nil), paneMustNot...), notThisLaunch),
	}.PointsToOperatorActions()
}

// DescSendKeysPendingLeftover is send-keys' ErrSpawnNotInteractive for a
// pending row, with allow_pending, whose lookup is Leftover (SR-1.4, SR-7.1,
// SR-3.4): the instance id; each leftover session's quoted name and tmux id
// in the order the description names them (lowest $N first; up to three,
// then their count; the rest's names must not appear); "not this launch's
// session"; that nothing was sent. Never "no tmux call was made" (the lookup
// ran), "the agent's pane was not found" or that more than one leftover
// session exists.
func DescSendKeysPendingLeftover(instanceID string, sessions []DescSession) DescCase {
	named, unnamed := namedSessions(sessions)
	return DescCase{
		Name:    "ErrSpawnNotInteractive, send-keys, pending row on Leftover",
		Require: append([]string{instanceID, notThisLaunch, PaneSendKeys.nothing()}, named...),
		MustNot: append(append(append([]string(nil), paneMustNot...),
			noTmuxCallMade, paneNotFound, moreThanOneLeftover), unnamed...),
	}
}

// DescKeysTimeout is ErrTmuxUnresponsive for verb v's timed-out keys action,
// call tmux.CallSendText, tmux.CallSendEnter or, for pause, tmux.CallSendKey
// (its line clear before /exit, b.9o4) (SR-1.4, SR-7.3): DescCallTimeout's
// call and effective timeout, with "the keys may have been delivered" in
// place of "nothing was done", which it must not say (nor "nothing was
// sent"); for the Enter call also "the text may be typed but not submitted",
// which a text call's or line clear's timeout must not say. The next step
// (b.9o4): send-keys' text call "the keys may have been delivered; read-pane;
// if the text is typed, send-keys with empty text, otherwise the same
// send-keys", its Enter call "...; the text may be typed but not submitted;
// send-keys with empty text submits it", never "retry later"; pause's
// "retry later", never "send-keys with empty text". Never "dead" or "gone".
// Any other call or verb, or the key send for send-keys, panics.
func DescKeysTimeout(v PaneVerb, call tmux.Call, timeout time.Duration) DescCase {
	c := DescCallTimeout(call, timeout)
	c.Name += ", the keys may have been delivered"
	c.Require = append(withoutPhrases(c.Require, nothingWasDone), keysMayHaveBeenDelivered)
	c.MustNot = append(append([]string(nil), c.MustNot...), keysMustNot...)
	consequence := keysMayHaveBeenDelivered
	switch call {
	case tmux.CallSendEnter:
		c.Require = append(c.Require, textNotSubmitted)
		consequence += "; " + textNotSubmitted
	case tmux.CallSendText:
		c.MustNot = append(c.MustNot, textNotSubmitted)
	case tmux.CallSendKey:
		if v != PanePause {
			panic("apitest: DescKeysTimeout: only pause sends a key, not " + string(v))
		}
		c.MustNot = append(c.MustNot, textNotSubmitted)
	default:
		panic("apitest: DescKeysTimeout takes the text, Enter or key send, not " + strconv.Quote(string(call)))
	}
	return c.keysNextStep(v, call, consequence)
}

// AfterEnterFailed returns c as given after verb v's keys action whose text
// call went through and whose Enter call then failed other than by a timeout
// (SR-1.4, SR-7.3), in any class: "the text may be typed but not submitted"
// in place of "nothing was done" and "nothing was sent", which it must not
// say, and never "the keys may have been delivered" (DescKeysTimeout's
// Enter case covers the timeout). An ErrTmuxUnresponsive case
// (DescUnrecognisedReply) also gives the next step after it (b.9o4):
// send-keys' "send-keys with empty text submits it", never "retry later";
// pause's "retry later", never "send-keys with empty text". Any other
// class (the gone error, ErrTmuxNotAvailable) gives no next step: neither
// "retry later" nor "send-keys with empty text". Use it on DescPaneGone
// (FailedCall tmux.CallSendEnter), DescUnrecognisedReply
// (tmux.CallSendEnter), DescDifferentServer, DescSocketPermission or
// DescTmuxNotRun.
func (c DescCase) AfterEnterFailed(v PaneVerb) DescCase {
	c.Name += ", after the Enter send failed"
	c.Require = append(withoutPhrases(c.Require, keysMustNot...), textNotSubmitted)
	c.MustNot = append(append(append([]string(nil), c.MustNot...), keysMustNot...), keysMayHaveBeenDelivered)
	if !c.unanswered {
		c.MustNot = append(c.MustNot, retryLater, sendKeysEmptyText)
		return c
	}
	return c.keysNextStep(v, tmux.CallSendEnter, textNotSubmitted)
}

// keysNextStep returns c, an ErrTmuxUnresponsive case after verb v's failed
// keys call, requiring consequence followed by v's next step (b.9o4):
// send-keys' (sendKeysTextTimeoutNext after a text call, else
// sendKeysEmptyTextSubmits) in place of "retry later", which it must not
// say; pause's "retry later", and never "send-keys with empty text".
func (c DescCase) keysNextStep(v PaneVerb, call tmux.Call, consequence string) DescCase {
	switch v {
	case PaneSendKeys:
		next := sendKeysEmptyTextSubmits
		if call == tmux.CallSendText {
			next = sendKeysTextTimeoutNext
		}
		c.Name += ", " + next
		c.Require = append(withoutPhrases(c.Require, retryLater), consequence+"; "+next)
		c.MustNot = append(append([]string(nil), c.MustNot...), retryLater)
	case PanePause:
		c.Require = append(withoutPhrases(c.Require, retryLater), consequence+"; "+retryLater)
		c.MustNot = append(append([]string(nil), c.MustNot...), sendKeysEmptyText)
	default:
		panic("apitest: keys next step: " + string(v) + " types no keys")
	}
	return c
}

// AfterTextFailed returns c as given after a keys action whose text call
// failed other than by a timeout, so no key was typed (SR-1.4, SR-7.3): c
// as given, never "the text may be typed but not submitted", "the keys may
// have been delivered" or "send-keys with empty text"; an
// ErrTmuxUnresponsive case (DescUnrecognisedReply) keeps the default next
// step, "nothing was done; retry later", for both verbs (b.9o4). Use it on
// the cases AfterEnterFailed takes, with tmux.CallSendText, or with
// tmux.CallSendKey after pause's line clear failed the same way.
func (c DescCase) AfterTextFailed() DescCase {
	c.Name += ", after the text send failed"
	c.MustNot = append(append([]string(nil), c.MustNot...), textNotSubmitted, keysMayHaveBeenDelivered, sendKeysEmptyText)
	if c.unanswered {
		c.Require = append(withoutPhrases(c.Require, retryLater), nothingWasDone+"; "+retryLater)
	}
	return c
}

// goneName returns v's gone error: ErrTmuxCaptureFailed for read-pane,
// ErrTmuxSendKeys for send-keys and pause (SR-7.2, SR-7.3).
func (v PaneVerb) goneName() string {
	switch v {
	case PaneReadPane:
		return "ErrTmuxCaptureFailed"
	case PaneSendKeys, PanePause:
		return "ErrTmuxSendKeys"
	}
	panic("apitest: unknown pane verb " + strconv.Quote(string(v)))
}

// DescPaneManifest is a pane verb's manifest Description as it states its
// tmux errors (SR-18.1's last paragraph, SR-1.1): the class of v's gone name
// and of each tmux error (an "ErrTmux" name) in errorNames, the verb's
// manifest ErrorNames, each as classStated gives it from tmuxErrorClasses
// (shared with DescKillManifest), and that only the gone error means "the
// row's session is not there". It must not state a class for a tmux error
// outside that set, nor give the stale "tracked Spawn's tmux pane", the
// retired "no pane 0.0" clause or a base-index hint. A tmux name with no
// defined class panics. Check it with AssertAgentTextCase.
func DescPaneManifest(v PaneVerb, errorNames []string) DescCase {
	names := []string{v.goneName()}
	for _, n := range errorNames {
		if strings.HasPrefix(n, "ErrTmux") && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	req := []string{rowSessionNotThere}
	for _, n := range names {
		req = append(req, classStated(n))
	}
	mustNot := append(append([]string(nil), paneMustNot...), "tracked Spawn's tmux pane")
	var others []string
	for n := range tmuxErrorClasses {
		if !slices.Contains(names, n) {
			others = append(others, classStated(n))
		}
	}
	slices.Sort(others)
	mustNot = append(mustNot, others...)
	return DescCase{
		Name:    string(v) + " manifest description, tmux error classes",
		Require: req,
		MustNot: mustNot,
	}
}

// AllowPendingSite is the kind of SR-18.14 site whose allow_pending text
// DescAllowPending checks.
type AllowPendingSite int

const (
	// AllowPendingFlag is the flag's own text: the send-keys manifest
	// parameter, the CLI usage string, the SendKeysParams.AllowPending Go
	// doc and the TypeScript SendKeysParams.allow_pending doc comment.
	AllowPendingFlag AllowPendingSite = iota
	// AllowPendingRefusals is a text that lists send-keys' state refusals:
	// the SendKeys state-precondition paragraph and the
	// ErrSpawnNotInteractive doc comment.
	AllowPendingRefusals
)

// allowPendingMustNot is what no allow_pending site may say (SR-18.14,
// SR-20.6): the retired resume-starting and young-claim concepts (hyphen,
// space and joined spellings), that a pending row is always refused, and
// that the caller must wait for the first hook (the pre-Task wording).
var allowPendingMustNot = []string{
	"resume-starting", "resume starting", "resumestarting", "young claim", "young-claim",
	"always refused", "always rejected", "non-interactive too",
	"pre-SessionStart", "first SessionStart hook", "first hook",
}

// DescAllowPending is send-keys' allow_pending text at an SR-18.14 site, by
// key phrase. Every site: a pending row, whose agent has not reported in
// yet, and the current launch. AllowPendingFlag adds that the flag also
// allows a pending row of a spawn, reuse or resume, that keys go only to a
// session started by the row's current launch, and that ended and missing
// rows are still rejected. AllowPendingRefusals adds that a finished row
// (ended or missing) is refused with or without the flag, and the refusal
// when the launch start or launch token is absent or unreadable (SR-22.8).
// Pass a doc comment with its line wrapping and comment markers removed.
// Check it with AssertAgentTextCase. Another site kind panics.
func DescAllowPending(site AllowPendingSite) DescCase {
	req := []string{"a pending row", "whose agent has not reported in yet", "current launch"}
	var name string
	switch site {
	case AllowPendingFlag:
		name = "flag text"
		req = append(req, "also allow", "spawn, reuse or resume",
			"only to a session started by the row's current launch",
			"ended and missing rows are still rejected")
	case AllowPendingRefusals:
		name = "refusals text"
		req = append(req, "finished row (ended or missing)", "with or without",
			"launch start or launch token is absent or unreadable")
	default:
		panic("apitest: DescAllowPending: unknown site kind " + strconv.Itoa(int(site)))
	}
	return DescCase{
		Name:    "SR-18.14, send-keys allow_pending, " + name,
		Require: req,
		MustNot: append([]string(nil), allowPendingMustNot...),
	}
}

// withoutPhrases returns a copy of phrases without any of drop.
func withoutPhrases(phrases []string, drop ...string) []string {
	return slices.DeleteFunc(slices.Clone(phrases), func(p string) bool { return slices.Contains(drop, p) })
}
