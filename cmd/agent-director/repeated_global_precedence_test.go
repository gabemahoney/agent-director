package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b.51h: global validation and repetition precede the verb's own parse,
// while any malformed global beats a global repeat in the complete stage.
func TestB51hGlobalStagePrecedence(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	type testCase struct {
		name string
		args []string
		want string
	}
	var cases []testCase
	for _, verbError := range []struct {
		name, global string
		verb         []string
	}{
		{"help", "store-path", []string{"get", "--help"}},
		{"unknown", "home", []string{"get", "--unknown"}},
		{"old-name", "tmux-command", []string{"get", "--claude-instance-id=C"}},
		{"bad-value", "create-if-missing", []string{"list", "--limit=bad"}},
	} {
		first, second := "{first}", "{second}"
		if verbError.global == "create-if-missing" {
			first, second = "false", "true"
		}
		a, b := []string{"--" + verbError.global, first}, []string{"--" + verbError.global + "=" + second}
		for _, position := range []string{"prefix", "verb", "cross-stage"} {
			var args []string
			switch position {
			case "prefix":
				args = append(append(append([]string{}, a...), b...), verbError.verb...)
			case "verb":
				args = append(append(append([]string{verbError.verb[0]}, a...), b...), verbError.verb[1:]...)
			case "cross-stage":
				args = append(append(append(append([]string{}, a...), verbError.verb[0]), b...), verbError.verb[1:]...)
			}
			cases = append(cases, testCase{verbError.name + "/" + position, args, "flag --" + verbError.global + " may only be specified once"})
		}
	}
	values := []string{"--create-if-missing=true", "--create-if-missing=false", "--create-if-missing=bad"}
	for split := 0; split <= len(values); split++ {
		args := append(append(append([]string{}, values[:split]...), "list"), values[split:]...)
		cases = append(cases, testCase{fmt.Sprintf("malformed-boolean/prefix-copies-%d", split), args, `--create-if-missing must be true or false, got "bad"`})
	}
	cases = append(cases,
		testCase{"empty-after-prefix-repeat", []string{"--home={first}", "--home={second}", "list", "--store-path="}, "--store-path requires a value"},
		testCase{"missing-after-prefix-repeat", []string{"--home={first}", "--home={second}", "list", "--tmux-command"}, "--tmux-command requires a value"},
		testCase{"trail-emit/split-repeat-before-help", []string{"trail-emit", "--home", "{first}", "relay-attempt", "--home", "{second}", "--help"}, "flag --home may only be specified once"},
		testCase{"trail-emit/malformed-after-middle-repeat", []string{"trail-emit", "--home", "{first}", "--home", "{second}", "relay-attempt", "--create-if-missing=bad"}, `--create-if-missing must be true or false, got "bad"`},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, overrides := t.TempDir(), t.TempDir()
			replace := strings.NewReplacer("{first}", filepath.Join(overrides, "first"), "{second}", filepath.Join(overrides, "second"))
			args := make([]string, len(tc.args))
			for i, arg := range tc.args {
				args[i] = replace.Replace(arg)
			}
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			if env.ErrDescription != tc.want {
				t.Errorf("description=%q; want %q", env.ErrDescription, tc.want)
			}
			assertHomeTree(t, home, "tmux-tmpdir")
			if entries, err := os.ReadDir(overrides); err != nil || len(entries) != 0 {
				t.Errorf("global refusal wrote override paths: %v (%v)", entries, err)
			}
		})
	}
}
