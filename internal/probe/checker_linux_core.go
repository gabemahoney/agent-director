package probe

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
)

// linuxChecker implements LivenessChecker over an INJECTABLE PROC ROOT
// (procRoot, default "/proc" via newChecker on Linux — see checker_linux.go).
// It reuses/mirrors the resolver's procRoot seam (resolver_linux.go): tests
// construct it with a fabricated proc tree so the SR-7.4 Linux table is
// exercisable off any OS. The struct + its verdict logic are build-tag-free
// (plain os.ReadFile over the injected root, no Linux-only syscalls) so the
// darwin-free logic and its errno table are directly unit-testable.
type linuxChecker struct {
	procRoot string
}

// CheckLiveness implements SR-7.4's Linux table EXACTLY:
//
//   - /proc/<pid> stat ENOENT/ESRCH ................... provably-dead
//   - starttime (stat field 22) mismatch vs stored .... provably-dead
//   - stat EACCES/EPERM ............................... unknown
//   - starttime match + readable environ LACKING id ... provably-dead (tiebreaker)
//   - starttime match + environ EACCES/EPERM ......... verified-alive
//   - starttime match + readable environ HAS id ....... verified-alive
//   - ANY other errno or stat parse failure ........... unknown (never dead)
func (c linuxChecker) CheckLiveness(pid int, storedProcStartTime, instanceID string) LivenessVerdict {
	statPath := c.procRoot + "/" + strconv.Itoa(pid) + "/stat"
	data, err := os.ReadFile(statPath)
	if err != nil {
		switch classifyLinuxErrno(err) {
		case dispGone:
			// /proc/<pid> absent → the process is gone.
			return VerdictProvablyDead
		case dispPermission:
			// Can't read stat → no evidence either way.
			return VerdictUnknown
		default:
			// Any other errno → fail-open, never dead.
			return VerdictUnknown
		}
	}

	_, liveStartTime, parseErr := parseLinuxStat(string(data))
	if parseErr != nil {
		// A malformed stat line is NOT evidence of death — fail-open.
		return VerdictUnknown
	}
	if liveStartTime != storedProcStartTime {
		// The pid is reused by a different process (or the boot clock reset):
		// the tracked process is gone.
		return VerdictProvablyDead
	}

	// starttime matches: the pid still names the tracked process. Cross-check
	// the environ for the instance id (SR-7.3 tiebreaker).
	environPath := c.procRoot + "/" + strconv.Itoa(pid) + "/environ"
	env, envErr := os.ReadFile(environPath)
	if envErr != nil {
		switch classifyLinuxErrno(envErr) {
		case dispGone:
			// The process exited between the stat read and the environ read.
			return VerdictProvablyDead
		case dispPermission:
			// environ EACCES with a matching starttime → verified-alive: the
			// process is provably still running, we simply can't read its env.
			return VerdictVerifiedAlive
		default:
			// Any other errno → fail-open.
			return VerdictUnknown
		}
	}

	if environHasInstanceID(env, instanceID) {
		return VerdictVerifiedAlive
	}
	// Readable environ with a matching starttime but LACKING the instance id →
	// the pid was reused fast enough that starttime collided; the tracked
	// process is gone (SR-7.3 tiebreaker).
	return VerdictProvablyDead
}

// classifyLinuxErrno maps a /proc read error into the SR-7.4 dispositions. It
// is the PM-mandated TESTABLE structure for the Linux errno table: a pure
// function a test iterates over a table of (errno → disposition) pairs, with no
// real /proc access.
//
//   - ENOENT / ESRCH ...... dispGone (process absent)
//   - EACCES / EPERM ...... dispPermission
//   - anything else ....... dispUnexpected (fail-open → unknown)
func classifyLinuxErrno(err error) procDisposition {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ESRCH):
		return dispGone
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return dispPermission
	default:
		return dispUnexpected
	}
}

// environHasInstanceID reports whether the NUL-separated /proc/<pid>/environ
// block carries EnvKey=id. Empty id never matches.
func environHasInstanceID(env []byte, id string) bool {
	if id == "" {
		return false
	}
	want := []byte(EnvKey + "=" + id)
	for _, kv := range bytes.Split(env, []byte{0}) {
		if bytes.Equal(kv, want) {
			return true
		}
	}
	return false
}
