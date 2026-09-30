package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_find_missing.go holds the shared description helper's texts of
// find-missing's own manifest description (Epic 12): its pending grace
// statement (DescFindMissingGrace), and FindMissingOwnText, which cuts the
// description down to the text before SR-18.6's pointer to the live-row
// sequence (LiveRowPointer), so a pin checks find-missing's own sentences
// only.

// DescFindMissingGrace is find-missing's pending grace statement as its
// manifest description states it (SR-18.9, SR-11.2): a pending row is not
// judged inside the pending grace period (pending_grace_seconds, its default
// config.DefaultPendingGraceSeconds), measured from its launch start rather
// than its started_at, and a pending row can be a resume's launch; never the
// old claim that the scan judges every pending row. Check it with
// AssertAgentTextCase on FindMissingOwnText of the description.
func DescFindMissingGrace() DescCase {
	return DescCase{
		Name: "find-missing manifest, pending grace statement",
		Require: []string{
			"is not judged while inside the pending grace period",
			fmt.Sprintf("pending_grace_seconds, %d s by default", config.DefaultPendingGraceSeconds),
			"measured from its launch start, not its started_at",
			"a resume's launch",
		},
		MustNot: []string{"live-state rows (including pending)"},
	}
}

// FindMissingOwnText returns the part of find-missing's description before
// LiveRowPointer, the sentence that ends it (SR-18.6). It panics when text
// does not carry the pointer, so a description that lost it fails the calling
// test instead of being pinned whole.
func FindMissingOwnText(text string) string {
	i := strings.Index(text, LiveRowPointer)
	if i < 0 {
		panic("apitest: FindMissingOwnText: find-missing description does not carry LiveRowPointer: " + text)
	}
	return text[:i]
}
