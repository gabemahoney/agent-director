package clisetup_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// TestParseGlobalFlags covers the three global flags (b.32k), which
// agent-director and agent-director-admin share (b.vqr): each value lands on
// its field with its Set sentinel true, in either form and anywhere in argv,
// and the flag tokens are stripped from the argv the verb dispatch sees.
func TestParseGlobalFlags(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		want     clisetup.GlobalFlags
		wantArgv []string
	}{
		{"empty argv", []string{}, clisetup.GlobalFlags{}, []string{}},
		{"store-path two-token form", []string{"--store-path", "/tmp/foo.db", "version"},
			clisetup.GlobalFlags{StorePath: "/tmp/foo.db", StorePathSet: true}, []string{"version"}},
		{"store-path equals form", []string{"--store-path=/tmp/foo.db", "version"},
			clisetup.GlobalFlags{StorePath: "/tmp/foo.db", StorePathSet: true}, []string{"version"}},
		{"home two-token form", []string{"--home", "/tmp/h", "help"},
			clisetup.GlobalFlags{Home: "/tmp/h", HomeSet: true}, []string{"help"}},
		{"home equals form", []string{"--home=/tmp/h", "help"},
			clisetup.GlobalFlags{Home: "/tmp/h", HomeSet: true}, []string{"help"}},
		{"tmux-command two-token form", []string{"--tmux-command", "/usr/local/bin/tmux", "spawn", "--cwd", "/x"},
			clisetup.GlobalFlags{TmuxCommand: "/usr/local/bin/tmux", TmuxCommandSet: true}, []string{"spawn", "--cwd", "/x"}},
		{"tmux-command equals form", []string{"--tmux-command=/usr/local/bin/tmux", "spawn", "--cwd", "/x"},
			clisetup.GlobalFlags{TmuxCommand: "/usr/local/bin/tmux", TmuxCommandSet: true}, []string{"spawn", "--cwd", "/x"}},
		{"all three, mixed forms", []string{"--store-path", "/tmp/foo.db", "--home=/tmp/h", "--tmux-command", "/usr/local/bin/tmux", "version"},
			clisetup.GlobalFlags{StorePath: "/tmp/foo.db", StorePathSet: true, Home: "/tmp/h", HomeSet: true,
				TmuxCommand: "/usr/local/bin/tmux", TmuxCommandSet: true}, []string{"version"}},
		{"after the verb and its flags", []string{"delete", "--claude-instance-id", "x", "--home", "~/h"},
			clisetup.GlobalFlags{Home: "~/h", HomeSet: true}, []string{"delete", "--claude-instance-id", "x"}},
		{"other tokens pass through untouched", []string{"spawn", "--cwd", "/x", "--label", "k=v", "--homer=1"},
			clisetup.GlobalFlags{}, []string{"spawn", "--cwd", "/x", "--label", "k=v", "--homer=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, argv, err := clisetup.ParseGlobalFlags(tc.argv)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("flags = %+v; want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(argv, tc.wantArgv) {
				t.Errorf("argv = %q; want %q", argv, tc.wantArgv)
			}
		})
	}
}

// TestParseGlobalFlagsMissingValue: a global flag with no value, at the end
// of argv, as `--flag=` or as `--flag ""` (b.pu2), is an error naming the
// flag, never a silent no-op.
func TestParseGlobalFlagsMissingValue(t *testing.T) {
	cases := []struct {
		argv []string
		flag string
	}{
		{[]string{"--store-path"}, "--store-path"},
		{[]string{"--home"}, "--home"},
		{[]string{"--tmux-command"}, "--tmux-command"},
		{[]string{"spawn", "--store-path"}, "--store-path"},
		{[]string{"--store-path="}, "--store-path"},
		{[]string{"--home="}, "--home"},
		{[]string{"--tmux-command="}, "--tmux-command"},
		{[]string{"--store-path=", "version"}, "--store-path"},
		{[]string{"--store-path", ""}, "--store-path"},
		{[]string{"--home", "", "version"}, "--home"},
		{[]string{"spawn", "--cwd", "/x", "--tmux-command", ""}, "--tmux-command"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.argv, "_"), func(t *testing.T) {
			_, _, err := clisetup.ParseGlobalFlags(tc.argv)
			if err == nil || err.Error() != tc.flag+" requires a value" {
				t.Errorf("argv %q: error = %v; want %q", tc.argv, err, tc.flag+" requires a value")
			}
		})
	}
}

// TestGlobalFlagsApply: Apply sets HOME from --home (a bare "~" or a leading
// "~/" expanded against the HOME before it), then returns --store-path as given
// and --tmux-command with "~" expanded against the new HOME; unset flags leave
// HOME and the overrides alone.
func TestGlobalFlagsApply(t *testing.T) {
	cases := []struct {
		name     string
		flags    clisetup.GlobalFlags
		wantHome string
		want     clisetup.Overrides
	}{
		{"no flags", clisetup.GlobalFlags{}, "/orig", clisetup.Overrides{}},
		{"--home absolute", clisetup.GlobalFlags{Home: "/other", HomeSet: true}, "/other", clisetup.Overrides{}},
		{"--home with a tilde", clisetup.GlobalFlags{Home: "~/sub", HomeSet: true}, "/orig/sub", clisetup.Overrides{}},
		{"--home bare tilde is HOME, not a literal ~ (b.38a)", clisetup.GlobalFlags{Home: "~", HomeSet: true}, "/orig", clisetup.Overrides{}},
		{"--tmux-command bare tilde", clisetup.GlobalFlags{TmuxCommand: "~", TmuxCommandSet: true},
			"/orig", clisetup.Overrides{TmuxCommand: "/orig"}},
		{"--store-path kept as given", clisetup.GlobalFlags{StorePath: "~/s.db", StorePathSet: true},
			"/orig", clisetup.Overrides{StorePath: "~/s.db"}},
		{"--tmux-command expanded against the old HOME", clisetup.GlobalFlags{TmuxCommand: "~/bin/tmux", TmuxCommandSet: true},
			"/orig", clisetup.Overrides{TmuxCommand: "/orig/bin/tmux"}},
		{"--tmux-command expanded against --home", clisetup.GlobalFlags{Home: "/other", HomeSet: true,
			TmuxCommand: "~/bin/tmux", TmuxCommandSet: true, StorePath: "/abs.db", StorePathSet: true},
			"/other", clisetup.Overrides{StorePath: "/abs.db", TmuxCommand: "/other/bin/tmux"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", "/orig")
			got, err := tc.flags.Apply()
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got != tc.want {
				t.Errorf("overrides = %+v; want %+v", got, tc.want)
			}
			if home := os.Getenv("HOME"); home != tc.wantHome {
				t.Errorf("HOME = %q; want %q", home, tc.wantHome)
			}
		})
	}
}

// setHome sets HOME to home for the test, or unsets it; cleanup restores it.
func setHome(t *testing.T, home string, unset bool) {
	t.Helper()
	t.Setenv("HOME", home)
	if unset {
		if err := os.Unsetenv("HOME"); err != nil {
			t.Fatalf("Unsetenv HOME: %v", err)
		}
	}
}

// TestGlobalFlagsApplyWithoutHOME: with HOME empty or unset, a --home of "~"
// or "~/…" is refused and HOME is left as it was, never set to the literal
// value nor to the passwd home (b.38a); other flags apply as before, a
// --tmux-command "~/" passed on unexpanded.
func TestGlobalFlagsApplyWithoutHOME(t *testing.T) {
	refusal := func(home string) string {
		return `--home "` + home + `": HOME is unset or empty, so there is no home directory to expand "~" against`
	}
	cases := []struct {
		name     string
		flags    clisetup.GlobalFlags
		wantErr  string
		wantHome string // HOME after a successful Apply; a refusal leaves HOME as it was
		want     clisetup.Overrides
	}{
		{"--home bare tilde", clisetup.GlobalFlags{Home: "~", HomeSet: true}, refusal("~"), "", clisetup.Overrides{}},
		{"--home tilde path", clisetup.GlobalFlags{Home: "~/x", HomeSet: true}, refusal("~/x"), "", clisetup.Overrides{}},
		{"--home tilde with the other flags", clisetup.GlobalFlags{Home: "~", HomeSet: true,
			StorePath: "/abs.db", StorePathSet: true, TmuxCommand: "/t", TmuxCommandSet: true}, refusal("~"), "", clisetup.Overrides{}},
		{"--home absolute", clisetup.GlobalFlags{Home: "/other", HomeSet: true}, "", "/other", clisetup.Overrides{}},
		{"--tmux-command tilde passed on unexpanded", clisetup.GlobalFlags{TmuxCommand: "~/bin/tmux", TmuxCommandSet: true},
			"", "", clisetup.Overrides{TmuxCommand: "~/bin/tmux"}},
	}
	for _, unset := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/HOME unset=%t", tc.name, unset), func(t *testing.T) {
				setHome(t, "", unset)
				got, err := tc.flags.Apply()
				if tc.wantErr != "" {
					if err == nil || err.Error() != tc.wantErr {
						t.Errorf("Apply error = %v; want %q", err, tc.wantErr)
					}
				} else if err != nil {
					t.Fatalf("Apply: %v", err)
				}
				if got != tc.want {
					t.Errorf("overrides = %+v; want %+v", got, tc.want)
				}
				home, set := os.LookupEnv("HOME")
				if tc.wantHome != "" {
					if home != tc.wantHome {
						t.Errorf("HOME = %q; want %q", home, tc.wantHome)
					}
				} else if home != "" || set == unset {
					t.Errorf("HOME = %q (set %t); want it left as it was (empty, set %t)", home, set, !unset)
				}
			})
		}
	}
}

// TestExpandTilde: a bare "~" or a leading "~/" expands against HOME, and with
// HOME empty or unset comes back unchanged with ok false (b.38a); anything
// else comes back unchanged with ok true.
func TestExpandTilde(t *testing.T) {
	cases := []struct {
		home   string
		unset  bool
		in     string
		want   string
		wantOK bool
	}{
		{"/h", false, "~/x/y", "/h/x/y", true},
		{"/h", false, "~/", "/h/", true},
		{"/h", false, "~", "/h", true},
		{"/h", false, "~user/x", "~user/x", true},
		{"/h", false, "/abs/~/x", "/abs/~/x", true},
		{"/h", false, "rel", "rel", true},
		{"/h", false, "", "", true},
		{"", false, "~", "~", false},
		{"", false, "~/x", "~/x", false},
		{"", true, "~", "~", false},
		{"", true, "~/x", "~/x", false},
		{"", false, "~user/x", "~user/x", true},
		{"", true, "/abs", "/abs", true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("HOME=%q unset=%t/%q", tc.home, tc.unset, tc.in), func(t *testing.T) {
			setHome(t, tc.home, tc.unset)
			if got, ok := clisetup.ExpandTilde(tc.in); got != tc.want || ok != tc.wantOK {
				t.Errorf("ExpandTilde(%q) = %q, %t; want %q, %t", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
