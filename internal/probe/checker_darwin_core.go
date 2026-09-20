package probe

import (
	"bytes"
	"errors"
	"syscall"
)

// darwinChecker implements LivenessChecker over per-pid KERN_PROC_PID +
// KERN_PROCARGS2 fetches, injected as function seams so the verdict logic + the
// macOS errno table are build-tag-free and unit-testable off-darwin against
// synthetic kinfo_proc bytes (the resolver_darwin_core precedent: byte parsing
// + verdict logic here, real syscalls in the darwin-tagged file).
//
//   - fetchKinfo(pid) returns the raw kinfo_proc entry bytes for the LIVE pid
//     (a single KERN_PROC_PID result). An empty result or ESRCH means the pid
//     is gone. Any error is classified by classifyDarwinErrno.
//   - fetchEnv(pid) returns pid's KERN_PROCARGS2 blob; parsed by
//     envFromProcArgs2. EPERM/EACCES is a permission wall.
type darwinChecker struct {
	fetchKinfo func(pid int) ([]byte, error)
	fetchEnv   func(pid int) ([]byte, error)
}

// CheckLiveness implements SR-7.4's macOS table EXACTLY:
//
//   - KERN_PROC_PID ESRCH / empty result ............. provably-dead
//   - p_starttime mismatch vs stored ................. provably-dead
//   - KERN_PROCARGS2 EPERM/EACCES .................... unknown, UNLESS pid+
//     starttime already matched → verified-alive
//   - starttime match + readable env LACKING id ...... provably-dead (tiebreaker)
//   - starttime match + readable env HAS id .......... verified-alive
//   - ANY other errno or ErrKinfoLayoutDrift ......... unknown (never dead)
func (c darwinChecker) CheckLiveness(pid int, storedProcStartTime, instanceID string) LivenessVerdict {
	buf, err := c.fetchKinfo(pid)
	if err != nil {
		switch classifyDarwinErrno(err) {
		case dispGone:
			return VerdictProvablyDead
		case dispPermission:
			// Can't read the process table entry → no evidence.
			return VerdictUnknown
		default:
			return VerdictUnknown
		}
	}
	if len(buf) == 0 {
		// Empty KERN_PROC_PID result: the pid is not live.
		return VerdictProvablyDead
	}

	// Single-entry parse at offset 0 (KERN_PROC_PID returns exactly one entry).
	sec, usec, perr := parseKinfoStartTime(buf, 0)
	if perr != nil {
		// Layout drift (or any parse failure) → fail-open, never dead.
		return VerdictUnknown
	}
	if formatDarwinProcStartTime(sec, usec) != storedProcStartTime {
		// pid reuse: a different process now holds this pid.
		return VerdictProvablyDead
	}

	// starttime matches: cross-check the env for the instance id.
	env, envErr := c.fetchEnv(pid)
	if envErr != nil {
		switch classifyDarwinErrno(envErr) {
		case dispGone:
			// Process exited between the two fetches.
			return VerdictProvablyDead
		case dispPermission:
			// PROCARGS2 permission wall but pid+starttime already matched →
			// verified-alive.
			return VerdictVerifiedAlive
		default:
			return VerdictUnknown
		}
	}

	section, ok := envFromProcArgs2(env)
	if !ok {
		// A too-short / unparseable PROCARGS2 blob is not evidence of death.
		return VerdictUnknown
	}
	if procArgs2EnvHasInstanceID(section, instanceID) {
		return VerdictVerifiedAlive
	}
	// Readable env with a matching starttime but LACKING the instance id → the
	// tracked process is gone (tiebreaker).
	return VerdictProvablyDead
}

// classifyDarwinErrno maps a KERN_PROC_PID / KERN_PROCARGS2 sysctl error into
// the SR-7.4 dispositions. PM-mandated TESTABLE structure: a pure function a
// test iterates over (errno → disposition) pairs, no real sysctl.
//
//   - ESRCH ............... dispGone (process absent)
//   - EACCES / EPERM ...... dispPermission
//   - ErrKinfoLayoutDrift . dispUnexpected (fail-open → unknown)
//   - anything else ....... dispUnexpected (fail-open → unknown)
func classifyDarwinErrno(err error) procDisposition {
	switch {
	case errors.Is(err, syscall.ESRCH):
		return dispGone
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return dispPermission
	default:
		// Includes ErrKinfoLayoutDrift and every unpinned errno.
		return dispUnexpected
	}
}

// procArgs2EnvHasInstanceID reports whether the NUL-separated env section of a
// KERN_PROCARGS2 blob carries EnvKey=id. Empty id never matches.
func procArgs2EnvHasInstanceID(section []byte, id string) bool {
	if id == "" {
		return false
	}
	want := []byte(EnvKey + "=" + id)
	for _, kv := range bytes.Split(section, []byte{0}) {
		if bytes.Equal(kv, want) {
			return true
		}
	}
	return false
}
