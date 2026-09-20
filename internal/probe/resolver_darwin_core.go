package probe

import "encoding/binary"

// darwinSnapshotReader implements ancestorReader over a single kern.proc.all
// snapshot buffer plus a per-pid env reader. It is build-tag-free (no sysctl
// plumbing) so the darwin walk logic is unit-testable off-darwin against
// synthetic kinfo_proc bytes: the darwin-tagged file wires the real sysctl
// snapshot + KERN_PROCARGS2 env reader into this struct, following the
// parse_kinfo.go discipline (byte parsing here, syscalls there).
//
// PM decision (Q3): the walk takes ONE kern.proc.all snapshot for its whole
// duration (the existing SR-6.4 mechanism), and the entry-granular kinfo
// parsers (parseKinfoPPID / parseKinfoStartTime) extract per-entry ppid +
// starttime from it. Per-pid KERN_PROC_PID fetches are Epic hp's read pattern,
// not built here.
type darwinSnapshotReader struct {
	// snapshot is the raw kern.proc.all blob: a packed array of kinfoProcSize
	// entries.
	snapshot []byte
	// self is the starting pid (the current process).
	self int
	// envID returns the EnvKey value in pid's environment and whether it was
	// present. It abstracts KERN_PROCARGS2 + envFromProcArgs2 so the walk is
	// testable with a synthetic env map. A non-nil error is a hard failure
	// that aborts the walk; a routine permission-denied / process-gone must be
	// reported as (\"\", false, nil).
	envID func(pid int) (string, bool, error)
	// index memoizes the pid → entry-offset map built from the snapshot on
	// first use. -1 entries are never stored: a pid absent from the map is a
	// vanished / unknown process.
	index map[int]int
}

func (d *darwinSnapshotReader) selfPID() int { return d.self }

// buildIndex maps each snapshot entry's pid to its byte offset. Entries that
// fail the PID plausibility bound (a struct-layout drift symptom) are skipped
// rather than indexed. Called lazily so a caller that only constructs the
// reader pays nothing.
func (d *darwinSnapshotReader) buildIndex() {
	if d.index != nil {
		return
	}
	idx := make(map[int]int)
	if len(d.snapshot) >= kinfoProcSize {
		n := len(d.snapshot) / kinfoProcSize
		for i := 0; i < n; i++ {
			off := i * kinfoProcSize
			pidOff := off + kinfoProcPIDOffset
			if pidOff+4 > len(d.snapshot) {
				break
			}
			pid := int(binary.LittleEndian.Uint32(d.snapshot[pidOff : pidOff+4]))
			if pid <= 0 || pid > maxPlausiblePID {
				continue
			}
			// First occurrence wins; the kernel does not duplicate live pids.
			if _, dup := idx[pid]; !dup {
				idx[pid] = off
			}
		}
	}
	d.index = idx
}

func (d *darwinSnapshotReader) instanceID(pid int) (string, bool, error) {
	return d.envID(pid)
}

// parent looks up pid's entry in the snapshot index and extracts its parent pid
// (parseKinfoPPID) and canonical starttime string (parseKinfoStartTime +
// formatDarwinProcStartTime). A pid absent from the snapshot has vanished /
// exited: (0, \"\", false, nil). A drift-guard trip in either parser is a hard
// error (ErrKinfoLayoutDrift) that aborts the walk fail-open.
func (d *darwinSnapshotReader) parent(pid int) (int, string, bool, error) {
	d.buildIndex()
	off, ok := d.index[pid]
	if !ok {
		return 0, "", false, nil
	}
	ppid, err := parseKinfoPPID(d.snapshot, off)
	if err != nil {
		return 0, "", false, err
	}
	sec, usec, err := parseKinfoStartTime(d.snapshot, off)
	if err != nil {
		return 0, "", false, err
	}
	return ppid, formatDarwinProcStartTime(sec, usec), true, nil
}
