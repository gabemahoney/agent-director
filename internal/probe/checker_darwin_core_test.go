package probe

import (
	"encoding/binary"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// darwinStartSec / darwinStartUsec are the (sec, usec) preimage of the canonical
// fixture procstarttimefix.DarwinProcStarttime ("1700000000.123456"). Planting
// these into a single kinfo entry at offset 0 makes parseKinfoStartTime(buf, 0)
// format back to exactly that constant, so a checker fixture's "matching
// starttime" is anchored to the single fixture definition site.
const (
	darwinStartSec  = 1700000000
	darwinStartUsec = 123456
)

// oneKinfoEntry builds a single-entry (kinfoProcSize) kinfo_proc buffer with the
// fixture starttime planted at offset 0, matching what fetchKinfo returns for one
// live pid (KERN_PROC_PID yields exactly one entry). ppid is irrelevant to the
// checker's starttime cross-check but planted for realism.
func oneKinfoEntry(t *testing.T, sec int64, usec int32) []byte {
	t.Helper()
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, sec, usec) // reuses parse_kinfo_identity_test.go helper
	return buf
}

// procArgs2Blob synthesizes a KERN_PROCARGS2 blob envFromProcArgs2 can parse:
//
//	uint32 argc | exec_path '\0' | argv[0..argc-1] each '\0' | envp entries each '\0'
//
// envKVs are the env KEY=VAL strings (NUL-separated, in order). The layout must
// round-trip through envFromProcArgs2 so the checker's env cross-check runs on
// realistic bytes rather than a raw NUL block.
func procArgs2Blob(argv []string, envKVs []string) []byte {
	var b []byte
	argc := make([]byte, 4)
	binary.LittleEndian.PutUint32(argc, uint32(len(argv)))
	b = append(b, argc...)
	// exec_path (null-terminated).
	b = append(b, []byte("/usr/bin/claude")...)
	b = append(b, 0)
	// argv strings, each null-terminated.
	for _, a := range argv {
		b = append(b, []byte(a)...)
		b = append(b, 0)
	}
	// env strings, each null-terminated.
	for _, kv := range envKVs {
		b = append(b, []byte(kv)...)
		b = append(b, 0)
	}
	return b
}

// envBlobWithID / envBlobWithoutID are the two env fixtures the tiebreaker keys
// on: one carries EnvKey=<id>, one carries only unrelated vars.
func envBlobWithID(id string) []byte {
	return procArgs2Blob(
		[]string{"claude", "run"},
		[]string{"PATH=/usr/bin", EnvKey + "=" + id, "HOME=/Users/x"},
	)
}

func envBlobWithoutID() []byte {
	return procArgs2Blob(
		[]string{"claude", "run"},
		[]string{"PATH=/usr/bin", "HOME=/Users/x"},
	)
}

// errFetch is a fetch seam that always fails with a given error.
func errFetch(err error) func(int) ([]byte, error) {
	return func(int) ([]byte, error) { return nil, err }
}

// okFetch is a fetch seam that always returns the given bytes.
func okFetch(b []byte) func(int) ([]byte, error) {
	return func(int) ([]byte, error) { return b, nil }
}

const darwinTestID = "inst-darwin-abc"

// TestDarwinCheckerMatchEnvHasIDAlive: kinfo starttime matches the stored value
// AND the readable env carries the instance id → verified-alive.
func TestDarwinCheckerMatchEnvHasIDAlive(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   okFetch(envBlobWithID(darwinTestID)),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictVerifiedAlive {
		t.Errorf("verdict = %v; want verified-alive", got)
	}
}

// TestDarwinCheckerMatchEnvLacksIDDead: starttime matches but the readable env
// LACKS the id → provably-dead (SR-7.3 tiebreaker: pid reused, starttime
// collided).
func TestDarwinCheckerMatchEnvLacksIDDead(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   okFetch(envBlobWithoutID()),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (env lacks id tiebreaker)", got)
	}
}

// TestDarwinCheckerStarttimeMismatchDead: the live entry parses to a DIFFERENT
// starttime than stored → pid reuse → provably-dead. fetchEnv must never be
// consulted once the starttime mismatch is proven, so it panics if called.
func TestDarwinCheckerStarttimeMismatchDead(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec+999, darwinStartUsec)),
		fetchEnv: func(int) ([]byte, error) {
			t.Fatalf("fetchEnv must not be called after a starttime mismatch")
			return nil, nil
		},
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (starttime mismatch)", got)
	}
}

// TestDarwinCheckerKinfoESRCHDead: fetchKinfo returns ESRCH → dispGone →
// provably-dead.
func TestDarwinCheckerKinfoESRCHDead(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: errFetch(syscall.ESRCH),
		fetchEnv:   errFetch(syscall.ESRCH),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (kinfo ESRCH)", got)
	}
}

// TestDarwinCheckerKinfoEmptyDead: fetchKinfo returns an empty (but no-error)
// buffer → the pid is not live → provably-dead.
func TestDarwinCheckerKinfoEmptyDead(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch([]byte{}),
		fetchEnv:   errFetch(syscall.ESRCH),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (empty kinfo)", got)
	}
}

// TestDarwinCheckerEnvEPERMAfterMatchAlive: starttime matches, then fetchEnv hits
// an EPERM permission wall → verified-alive (pid+starttime already proved the
// process live; we simply can't read its env). This pins the "permission is
// verified-alive ONLY as a follow-on to a matched starttime" branch.
func TestDarwinCheckerEnvEPERMAfterMatchAlive(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   errFetch(syscall.EPERM),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictVerifiedAlive {
		t.Errorf("verdict = %v; want verified-alive (env EPERM after starttime match)", got)
	}
}

// TestDarwinCheckerEnvEACCESAfterMatchAlive: same as above with EACCES, pinning
// both permission errnos through the post-match branch.
func TestDarwinCheckerEnvEACCESAfterMatchAlive(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   errFetch(syscall.EACCES),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictVerifiedAlive {
		t.Errorf("verdict = %v; want verified-alive (env EACCES after starttime match)", got)
	}
}

// TestDarwinCheckerEnvEPERMWithoutMatchImpossible pins the impl's ordering
// contract: a starttime MATCH is a strict prerequisite for the env fetch, so
// there is no "EPERM without match" code path — a mismatched starttime returns
// provably-dead BEFORE fetchEnv is ever called (proven here by a panicking
// fetchEnv). This documents that "fetchEnv EPERM" can only ever be reached in the
// verified-alive (post-match) branch above.
func TestDarwinCheckerEnvEPERMWithoutMatchImpossible(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec+1, darwinStartUsec)),
		fetchEnv: func(int) ([]byte, error) {
			t.Fatalf("env fetch reached despite starttime mismatch — match is a prerequisite")
			return nil, syscall.EPERM
		},
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (mismatch short-circuits before env)", got)
	}
}

// TestDarwinCheckerKinfoDriftUnknown: fetchKinfo returns a non-empty buffer that
// trips parseKinfoStartTime's drift guard (tv_sec=0 is an implausible start
// second). Layout drift is NOT evidence of death → VerdictUnknown, never dead.
func TestDarwinCheckerKinfoDriftUnknown(t *testing.T) {
	// A full-size entry with a drift-tripping starttime (tv_sec=0).
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, 0, darwinStartUsec)
	c := darwinChecker{
		fetchKinfo: okFetch(buf),
		fetchEnv:   errFetch(syscall.ESRCH),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictUnknown {
		t.Errorf("verdict = %v; want unknown (kinfo drift sentinel, never dead)", got)
	}
}

// TestDarwinCheckerKinfoUnexpectedErrnoUnknown: fetchKinfo returns an unpinned
// errno (EINVAL) → dispUnexpected → VerdictUnknown, never dead.
func TestDarwinCheckerKinfoUnexpectedErrnoUnknown(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: errFetch(syscall.EINVAL),
		fetchEnv:   errFetch(syscall.EINVAL),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictUnknown {
		t.Errorf("verdict = %v; want unknown (unexpected kinfo errno)", got)
	}
}

// TestDarwinCheckerEnvUnexpectedErrnoUnknown: starttime matches, then fetchEnv
// returns an unpinned errno (EIO). Unexpected env errno → VerdictUnknown, never
// dead (it is NOT the permission→verified-alive branch and NOT gone).
func TestDarwinCheckerEnvUnexpectedErrnoUnknown(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   errFetch(syscall.EIO),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictUnknown {
		t.Errorf("verdict = %v; want unknown (unexpected env errno)", got)
	}
}

// TestDarwinCheckerEnvGoneAfterMatchDead: starttime matches, then the process
// exits between fetches so fetchEnv returns ESRCH → dispGone → provably-dead.
func TestDarwinCheckerEnvGoneAfterMatchDead(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   errFetch(syscall.ESRCH),
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (process exited between fetches)", got)
	}
}

// TestDarwinCheckerEnvUnparseableUnknown: starttime matches, fetchEnv returns a
// too-short PROCARGS2 blob that envFromProcArgs2 rejects. An unparseable env blob
// is NOT evidence of death → VerdictUnknown.
func TestDarwinCheckerEnvUnparseableUnknown(t *testing.T) {
	c := darwinChecker{
		fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
		fetchEnv:   okFetch([]byte{0x01, 0x02}), // < 4 bytes → envFromProcArgs2 returns (nil,false)
	}
	if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != VerdictUnknown {
		t.Errorf("verdict = %v; want unknown (unparseable env blob, never dead)", got)
	}
}
