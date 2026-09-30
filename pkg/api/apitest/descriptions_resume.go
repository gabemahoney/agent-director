package apitest

import (
	"strconv"
	"strings"
	"time"
)

// descriptions_resume.go holds the shared description helper's SR-1.4 cases
// that `resume` introduces (Epic 8): the launch-in-progress and lost-race
// refusals, the move's store error, the control-character id, and the
// restore's result that ends every resume launch error after a failed launch
// (SR-8.5). Resume reuses DescLaunchTimeout, DescSocketDir,
// DescSocketPermission, DescTmuxNotRun and DescSessionCreateFailed as they
// are, and DescUnlabelledSession with its Restore field.

// RestoreOutcome is what resume's one restore attempt after a failed launch
// did (SR-8.5). RestoreNone (the zero value) means the launch was not a
// resume's.
type RestoreOutcome int

// The restore outcomes a resume launch error states.
const (
	RestoreNone       RestoreOutcome = iota
	RestoreApplied                   // the row is back in its prior state
	RestoreRowChanged                // another write changed the row; nothing written
	RestoreRowRemoved                // the row was removed; nothing written
	RestoreStoreError                // the restore write failed; the row stays pending
)

// ResumeRestore is the restore's result as a resume launch error states it:
// the outcome and, for RestoreApplied, the prior state the row went back to
// ("ended" or "missing").
type ResumeRestore struct {
	Outcome    RestoreOutcome
	PriorState string
}

// restorePhrase is the row sentence each restore outcome gives.
func (r ResumeRestore) restorePhrase() string {
	switch r.Outcome {
	case RestoreApplied:
		return "the row was restored to its prior state, " + r.PriorState
	case RestoreRowChanged:
		return "the row changed after resume moved it to pending and was left as it is"
	case RestoreRowRemoved:
		return "the row was removed after resume moved it to pending, so nothing was restored"
	case RestoreStoreError:
		return "the row could not be restored and stays pending"
	}
	panic("apitest: ResumeRestore with no outcome")
}

// AfterResumeRestore returns c also requiring the restore's result r as the
// description's row sentence (SR-1.4, SR-8.5), in place of a plain spawn's
// "the row stays pending", which it must not say. Use it on the case of a
// resume launch error that follows a restore (DescSocketPermission,
// DescTmuxNotRun, DescSessionCreateFailed; DescUnlabelledSession applies it
// through its Restore field).
func (c DescCase) AfterResumeRestore(r ResumeRestore) DescCase {
	c.Require = append(append([]string(nil), c.Require...), r.restorePhrase())
	c.MustNot = append(append([]string(nil), c.MustNot...), rowStaysPending)
	return c
}

// LaunchInProgress parameterises DescResumeLaunchInProgress: the pending
// row's instance id and its launch start (the zero time: no launch start is
// recorded).
type LaunchInProgress struct {
	InstanceID  string
	LaunchStart time.Time
}

// launchBegan and noLaunchStart are the launch-in-progress refusal's two
// forms of the launch start (SR-1.4).
const (
	launchBegan   = "a launch of this row began at "
	noLaunchStart = "no launch start is recorded"
)

// DescResumeLaunchInProgress is ErrSpawnNotResumable for a resume of a
// pending row (SR-8.4): the instance id; when the launch began, as RFC3339
// UTC exactly as get shows launch_started_at, or that no launch start is
// recorded; that its agent has not reported in; that resume applies only to
// an ended or missing row; that the row becomes live if the agent reports in;
// that find-missing marks it missing after the pending grace period; that
// nothing was written. It must not say the refusal ends when the agent
// reports in, nor "dead" or "gone" (naming find-missing is allowed).
func DescResumeLaunchInProgress(p LaunchInProgress) DescCase {
	req := []string{p.InstanceID}
	mustNot := []string{
		"dead", "gone", "retry", "try again", "then resume", "resume succeeds",
		"resume will succeed", "resume can", "until the agent reports in",
		"until its agent reports in",
	}
	if p.LaunchStart.IsZero() {
		req = append(req, noLaunchStart)
		mustNot = append(mustNot, strings.TrimSpace(launchBegan))
	} else {
		req = append(req, launchBegan+p.LaunchStart.UTC().Format(time.RFC3339Nano))
		mustNot = append(mustNot, noLaunchStart)
	}
	req = append(req,
		"its agent has not reported in",
		"resume applies only to an ended or missing row",
		"if the agent reports in, the row becomes live",
		"if the launch was abandoned or failed",
		"find-missing marks the row missing once the pending grace period has passed since its launch start",
		"nothing was written",
	)
	return DescCase{Name: "ErrSpawnNotResumable, launch in progress", Require: req, MustNot: mustNot}
}

// DescResumeLostRace is ErrSpawnNotResumable for a resume whose move to
// pending found the row changed since resume examined it (SR-8.3).
func DescResumeLostRace() DescCase {
	return DescCase{
		Name:    "ErrSpawnNotResumable, lost race",
		Require: []string{"the row changed after resume examined it and nothing was written"},
	}
}

// DescResumeMoveStoreError is ErrInternal for a store error on resume's move
// to pending (SR-8.3, SR-5.8).
func DescResumeMoveStoreError() DescCase {
	return DescCase{
		Name:    "ErrInternal, resume move store error",
		Require: []string{"recording the launch failed and nothing was launched"},
	}
}

// DescResumeInstanceIDControlChar is ErrInternal for a resume of a row whose
// instance id contains a control character (SR-3.13): its session cannot be
// labelled, removing the row is a human's decision (with the "Operator
// actions" pointer), and no tmux call was made and nothing was written. The
// description never carries the id, raw or escaped.
func DescResumeInstanceIDControlChar(id string) DescCase {
	return DescCase{
		Name: "ErrInternal, resume of a control-character instance id",
		Require: []string{
			"the instance id contains a control character, so its session cannot be labelled",
			"removing the row is a human's decision",
			"no tmux call was made and nothing was written",
		},
		Forbid: []string{id, strings.Trim(strconv.Quote(id), `"`)},
	}.PointsToOperatorActions()
}
