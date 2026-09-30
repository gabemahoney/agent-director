//go:build !linux && !darwin

package probe

import "testing"

// TestUnsupportedCheckerAlwaysUnknown pins the fail-open contract on an
// unsupported OS: CheckLiveness returns VerdictUnknown for every input — no OS to
// evidence liveness OR death, so the row is skipped, never marked missing.
func TestUnsupportedCheckerAlwaysUnknown(t *testing.T) {
	c := unsupportedChecker{}
	cases := []struct {
		name      string
		pid       int
		starttime string
		id        string
	}{
		{"zero_values", 0, "", ""},
		{"populated_identity", 4242, "12345678", "inst-xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.CheckLiveness(tc.pid, tc.starttime, tc.id); got != VerdictUnknown {
				t.Errorf("verdict = %v; want unknown", got)
			}
		})
	}
}

// TestNewCheckerUnsupportedType confirms the unsupported-OS factory returns the
// fail-open unsupportedChecker.
func TestNewCheckerUnsupportedType(t *testing.T) {
	if _, ok := NewChecker().(unsupportedChecker); !ok {
		t.Errorf("NewChecker() = %T; want unsupportedChecker on an unsupported OS", NewChecker())
	}
}

// TestUnsupportedProcCheckerAlwaysUnreadable pins the start-time reader on an
// unsupported OS: unreadable (known false) for every pid, never gone.
func TestUnsupportedProcCheckerAlwaysUnreadable(t *testing.T) {
	cases := []struct {
		name string
		pid  int
	}{
		{"zero_pid", 0},
		{"populated_pid", 4242},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, alive, known := unsupportedProcChecker{}.StartTime(tc.pid)
			if start != "" || alive || known {
				t.Errorf("StartTime(%d) = (%q, %v, %v); want (\"\", false, false)", tc.pid, start, alive, known)
			}
		})
	}
}

// TestNewProcCheckerUnsupportedType confirms the unsupported-OS factory returns
// the always-unreadable unsupportedProcChecker.
func TestNewProcCheckerUnsupportedType(t *testing.T) {
	if _, ok := NewProcChecker().(unsupportedProcChecker); !ok {
		t.Errorf("NewProcChecker() = %T; want unsupportedProcChecker on an unsupported OS", NewProcChecker())
	}
}
