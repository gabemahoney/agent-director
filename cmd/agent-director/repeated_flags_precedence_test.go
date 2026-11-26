package main_test

import (
	"strings"
	"testing"
)

// b.51h, CR-1: detecting a duplicate before opening the client must preserve
// the real parser's help, old-name and malformed-value diagnostics.
func TestB51hRepeatedFlagErrorPrecedence(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"ordinary-help", []string{"get", "--instance-id", "A", "--instance-id", "B", "--help"}, "flag: help requested"},
		{"old-name", []string{"get", "--instance-id", "A", "--instance-id", "B", "--claude-instance-id", "C"}, oldIDRefusal},
		{"malformed-first", []string{"list", "--limit=bad", "--parent", "A", "--parent", "B"}, `invalid value "bad" for flag -limit: not a base-10 integer`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runSpawnCLI(t, t.TempDir(), fakeDir, tc.args...)
			if got := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; got != tc.want {
				t.Errorf("description=%q; want %q", got, tc.want)
			}
		})
	}
}

// b.51h, CR-1/CR-2: cursor-host keeps flag's plain-text help and unknown-flag
// output, including help requested after repeated flags.
func TestB51hCursorHostParseDiagnostics(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name   string
		args   []string
		prefix string
	}{
		{"help", []string{"--help"}, "Usage of cursor-host:\n"},
		{"repeat-then-help", []string{"--runtime", "/x", "--runtime", "/y", "--help"}, "Usage of cursor-host:\n"},
		{"unknown", []string{"--unknown"}, "flag provided but not defined: -unknown\nUsage of cursor-host:\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runSpawnCLI(t, t.TempDir(), fakeDir, append([]string{"cursor-host"}, tc.args...)...)
			if code != 1 || stdout != "" || !strings.HasPrefix(stderr, tc.prefix) {
				t.Fatalf("cursor-host: exit=%d stdout=%q stderr=%q; want exit 1, empty stdout and plain diagnostic prefix %q", code, stdout, stderr, tc.prefix)
			}
			for _, name := range []string{"runtime", "runtime-args", "initial-prompt", "load-session", "requested-mode"} {
				if !strings.Contains(stderr, "\n  -"+name+" string\n") {
					t.Errorf("usage lacks --%s: %q", name, stderr)
				}
			}
		})
	}
}

// b.51h: preserving cursor-host's older parse errors must not restore
// last-wins for a successfully parsed repeated scalar.
func TestB51hCursorHostRepeatedFlagStillRefused(t *testing.T) {
	stdout, stderr, code := runSpawnCLI(t, t.TempDir(), buildFakeTmux(t), "cursor-host", "--runtime", "/x", "--runtime", "/y")
	if got := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; got != "flag --runtime may only be specified once" {
		t.Errorf("description=%q; want the name-only runtime repeat refusal", got)
	}
}
