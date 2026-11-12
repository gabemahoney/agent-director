package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// plantEntry writes one synthetic kinfo_proc entry's identity fields
// (e_ppid, p_starttime.tv_sec, p_starttime.tv_usec) at their named-constant
// offsets within the entry that begins at byte offset off in buf, which must
// hold at least off+kinfoProcSize bytes.
func plantEntry(buf []byte, off int, ppid int32, sec int64, usec int32) {
	p := off + kinfoEprocPPIDOffset
	binary.LittleEndian.PutUint32(buf[p:p+4], uint32(ppid))
	s := off + kinfoProcStartSecOffset
	binary.LittleEndian.PutUint64(buf[s:s+8], uint64(sec))
	u := off + kinfoProcStartUsecOffset
	binary.LittleEndian.PutUint32(buf[u:u+4], uint32(usec))
}

// plantStat writes extern_proc.p_stat at kinfoProcStatOffset within the entry
// that begins at byte offset off in buf.
func plantStat(buf []byte, off int, stat byte) {
	buf[off+kinfoProcStatOffset] = stat
}

// plantPID adds extern_proc.p_pid to the existing synthetic entry fixture.
func plantPID(buf []byte, off int, pid int32) {
	binary.LittleEndian.PutUint32(buf[off+kinfoProcPIDOffset:], uint32(pid))
}

func TestParseKinfoPIDs(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 3*kinfoProcSize)
	want := []int{0, 4242, maxPlausiblePID}
	for i, pid := range want {
		off := i * kinfoProcSize
		plantEntry(buf, off, 999, darwinStartSec, darwinStartUsec)
		plantStat(buf, off, kinfoStatSRUN)
		plantPID(buf, off, int32(pid))
		binary.LittleEndian.PutUint32(buf[off+kinfoProcPIDOffset+4:], 777) // neighbouring decoy
	}
	if got, err := parseKinfoPIDs(buf); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("pids = %v, %v; want %v", got, err, want)
	}
	for _, kind := range []string{"short", "ragged", "negative", "above-bound", "bad-first-stat", "bad-last-stat"} {
		t.Run(kind, func(t *testing.T) {
			data := append([]byte(nil), buf...)
			switch kind {
			case "short":
				data = data[:kinfoProcSize-1]
			case "ragged":
				data = data[:len(data)-1]
			case "negative":
				plantPID(data, kinfoProcSize, -1)
			case "above-bound":
				plantPID(data, kinfoProcSize, maxPlausiblePID+1)
			case "bad-first-stat":
				plantStat(data, 0, 0)
			case "bad-last-stat":
				plantStat(data, 2*kinfoProcSize, kinfoStatSZOMB+1)
			}
			got, err := parseKinfoPIDs(data)
			assertKinfoDrift(t, err)
			if got != nil {
				t.Fatalf("drift returned partial selection %v", got)
			}
		})
	}
}

func TestParseDarwinProcStartTime(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]int64{{1, 0}, {darwinStartSec, 5}, {darwinStartSec, darwinStartUsec}, {maxPlausibleStartSec - 1, maxPlausibleStartUsec - 1}} {
		text := formatDarwinProcStartTime(pair[0], pair[1])
		got, ok := ParseDarwinProcStartTime(text)
		want := time.Unix(pair[0], pair[1]*int64(time.Microsecond))
		if !ok || !got.Equal(want) {
			t.Errorf("parse %q = %v, %v; want %v", text, got, ok, want)
		}
	}
	fixture, ok := ParseDarwinProcStartTime(procstarttimefix.DarwinProcStarttime)
	if !ok || !fixture.Equal(time.Unix(darwinStartSec, darwinStartUsec*1000)) {
		t.Fatal("canonical fixture did not round-trip")
	}
	// Microseconds are an integer count: .5 is 5 microseconds, not .5 seconds.
	var previous time.Time
	for _, pair := range [][2]int64{{darwinStartSec, 5}, {darwinStartSec, 50}, {darwinStartSec, 500000}, {darwinStartSec + 1, 0}} {
		got, ok := ParseDarwinProcStartTime(formatDarwinProcStartTime(pair[0], pair[1]))
		if !ok || !got.After(previous) {
			t.Fatalf("start order: %v after %v (%v)", got, previous, ok)
		}
		previous = got
	}
	for _, text := range []string{"", ".", "1.", ".1", "1", "+1.0", "1.+0", "-1.0", "1.-1", " 1.0", "1.0 ", "1.0.1", "x.0", "1.x", "1.1e2", "01.0", "1.00", "0.0", "1.1000000", "999999999999999999999999.0", fmt.Sprintf("%d.0", maxPlausibleStartSec)} {
		if got, ok := ParseDarwinProcStartTime(text); ok || !got.IsZero() {
			t.Errorf("invalid %q = %v, %v", text, got, ok)
		}
	}
}

// assertKinfoDrift asserts err is the fail-open drift sentinel and NOT the
// fail-closed ErrProbeUnsupported (opposite fail-semantics, parse_kinfo.go).
func assertKinfoDrift(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrKinfoLayoutDrift) || errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("err = %v; want ErrKinfoLayoutDrift and not ErrProbeUnsupported", err)
	}
}

// TestParseKinfoIdentityAccepted: plausible e_ppid, p_starttime and p_stat
// values round-trip, read from the entry at off (a decoy sits in entry 0),
// at both ends of each accepted range; the start time formats as
// procstarttimefix.DarwinProcStarttime, and usec 0 without zero-padding.
func TestParseKinfoIdentityAccepted(t *testing.T) {
	cases := []struct {
		name       string
		entry      int // index of the target entry; entry 0 holds a decoy when > 0
		ppid       int32
		sec        int64
		usec       int32
		stat       byte
		wantFormat string // "" skips the format check
	}{
		{"fixture entry", 0, 4242, 1700000000, 123456, kinfoStatSRUN, procstarttimefix.DarwinProcStarttime},
		{"entry 2 of 3", 2, 9001, 1700000000, 123456, kinfoStatSRUN, procstarttimefix.DarwinProcStarttime},
		{"ppid at its bound, zombie", 0, int32(maxPlausiblePID), 1700000000, 123456, kinfoStatSZOMB, ""},
		{"usec zero, SIDL", 0, 4242, 1700000000, 0, kinfoStatSIDL, "1700000000.0"},
		{"sec and usec just below their bounds", 1, 4242, maxPlausibleStartSec - 1, int32(maxPlausibleStartUsec) - 1, kinfoStatSRUN, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, (tc.entry+1)*kinfoProcSize)
			plantEntry(buf, 0, 111, 111111111, 999)
			plantStat(buf, 0, kinfoStatSZOMB+1)
			off := tc.entry * kinfoProcSize
			plantEntry(buf, off, tc.ppid, tc.sec, tc.usec)
			plantStat(buf, off, tc.stat)

			ppid, err := parseKinfoPPID(buf, off)
			if err != nil || ppid != int(tc.ppid) {
				t.Errorf("parseKinfoPPID = %d, %v; want %d", ppid, err, tc.ppid)
			}
			sec, usec, err := parseKinfoStartTime(buf, off)
			if err != nil || sec != tc.sec || usec != int64(tc.usec) {
				t.Errorf("parseKinfoStartTime = (%d, %d), %v; want (%d, %d)", sec, usec, err, tc.sec, tc.usec)
			}
			if got := formatDarwinProcStartTime(sec, usec); tc.wantFormat != "" && got != tc.wantFormat {
				t.Errorf("formatDarwinProcStartTime = %q; want %q", got, tc.wantFormat)
			}
			if stat, err := parseKinfoStat(buf, off); err != nil || stat != int(tc.stat) {
				t.Errorf("parseKinfoStat = %d, %v; want %d", stat, err, tc.stat)
			}
		})
	}
}

// TestParseKinfoIdentityDrift: an implausible e_ppid, p_starttime component
// (each bound included) or p_stat outside [SIDL, SZOMB] is ErrKinfoLayoutDrift
// with no partial value; the other fields of the entry still parse.
func TestParseKinfoIdentityDrift(t *testing.T) {
	cases := []struct {
		name  string
		field string // which parser must refuse: ppid, start or stat
		ppid  int32
		sec   int64
		usec  int32
		stat  byte
	}{
		{"ppid zero", "ppid", 0, 1700000000, 123456, kinfoStatSRUN},
		{"ppid negative", "ppid", -1, 1700000000, 123456, kinfoStatSRUN},
		{"ppid above bound", "ppid", int32(maxPlausiblePID) + 1, 1700000000, 123456, kinfoStatSRUN},
		{"ppid 0x80000000", "ppid", -2147483648, 1700000000, 123456, kinfoStatSRUN},
		{"sec zero", "start", 4242, 0, 123456, kinfoStatSRUN},
		{"sec negative", "start", 4242, -1, 123456, kinfoStatSRUN},
		{"sec at bound", "start", 4242, maxPlausibleStartSec, 123456, kinfoStatSRUN},
		{"sec above bound", "start", 4242, maxPlausibleStartSec + 1, 123456, kinfoStatSRUN},
		{"usec negative", "start", 4242, 1700000000, -1, kinfoStatSRUN},
		{"usec at bound", "start", 4242, 1700000000, int32(maxPlausibleStartUsec), kinfoStatSRUN},
		{"usec above bound", "start", 4242, 1700000000, int32(maxPlausibleStartUsec) + 1, kinfoStatSRUN},
		{"stat zero", "stat", 4242, 1700000000, 123456, 0},
		{"stat above zombie", "stat", 4242, 1700000000, 123456, kinfoStatSZOMB + 1},
		{"stat max byte", "stat", 4242, 1700000000, 123456, 0xff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, kinfoProcSize)
			plantEntry(buf, 0, tc.ppid, tc.sec, tc.usec)
			plantStat(buf, 0, tc.stat)
			ppid, ppidErr := parseKinfoPPID(buf, 0)
			sec, usec, startErr := parseKinfoStartTime(buf, 0)
			stat, statErr := parseKinfoStat(buf, 0)
			for field, err := range map[string]error{"ppid": ppidErr, "start": startErr, "stat": statErr} {
				if field == tc.field {
					assertKinfoDrift(t, err)
				} else if err != nil {
					t.Errorf("%s: unexpected %v", field, err)
				}
			}
			if got := map[string]bool{"ppid": ppid != 0, "start": sec != 0 || usec != 0, "stat": stat != 0}; got[tc.field] {
				t.Errorf("%s drift returned a partial value: ppid %d, start (%d, %d), stat %d", tc.field, ppid, sec, usec, stat)
			}
		})
	}
}

// TestParseKinfoEntryTruncatedBuffer: an entry that does not fit in buf (too
// short, an offset past the end, a negative offset) is ErrKinfoLayoutDrift
// from every extractor with no value, even when the p_stat byte is present.
func TestParseKinfoEntryTruncatedBuffer(t *testing.T) {
	cases := []struct {
		name   string
		bufLen int
		off    int
	}{
		{"buffer_shorter_than_one_entry", kinfoProcSize - 1, 0},
		{"entry_offset_runs_past_end", kinfoProcSize, kinfoProcSize},
		{"negative_offset", kinfoProcSize, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.bufLen)
			if tc.off >= 0 && tc.off+kinfoProcStatOffset < len(buf) {
				plantStat(buf, tc.off, kinfoStatSRUN)
			}
			p, err := parseKinfoPPID(buf, tc.off)
			assertKinfoDrift(t, err)
			sec, usec, err := parseKinfoStartTime(buf, tc.off)
			assertKinfoDrift(t, err)
			stat, err := parseKinfoStat(buf, tc.off)
			assertKinfoDrift(t, err)
			if p != 0 || sec != 0 || usec != 0 || stat != 0 {
				t.Errorf("truncated entry returned ppid %d, start (%d, %d), stat %d; want zeros", p, sec, usec, stat)
			}
		})
	}
}
