package probe

import (
	"encoding/binary"
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
// kinfoProcSize + kinfoProcPIDOffset from the headers, then refresh
// the comment above + the bump-policy paragraph in docs/architecture.md.
//
// See also: <bsd/sys/proc.h> (extern_proc.p_pid) and
// <bsd/sys/sysctl.h> (struct kinfo_proc) in the XNU source tree.
const kinfoProcSize = 648

// kinfoProcPIDOffset is the byte offset of extern_proc.p_pid inside
// kinfo_proc on XNU 11.x. The eproc tail follows extern_proc, so p_pid
// lives at extern_proc's offset 40 (its position inside the leading
// substruct). Same XNU-version sensitivity as kinfoProcSize.
const kinfoProcPIDOffset = 40

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
