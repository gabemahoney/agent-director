package probe

// LivenessVerdict is the three-way outcome of an evidence-based liveness check
// for a single tracked row identity (pid + stored proc_starttime + instance id).
// It is DELIBERATELY narrow — find-missing (SR-7) maps each value to a per-row
// action: provably-dead → mark missing; verified-alive → clear liveness fields;
// unknown → skip THIS ROW ONLY (fail-open) and record liveness metadata.
//
// The cardinal rule enforced by every per-OS impl below: ANY unexpected errno
// or parse failure resolves to VerdictUnknown, NEVER VerdictProvablyDead. Only
// positive, pinned evidence (SR-7.4 table) yields dead.
type LivenessVerdict int

const (
	// VerdictUnknown means the checker could not obtain conclusive evidence for
	// either liveness or death — a permission wall, an unexpected errno, or a
	// parse/layout-drift failure. It is the fail-open default: the caller skips
	// the row rather than marking it missing.
	VerdictUnknown LivenessVerdict = iota
	// VerdictProvablyDead means the checker holds positive evidence the tracked
	// process is gone: /proc entry absent, a starttime mismatch (pid reuse), or
	// a readable environment that no longer carries the tracked instance id.
	VerdictProvablyDead
	// VerdictVerifiedAlive means the checker confirmed the tracked process is
	// still running with the matching identity.
	VerdictVerifiedAlive
)

func (v LivenessVerdict) String() string {
	switch v {
	case VerdictProvablyDead:
		return "provably-dead"
	case VerdictVerifiedAlive:
		return "verified-alive"
	default:
		return "unknown"
	}
}

// LivenessChecker is the narrow, verdict-level seam pkg/api's per-row
// find-missing engine depends on. Given a row's identity it returns a
// three-way LivenessVerdict. The SR-7.3 tiebreaker and the SR-7.4 per-OS errno
// table live INSIDE the per-OS implementations — the interface exposes only the
// verdict, so a per-id programmable fake (SR-12.3) substitutes cleanly from
// pkg/api WITHOUT importing any build-tagged code or reproducing the tables.
//
// The interface mirrors Prober/Resolver: a New-style factory selects the per-OS
// impl at compile time, and the seam is fake-substitutable in tests.
type LivenessChecker interface {
	// CheckLiveness returns the liveness verdict for the tracked process
	// identified by (pid, storedProcStartTime, instanceID). storedProcStartTime
	// is the canonical per-OS proc_starttime string recorded at spawn time (the
	// verbatim Linux clock-ticks decimal, or darwin "<tv_sec>.<tv_usec>"); it is
	// compared against the live process's current starttime to defeat pid reuse.
	// instanceID is the AGENT_DIRECTOR_INSTANCE_ID used for the environ
	// cross-check tiebreaker. The method never returns an error: every failure
	// mode is folded into VerdictUnknown per the fail-open contract.
	CheckLiveness(pid int, storedProcStartTime, instanceID string) LivenessVerdict
}

// NewChecker returns the per-OS LivenessChecker. The implementation is selected
// by build tags at compile time (Linux /proc read over the default /proc root;
// darwin per-pid KERN_PROC_PID + KERN_PROCARGS2; an unsupported-OS fallback).
// The factory mirrors New()/NewResolver() so pkg/api can inject a fake in tests
// without touching the production path.
func NewChecker() LivenessChecker {
	return newChecker()
}

// procDisposition is the classification of a raw /proc (or sysctl) read error
// into the SR-7.4 table's abstract dispositions. It is the build-tag-free,
// PM-mandated TESTABLE structure: the per-OS errno tables are expressed as pure
// classification functions (see classifyLinuxErrno / classifyDarwinErrno) that
// return one of these values, so a test can iterate a table of
// (errno → disposition) pairs without any real process or syscall.
type procDisposition int

const (
	// dispUnexpected is the fail-open default: any errno not explicitly pinned
	// in the SR-7.4 table classifies here → VerdictUnknown, never dead.
	dispUnexpected procDisposition = iota
	// dispGone means the read proved the process is absent (ENOENT/ESRCH on the
	// /proc entry, or an empty KERN_PROC_PID result) → VerdictProvablyDead.
	dispGone
	// dispPermission means the read hit a permission wall (EACCES/EPERM). Its
	// verdict depends on WHICH read (stat vs environ) and prior starttime
	// evidence — the per-OS impl resolves that; the classifier only labels the
	// errno.
	dispPermission
)
