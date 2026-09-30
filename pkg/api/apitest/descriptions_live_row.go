package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_live_row.go holds SR-18.6's bounded, paced live-row sequence
// as the kill, find-missing and spawn manifest descriptions state it
// (Epic 10): DescLiveRowSequence, and LiveRowSequenceSpans, which cuts each
// statement of the sequence out of a text so the three can be compared.

// The live-row sequence's first and last phrases, which bound a statement of
// it (LiveRowSequenceSpans).
const (
	liveRowOpening = "To end a live row (pending included) and relaunch its id"
	liveRowClosing = "a reuse makes that conversation unreachable for good"
)

// DescLiveRowSequence is SR-18.6's live-row sequence as a manifest
// description states it, by key phrase: kill first, follow the error's class
// and never delete; for a pending row, wait out its launch start plus the
// pending grace period (config.DefaultPendingGraceSeconds, a default the
// operator can change), and inside it wait and check again, never escalate;
// up to three find-missing runs about 5 s apart, each confirmed with status or
// get; one more kill, a wait and a last find-missing; then a human; once
// ended or missing, resume or spawn with --reuse-finished, and a caller whose
// ids agent-director mints spawns fresh. Check it with AssertAgentTextCase.
func DescLiveRowSequence() DescCase {
	return DescCase{
		Name: "manifest description, live-row sequence",
		Require: []string{
			liveRowOpening, "bounded, paced sequence",
			"(1) kill, and check the result; on any error follow its class and never delete the row",
			"(2) If the row is pending, wait until its launch start (shown by status)",
			fmt.Sprintf("plus the pending grace period (%d s unless the operator configured another value) has passed", config.DefaultPendingGraceSeconds),
			"a pending row inside its grace period means wait and check again later, never escalate",
			"(3) Run find-missing, then confirm with status or get that the row is ended or missing",
			"if not, wait about 5 s and repeat, up to three find-missing runs in all",
			"(4) If still live, kill once more, wait about 5 s, run find-missing once more and check",
			"(5) If still live, stop and escalate to a human",
			"(6) Once the row is ended or missing, resume it if it has a session id and the caller wants the conversation back",
			"otherwise spawn with --reuse-finished",
			"A caller whose ids agent-director mints spawns fresh instead of reusing",
			"A pending row, a resumed one included, enters this sequence", liveRowClosing,
		},
	}
}

// LiveRowSequenceSpans returns each statement of the live-row sequence in
// text, from its opening phrase through its closing phrase, in order; a
// statement with no closing phrase runs to the end of text.
func LiveRowSequenceSpans(text string) []string {
	var spans []string
	for {
		i := strings.Index(text, liveRowOpening)
		if i < 0 {
			return spans
		}
		text = text[i:]
		end := len(text)
		if j := strings.Index(text, liveRowClosing); j >= 0 {
			end = j + len(liveRowClosing)
		}
		spans = append(spans, text[:end])
		text = text[end:]
	}
}
