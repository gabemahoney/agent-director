package spawn

import (
	"log"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the launch owner (b.kdf, b.146 rule 11): the process that
// began a row's current launch records itself on the pending row, and
// find-missing does not judge that row while the owner is provably alive and
// its launch holds the row. A spawn stalled past the pending grace period is
// then neither marked missing (and then starts an agent no row tracks) nor
// noted unreported under it. The launch ends its hold when it ends: its identity
// write clears the owner, and ReleaseLaunchOwner does on every other path
// that leaves the row pending, so a launch run by a long-lived process (the
// MCP server, or a Go caller of pkg/api) does not hold the row for that
// process's life. Only a release that fails on every one of its bounded
// tries leaves such a hold in place (ReleaseLaunchOwner).

// CurrentLaunchOwner returns the calling process as the owner of the launch
// it is about to begin: its pid, its start time through pc (the start-time
// reader every process judgement uses, so find-missing compares like with
// like) and its pid namespace (probe.SelfPIDNamespace). Plain spawn's insert,
// reuse's reset and resume's move record it.
//
// When the start time cannot be read (pc answers unreadable, or gone for the
// caller itself) or the namespace cannot be read, it returns the zero
// LaunchOwner, which records no owner: find-missing then judges the row by
// its pending grace period alone, as for a row from before schema v6. It
// calls pc.StartTime once.
func CurrentLaunchOwner(pc tmux.ProcChecker) store.LaunchOwner {
	pid := os.Getpid()
	start := tmux.KnownStartTime(pc, pid)
	ns, known := probe.SelfPIDNamespace()
	if start == "" || !known {
		return store.LaunchOwner{}
	}
	return store.LaunchOwner{PID: pid, Starttime: start, PIDNamespace: ns}
}

// LaunchOwnerAlive reports whether owner, a pending row's recorded launch
// owner, is provably alive to the calling process (b.146 rules 11 and 14):
// an owner is recorded (PID positive), the caller's own pid namespace can be
// read and equals the owner's, and pc judges the owner's pid alive with
// exactly its recorded start time (tmux.JudgeProcess's ProcAlive).
//
// Every other case is false: no owner recorded; another pid namespace or one
// that cannot be read, where the pid names another process or none (the
// caller cannot tell); and an owner gone, a zombie, reused with another start
// time, or unreadable. A false answer leaves the row to its pending grace
// period alone, the rule from before schema v6. It calls pc.StartTime at most
// once.
func LaunchOwnerAlive(pc tmux.ProcChecker, owner store.LaunchOwner) bool {
	if owner.PID <= 0 {
		return false
	}
	ns, known := probe.SelfPIDNamespace()
	if !known || ns != owner.PIDNamespace {
		return false
	}
	return tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: owner.PID, Starttime: owner.Starttime}) == tmux.ProcAlive
}

// LaunchOwnerReleaser is the store write that ends a launch's hold on its
// pending row (store.ReleaseLaunchOwner). *store.Store satisfies it.
type LaunchOwnerReleaser interface {
	ReleaseLaunchOwner(instanceID, token string) (store.CondResult, error)
}

// releaseAttempts bounds ReleaseLaunchOwner's tries of the release write: one
// try and at most two retries. Every release follows a write that did not
// apply or failed, often under store contention, and a release that never
// applies holds the row for as long as the launching process lives (the MCP
// server, or a Go caller of pkg/api). Each try waits for the write lock up to
// the store's busy timeout ([store] busy_timeout_ms) on its own.
const releaseAttempts = 3

// releaseRetryDelay is the sleep before the first retry; it doubles before
// each later one, so the retries add at most 50 ms + 100 ms = 150 ms of sleep
// to a launch whose release keeps failing. 50 ms is the relay polling loop's
// sleep between its read retries (internal/hook's pollFloor).
const releaseRetryDelay = 50 * time.Millisecond

// releaseSleep is the sleep between release tries. Held as a var so tests can
// skip it.
var releaseSleep = time.Sleep

// ReleaseLaunchOwner ends the hold of the launch with token on instanceID's
// pending row, through r's conditional write, when the launch ends without a
// write of its own that ends it (its identity write). A write that does not
// apply (the row left pending, or another launch began) changes nothing and
// is not retried. A store error is retried, up to releaseAttempts tries in
// all with releaseRetryDelay's doubling sleep between them; the write is
// guarded on pending, the token and a recorded owner, so a retry can never
// release another launch's hold. When the last try fails too, one WARN line
// on lg names the instance id and the number of tries, with no token, label
// or environment value, and the row stays held until its owner exits. A
// failed release never changes the launch's result (SR-5.8). A nil lg logs
// nothing.
func ReleaseLaunchOwner(r LaunchOwnerReleaser, lg *log.Logger, instanceID, token string) {
	delay := releaseRetryDelay
	var err error
	for try := 1; ; try++ {
		if _, err = r.ReleaseLaunchOwner(instanceID, token); err == nil {
			return
		}
		if try == releaseAttempts {
			break
		}
		releaseSleep(delay)
		delay *= 2
	}
	if lg != nil {
		lg.Printf("WARN: ending the launch hold of instance %s failed after %d tries: %v", instanceID, releaseAttempts, err)
	}
}
