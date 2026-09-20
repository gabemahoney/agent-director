package probe

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// plantEntry writes one synthetic kinfo_proc entry's identity fields
// (e_ppid, p_starttime.tv_sec, p_starttime.tv_usec) at their named-constant
// offsets within the entry that begins at byte offset off in buf. The buffer
// must already be at least off+kinfoProcSize bytes long. Mirrors the
// synthetic-buffer pattern in parse_kinfo_test.go: values planted at
// named-constant offsets, entries sized in kinfoProcSize slots.
func plantEntry(buf []byte, off int, ppid int32, sec int64, usec int32) {
	p := off + kinfoEprocPPIDOffset
	binary.LittleEndian.PutUint32(buf[p:p+4], uint32(ppid))
	s := off + kinfoProcStartSecOffset
	binary.LittleEndian.PutUint64(buf[s:s+8], uint64(sec))
	u := off + kinfoProcStartUsecOffset
	binary.LittleEndian.PutUint32(buf[u:u+4], uint32(usec))
}

// TestParseKinfoPPIDRoundTrip pins the e_ppid happy path: a plausible
// parent-pid planted at kinfoEprocPPIDOffset inside a single 648-byte entry
// round-trips through the entry-granular extractor at offset 0.
func TestParseKinfoPPIDRoundTrip(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	const wantPPID = 4242
	plantEntry(buf, 0, wantPPID, 1700000000, 123456)

	got, err := parseKinfoPPID(buf, 0)
	if err != nil {
		t.Fatalf("parseKinfoPPID: %v", err)
	}
	if got != wantPPID {
		t.Errorf("ppid = %d; want %d", got, wantPPID)
	}
}

// TestParseKinfoPPIDNonZeroEntryOffset exercises the entry-granular API's
// core contract: the extractor reads exactly the entry at the given offset,
// not the whole buffer. A three-entry buffer plants a decoy ppid in entry 0
// and the target in entry 2; parsing at that entry's offset must return the
// target, proving no baked-in whole-buffer iteration and correct offset math.
func TestParseKinfoPPIDNonZeroEntryOffset(t *testing.T) {
	const entries = 3
	buf := make([]byte, entries*kinfoProcSize)
	// Decoy in entry 0 that must NOT leak into the entry-2 read.
	plantEntry(buf, 0, 111, 1700000000, 1)
	const targetOff = 2 * kinfoProcSize
	const wantPPID = 9001
	plantEntry(buf, targetOff, wantPPID, 1700000000, 2)

	got, err := parseKinfoPPID(buf, targetOff)
	if err != nil {
		t.Fatalf("parseKinfoPPID at offset %d: %v", targetOff, err)
	}
	if got != wantPPID {
		t.Errorf("ppid = %d; want %d (must read the entry at off, not entry 0)", got, wantPPID)
	}
}

// TestParseKinfoPPIDAtMaxBound pins the exactly-at-bound behavior for the ppid
// guard: the guard refuses ppid > maxPlausiblePID, so ppid == maxPlausiblePID
// is the largest ACCEPTED value.
func TestParseKinfoPPIDAtMaxBound(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, int32(maxPlausiblePID), 1700000000, 123456)

	got, err := parseKinfoPPID(buf, 0)
	if err != nil {
		t.Fatalf("ppid == maxPlausiblePID should be accepted; got %v", err)
	}
	if got != maxPlausiblePID {
		t.Errorf("ppid = %d; want %d", got, maxPlausiblePID)
	}
}

// TestParseKinfoStartTimeRoundTripFormatting parses p_starttime from a single
// synthetic entry and renders it through formatDarwinProcStartTime, asserting
// the output equals the shared fixture constant. Expectations are sourced from
// procstarttimefix.DarwinProcStarttime (the single definition site), NOT
// re-derived literals — the planted timeval (sec=1700000000, usec=123456) is
// the exact preimage of that constant.
func TestParseKinfoStartTimeRoundTripFormatting(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	// These are the components the fixture constant "1700000000.123456"
	// canonically formats from; keep in sync with procstarttimefix.
	const secIn = 1700000000
	const usecIn = 123456
	plantEntry(buf, 0, 4242, secIn, usecIn)

	sec, usec, err := parseKinfoStartTime(buf, 0)
	if err != nil {
		t.Fatalf("parseKinfoStartTime: %v", err)
	}
	if sec != secIn || usec != usecIn {
		t.Fatalf("(sec,usec) = (%d,%d); want (%d,%d)", sec, usec, secIn, usecIn)
	}
	got := formatDarwinProcStartTime(sec, usec)
	if got != procstarttimefix.DarwinProcStarttime {
		t.Errorf("formatDarwinProcStartTime = %q; want %q (must byte-agree with fixture constant)",
			got, procstarttimefix.DarwinProcStarttime)
	}
}

// TestParseKinfoStartTimeNonZeroEntryOffset proves the start-time extractor is
// entry-granular too: the target timeval lives in entry 1 of a multi-entry
// buffer with a decoy in entry 0.
func TestParseKinfoStartTimeNonZeroEntryOffset(t *testing.T) {
	const entries = 2
	buf := make([]byte, entries*kinfoProcSize)
	plantEntry(buf, 0, 111, 111111111, 999) // decoy
	const targetOff = kinfoProcSize
	const secIn = 1700000000
	const usecIn = 123456
	plantEntry(buf, targetOff, 4242, secIn, usecIn)

	sec, usec, err := parseKinfoStartTime(buf, targetOff)
	if err != nil {
		t.Fatalf("parseKinfoStartTime at offset %d: %v", targetOff, err)
	}
	if formatDarwinProcStartTime(sec, usec) != procstarttimefix.DarwinProcStarttime {
		t.Errorf("(sec,usec)=(%d,%d) formatted %q; want %q",
			sec, usec, formatDarwinProcStartTime(sec, usec), procstarttimefix.DarwinProcStarttime)
	}
}

// TestParseKinfoStartTimeUsecZeroAccepted pins the low end of the usec guard:
// the range is [0, maxPlausibleStartUsec), so tv_usec == 0 is accepted.
func TestParseKinfoStartTimeUsecZeroAccepted(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, 1700000000, 0)

	sec, usec, err := parseKinfoStartTime(buf, 0)
	if err != nil {
		t.Fatalf("usec == 0 should be accepted; got %v", err)
	}
	if usec != 0 {
		t.Errorf("usec = %d; want 0", usec)
	}
	if got := formatDarwinProcStartTime(sec, usec); got != "1700000000.0" {
		t.Errorf("format = %q; want %q (no zero-padding)", got, "1700000000.0")
	}
}

// assertKinfoDrift asserts err is the fail-open drift sentinel and NOT the
// fail-closed ErrProbeUnsupported — the two must be distinguishable via
// errors.Is (they carry opposite fail-semantics per parse_kinfo.go's contract).
func assertKinfoDrift(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrKinfoLayoutDrift) {
		t.Fatalf("err = %v; want errors.Is(err, ErrKinfoLayoutDrift) == true", err)
	}
	if errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("err = %v; drift sentinel must NOT match ErrProbeUnsupported (fail-open vs fail-closed)", err)
	}
}

// TestParseKinfoPPIDDriftRefusals covers the e_ppid drift guard: a zero,
// negative, or above-bound ppid each returns ErrKinfoLayoutDrift (distinct
// from ErrProbeUnsupported) and NO partial/garbage value (returned ppid == 0).
func TestParseKinfoPPIDDriftRefusals(t *testing.T) {
	cases := []struct {
		name string
		ppid int32
	}{
		{"zero", 0},
		{"negative", -1},
		{"above_max_bound", int32(maxPlausiblePID) + 1},
		{"large_garbage", -2147483648}, // 0x80000000 reinterpreted as int32
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, kinfoProcSize)
			plantEntry(buf, 0, tc.ppid, 1700000000, 123456)

			got, err := parseKinfoPPID(buf, 0)
			assertKinfoDrift(t, err)
			if got != 0 {
				t.Errorf("ppid = %d; want 0 (no partial/garbage value on drift)", got)
			}
		})
	}
}

// TestParseKinfoStartTimeDriftRefusals covers the p_starttime drift guards for
// both components including their threshold boundaries. Each implausible
// layout returns ErrKinfoLayoutDrift (never ErrProbeUnsupported) and zeroed
// components — no partial value leaks.
func TestParseKinfoStartTimeDriftRefusals(t *testing.T) {
	cases := []struct {
		name string
		sec  int64
		usec int32
	}{
		{"sec_zero", 0, 123456},
		{"sec_negative", -1, 123456},
		// sec >= maxPlausibleStartSec is refused: exactly-at-bound trips it.
		{"sec_at_bound", maxPlausibleStartSec, 123456},
		{"sec_above_bound", maxPlausibleStartSec + 1, 123456},
		{"usec_negative", 1700000000, -1},
		// usec >= maxPlausibleStartUsec is refused: exactly-at-bound trips it.
		{"usec_at_bound", 1700000000, int32(maxPlausibleStartUsec)},
		{"usec_above_bound", 1700000000, int32(maxPlausibleStartUsec) + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, kinfoProcSize)
			plantEntry(buf, 0, 4242, tc.sec, tc.usec)

			sec, usec, err := parseKinfoStartTime(buf, 0)
			assertKinfoDrift(t, err)
			if sec != 0 || usec != 0 {
				t.Errorf("(sec,usec) = (%d,%d); want (0,0) (no partial value on drift)", sec, usec)
			}
		})
	}
}

// TestParseKinfoStartTimeSecJustBelowBoundAccepted pins the accepted side of
// the sec threshold: maxPlausibleStartSec-1 is the largest accepted tv_sec.
func TestParseKinfoStartTimeSecJustBelowBoundAccepted(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, maxPlausibleStartSec-1, 0)

	sec, _, err := parseKinfoStartTime(buf, 0)
	if err != nil {
		t.Fatalf("sec == maxPlausibleStartSec-1 should be accepted; got %v", err)
	}
	if sec != maxPlausibleStartSec-1 {
		t.Errorf("sec = %d; want %d", sec, maxPlausibleStartSec-1)
	}
}

// TestParseKinfoStartTimeUsecJustBelowBoundAccepted pins the accepted side of
// the usec threshold: maxPlausibleStartUsec-1 is the largest accepted tv_usec.
func TestParseKinfoStartTimeUsecJustBelowBoundAccepted(t *testing.T) {
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, 1700000000, int32(maxPlausibleStartUsec)-1)

	_, usec, err := parseKinfoStartTime(buf, 0)
	if err != nil {
		t.Fatalf("usec == maxPlausibleStartUsec-1 should be accepted; got %v", err)
	}
	if usec != maxPlausibleStartUsec-1 {
		t.Errorf("usec = %d; want %d", usec, maxPlausibleStartUsec-1)
	}
}

// TestParseKinfoEntryTruncatedBuffer pins the short/truncated-buffer
// convention for the entry-granular extractors: an entry that extends past the
// buffer end is refused with ErrKinfoLayoutDrift (via entryOffset) and returns
// no values. Covers both a whole-buffer-too-short case and a non-zero offset
// whose entry runs off the end.
func TestParseKinfoEntryTruncatedBuffer(t *testing.T) {
	t.Run("buffer_shorter_than_one_entry", func(t *testing.T) {
		buf := make([]byte, kinfoProcSize-1)
		if p, err := parseKinfoPPID(buf, 0); err == nil || p != 0 {
			t.Fatalf("parseKinfoPPID(short) = (%d, %v); want (0, drift)", p, err)
		} else {
			assertKinfoDrift(t, err)
		}
		if sec, usec, err := parseKinfoStartTime(buf, 0); err == nil || sec != 0 || usec != 0 {
			t.Fatalf("parseKinfoStartTime(short) = (%d, %d, %v); want (0, 0, drift)", sec, usec, err)
		} else {
			assertKinfoDrift(t, err)
		}
	})

	t.Run("entry_offset_runs_past_end", func(t *testing.T) {
		// One full entry, but we ask for entry 1 which does not fit.
		buf := make([]byte, kinfoProcSize)
		off := kinfoProcSize // second entry, buffer holds only one
		if p, err := parseKinfoPPID(buf, off); err == nil || p != 0 {
			t.Fatalf("parseKinfoPPID(off past end) = (%d, %v); want (0, drift)", p, err)
		} else {
			assertKinfoDrift(t, err)
		}
		if sec, usec, err := parseKinfoStartTime(buf, off); err == nil || sec != 0 || usec != 0 {
			t.Fatalf("parseKinfoStartTime(off past end) = (%d, %d, %v); want (0, 0, drift)", sec, usec, err)
		} else {
			assertKinfoDrift(t, err)
		}
	})

	t.Run("negative_offset", func(t *testing.T) {
		buf := make([]byte, kinfoProcSize)
		if p, err := parseKinfoPPID(buf, -1); err == nil || p != 0 {
			t.Fatalf("parseKinfoPPID(-1) = (%d, %v); want (0, drift)", p, err)
		} else {
			assertKinfoDrift(t, err)
		}
	})
}
