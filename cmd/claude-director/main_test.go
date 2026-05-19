package main_test

import (
	"testing"
)

func TestHelpVerbExitsZeroAndWritesStdout(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Errorf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Errorf("stdout empty; want some help output")
	}
}

func TestHelpFlagAndHelpVerbProduceIdenticalOutput(t *testing.T) {
	// Both invocations need the same HOME so the underlying state.db is
	// in the same place — only the verb differs, stdout shouldn't.
	home := t.TempDir()
	stdoutVerb, _, codeVerb := runCLIWithHome(t, home, "help")
	stdoutFlag, _, codeFlag := runCLIWithHome(t, home, "--help")
	if codeVerb != 0 || codeFlag != 0 {
		t.Fatalf("expected exit 0 for both: verb=%d flag=%d", codeVerb, codeFlag)
	}
	if stdoutVerb != stdoutFlag {
		t.Errorf("help and --help produced different output:\nhelp=%q\n--help=%q",
			stdoutVerb, stdoutFlag)
	}
}

func TestNoArgsRoutesToHelpAndExitsZero(t *testing.T) {
	home := t.TempDir()
	stdout, _, code := runCLIWithHome(t, home)
	if code != 0 {
		t.Errorf("exit=%d want 0", code)
	}
	helpStdout, _, _ := runCLIWithHome(t, home, "help")
	if stdout != helpStdout {
		t.Errorf("no-args output differs from help:\nno-args=%q\nhelp=%q",
			stdout, helpStdout)
	}
}

func TestUnknownVerbWritesErrorEnvelope(t *testing.T) {
	cases := []struct {
		name string
		verb string
	}{
		{name: "bogusverb", verb: "bogusverb"},
		{name: "nope", verb: "nope"},
		{name: "nonexistent", verb: "nonexistent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, tc.verb)
			if code == 0 {
				t.Errorf("exit=0 want non-zero")
			}
			if stdout != "" {
				t.Errorf("stdout=%q want empty", stdout)
			}
			env := parseEnvelope(t, stderr)
			if env.ErrName == "" {
				t.Errorf("err_name empty in %q", stderr)
			}
			if env.ErrDescription == "" {
				t.Errorf("err_description empty in %q", stderr)
			}
		})
	}
}
