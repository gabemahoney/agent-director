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

		// unreadable: fetch errors other than ESRCH.
		{name: "unreadable_eacces", pid: pid, err: syscall.EACCES},
		{name: "unreadable_eperm", pid: pid, err: syscall.EPERM},
		{name: "unreadable_unpinned_errno", pid: pid, err: syscall.EINVAL},

		// unreadable: layout drift in the state byte or the start time, never gone.
		{name: "unreadable_state_zero", pid: pid, buf: aliveEntry(0)},
		{name: "unreadable_state_above_zombie", pid: pid, buf: aliveEntry(kinfoStatSZOMB + 1)},
		{name: "unreadable_state_max_byte", pid: pid, buf: aliveEntry(0xff)},
		{name: "unreadable_start_sec_drift", pid: pid, buf: darwinStartEntry(kinfoStatSRUN, 0, darwinStartUsec)},
		{name: "unreadable_start_usec_drift", pid: pid, buf: darwinStartEntry(kinfoStatSRUN, darwinStartSec, int32(maxPlausibleStartUsec))},
		{name: "unreadable_zombie_with_start_drift", pid: pid, buf: darwinStartEntry(kinfoStatSZOMB, 0, darwinStartUsec)},
		{name: "unreadable_short_entry", pid: pid, buf: shortEntry},

		// unreadable: non-positive pid, answered without a fetch.
		{name: "unreadable_pid_zero", pid: 0},
		{name: "unreadable_pid_negative", pid: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []int
			r := darwinStartTimeReader{fetchKinfo: func(p int) ([]byte, error) {
				calls = append(calls, p)
				return tc.buf, tc.err
			}}

			start, alive, known := r.StartTime(tc.pid)
			if start != tc.wantStart || alive != tc.wantAlive || known != tc.wantKnown {
				t.Errorf("StartTime(%d) = (%q, %v, %v); want (%q, %v, %v)",
					tc.pid, start, alive, known, tc.wantStart, tc.wantAlive, tc.wantKnown)
			}

			wantCalls := []int{tc.pid}
			if tc.pid <= 0 {
				wantCalls = nil
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("fetchKinfo calls = %v; want %v", calls, wantCalls)
			}
		})
	}
}

// TestDarwinStartTimeReaderHasNoEnvSeam: the kinfo fetch is the reader's only
// seam, so it has no way to read a process environment (KERN_PROCARGS2).
func TestDarwinStartTimeReaderHasNoEnvSeam(t *testing.T) {
	typ := reflect.TypeOf(darwinStartTimeReader{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(fields, []string{"fetchKinfo"}) {
		t.Errorf("darwinStartTimeReader fields = %v; want only [fetchKinfo]", fields)
	}
}
