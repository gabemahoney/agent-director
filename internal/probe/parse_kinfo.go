package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// kinfoProcSize is sizeof(struct kinfo_proc) on XNU 11.x (macOS 14 /
// 15). The struct is composed of extern_proc + a fixed-size eproc tail
// in <bsd/sys/sysctl.h>; the size has been stable across recent macOS
// majors but is NOT a kernel ABI guarantee. A future XNU bump that
// resizes the struct (or repositions p_pid inside it) will make the
// stride-based PID walker drift — see the sanity check below.
//
// Bump policy: when supporting a new macOS major version, compile the
// XNU sources for that release (Apple publishes them under
// https://github.com/apple-oss-distributions/xnu) and re-derive
// kinfoProcSize + kinfoProcPIDOffset (and the identity offsets
// kinfoProcStartSecOffset / kinfoProcStartUsecOffset / kinfoEprocPPIDOffset
// below) from the headers, then refresh the comment above + the
// bump-policy paragraph in docs/architecture.md.
//
// See also: <bsd/sys/proc.h> (extern_proc.p_pid, extern_proc.p_starttime)
// and <bsd/sys/sysctl.h> (struct kinfo_proc, struct eproc.e_ppid) in the
// XNU source tree.
const kinfoProcSize = 648

// kinfoProcPIDOffset is the byte offset of extern_proc.p_pid inside
// kinfo_proc on XNU 11.x. The eproc tail follows extern_proc, so p_pid
// lives at extern_proc's offset 40 (its position inside the leading
// substruct). Same XNU-version sensitivity as kinfoProcSize.
const kinfoProcPIDOffset = 40

// Process-identity offsets (SR-6.4). These pin the byte positions of the
// parent-pid and start-time fields inside a single kinfo_proc entry, all
// derived from the SAME XNU LP64 header basis as kinfoProcPIDOffset above
// (verified against apple-oss-distributions/xnu: bsd/sys/proc.h +
// bsd/sys/sysctl.h, with the eproc size arithmetic summing extern_proc=296
// + eproc=352 = kinfoProcSize=648). They carry the identical XNU-version
// sensitivity as kinfoProcSize/kinfoProcPIDOffset and share the bump policy
// above.
const (
	// kinfoProcStartSecOffset is the byte offset of
	// extern_proc.p_starttime.tv_sec (int64). p_starttime aliases the head
	// of extern_proc's leading p_un union, so tv_sec sits at extern_proc's
	// offset 0 — i.e. the very start of the kinfo_proc entry.
	kinfoProcStartSecOffset = 0
	// kinfoProcStartUsecOffset is the byte offset of
	// extern_proc.p_starttime.tv_usec (int32), 8 bytes into the timeval
	// that overlays the p_un union.
	kinfoProcStartUsecOffset = 8
	// kinfoEprocPPIDOffset is the byte offset of kp_eproc.e_ppid (pid_t /
	// int32) inside kinfo_proc: sizeof(extern_proc)=296 +
	// offsetof(eproc, e_ppid)=264 = 560. The 264 lands after e_paddr(8) +
	// e_sess(8) + e_pcred(104) + e_ucred(76) + 4 bytes of padding to
	// 8-align e_vm + e_vm(64) = 264.
	kinfoEprocPPIDOffset = 560
)

// maxPlausibleStartSec bounds extern_proc.p_starttime.tv_sec for the
// identity-offset drift guard. p_starttime is a wall-clock timeval, so
// tv_sec is a positive Unix epoch second; a legitimate value is far below
// this cap while a struct-layout drift (reinterpreting an unrelated 8-byte
// field) trips it. ~4102444800 = 2100-01-01 UTC, generously future-proof.
const maxPlausibleStartSec = 4_102_444_800

// maxPlausibleStartUsec bounds tv_usec: microseconds within a second are
// in [0, 1_000_000). Anything at/above trips the drift guard.
const maxPlausibleStartUsec = 1_000_000

// maxPlausiblePID is the upper bound used by the parse-time sanity
// check. Linux's CONFIG_BASE_FULL caps PIDs at 4_194_304; macOS's PID
// space is smaller in practice but we use the generous Linux cap so a
// legitimate macOS PID can never trip the guard while obvious garbage
// from a struct-layout drift (very-large random uint32 values) does.
const maxPlausiblePID = 4_194_304

// parsePIDsFromSysctlBuf reads PIDs from a sysctl(kern.proc.all) blob.
// The function is build-tag-free (Linux test runs can exercise it
// against synthetic input) and deliberately holds NO sysctl plumbing
// — it is a pure byte parser the platform-specific glue feeds.
//
// Returns ErrProbeUnsupported when more than 10% of parsed PIDs fail
// the plausibility check (must be a positive int32 ≤ maxPlausiblePID).
// That is the signal the kinfoProcSize / kinfoProcPIDOffset constants
// have drifted under us — a future macOS major bump that resized
// struct kinfo_proc would land here as a flood of garbage values.
// find-missing surfaces ErrProbeUnsupported as a hard failure
// (fail-closed per SRD §14.6).
//
// Buffers shorter than one kinfoProcSize entry return (nil, nil); the
// caller treats that as "no live processes" rather than a hard error.
func parsePIDsFromSysctlBuf(buf []byte) ([]int, error) {
	if len(buf) < kinfoProcSize {
		return nil, nil
	}
	n := len(buf) / kinfoProcSize
	out := make([]int, 0, n)
	var bogus int
	for i := 0; i < n; i++ {
		pidOff := i*kinfoProcSize + kinfoProcPIDOffset
		if pidOff+4 > len(buf) {
			break
		}
		pid := int(binary.LittleEndian.Uint32(buf[pidOff : pidOff+4]))
		if pid <= 0 || pid > maxPlausiblePID {
			bogus++
			continue
		}
		out = append(out, pid)
	}
	// 10% threshold in integer arithmetic: bogus / n > 1/10 ⇔ bogus*10 > n.
	if n > 0 && bogus*10 > n {
		return nil, fmt.Errorf("%w: %d of %d parsed PIDs failed plausibility (kinfoProcSize=%d may be stale for this XNU version)",
			ErrProbeUnsupported, bogus, n, kinfoProcSize)
	}
	return out, nil
}

// ErrKinfoLayoutDrift is returned by the entry-granular identity extractors
// (parseKinfoPPID, parseKinfoStartTime) when a single kinfo_proc entry's
// bytes fail their field plausibility guards — the signal that the pinned
// XNU offsets (kinfoEprocPPIDOffset / kinfoProcStart*Offset) have drifted
// under us, e.g. after a macOS major bump resized struct kinfo_proc.
//
// It is DELIBERATELY a distinct sentinel that does NOT wrap
// ErrProbeUnsupported: the two carry opposite fail-semantics.
// ErrProbeUnsupported has a pinned meaning of fail-CLOSED (find-missing
// treats it as a hard error, SRD §14.6), whereas identity-offset drift must
// map to fail-OPEN / unknown identity (SessionStart records NULL pid+
// starttime and proceeds; Epic hp's errno table must not conflate the two).
// Wrapping would make errors.Is(err, ErrProbeUnsupported) also match drift
// and re-import fail-closed semantics into that table. Callers that need to
// branch on drift test errors.Is(err, ErrKinfoLayoutDrift).
//
// Note this is a SEPARATE surface from parsePIDsFromSysctlBuf's existing
// ErrProbeUnsupported drift return, which is unchanged (SR-11): the whole-
// buffer PID walker keeps its fail-closed meaning; only the new per-entry
// identity extractors use this fail-open sentinel.
var ErrKinfoLayoutDrift = errors.New("ErrKinfoLayoutDrift")

// entryOffset validates that a kinfo_proc entry of kinfoProcSize bytes
// starting at off fits within buf, returning ErrKinfoLayoutDrift otherwise.
// The identity extractors operate ENTRY-GRANULARLY (PM-mandated): they read
// a single kinfo_proc entry — either the whole buf when off==0, or one entry
// at an explicit offset — so Epic hp can feed a single KERN_PROC_PID result
// straight through them without any baked-in whole-buffer iteration.
func entryOffset(buf []byte, off int) error {
	if off < 0 || off+kinfoProcSize > len(buf) {
		return fmt.Errorf("%w: entry at offset %d does not fit in %d-byte buffer (kinfoProcSize=%d)",
			ErrKinfoLayoutDrift, off, len(buf), kinfoProcSize)
	}
	return nil
}

// parseKinfoPPID extracts kp_eproc.e_ppid (parent process id) from the
// single kinfo_proc entry beginning at byte offset off within buf. It is
// build-tag-free so it can be unit-tested off-darwin against synthetic
// entries.
//
// Returns ErrKinfoLayoutDrift when the entry does not fit or when the
// parsed ppid fails the plausibility guard (must be a positive int32 ≤
// maxPlausiblePID — the same PID bound the PID walker uses). A ppid of 0 is
// implausible for a real tracked process (only the swapper/pid-0 has ppid 0
// and we never track it) and is refused as drift.
func parseKinfoPPID(buf []byte, off int) (int, error) {
	if err := entryOffset(buf, off); err != nil {
		return 0, err
	}
	p := off + kinfoEprocPPIDOffset
	ppid := int(int32(binary.LittleEndian.Uint32(buf[p : p+4])))
	if ppid <= 0 || ppid > maxPlausiblePID {
		return 0, fmt.Errorf("%w: e_ppid=%d out of range at entry offset %d (kinfoEprocPPIDOffset=%d may be stale for this XNU version)",
			ErrKinfoLayoutDrift, ppid, off, kinfoEprocPPIDOffset)
	}
	return ppid, nil
}

// parseKinfoStartTime extracts extern_proc.p_starttime (a struct timeval:
// tv_sec + tv_usec) from the single kinfo_proc entry beginning at byte
// offset off within buf, returning the two components. Build-tag-free for
// off-darwin unit testing.
//
// Returns ErrKinfoLayoutDrift when the entry does not fit or when either
// component fails its plausibility guard: tv_sec must be a positive Unix
// epoch second < maxPlausibleStartSec, and tv_usec must be in
// [0, maxPlausibleStartUsec). A drifted offset reinterpreting an unrelated
// field lands here as an out-of-range value.
func parseKinfoStartTime(buf []byte, off int) (sec int64, usec int64, err error) {
	if e := entryOffset(buf, off); e != nil {
		return 0, 0, e
	}
	s := off + kinfoProcStartSecOffset
	u := off + kinfoProcStartUsecOffset
	sec = int64(binary.LittleEndian.Uint64(buf[s : s+8]))
	usec = int64(int32(binary.LittleEndian.Uint32(buf[u : u+4])))
	if sec <= 0 || sec >= maxPlausibleStartSec {
		return 0, 0, fmt.Errorf("%w: p_starttime.tv_sec=%d out of range at entry offset %d (kinfoProcStartSecOffset=%d may be stale)",
			ErrKinfoLayoutDrift, sec, off, kinfoProcStartSecOffset)
	}
	if usec < 0 || usec >= maxPlausibleStartUsec {
		return 0, 0, fmt.Errorf("%w: p_starttime.tv_usec=%d out of range at entry offset %d (kinfoProcStartUsecOffset=%d may be stale)",
			ErrKinfoLayoutDrift, usec, off, kinfoProcStartUsecOffset)
	}
	return sec, usec, nil
}

// formatDarwinProcStartTime renders a kinfo_proc p_starttime timeval into
// the canonical macOS proc_starttime string: "<tv_sec>.<tv_usec>" — decimal,
// a single literal dot, NO zero-padding on either component.
//
// This is the AUTHORITATIVE production counterpart to the Darwin fixture
// constant procstarttimefix.DarwinProcStarttime ("1700000000.123456"): the
// output for that timeval (sec=1700000000, usec=123456) is byte-identical to
// the constant. Keep the two in sync — see
// internal/testsupport/procstarttimefix/procstarttimefix.go (SR-6.4).
func formatDarwinProcStartTime(sec, usec int64) string {
	return fmt.Sprintf("%d.%d", sec, usec)
}
