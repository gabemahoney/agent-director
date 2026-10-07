package probe

import (
	"reflect"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// darwinStartTimeReader must satisfy the start-time reader interface.
var _ ProcChecker = darwinStartTimeReader{}

// darwinStartSec / darwinStartUsec are the (sec, usec) preimage of
// procstarttimefix.DarwinProcStarttime ("1700000000.123456").
const (
	darwinStartSec  = 1700000000
	darwinStartUsec = 123456
)

// darwinStartEntry builds one kinfoProcSize entry with the given p_stat and
// p_starttime planted at their named offsets.
func darwinStartEntry(stat byte, sec int64, usec int32) []byte {
	buf := make([]byte, kinfoProcSize)
	plantEntry(buf, 0, 4242, sec, usec)
	plantStat(buf, 0, stat)
	return buf
}

// fakeKinfo returns a fetchKinfo answering buf and err, and a check that it
// was called once, for pid, or never for a non-positive pid.
func fakeKinfo(t *testing.T, pid int, buf []byte, err error) (func(int) ([]byte, error), func()) {
	var calls []int
	fetch := func(p int) ([]byte, error) {
		calls = append(calls, p)
		return buf, err
	}
	return fetch, func() {
		t.Helper()
		want := []int{pid}
		if pid <= 0 {
			want = nil
		}
		if !reflect.DeepEqual(calls, want) {
			t.Errorf("fetchKinfo calls = %v; want %v", calls, want)
		}
	}
}

// TestDarwinStartTimeReader pins the full (start, alive, known) triple for
// every darwin outcome, and that the kinfo fetch is the reader's only I/O.
func TestDarwinStartTimeReader(t *testing.T) {
	const pid = 4242
	aliveEntry := func(stat byte) []byte {
		return darwinStartEntry(stat, darwinStartSec, darwinStartUsec)
	}
	shortEntry := darwinStartEntry(kinfoStatSRUN, darwinStartSec, darwinStartUsec)[:kinfoProcSize-1]

	cases := []struct {
		name      string
		pid       int
		buf       []byte
		err       error
		wantStart string
		wantAlive bool
		wantKnown bool
	}{
		// alive: any known non-zombie state, start formatted "<sec>.<usec>".
		{name: "alive_srun", pid: pid, buf: aliveEntry(kinfoStatSRUN), wantStart: procstarttimefix.DarwinProcStarttime, wantAlive: true, wantKnown: true},
		{name: "alive_sidl", pid: pid, buf: aliveEntry(kinfoStatSIDL), wantStart: procstarttimefix.DarwinProcStarttime, wantAlive: true, wantKnown: true},
		{name: "alive_ssleep", pid: pid, buf: aliveEntry(kinfoStatSSLEEP), wantStart: procstarttimefix.DarwinProcStarttime, wantAlive: true, wantKnown: true},
		{name: "alive_sstop", pid: pid, buf: aliveEntry(kinfoStatSSTOP), wantStart: procstarttimefix.DarwinProcStarttime, wantAlive: true, wantKnown: true},

		// gone.
		{name: "gone_esrch", pid: pid, err: syscall.ESRCH, wantKnown: true},
		{name: "gone_empty_result", pid: pid, buf: []byte{}, wantKnown: true},
		{name: "gone_zombie", pid: pid, buf: aliveEntry(kinfoStatSZOMB), wantKnown: true},

		// unreadable: fetch errors other than ESRCH (TestClassifyErrno covers each errno).
		{name: "unreadable_eacces", pid: pid, err: syscall.EACCES},
		{name: "unreadable_unpinned_errno", pid: pid, err: syscall.EINVAL},

		// unreadable: layout drift in the state byte or the start time, never gone
		// (TestParseKinfoIdentityDrift covers each drift value).
		{name: "unreadable_state_above_zombie", pid: pid, buf: aliveEntry(kinfoStatSZOMB + 1)},
		{name: "unreadable_start_sec_drift", pid: pid, buf: darwinStartEntry(kinfoStatSRUN, 0, darwinStartUsec)},
		{name: "unreadable_zombie_with_start_drift", pid: pid, buf: darwinStartEntry(kinfoStatSZOMB, 0, darwinStartUsec)},
		{name: "unreadable_short_entry", pid: pid, buf: shortEntry},

		// unreadable: non-positive pid, answered without a fetch.
		{name: "unreadable_pid_zero", pid: 0},
		{name: "unreadable_pid_negative", pid: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetch, checkCalls := fakeKinfo(t, tc.pid, tc.buf, tc.err)
			r := darwinStartTimeReader{fetchKinfo: fetch}

			start, alive, known := r.StartTime(tc.pid)
			if start != tc.wantStart || alive != tc.wantAlive || known != tc.wantKnown {
				t.Errorf("StartTime(%d) = (%q, %v, %v); want (%q, %v, %v)",
					tc.pid, start, alive, known, tc.wantStart, tc.wantAlive, tc.wantKnown)
			}
			checkCalls()
		})
	}
}
