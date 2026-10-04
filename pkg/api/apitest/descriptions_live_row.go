package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_live_row.go holds SR-18.6's live-row sequence as the manifest
// states it (decision-0930b Q6): its short form, which only kill's
// description states (DescLiveRowSequence, LiveRowSequenceCount), and the one-sentence pointer to it that ends the
// find-missing and spawn descriptions (LiveRowPointer, DescLiveRowPointer,
// LiveRowPointerCount). The two are told apart by their openings: the short
// form opens with liveRowShortOpening, the pointer with liveRowOpening.

const (
	// liveRowShortOpening opens the short form.
	liveRowShortOpening = "Live-row sequence (a pending row included):"
	// liveRowShortClosing is the short form's last phrase (step 6's).
	liveRowShortClosing = "; callers whose ids agent-director mints spawn fresh."
	// liveRowOpening opens the pointer, and opened the long form the manifest
	// stated before the short form; LiveRowSequenceCount counts a long-form
	// statement by it.
	liveRowOpening = "To end a live row (pending included) and relaunch its id"
)

// LiveRowPointer is SR-18.6's pointer sentence, verbatim: it ends the
// find-missing and spawn descriptions in place of the sequence.
const LiveRowPointer = liveRowOpening + ", follow the live-row sequence in kill's description."

// liveRowStep is one step of the short form: phrases, the step's text as
// DescLiveRowSequence requires it (the first one numbered), and keys, short
// phrases of it that DescLiveRowPointer forbids so a text restating the step
// in any wording that keeps them fails.
type liveRowStep struct {
	phrases []string
	keys    []string
}

// liveRowSteps is the short form's six steps, in order.
func liveRowSteps() []liveRowStep {
	return []liveRowStep{
		{
			phrases: []string{"kill and check the result; on an error follow its class, never delete the row"},
			keys:    []string{"kill and check the result", "never delete the row"},
		},
		{
			phrases: []string{
				"If the row is pending, wait until its launch start (status)",
				fmt.Sprintf("plus the pending grace period (%d s unless configured)", config.DefaultPendingGraceSeconds),
				"inside it, wait and check again, never escalate",
			},
			keys: []string{"wait until its launch start", "unless configured", "never escalate"},
		},
		{
			phrases: []string{"Run find-missing, then check status; repeat about 5 s apart until the row is ended or missing, at most three runs"},
			keys:    []string{"then check status", "about 5 s apart", "at most three runs"},
		},
		{
			phrases: []string{"Still live: kill once more, wait about 5 s, run find-missing once more and check"},
			keys:    []string{"kill once more", "wait about 5 s", "find-missing once more"},
		},
		{
			phrases: []string{"Still live: escalate to a human"},
			keys:    []string{"escalate to a human"},
		},
		{
			phrases: []string{
				"Then resume the row if it has a session id and the caller wants the conversation back",
				"otherwise spawn with " + reuseOptInSpelling,
				liveRowShortClosing,
			},
			keys: []string{"if it has a session id", "wants the conversation back", "otherwise spawn with " + reuseOptInName, "spawn fresh"},
		},
	}
}

// DescLiveRowSequence is SR-18.6's live-row sequence in its short form, as
// kill's manifest description states it, by key phrase: its opening; (1) kill,
// follow the error's class and never delete; (2) for a pending row, wait out
// its launch start plus the pending grace period
// (config.DefaultPendingGraceSeconds, a default the operator can change), and
// inside it wait and check again, never escalate; (3) up to three find-missing
// runs about 5 s apart, each checked with status; (4) one more kill, "wait
// about 5 s" and a last find-missing; (5) a human; (6) resume or spawn with
// the reuse opt-in in its one spelling (reuseOptInSpelling, b.c4u), and a
// caller whose ids agent-director mints spawns fresh.
// It must not carry the rationale the README keeps ("history belongs to a
// life", "unreachable for good", SR/OFR citations), the long form's or the
// pointer's opening, or the pointer. Check it with AssertAgentTextCase.
func DescLiveRowSequence() DescCase {
	req := []string{liveRowShortOpening}
	for i, s := range liveRowSteps() {
		req = append(req, fmt.Sprintf("%d. %s", i+1, s.phrases[0]))
		req = append(req, s.phrases[1:]...)
	}
	return DescCase{
		Name:    "manifest description, live-row sequence (short form)",
		Require: req,
		MustNot: []string{
			"history belongs to a life", "unreachable for good", "SR-", "OFR",
			liveRowOpening, LiveRowPointer,
		},
	}
}

// DescLiveRowPointer is SR-18.6's pointer to the live-row sequence as the
// find-missing and spawn manifest descriptions state it: LiveRowPointer
// exactly, and neither the short form's opening nor any key phrase of its
// steps, so a description that restates a step, with or without the opening,
// fails. Check it with AssertAgentTextCase.
func DescLiveRowPointer() DescCase {
	mustNot := []string{liveRowShortOpening}
	for _, s := range liveRowSteps() {
		mustNot = append(mustNot, s.keys...)
	}
	return DescCase{
		Name:    "manifest description, pointer to the live-row sequence",
		Require: []string{LiveRowPointer},
		MustNot: mustNot,
	}
}

// LiveRowSequenceCount returns how many times text states the live-row
// sequence itself: each short-form opening, plus each long-form opening
// (liveRowOpening where it does not begin LiveRowPointer). Pointer sentences
// are never counted; LiveRowPointerCount counts those.
func LiveRowSequenceCount(text string) int {
	return strings.Count(text, liveRowShortOpening) +
		strings.Count(text, liveRowOpening) - LiveRowPointerCount(text)
}

// LiveRowPointerCount returns how many times text carries LiveRowPointer,
// the whole sentence. A short-form or long-form statement of the sequence is
// never counted; LiveRowSequenceCount counts those.
func LiveRowPointerCount(text string) int {
	return strings.Count(text, LiveRowPointer)
}
