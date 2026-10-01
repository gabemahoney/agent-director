package apitest

// descriptions_reuse.go holds the shared description helper's SR-1.4 cases
// that reuse (spawn with the reuse opt-in, Epic 17; SR-10) adds: the lost
// race, the two ErrInternal failures of the reuse change, the launch
// timeout's "the row was reset" (DescLaunchTimeout with RowReset), and
// LaunchKind, which ResumeRestore.Launch sets to word the restore's result
// after reuse's reset instead of resume's move (SR-10.4). Reuse's other
// cases are the existing ones, given reuse's parameters, never copies:
//
//   - the old-row lookup's refusals: DescPreLaunchLeftover, the
//     starting-session cases (DescStillStopping, DescStillStarting,
//     DescOwnOldSession) and the Can't tell cases, quoting the recorded name;
//   - the new-name pre-check's holders: the DescHeld* cases with
//     HeldName.BeforeLaunch and the requested name;
//   - a launch error after a restore: AfterResumeRestore (and
//     UnlabelledSession.Restore) with Launch LaunchReuse;
//   - after "duplicate session": the cases resume uses
//     (descriptions_resume_held.go), with HeldName.Name the requested name
//     and HeldName.Restore.Launch LaunchReuse.
//
// Reuse's documentation cases (Task 4) are in descriptions_reuse_docs.go.

// LaunchKind is the write that began a launch onto a row examined as
// finished, which the restore's changed and removed sentences name
// (ResumeRestore.Launch). LaunchResume, the zero value, keeps every resume
// case as it was.
type LaunchKind int

// The launch kinds.
const (
	LaunchResume LaunchKind = iota // resume's move to pending (SR-8.5)
	LaunchReuse                    // reuse's reset (SR-10.4)
)

// launchWrites are the restore sentences' names of each kind's write.
var launchWrites = map[LaunchKind]string{
	LaunchResume: "resume moved it to pending",
	LaunchReuse:  "this spawn reset it",
}

// write is k's write as the restore's changed and removed sentences name it.
func (k LaunchKind) write() string {
	w, ok := launchWrites[k]
	if !ok {
		panic("apitest: unknown LaunchKind")
	}
	return w
}

// name is k's word in a case name ("resume", "reuse").
func (k LaunchKind) name() string {
	if k == LaunchReuse {
		return "reuse"
	}
	return "resume"
}

// otherWrites are the other kinds' writes, which a description of k's launch
// must not name; none for LaunchResume, so resume's cases stay as they were.
func (k LaunchKind) otherWrites() []string {
	if k == LaunchResume {
		return nil
	}
	var out []string
	for _, o := range []LaunchKind{LaunchResume, LaunchReuse} {
		if o != k {
			out = append(out, o.write())
		}
	}
	return out
}

// reuseNothingChanged ends each of reuse's refusals once it decided to change
// the row and the change did not apply (SR-10.3, SR-5.8).
const reuseNothingChanged = "nothing was changed"

// The two ErrInternal sentences of a failed reuse change (SR-1.4, SR-5.8).
const (
	reuseArchiveFailed = "archiving the previous session failed"
	reuseChangeFailed  = "the reuse could not be applied"
)

// reuseRefusalMustNot is what none of reuse's refusals says: another verb's
// or another refusal's "nothing ..." sentence.
var reuseRefusalMustNot = []string{nothingWasDone, nothingWritten}

// DescReuseLostRace is ErrInstanceIdCollision for a reuse whose reset (or
// re-read after a left-over refusal) found the row changed or removed since
// this spawn examined it (SR-1.4, SR-10.3, SR-10.5): the instance id, that the
// row changed or was removed after this spawn examined it, and that nothing
// was changed; never the live-row collision's "already live".
func DescReuseLostRace(instanceID string) DescCase {
	return DescCase{
		Name:    "ErrInstanceIdCollision, reuse lost race",
		Require: []string{instanceID, "the row changed or was removed after this spawn examined it", reuseNothingChanged},
		MustNot: append([]string{"already live"}, reuseRefusalMustNot...),
	}
}

// DescReuseArchiveFailure is ErrInternal for a reuse whose archive of the
// previous session failed (SR-1.4, SR-5.8): the instance id, that archiving
// the previous session failed and that nothing was changed; never the other
// reuse-change sentence. SR-1.4 gives it no "Operator actions" pointer.
func DescReuseArchiveFailure(instanceID string) DescCase {
	return reuseChangeCase("archive failure", instanceID, reuseArchiveFailed, reuseChangeFailed)
}

// DescReuseChangeFailure is ErrInternal for any other failure of the reuse
// change, a busy store past its timeout included (SR-1.4, SR-5.8): the
// instance id, that the reuse could not be applied and that nothing was
// changed; never the archive sentence. SR-1.4 gives it no "Operator actions"
// pointer.
func DescReuseChangeFailure(instanceID string) DescCase {
	return reuseChangeCase("change failure", instanceID, reuseChangeFailed, reuseArchiveFailed)
}

// reuseChangeCase is one of the two ErrInternal reuse-change cases: what
// failed (want), never the other's sentence.
func reuseChangeCase(what, instanceID, want, other string) DescCase {
	return DescCase{
		Name:    "ErrInternal, reuse " + what,
		Require: []string{instanceID, want + " and " + reuseNothingChanged},
		MustNot: append([]string{other}, reuseRefusalMustNot...),
	}
}

// rowEarlierLife are claims that a reset row stays in, or went back to, its
// earlier life, which reuse's launch timeout never makes: the row was reset
// and stays pending (SR-1.4, SR-10.4).
var rowEarlierLife = []string{
	reuseNothingChanged, "was not reset", "the row was restored", "prior state", "earlier life",
	"previous life", "stays ended", "stays missing", "still ended", "still missing",
}

// withRowReset returns c, a DescLaunchTimeout case, for a reuse when reset:
// also requiring "the row was reset" and none of rowEarlierLife. Without
// reset c is returned unchanged.
func (c DescCase) withRowReset(reset bool) DescCase {
	if !reset {
		return c
	}
	c.Name += ", reuse"
	c.Require = append(append([]string(nil), c.Require...), "the row was reset")
	c.MustNot = appendMissing(append([]string(nil), c.MustNot...), rowEarlierLife...)
	return c
}
