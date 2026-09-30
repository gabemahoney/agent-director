//go:build !linux && !darwin

package probe

import "testing"

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
