package apitest

import "strconv"

// descriptions_lookup.go holds the shared description helper's SR-1.4 cases
// for a lookup or pane listing that cannot decide (Can't tell) and that every
// single-row verb and the plain-spawn label scan share: conflicting labels
// and a different tmux server. A verb's refusal after it sent a kill adds
// DescCase.AfterKillSent (descriptions_kill.go); the pane verbs use these
// cases unchanged. The unreadable and
// unavailable outcomes use DescCallTimeout, DescUnrecognisedReply,
// DescSocketPermission and DescTmuxNotRun (descriptions.go).

// nothingWasDone is a Can't tell refusal's consequence sentence before any
// action was sent (SR-1.4).
const nothingWasDone = "nothing was done"

// ConflictingLabels parameterises DescConflictingLabels: Scope is an
// @ad_owner value at a scope; otherwise Sessions are the sessions carrying
// this launch's label (the full provenance_conflict row, Epic 10);
// NothingWasDone requires "nothing was done" (a single-row verb's refusal;
// the plain-spawn scan says nothing was written instead).
type ConflictingLabels struct {
	InstanceID     string
	Scope          bool
	Sessions       []DescSession
	NothingWasDone bool
}

// DescConflictingLabels is ErrTmuxSessionConflict's "conflicting labels"
// (provenance_conflict) case.
func DescConflictingLabels(p ConflictingLabels) DescCase {
	req := []string{p.InstanceID, "conflicting labels", "a human must look", "list --tmux-session-name"}
	if p.Scope {
		req = append(req, "@ad_owner value is set at the global, server or global-window scope")
	} else if len(p.Sessions) > 0 {
		req = append(req, "more than one session carries this launch's label")
	}
	if p.NothingWasDone {
		req = append(req, nothingWasDone)
	}
	for _, s := range p.Sessions {
		req = append(req, strconv.Quote(s.Name), s.ID)
	}
	return DescCase{
		Name:    "ErrTmuxSessionConflict, conflicting labels",
		Require: req,
		MustNot: rowEndedStatements,
	}.PointsToOperatorActions()
}

// DescDifferentServer is ErrTmuxNotAvailable for a lookup that found a tmux
// server other than the one the agent was launched on (SR-1.4, SR-3.3): the
// instance id, "this is not the tmux server the agent was launched on", that
// nothing was done, and that the caller must run as the agents' user in
// their tmux environment (SR-18.7); never "dead" or "gone".
func DescDifferentServer(instanceID string) DescCase {
	return DescCase{
		Name: "ErrTmuxNotAvailable, different tmux server",
		Require: []string{
			instanceID, "this is not the tmux server the agent was launched on", nothingWasDone,
			"the caller must run as the agents' user in their tmux environment",
		},
		MustNot: unresponsiveMustNot,
	}
}
