package main_test

import (
	"fmt"
	"os"
	"testing"
)

// b.51h, CR-3: invalid repeated int/bool values stop before later help or
// unknown flags, preserving the original diagnostic and doing no work.
func TestB51hMalformedRepeatedValues(t *testing.T) {
	for _, tc := range []struct{ verb, valid, invalid, want string }{
		{"snapshot-store", "--busy-timeout-ms=1", "--busy-timeout-ms=bad", `invalid value "bad" for flag -busy-timeout-ms: parse error`},
		{"migrate-journal", "--check-only", "--check-only=bad", `invalid boolean value "bad" for -check-only: parse error`},
	} {
		for _, goodCopies := range []int{1, 2} {
			for _, suffix := range []string{"", "--help", "--unknown"} {
				t.Run(fmt.Sprintf("%s/good-copies-%d/then-%s", tc.verb, goodCopies, suffix), func(t *testing.T) {
					home := t.TempDir()
					args := []string{tc.verb}
					for i := 0; i < goodCopies; i++ {
						args = append(args, tc.valid)
					}
					args = append(args, tc.invalid)
					if suffix != "" {
						args = append(args, suffix)
					}
					stdout, stderr, code := runAdmin(t, home, args...)
					if got := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; got != tc.want {
						t.Errorf("description=%q; want %q", got, tc.want)
					}
					if entries, err := os.ReadDir(home); err != nil || len(entries) != 1 || entries[0].Name() != "tmux-tmpdir" {
						t.Errorf("parse error wrote under HOME: %v (%v)", entries, err)
					}
				})
			}
		}
	}
}
