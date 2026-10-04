package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// kinfoProcSize is sizeof(struct kinfo_proc) on XNU 11.x (macOS 14 /
// 15). The struct is composed of extern_proc + a fixed-size eproc tail
// in <bsd/sys/sysctl.h>; the size has been stable across recent macOS
// majors but is NOT a kernel ABI guarantee. A future XNU bump that
// resizes the struct (or moves a field inside it) makes the entry-granular
// extractors below read the wrong bytes; their plausibility guards turn that
// into ErrKinfoLayoutDrift.
//
// Bump policy: when supporting a new macOS major version, compile the
// XNU sources for that release (Apple publishes them under
// https://github.com/apple-oss-distributions/xnu) and re-derive
// kinfoProcSize + kinfoProcPIDOffset (and the identity offsets
// kinfoProcStartSecOffset / kinfoProcStartUsecOffset / kinfoEprocPPIDOffset
// and the process-state offset kinfoProcStatOffset below) from the
// headers, then refresh the comment above + the bump-policy paragraph in
// docs/architecture.md.
//
// See also: <bsd/sys/proc.h> (extern_proc.p_pid, extern_proc.p_starttime)
// and <bsd/sys/sysctl.h> (struct kinfo_proc, struct eproc.e_ppid) in the
// XNU source tree; extern_proc.p_stat and the S* values also come from
// <bsd/sys/proc.h>.
const kinfoProcSize = 648

// kinfoProcPIDOffset is the byte offset of extern_proc.p_pid inside
// kinfo_proc on XNU 11.x. The eproc tail follows extern_proc, so p_pid
// lives at extern_proc's offset 40 (its position inside the leading
// substruct). No extractor reads p_pid; the constant anchors the p_stat and
// p_comm offset derivations below. Same XNU-version sensitivity as
// kinfoProcSize.
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

// Process-state offset (SR-3.8). The start-time reader counts a zombie as
// gone, so it needs extern_proc.p_stat from the same single kinfo_proc entry
// it reads p_starttime from. Derived from the SAME XNU LP64 header basis as
// the offsets above (bsd/sys/proc.h, struct extern_proc): p_un (16: the
// p_starttime timeval / two pointers) + p_vmspace (8) + p_sigacts (8) +
// p_flag (int, 4) = 36, so p_stat (char) sits at offset 36, immediately
// before p_pid at 40 (3 bytes of padding align the pid_t), which matches
// kinfoProcPIDOffset. Same XNU-version sensitivity and bump policy as
// kinfoProcSize.
const kinfoProcStatOffset = 36

// Command-name offset (SR-14's `parent_command`, read by
// darwinCommandNameReader). extern_proc.p_comm is char[MAXCOMLEN+1] with
// MAXCOMLEN = 16 (bsd/sys/param.h), NUL-terminated. Derived from the SAME XNU
// LP64 header basis as the offsets above (bsd/sys/proc.h, struct
// extern_proc): after p_pid (40) come p_oppid (44), p_dupfd (48), user_stack
// (56, 8-aligned), exit_thread (64), p_debugger (72), sigwait (76), p_estcpu
// (80), p_cpticks (84), p_pctcpu (88), p_wchan (96, 8-aligned), p_wmesg
// (104), p_swtime (112), p_slptime (116), p_realtimer (120, an itimerval of
// two 16-byte timevals), p_rtime (152), p_uticks (168), p_sticks (176),
// p_iticks (184), p_traceflag (192), p_tracep (200, 8-aligned), p_siglist
// (208), p_textvp (216, 8-aligned), p_holdcnt (224), p_sigmask (228),
// p_sigignore (232), p_sigcatch (236), p_priority (240), p_usrpri (241),
// p_nice (242), so p_comm sits at 243..259; p_pgrp follows at 264 and the
// struct ends at 296 = sizeof(extern_proc), matching kinfoProcSize's
// arithmetic. golang.org/x/sys/unix's ExternProc.P_comm ([17]byte) agrees.
// Same XNU-version sensitivity and bump policy as kinfoProcSize.
const (
	kinfoProcCommOffset = 243
	kinfoProcCommLen    = 17
)

// extern_proc.p_stat values (bsd/sys/proc.h). SIDL..SZOMB is the whole known
// range; a value outside it is the drift signal for kinfoProcStatOffset.
const (
	kinfoStatSIDL   = 1 // process being created by fork
	kinfoStatSRUN   = 2 // currently runnable
	kinfoStatSSLEEP = 3 // sleeping on an address
	kinfoStatSSTOP  = 4 // process debugging or suspension
	kinfoStatSZOMB  = 5 // awaiting collection by parent (a zombie)
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

// maxPlausiblePID is the upper bound of parseKinfoPPID's plausibility
// guard. Linux's CONFIG_BASE_FULL caps PIDs at 4_194_304; macOS's PID
// space is smaller in practice but we use the generous Linux cap so a
// legitimate macOS PID can never trip the guard while obvious garbage
// from a struct-layout drift (very-large random uint32 values) does.
const maxPlausiblePID = 4_194_304

// ErrKinfoLayoutDrift is returned by the entry-granular identity extractors
// (parseKinfoPPID, parseKinfoStartTime, parseKinfoStat, parseKinfoComm) when
// a single kinfo_proc entry's bytes fail their field plausibility guards — the
// signal that the pinned XNU offsets (kinfoEprocPPIDOffset /
// kinfoProcStart*Offset / kinfoProcStatOffset / kinfoProcCommOffset) have
// drifted under us, e.g. after a macOS major bump resized struct kinfo_proc.
//
// Drift is fail-OPEN: the start-time reader, the command-name reader and the
// parent-pid reader answer unreadable, never gone. It is a distinct sentinel that wraps nothing,
// so callers that need to branch on drift test
// errors.Is(err, ErrKinfoLayoutDrift).
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
// maxPlausiblePID). A ppid of 0 is
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

// parseKinfoStat extracts extern_proc.p_stat (the S* process status) from the
// single kinfo_proc entry beginning at byte offset off within buf, so the
// start-time reader can recognise a zombie (kinfoStatSZOMB). Build-tag-free
// for off-darwin unit testing; entry-granular like parseKinfoPPID and
// parseKinfoStartTime.
//
// Returns ErrKinfoLayoutDrift when the entry does not fit or when the value
// is outside the known p_stat range [kinfoStatSIDL, kinfoStatSZOMB]: a
// drifted kinfoProcStatOffset reinterpreting an unrelated byte lands here.
func parseKinfoStat(buf []byte, off int) (int, error) {
	if err := entryOffset(buf, off); err != nil {
		return 0, err
	}
	stat := int(buf[off+kinfoProcStatOffset])
	if stat < kinfoStatSIDL || stat > kinfoStatSZOMB {
		return 0, fmt.Errorf("%w: p_stat=%d out of range at entry offset %d (kinfoProcStatOffset=%d may be stale for this XNU version)",
			ErrKinfoLayoutDrift, stat, off, kinfoProcStatOffset)
	}
	return stat, nil
}

// parseKinfoComm extracts extern_proc.p_comm (the process's command name)
// from the single kinfo_proc entry beginning at byte offset off within buf.
// Build-tag-free for off-darwin unit testing; entry-granular like
// parseKinfoPPID and parseKinfoStat.
//
// Returns ErrKinfoLayoutDrift when the entry does not fit, when the
// kinfoProcCommLen-byte field holds no NUL terminator, when the name before
// the NUL is empty, or when it contains an ASCII control byte (0x01-0x1f or
// 0x7f): a drifted kinfoProcCommOffset reinterpreting unrelated bytes lands
// here. Bytes >= 0x80 are kept (a UTF-8 name, possibly truncated mid-rune by
// the kernel).
func parseKinfoComm(buf []byte, off int) (string, error) {
	if err := entryOffset(buf, off); err != nil {
		return "", err
	}
	field := buf[off+kinfoProcCommOffset : off+kinfoProcCommOffset+kinfoProcCommLen]
	n := bytes.IndexByte(field, 0)
	if n < 0 {
		return "", fmt.Errorf("%w: p_comm has no NUL terminator at entry offset %d (kinfoProcCommOffset=%d may be stale for this XNU version)",
			ErrKinfoLayoutDrift, off, kinfoProcCommOffset)
	}
	if n == 0 {
		return "", fmt.Errorf("%w: p_comm is empty at entry offset %d (kinfoProcCommOffset=%d may be stale for this XNU version)",
			ErrKinfoLayoutDrift, off, kinfoProcCommOffset)
	}
	for _, c := range field[:n] {
		if c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("%w: p_comm holds control byte 0x%02x at entry offset %d (kinfoProcCommOffset=%d may be stale for this XNU version)",
				ErrKinfoLayoutDrift, c, off, kinfoProcCommOffset)
		}
	}
	return string(field[:n]), nil
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
