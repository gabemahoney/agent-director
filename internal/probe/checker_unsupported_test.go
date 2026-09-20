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
