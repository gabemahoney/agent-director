package probe

import (
	"errors"
	"fmt"
)

// Resolver resolves the process identity of a tracked Claude process from the
// perspective of a running hook. Given the spawn's instance id, it walks the
// parent chain starting at the CURRENT process and returns the TOPMOST ancestor
// whose environment carries AGENT_DIRECTOR_INSTANCE_ID=<id> — the claude
// process in the hook → sh → claude chain (the tmux server above it lacks the
// var, terminating the walk). See SR-6.2.
//
// Fail-open contract: unlike Prober (whose sole consumer, find-missing, is
// fail-CLOSED and treats ErrProbeUnsupported as a hard error per SRD §14.6),
// every Resolve failure — a read error, a byte-layout drift trip
// (ErrKinfoLayoutDrift / ErrLinuxStatMalformed), or simply no matching ancestor
// (ErrNoMatchingAncestor) — is a typed error the hook maps to a NULL process
// identity (records no pid + starttime and proceeds). Resolve never panics and
// never hangs: the walk is depth-bounded and cycle-guarded.
//
// Epic hp (SR-7.2) will reuse the per-OS parent+starttime reads factored out
// here (parseLinuxStat over an injectable proc root on Linux; the entry-granular
// kinfo parsers over a kern.proc.all snapshot on darwin) for its evidence-based
// liveness checker.
type Resolver interface {
	// Resolve returns the pid and canonical per-OS proc_starttime string of
	// the topmost ancestor of the current process whose environment carries
	// EnvKey=id. On any failure it returns a typed (fail-open) error.
	//
	// The starttime string is the per-OS canonical form: on Linux the verbatim
	// clock-ticks-since-boot decimal from /proc/<pid>/stat field 22; on darwin
	// "<tv_sec>.<tv_usec>" via formatDarwinProcStartTime.
	Resolve(id string) (pid int, procStartTime string, err error)
}

// ErrNoMatchingAncestor is returned by Resolve when the parent-chain walk
// completes without finding any ancestor whose environment carries EnvKey=id.
// It is a distinct fail-open sentinel: the hook maps it (like the drift
// sentinels) to a NULL process identity. It deliberately does NOT wrap
// ErrProbeUnsupported — that error's pinned meaning is fail-CLOSED in
// find-missing, whereas a missing ancestor is an ordinary fail-open outcome.
var ErrNoMatchingAncestor = errors.New("ErrNoMatchingAncestor")

// NewResolver returns the per-OS Resolver. The implementation is selected by
// build tags at compile time (Linux /proc walk with the default /proc root;
// darwin kern.proc.all snapshot walk; an unsupported-OS fallback that returns a
// not-supported error). The factory mirrors New() so a hook can inject a fake
// in tests without touching the production path.
func NewResolver() Resolver {
	return newResolver()
}

// maxWalkDepth bounds the parent-chain walk. A hook → sh → claude → tmux chain
// is a handful of hops; a real process tree is never anywhere near this deep.
// The bound is a belt-and-suspenders guard alongside the pid-cycle check: if a
// /proc tree (real or fabricated) somehow presents a ppid cycle or an
// ever-climbing chain, the walk still terminates rather than hanging.
const maxWalkDepth = 1024

// ancestorReader is the per-OS surface the build-tag-free walk core drives. It
// abstracts the two facts the walk needs about a pid: its environment's
// instance-id value (for matching) and its parent pid + canonical starttime
// string (for climbing and for the returned identity). Both Linux and darwin
// implement it — Linux against an injectable proc root, darwin against a single
// kern.proc.all snapshot — so walkAncestors stays build-tag-free and testable
// off the target OS.
type ancestorReader interface {
	// selfPID returns the pid the walk starts from (the current process).
	selfPID() int
	// instanceID returns the EnvKey value in pid's environment and whether it
	// was present. A pid whose environ is unreadable (foreign uid) or gone
	// mid-walk returns (\"\", false, nil) — a non-match that does not abort the
	// walk. A hard read failure that should abort returns a non-nil error.
	instanceID(pid int) (id string, ok bool, err error)
	// parent returns pid's parent pid and canonical proc_starttime string.
	// A pid that has exited mid-walk (its stat/entry is gone) returns
	// (0, \"\", false, nil): a clean end-of-chain, not an error.
	parent(pid int) (ppid int, procStartTime string, present bool, err error)
}

// walkAncestors is the build-tag-free heart of Resolve. It climbs the parent
// chain from r.selfPID(), remembering the LAST (i.e. TOPMOST) ancestor whose
// environment matches id, and returns that ancestor's pid + starttime.
//
// Both-or-neither identity: a level is only recorded as a match when a COMPLETE
// identity pair is obtained — its environ matches id AND its own parent()/stat
// read succeeded (present=true) yielding a non-empty starttime for that SAME
// level. A matching level whose parent()/stat read reports the process vanished
// (present=false, empty starttime) is treated as a NON-match: recording it would
// write a non-NULL pid with a NULL proc_starttime — the pid-reuse-ambiguous
// evidence SR-6's starttime exists to defeat (and a process whose stat vanished
// is dead anyway). The walk therefore never yields a pid without a starttime,
// nor a starttime without a pid.
//
// Safety: the walk stops at pid 0/1 (init/kernel), on a vanished ancestor, and
// at maxWalkDepth; a pid-cycle guard (visited set) breaks any ppid loop. A pid
// whose environ is unreadable or which exits mid-walk is treated as a non-match
// and the walk continues (the sandbox may run --pid=host with foreign-uid
// ancestors). Any hard reader error aborts and is returned verbatim (fail-open:
// the hook maps it to NULL identity). No complete match anywhere (including a
// matching level that vanished before a prior complete match was recorded) →
// ErrNoMatchingAncestor. A prior complete match is still returned.
func walkAncestors(r ancestorReader, id string) (int, string, error) {
	var (
		bestPID       = -1
		bestStartTime string
		found         bool
	)

	visited := make(map[int]struct{})
	pid := r.selfPID()

	for depth := 0; depth < maxWalkDepth; depth++ {
		// Stop at the kernel/init boundary and before any ppid cycle.
		if pid <= 1 {
			break
		}
		if _, seen := visited[pid]; seen {
			break
		}
		visited[pid] = struct{}{}

		gotID, ok, err := r.instanceID(pid)
		if err != nil {
			return 0, "", err
		}

		ppid, startTime, present, err := r.parent(pid)
		if err != nil {
			return 0, "", err
		}

		// Env match: a COMPLETE match at this level makes it the new best
		// (TOPMOST wins — we keep climbing and overwrite with any higher
		// match). The match is only recorded when this SAME level's
		// parent()/stat read succeeded (present=true, yielding a starttime):
		// both-or-neither identity. A matching level whose stat vanished
		// (present=false) is a non-match — never a pid with an empty starttime.
		if ok && gotID == id && present {
			bestPID = pid
			bestStartTime = startTime
			found = true
		}

		// Ancestor exited mid-walk (no stat/entry): clean end of chain.
		if !present {
			break
		}
		pid = ppid
	}

	if !found {
		return 0, "", fmt.Errorf("%w: no ancestor of the current process carries %s=%s", ErrNoMatchingAncestor, EnvKey, id)
	}
	return bestPID, bestStartTime, nil
}
