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

// panicFetchEnv is a fetchEnv seam that fails the test if consulted. It pins the
// impl's ordering contract: a starttime MATCH is a strict prerequisite for the
// env fetch, so a mismatched (or absent) starttime must return its verdict
// BEFORE fetchEnv is ever called. reason is the message reported if the seam is
// wrongly reached.
func panicFetchEnv(t *testing.T, reason string) func(int) ([]byte, error) {
	return func(int) ([]byte, error) {
		t.Fatalf("fetchEnv must not be called: %s", reason)
		return nil, nil
	}
}

// driftKinfoEntry builds a full-size kinfo_proc entry whose starttime trips
// parseKinfoStartTime's drift guard (tv_sec=0 is an implausible start second),
// standing in for a layout-drifted buffer that must resolve to VerdictUnknown
// rather than dead.
func driftKinfoEntry() []byte {
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, 0, darwinStartUsec)
	return buf
}

// TestDarwinChecker is the table-driven verdict matrix for darwinChecker: each
// case constructs a darwinChecker from a (fetchKinfo, fetchEnv) seam pair, calls
// CheckLiveness(4242, DarwinProcStarttime, darwinTestID), and asserts one
// verdict. The cases exhaustively pin the checker's decision tree — starttime
// match/mismatch, the env-id tiebreaker, the permission→verified-alive branch,
// the drift/unexpected-errno→unknown branches, and the gone→dead branches —
// with each seam pair anchored to the shared fixture builders above.
func TestDarwinChecker(t *testing.T) {
	// fetchEnvFn is built per-case because the "must not be called" seams close
	// over t; a func field lets the mismatch/absent cases plant a panicking seam
	// inline alongside the ordinary okFetch/errFetch seams.
	cases := []struct {
		name       string
		fetchKinfo func(int) ([]byte, error)
		fetchEnv   func(int) ([]byte, error)
		want       LivenessVerdict
	}{
		{
			// kinfo starttime matches the stored value AND the readable env carries
			// the instance id → verified-alive.
			name:       "match_env_has_id_alive",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   okFetch(envBlobWithID(darwinTestID)),
			want:       VerdictVerifiedAlive,
		},
		{
			// starttime matches but the readable env LACKS the id → provably-dead
			// (SR-7.3 tiebreaker: pid reused, starttime collided).
			name:       "match_env_lacks_id_dead",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   okFetch(envBlobWithoutID()),
			want:       VerdictProvablyDead,
		},
		{
			// the live entry parses to a DIFFERENT starttime than stored → pid
			// reuse → provably-dead. fetchEnv must never be consulted once the
			// starttime mismatch is proven, so it panics if called — this is also
			// the "EPERM without match is impossible" ordering pin: a mismatch
			// returns dead BEFORE fetchEnv is ever reached.
			name:       "starttime_mismatch_dead",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec+999, darwinStartUsec)),
			fetchEnv:   panicFetchEnv(t, "starttime mismatch must short-circuit before env fetch"),
			want:       VerdictProvablyDead,
		},
		{
			// fetchKinfo returns ESRCH → dispGone → provably-dead.
			name:       "kinfo_esrch_dead",
			fetchKinfo: errFetch(syscall.ESRCH),
			fetchEnv:   errFetch(syscall.ESRCH),
			want:       VerdictProvablyDead,
		},
		{
			// fetchKinfo returns an empty (but no-error) buffer → the pid is not
			// live → provably-dead.
			name:       "kinfo_empty_dead",
			fetchKinfo: okFetch([]byte{}),
			fetchEnv:   errFetch(syscall.ESRCH),
			want:       VerdictProvablyDead,
		},
		{
			// starttime matches, then fetchEnv hits an EPERM permission wall →
			// verified-alive (pid+starttime already proved the process live; we
			// simply can't read its env). Pins the "permission is verified-alive
			// ONLY as a follow-on to a matched starttime" branch.
			name:       "env_eperm_after_match_alive",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   errFetch(syscall.EPERM),
			want:       VerdictVerifiedAlive,
		},
		{
			// same as above with EACCES, pinning both permission errnos through the
			// post-match branch.
			name:       "env_eacces_after_match_alive",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   errFetch(syscall.EACCES),
			want:       VerdictVerifiedAlive,
		},
		{
			// fetchKinfo returns a non-empty buffer that trips
			// parseKinfoStartTime's drift guard (tv_sec=0). Layout drift is NOT
			// evidence of death → VerdictUnknown, never dead.
			name:       "kinfo_drift_unknown",
			fetchKinfo: okFetch(driftKinfoEntry()),
			fetchEnv:   errFetch(syscall.ESRCH),
			want:       VerdictUnknown,
		},
		{
			// fetchKinfo returns an unpinned errno (EINVAL) → dispUnexpected →
			// VerdictUnknown, never dead.
			name:       "kinfo_unexpected_errno_unknown",
			fetchKinfo: errFetch(syscall.EINVAL),
			fetchEnv:   errFetch(syscall.EINVAL),
			want:       VerdictUnknown,
		},
		{
			// starttime matches, then fetchEnv returns an unpinned errno (EIO).
			// Unexpected env errno → VerdictUnknown, never dead (it is NOT the
			// permission→verified-alive branch and NOT gone).
			name:       "env_unexpected_errno_unknown",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   errFetch(syscall.EIO),
			want:       VerdictUnknown,
		},
		{
			// starttime matches, then the process exits between fetches so fetchEnv
			// returns ESRCH → dispGone → provably-dead.
			name:       "env_gone_after_match_dead",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   errFetch(syscall.ESRCH),
			want:       VerdictProvablyDead,
		},
		{
			// starttime matches, fetchEnv returns a too-short PROCARGS2 blob that
			// envFromProcArgs2 rejects (< 4 bytes → (nil,false)). An unparseable env
			// blob is NOT evidence of death → VerdictUnknown.
			name:       "env_unparseable_unknown",
			fetchKinfo: okFetch(oneKinfoEntry(t, darwinStartSec, darwinStartUsec)),
			fetchEnv:   okFetch([]byte{0x01, 0x02}),
			want:       VerdictUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := darwinChecker{fetchKinfo: tc.fetchKinfo, fetchEnv: tc.fetchEnv}
			if got := c.CheckLiveness(4242, procstarttimefix.DarwinProcStarttime, darwinTestID); got != tc.want {
				t.Errorf("verdict = %v; want %v", got, tc.want)
			}
		})
	}
}
