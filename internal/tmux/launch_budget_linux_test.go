//go:build linux

package tmux

import (
	"testing"

	"golang.org/x/sys/unix"
)

// b.xhh: serial, because RLIMIT_STACK belongs to the process. Change only the
// soft limit and restore it after every case, before any parallel tests resume.
func TestLaunchSizeLinuxBudgetFormula(t *testing.T) {
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &original); err != nil {
		t.Fatal(err)
	}
	t.Logf("original RLIMIT_STACK: soft=%d hard=%d", original.Cur, original.Max)
	for _, tc := range []struct {
		name       string
		soft, want uint64
	}{
		{"below floor", 256 * 1024, 128 * 1024},
		{"stack divided by four", 4*1024*1024 + 3, 1024 * 1024},
		{"above ceiling", 32 * 1024 * 1024, 6 * 1024 * 1024},
		{"infinity", unix.RLIM_INFINITY, 6 * 1024 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if original.Max != unix.RLIM_INFINITY && tc.soft > original.Max {
				t.Skipf("soft limit %d exceeds hard limit %d", tc.soft, original.Max)
			}
			t.Cleanup(func() {
				if err := unix.Setrlimit(unix.RLIMIT_STACK, &original); err != nil {
					t.Errorf("restore stack limit: %v", err)
					return
				}
				var restored unix.Rlimit
				if err := unix.Getrlimit(unix.RLIMIT_STACK, &restored); err != nil {
					t.Errorf("read restored stack limit: %v", err)
				} else if restored != original {
					t.Errorf("restored stack limit = %+v, want %+v", restored, original)
				}
			})
			limited := original
			limited.Cur = tc.soft
			if err := unix.Setrlimit(unix.RLIMIT_STACK, &limited); err != nil {
				t.Fatalf("set soft stack limit to %d: %v", tc.soft, err)
			}
			got, err := runtimeLaunchBudget()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("runtime budget for soft stack %d = %d, want %d", tc.soft, got, tc.want)
			}
		})
	}
}
