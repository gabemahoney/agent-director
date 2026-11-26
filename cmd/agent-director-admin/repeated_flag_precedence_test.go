package main_test

import (
	"maps"
	"testing"
)

// b.51h, CR-1: a duplicate must preserve help and existing parse diagnostics,
// including old-name advice, before any config/store access or handler work.
func TestB51hAdminRepeatedFlagPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		help bool
		want string
	}{
		{"snapshot/help", []string{"snapshot-store", "--directory", "/x", "--directory", "/y", "--help"}, true, ""},
		{"kill-finished/old-name", []string{"kill-finished", "--instance-id", "A", "--instance-id", "B", "--claude-instance-id", "C"}, false, oldIDRefusal},
		{"snapshot/invalid-int", []string{"snapshot-store", "--busy-timeout-ms=bad", "--directory", "/x", "--directory", "/y"}, false, `invalid value "bad" for flag -busy-timeout-ms: parse error`},
		{"kill-finished/help", []string{"kill-finished", "--instance-id=A", "-instance-id", "B", "-h"}, true, ""},
		{"migrate/help", []string{"migrate-journal", "--check-only", "--backup-dir", "/x", "--backup-dir=/y", "--help"}, true, ""},
		// This verb has no instance-id flag, so the old name retains the
		// ordinary unknown-flag diagnostic rather than rename advice.
		{"migrate/old-name", []string{"migrate-journal", "--check-only", "--backup-dir", "/x", "--backup-dir=/y", "--claude-instance-id=C"}, false, "flag provided but not defined: -claude-instance-id"},
		{"migrate/invalid-bool", []string{"migrate-journal", "--check-only=bad", "--backup-dir", "/x", "--backup-dir=/y"}, false, `invalid boolean value "bad" for -check-only: parse error`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := poisonedHome(t)
			before := dirEntries(t, home)
			stdout, stderr, code := runAdmin(t, home, tc.args...)
			if tc.help {
				want, helpErr, helpCode := runAdmin(t, home, tc.args[0], "--help")
				if helpCode != 0 || helpErr != "" || want == "" {
					t.Fatalf("plain help: exit=%d stdout=%q stderr=%q", helpCode, want, helpErr)
				}
				if code != 0 || stderr != "" || stdout != want {
					t.Errorf("exit=%d stdout=%q stderr=%q; want successful unchanged verb help %q", code, stdout, stderr, want)
				}
			} else if env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags"); env.ErrDescription != tc.want {
				t.Errorf("description=%q; want %q", env.ErrDescription, tc.want)
			}
			if after := dirEntries(t, home); !maps.Equal(after, before) {
				t.Errorf("parse/help changed HOME config: %v; want %v", after, before)
			}
			assertInvocationKinds(t, home)
		})
	}
}
