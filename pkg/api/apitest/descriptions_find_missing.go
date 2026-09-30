package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_find_missing.go holds the shared description helper's texts of
// find-missing's manifest texts: its pending grace statement
// (DescFindMissingGrace, Epic 12), its liveness and same-environment rules
// (DescFindMissingManifest), its ids and unverified_ids result fields
// (DescFindMissingField), SR-18.2's "not proof" statement for any text that
// describes missing (DescMissingNotProof), and FindMissingOwnText, which cuts
// the description down to the text before SR-18.6's pointer to the live-row
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

// DescFindMissingManifest is find-missing's liveness and same-environment
// rules as its manifest description states them (SR-18.9, SR-18.7, SR-3.8),
// by key phrase: liveness comes only from the agent process; tmux is
// consulted, on the row's recorded socket, only for a row whose process
// cannot be checked or was never recorded; the sweep runs as the agents'
// user in their tmux environment, and a run as another user, as root or
// against another tmux server can mark live rows missing; and SR-18.7's two
// consequences (shared with DescKillManifest). It must not carry the dropped
// claims: the environment probe-set scan and "never marked missing on
// ambiguous evidence". Check it with AssertAgentTextCase on
// FindMissingOwnText of the description.
func DescFindMissingManifest() DescCase {
	return DescCase{
		Name: "find-missing manifest, liveness and same environment",
		Require: []string{
			"liveness comes only from its agent process",
			"whose process cannot be checked or was never recorded is looked up in tmux",
			"on its recorded socket",
			"same user", "same tmux environment",
			"another user", "as root", "another tmux server", "can mark live rows `missing`",
			finishedRowNotVerification, wrongServerSecondAgent,
		},
		MustNot: []string{"probe-set", "environ probe", "environment scan", "on ambiguous evidence"},
	}
}

// FindMissingField names one of find-missing's id-list result fields for
// DescFindMissingField.
type FindMissingField string

// The id-list result fields of find-missing.
const (
	FindMissingIDs           FindMissingField = "ids"
	FindMissingUnverifiedIDs FindMissingField = "unverified_ids"
)

// DescFindMissingField is find-missing's ids or unverified_ids result field
// as the manifest states it (SR-11.7, SR-18.3, SR-18.11), by key phrase. ids:
// rows marked missing on process or tmux evidence (a dead agent process, or a
// row whose process could not be checked with no session or pane of its
// current launch in tmux), a pending row only past the pending grace period,
// not proof of exit. unverified_ids: rows left unverified with a note,
// written or already current, never a pending row inside the pending grace
// period; never the old "left untouched" meaning. Both: a row whose guarded
// write found it changed or gone, or failed, is in neither list; never null.
// Check it with AssertAgentTextCase. It panics on another field.
func DescFindMissingField(f FindMissingField) DescCase {
	both := []string{"changed or gone, or failed", "in neither list", "Never null"}
	switch f {
	case FindMissingIDs:
		return DescCase{
			Name: "find-missing result field, ids",
			Require: append([]string{
				"marked `missing`, on process or tmux evidence", "agent process",
				"could not be checked", "no session or pane of their current launch",
				"a pending row only past the pending grace period",
				"not proof that the agent has exited",
			}, both...),
			MustNot: []string{"left untouched"},
		}
	case FindMissingUnverifiedIDs:
		return DescCase{
			Name: "find-missing result field, unverified_ids",
			Require: append([]string{
				"left unverified with a liveness note", "already current",
				"could not be checked", "Never a pending row inside the pending grace period",
			}, both...),
			MustNot: []string{"left untouched"},
		}
	}
	panic("apitest: DescFindMissingField: not an id-list field of find-missing: " + string(f))
}

// DescMissingNotProof is SR-18.2's statement for any text that describes
// missing (a manifest description or result field, a Go doc): missing is the
// sweep's judgement on the evidence available to it, not proof that the agent
// has exited. Its must-not phrases are claims that present ended or missing
// as dead or safe to delete; negations ("neither ended nor missing means that
// the agent is dead") pass. Check it with AssertAgentTextCase.
func DescMissingNotProof() DescCase {
	return DescCase{
		Name: "SR-18.2, missing is not proof that the agent exited",
		Require: []string{
			"is the sweep's judgement on the evidence available to it",
			"not proof that the agent has exited",
		},
		MustNot: []string{
			"means the agent is dead", "means the agent has exited", "means the agent exited",
			"is proof that", "proves that the agent", "confirms that the agent",
			"can safely be deleted", "can safely delete", "so it is safe to delete",
			"so the row is safe to delete",
		},
	}
}
