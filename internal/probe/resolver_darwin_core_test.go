package probe

import (
	"encoding/binary"
	"errors"
	"testing"
)

// plantSnapshotEntry writes a full synthetic kern.proc.all entry at offset off:
// the p_pid at kinfoProcPIDOffset (which buildIndex reads) PLUS the identity
// fields (e_ppid, p_starttime.tv_sec/tv_usec) at their named offsets. It extends
// plantEntry (which does NOT plant the pid) so buildIndex can locate the entry.
// Mirrors the parse_kinfo_identity_test.go fixture pattern: values planted at
// named-constant offsets, entries sized in kinfoProcSize slots.
func plantSnapshotEntry(buf []byte, off int, pid int32, ppid int32, sec int64, usec int32) {
	pp := off + kinfoProcPIDOffset
	binary.LittleEndian.PutUint32(buf[pp:pp+4], uint32(pid))
	plantEntry(buf, off, ppid, sec, usec)
}

// TestDarwinBuildIndexMapsPIDsToOffsets pins buildIndex over a multi-entry
// synthetic kern.proc.all buffer: each plausible pid maps to the byte offset of
// its OWN entry, so parent() reads that entry's identity fields.
func TestDarwinBuildIndexMapsPIDsToOffsets(t *testing.T) {
	const entries = 3
	buf := make([]byte, entries*kinfoProcSize)
	plantSnapshotEntry(buf, 0*kinfoProcSize, 100, 200, 1700000000, 1)
	plantSnapshotEntry(buf, 1*kinfoProcSize, 200, 300, 1700000000, 2)
	plantSnapshotEntry(buf, 2*kinfoProcSize, 300, 1, 1700000000, 3)

	d := &darwinSnapshotReader{snapshot: buf}
	d.buildIndex()

	want := map[int]int{
		100: 0 * kinfoProcSize,
		200: 1 * kinfoProcSize,
		300: 2 * kinfoProcSize,
	}
	if len(d.index) != len(want) {
		t.Fatalf("index has %d entries; want %d (%v)", len(d.index), len(want), d.index)
	}
	for pid, off := range want {
		if got, ok := d.index[pid]; !ok || got != off {
			t.Errorf("index[%d] = (%d, %v); want (%d, true)", pid, got, ok, off)
		}
	}
}

// TestDarwinBuildIndexDedupsFirstWins pins the dedup rule: when the same pid
// appears in two entries (kernel should not, but a drifted/duplicated snapshot
// might), the FIRST occurrence's offset wins.
func TestDarwinBuildIndexDedupsFirstWins(t *testing.T) {
	const entries = 2
	buf := make([]byte, entries*kinfoProcSize)
	// Same pid 100 in both entries; entry 0 carries ppid 200, entry 1 ppid 999.
	plantSnapshotEntry(buf, 0*kinfoProcSize, 100, 200, 1700000000, 1)
	plantSnapshotEntry(buf, 1*kinfoProcSize, 100, 999, 1700000000, 2)

	d := &darwinSnapshotReader{snapshot: buf}
	d.buildIndex()

	if got := d.index[100]; got != 0 {
		t.Fatalf("index[100] = %d; want 0 (first occurrence wins)", got)
	}
	// parent() must therefore read entry 0's ppid, not entry 1's.
	ppid, _, present, err := d.parent(100)
	if err != nil || !present {
		t.Fatalf("parent(100) = (_, _, %v, %v); want present, no error", present, err)
	}
	if ppid != 200 {
		t.Errorf("ppid = %d; want 200 (from the first-wins entry, not 999)", ppid)
	}
}

// TestDarwinBuildIndexSkipsImplausiblePID pins that an entry whose p_pid fails
// the plausibility bound (a layout-drift symptom) is SKIPPED, not indexed, while
// plausible sibling entries remain reachable.
func TestDarwinBuildIndexSkipsImplausiblePID(t *testing.T) {
	const entries = 3
	buf := make([]byte, entries*kinfoProcSize)
	plantSnapshotEntry(buf, 0*kinfoProcSize, 100, 200, 1700000000, 1)
	// Entry 1: implausible pid (above the bound) → skipped.
	plantSnapshotEntry(buf, 1*kinfoProcSize, int32(maxPlausiblePID)+1, 300, 1700000000, 2)
	plantSnapshotEntry(buf, 2*kinfoProcSize, 300, 1, 1700000000, 3)

	d := &darwinSnapshotReader{snapshot: buf}
	d.buildIndex()

	if _, ok := d.index[int(maxPlausiblePID)+1]; ok {
		t.Errorf("implausible pid was indexed; want skipped")
	}
	if _, ok := d.index[100]; !ok {
		t.Errorf("plausible pid 100 missing from index")
	}
	if _, ok := d.index[300]; !ok {
		t.Errorf("plausible pid 300 missing from index")
	}
	if len(d.index) != 2 {
		t.Errorf("index has %d entries; want 2 (implausible skipped)", len(d.index))
	}
}

// TestDarwinBuildIndexTruncatedSnapshot pins the truncated-snapshot handling:
// a buffer shorter than one entry yields an empty index, and a buffer holding
// one full entry plus a trailing partial entry indexes only the complete one
// (the partial tail is never read past the buffer end).
func TestDarwinBuildIndexTruncatedSnapshot(t *testing.T) {
	t.Run("shorter_than_one_entry", func(t *testing.T) {
		buf := make([]byte, kinfoProcSize-1)
		d := &darwinSnapshotReader{snapshot: buf}
		d.buildIndex()
		if len(d.index) != 0 {
			t.Errorf("index has %d entries; want 0 for a sub-entry buffer", len(d.index))
		}
	})

	t.Run("one_full_plus_partial_tail", func(t *testing.T) {
		// One full entry then a partial tail (half an entry).
		buf := make([]byte, kinfoProcSize+kinfoProcSize/2)
		plantSnapshotEntry(buf, 0, 100, 200, 1700000000, 1)
		d := &darwinSnapshotReader{snapshot: buf}
		d.buildIndex()
		if len(d.index) != 1 {
			t.Fatalf("index has %d entries; want 1 (partial tail ignored)", len(d.index))
		}
		if _, ok := d.index[100]; !ok {
			t.Errorf("pid 100 (the complete entry) missing from index")
		}
	})
}

// TestDarwinParentPresentPID pins parent() for a pid present in the snapshot:
// it returns the entry's ppid, the formatted starttime string, present=true,
// and no error.
func TestDarwinParentPresentPID(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	// sec=1700000000 usec=123456 is the fixture preimage of "1700000000.123456".
	plantSnapshotEntry(buf, 0, 100, 4242, 1700000000, 123456)

	d := &darwinSnapshotReader{snapshot: buf}
	ppid, start, present, err := d.parent(100)
	if err != nil {
		t.Fatalf("parent(100): unexpected error %v", err)
	}
	if !present {
		t.Fatalf("present = false; want true for a pid in the snapshot")
	}
	if ppid != 4242 {
		t.Errorf("ppid = %d; want 4242", ppid)
	}
	if start != "1700000000.123456" {
		t.Errorf("starttime = %q; want %q", start, "1700000000.123456")
	}
}

// TestDarwinParentVanishedPID pins parent() for a pid absent from the snapshot
// (exited / never present): the clean end-of-chain convention
// (0, "", false, nil) — not an error.
func TestDarwinParentVanishedPID(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	plantSnapshotEntry(buf, 0, 100, 200, 1700000000, 1)

	d := &darwinSnapshotReader{snapshot: buf}
	ppid, start, present, err := d.parent(999) // not in the snapshot
	if err != nil {
		t.Fatalf("parent(999): unexpected error %v", err)
	}
	if present || ppid != 0 || start != "" {
		t.Errorf("got (ppid=%d, start=%q, present=%v); want (0, \"\", false)", ppid, start, present)
	}
}

// TestDarwinParentDriftTrips pins the drift error path: an indexed pid whose
// entry carries a drift-tripping identity field (here an implausible
// p_starttime.tv_sec) surfaces ErrKinfoLayoutDrift from parent(), aborting the
// walk fail-open, with no partial identity returned. The pid IS plausible (so it
// is indexed), isolating the parser drift guard from the buildIndex guard.
func TestDarwinParentDriftTrips(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	// Plausible pid (indexed) + plausible ppid, but tv_sec=0 trips the
	// starttime drift guard inside parseKinfoStartTime.
	plantSnapshotEntry(buf, 0, 100, 200, 0, 123456)

	d := &darwinSnapshotReader{snapshot: buf}
	ppid, start, present, err := d.parent(100)
	if !errors.Is(err, ErrKinfoLayoutDrift) {
		t.Fatalf("err = %v; want errors.Is(err, ErrKinfoLayoutDrift)", err)
	}
	if errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("drift error must NOT match ErrProbeUnsupported (fail-open vs fail-closed)")
	}
	if present || ppid != 0 || start != "" {
		t.Errorf("got (ppid=%d, start=%q, present=%v); want zeroed on drift", ppid, start, present)
	}
}

// TestDarwinWalkAncestorsThroughSnapshot pins a small end-to-end walkAncestors
// run driven by the darwinSnapshotReader off-darwin: a synthetic snapshot builds
// a 100 → 200 → 300(terminates) chain, a fake envID func makes 100 and 200 carry
// the id (200 is TOPMOST), and the walk must climb the darwin path and return
// pid 200 with its formatted starttime — proving topmost-match through the real
// darwin ancestorReader implementation without a mac.
func TestDarwinWalkAncestorsThroughSnapshot(t *testing.T) {
	const id = "match-me"
	const entries = 3
	buf := make([]byte, entries*kinfoProcSize)
	// self=100 → 200 → 300; 300's ppid is 1 so the chain terminates at init.
	plantSnapshotEntry(buf, 0*kinfoProcSize, 100, 200, 1700000000, 111111)
	plantSnapshotEntry(buf, 1*kinfoProcSize, 200, 300, 1700000000, 222222)
	plantSnapshotEntry(buf, 2*kinfoProcSize, 300, 1, 1700000000, 333333)

	// Fake env: 100 and 200 match; 300 does not (terminates the topmost search).
	env := map[int]string{100: id, 200: id}
	envID := func(pid int) (string, bool, error) {
		v, ok := env[pid]
		return v, ok, nil
	}

	d := &darwinSnapshotReader{snapshot: buf, self: 100, envID: envID}

	pid, start, err := walkAncestors(d, id)
	if err != nil {
		t.Fatalf("walkAncestors over darwin snapshot: %v", err)
	}
	if pid != 200 {
		t.Errorf("pid = %d; want 200 (topmost match through the darwin path)", pid)
	}
	if start != "1700000000.222222" {
		t.Errorf("starttime = %q; want %q (entry 200's formatted starttime)", start, "1700000000.222222")
	}
}
