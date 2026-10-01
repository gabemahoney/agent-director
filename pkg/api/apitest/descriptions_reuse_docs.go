package apitest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_reuse_docs.go holds the shared description helper's cases for
// reuse's documentation (Epic 17 Task 4): the reuse parameter text
// (DescReuseFinishedParam, SR-18.10), the history-by-life forbidden forms
// (DescReuseHistoryByLife, SR-18.16), delete's deprecation notice
// (DescDeleteDeprecated, SR-18.8), the collision text (DescInstanceIDCollision,
// SR-18.9), the recovery recourse (DescReuseRecourse, SR-18.4), and the
// three forbidden lists together (DescReuseDocsForbidden), which
// AssertMustNot checks over any text, Go source and Markdown included.

// historyByLifeClaims present reattaching an earlier conversation, a false
// lost-transcript report or transcript_status rotated as an effect of reuse,
// or the history leak as an accepted risk or known limitation (SR-18.16,
// AC-DOC-13). The false-report and accepted-risk forms are whole clauses, so
// an unrelated "falsely reports" or accepted risk (SR-18.12) does not match.
var historyByLifeClaims = concatStrings(reattachClaims(), falseReportClaims(), historyRiskClaims(), []string{
	"false lost-transcript", "reports a lost transcript", "reports the transcript as lost",
	"rotated after a reuse", "rotated after reuse", "reuse makes transcript_status rotated",
	"reused row shows rotated", "reused row reports rotated",
	"history leak", "leaks history", "leak history", "leaks an earlier life", "leaks the earlier",
})

// reattachClaims are the forms of "reattaches an earlier conversation".
func reattachClaims() []string {
	var out []string
	for _, verb := range []string{"reattach", "reattaches", "reattaching", "re-attach", "re-attaches", "re-attaching"} {
		for _, obj := range []string{"an abandoned", "the abandoned", "an earlier", "the earlier", "the old", "its earlier"} {
			out = append(out, verb+" "+obj+" conversation")
		}
	}
	return out
}

// falseReportClaims are the forms of "falsely reports a lost transcript".
func falseReportClaims() []string {
	var out []string
	for _, verb := range []string{"falsely report", "falsely reports", "falsely reporting"} {
		for _, obj := range []string{"a lost transcript", "the transcript as lost", "the transcript lost",
			"a missing transcript", "the transcript as missing", "the transcript missing", "ErrJsonlMissing"} {
			out = append(out, verb+" "+obj)
		}
	}
	return out
}

// historyRiskClaims are the forms of "the history leak is an accepted risk"
// or "a known limitation": the clause names history as what is accepted.
func historyRiskClaims() []string {
	var out []string
	for _, subject := range []string{"history", "history leak", "history leaks"} {
		for _, risk := range []string{"an accepted risk", "a known limitation"} {
			for _, link := range []string{" is ", " as ", " are ", ", ", " ("} {
				out = append(out, subject+link+risk)
			}
		}
		out = append(out, subject+" (accepted risk)", subject+" (known limitation)")
	}
	return out
}

// collisionStaleClaims are the claims SR-18.9 corrected: that only a live row
// collides, that a finished row does not, or that the collision set is the
// live states. The reused-id forms are the old liveStates clause ("so an
// `ended` Spawn's id can be reused (the resume verb handles that case)"), so
// a true statement that the opt-in reuses a finished row's id does not match.
var collisionStaleClaims = []string{
	"already in use by a live", "collision against a live row", "only a live row collides",
	"only live rows collide", "collides only with a live row", "collide only with a live row",
	"a finished row does not collide", "finished rows do not collide", "an ended row does not collide",
	"so an `ended` Spawn's id can be reused", "so an ended Spawn's id can be reused",
	"so a finished Spawn's id can be reused", "id can be reused (the resume verb handles that case)",
	"live-state collisions", "collision set is the live states",
}

// deleteThenSpawn are prescriptions of delete followed by spawn for recovery
// (SR-18.4, AC-DOC-03); "never delete" and delete's notice do not match.
var deleteThenSpawn = []string{
	"delete, then spawn", "delete then spawn", "delete and spawn", "delete and re-spawn", "delete and respawn",
	"delete + spawn", "delete + re-spawn", "delete + fresh spawn", "delete the row and spawn",
	"delete the row, then spawn", "delete the row and re-spawn", "delete it and spawn",
	"`delete` and `spawn`", "`delete` and spawn", "`delete` + fresh `spawn`", "`delete`, then `spawn`",
}

// The phrases of the opt-in's surfaces that the recovery and collision texts
// share.
const (
	sameIDOptIn = "spawn again with the same id, opting in to reuse"
	noMemory    = "no memory of the"
	lostRace    = "changed or was removed after this spawn examined it"
)

// DescReuseHistoryByLife is SR-18.16's forbidden-only case: no claim that
// reuse reattaches an earlier conversation, gives a false lost-transcript
// report or transcript_status rotated, or that the history leak is an
// accepted risk. Use it on any text.
func DescReuseHistoryByLife() DescCase {
	return DescCase{Name: "history by life, reuse claims", MustNot: append([]string(nil), historyByLifeClaims...)}
}

// DescReuseFinishedParam is spawn's reuse-finished parameter text (SR-18.10,
// SR-10.7, SR-18.7), by key phrase: finished rows only, explicit id, this
// call only, default unchanged, silent success, no memory of earlier lives,
// the failed-plain-spawn retry with the pending grace default, feature
// detection per surface, development builds and older binaries, and the same
// environment with SR-18.7's two consequences; plus DescReuseHistoryByLife.
// Check it with AssertAgentTextCase.
func DescReuseFinishedParam() DescCase {
	return DescCase{
		Name: "spawn manifest, reuse-finished parameter",
		Require: []string{
			"whose row is finished (ended or missing)", "A live row (pending included) still collides",
			"No effect without an explicit claude_instance_id", "Applies to this call only",
			"no template or config setting carries it",
			"The default (off) is unchanged: any existing row with the id collides",
			"does not say whether it created a fresh row or reset a finished one",
			"starts with no memory of its earlier lives",
			"resume and get never use or show an earlier life's history",
			"after a held-name refusal (duplicate session)", "decides it at once",
			"collides until find-missing marks it missing", "only after the pending grace period",
			fmt.Sprintf("(%d s by default)", config.DefaultPendingGraceSeconds),
			"Feature detection: read the version of the binary", "on the CLI the version verb",
			"over MCP the version tool", "in the TypeScript client binaryVersion", "never version()",
			"X.Y.Z-rc.N counts as X.Y.Z", "0.0.0-dev", "dev (a plain go build)",
			"returns ErrInvalidFlags on the CLI and in the TypeScript client",
			"over MCP silently ignores the parameter",
			"same user and in the same tmux environment as the agents",
			finishedRowNotVerification, wrongServerSecondAgent,
		},
		MustNot: append([]string(nil), historyByLifeClaims...),
	}
}

// DescDeleteDeprecated is delete's deprecation notice as its manifest
// description states it in compact form (SR-18.8, AC-DOC-07): DEPRECATED,
// b.tep, not for cleanup or recovery, expire, spawn --reuse-finished, kill
// then find-missing, never after a failed kill or on an assumed exit; never
// the operator-only unusable-name removal or its "Operator actions" pointer,
// nor delete-then-spawn. Check it with AssertAgentTextCase.
func DescDeleteDeprecated() DescCase {
	return DescCase{
		Name: "delete manifest, deprecation notice",
		Require: []string{
			"DEPRECATED", "removal planned (b.tep)", "Not for cleanup or recovery",
			"expire removes finished rows", "respawn with spawn --reuse-finished",
			"for a stuck live row, kill then find-missing", "Never delete after a failed kill",
			"assuming a finished row's agent exited",
		},
		MustNot: append([]string{
			OperatorActionsTitle, "name cannot be used", "unusable", "recorded tmux session name", "recorded name",
		}, deleteThenSpawn...),
	}
}

// CollisionSite selects the form DescInstanceIDCollision requires. Code is
// the quote a Markdown site puts around a state ("`"); empty elsewhere.
type CollisionSite struct {
	Full bool // both cases (Client.Spawn's Errors line); else without the opt-in only
	Code string
}

// DescInstanceIDCollision is SR-18.9's collision text: ErrInstanceIdCollision
// and, without the opt-in, any existing row in any state; Full adds the
// opt-in's live row (pending included) and lost race. Never a stale claim
// that only a live row collides. Check it with AssertAgentTextCase.
func DescInstanceIDCollision(s CollisionSite) DescCase {
	req := []string{"ErrInstanceIdCollision", "in any state"}
	name := "collision text, without the opt-in"
	if s.Full {
		req = append(req, "live", "("+s.Code+"pending"+s.Code+" included)", lostRace)
		name = "collision text, both cases"
	}
	return DescCase{Name: name, Require: req, MustNot: append([]string(nil), collisionStaleClaims...)}
}

// RecourseSite selects the wording DescReuseRecourse requires.
type RecourseSite int

// The recourse sites: a Go doc names SpawnParams.ReuseFinished; the
// TypeScript README spawns the same claude_instance_id with reuse_finished.
const (
	RecourseGoDoc RecourseSite = iota
	RecourseTSREADME
)

// DescReuseRecourse is SR-18.4's recovery recourse at site: spawn again with
// the same id, opting in to reuse by the site's spelling, the reused id
// having no memory of the earlier conversation; never delete-then-spawn.
// Check it with AssertAgentTextCase.
func DescReuseRecourse(site RecourseSite) DescCase {
	req := map[RecourseSite][]string{
		RecourseGoDoc:    {sameIDOptIn, "(SpawnParams.ReuseFinished"},
		RecourseTSREADME: {"spawn the same `claude_instance_id` again", "`reuse_finished: true`"},
	}[site]
	if req == nil {
		panic("apitest: unknown RecourseSite")
	}
	return DescCase{
		Name:    "recovery recourse, reuse",
		Require: append(req, noMemory),
		MustNot: append([]string(nil), deleteThenSpawn...),
	}
}

// DescReuseDocsForbidden is the recovery, collision and history-by-life
// forbidden forms together, for AssertMustNot over any text.
func DescReuseDocsForbidden() DescCase {
	return DescCase{
		Name:    "reuse docs, forbidden forms",
		MustNot: concatStrings(deleteThenSpawn, collisionStaleClaims, historyByLifeClaims),
	}
}

func concatStrings(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// AssertMustNot checks text for c's must-not phrases only, naming what and
// the match with its context; for texts the agent-text forms do not govern
// (a Go source file, a README).
func AssertMustNot(t testing.TB, what, text string, c DescCase) {
	t.Helper()
	for _, p := range c.MustNot {
		for _, loc := range mustNotPattern(p).FindAllStringIndex(text, -1) {
			from, to := max(loc[0]-80, 0), min(loc[1]+80, len(text))
			t.Errorf("%s: case %q: contains must-not phrase %q in %q", what, c.Name, p,
				strings.TrimSpace(text[from:to]))
		}
	}
}
