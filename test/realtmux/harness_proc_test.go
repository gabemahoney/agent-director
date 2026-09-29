package realtmux_test

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Bounded polling and /proc readers. Waiting on tmux or process state always
// goes through waitFor / waitForObserved, never a fixed sleep.

// pollBudget is the default wait budget; pollInterval the poll period.
const (
	pollBudget   = 10 * time.Second
	pollInterval = 20 * time.Millisecond
)

// waitForObserved polls cond until it holds or budget expires; on expiry it
// fails the test with msg and, when observe is non-nil, the state it reports.
func waitForObserved(t testing.TB, budget time.Duration, msg string, cond func() bool, observe func() string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(pollInterval)
	}
	if cond() {
		return
	}
	if observe != nil {
		t.Fatalf("timed out after %s waiting for: %s\n  observed at expiry: %s", budget, msg, observe())
	}
	t.Fatalf("timed out after %s waiting for: %s", budget, msg)
}

// waitFor is waitForObserved with pollBudget.
func waitFor(t testing.TB, msg string, cond func() bool, observe func() string) {
	t.Helper()
	waitForObserved(t, pollBudget, msg, cond, observe)
}

// procStat is the part of /proc/<pid>/stat the harness reads.
type procStat struct {
	state byte   // field 3
	start string // field 22, start time in clock ticks
}

// gone reports a zombie (or dead) process, which counts as gone.
func (s procStat) gone() bool { return s.state == 'Z' || s.state == 'X' }

// readStat reads /proc/<pid>/stat; ok is false when the process does not
// exist (or the line cannot be parsed).
func readStat(pid int) (procStat, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, false
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 {
		return procStat{}, false
	}
	fields := strings.Fields(line[rparen+1:]) // field 3 onward
	if len(fields) < 20 || len(fields[0]) != 1 {
		return procStat{}, false
	}
	return procStat{state: fields[0][0], start: fields[19]}, true
}

// pidGone reports whether pid no longer runs: absent from /proc, or a zombie.
func pidGone(pid int) bool {
	st, ok := readStat(pid)
	return !ok || st.gone()
}

// procState describes pid for an observed-state dump.
func procState(pid int) string {
	st, ok := readStat(pid)
	if !ok {
		return "pid " + strconv.Itoa(pid) + ": absent"
	}
	return "pid " + strconv.Itoa(pid) + ": state " + string(st.state)
}

// waitPidGone waits (pollBudget) until pid is gone, a zombie counting as gone.
func waitPidGone(t testing.TB, pid int) {
	t.Helper()
	waitFor(t, "pid "+strconv.Itoa(pid)+" gone", func() bool { return pidGone(pid) }, func() string { return procState(pid) })
}

// syscallKill sends SIGKILL to pid.
func syscallKill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// procEnviron returns a live process's environment entries.
func procEnviron(t testing.TB, pid int) []string {
	t.Helper()
	return procNulList(t, pid, "environ")
}

// procCmdline returns a live process's argv.
func procCmdline(t testing.TB, pid int) []string {
	t.Helper()
	return procNulList(t, pid, "cmdline")
}

// procNulList reads a NUL-separated /proc/<pid>/<file>.
func procNulList(t testing.TB, pid int, file string) []string {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/" + file)
	if err != nil {
		t.Fatalf("read /proc/%d/%s: %v", pid, file, err)
	}
	s := strings.TrimSuffix(string(data), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// envValue returns the value of key in environ entries, and whether it is set.
func envValue(environ []string, key string) (string, bool) {
	for _, kv := range environ {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v, true
		}
	}
	return "", false
}
