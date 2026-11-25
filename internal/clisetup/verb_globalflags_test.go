package clisetup_test

import (
	"flag"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// b.t1b: prefix parsing never examines any word after the verb.
func TestParseGlobalFlagsStopsAtVerb(t *testing.T) {
	for _, tail := range [][]string{
		{"get", "--instance-id", "--store-path=/x"},
		{"list", "--home", "/x"},
		{"get-many", "--home", "/x", "--"},
		{"--", "--home", "/x"},
		{"--unknown", "--home", "/x"},
	} {
		argv := append([]string{"--home", "/prefix"}, tail...)
		g, out, err := clisetup.ParseGlobalFlags(argv)
		if err != nil || g.Home != "/prefix" || !g.HomeSet || !slices.Equal(out, tail) {
			t.Errorf("%q: flags=%+v argv=%q err=%v", argv, g, out, err)
		}
	}
}

// b.t1b: flag ownership, rather than the text of a value, controls extraction.
func TestParseVerbGlobalFlagsOwnership(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		out  []string
		home string
	}{
		{"own two-token", []string{"--home", "/h"}, []string{}, "/h"},
		{"own equals", []string{"--home=/h"}, []string{}, "/h"},
		{"after native value", []string{"--cwd", "/w", "--home", "/h"}, []string{"--cwd", "/w"}, "/h"},
		{"native equals", []string{"--text=--home", "--home=/h"}, []string{"--text=--home"}, "/h"},
		{"bare boolean", []string{"--switch", "--home", "/h"}, []string{"--switch"}, "/h"},
		{"equals boolean", []string{"--switch=false", "--home=/h"}, []string{"--switch=false"}, "/h"},
		{"custom boolean", []string{"--optional", "--home=/h"}, []string{"--optional"}, "/h"},
		{"single-dash native", []string{"-text", "--home", "--home=/h"}, []string{"-text", "--home"}, "/h"},
		{"separator value", []string{"--cwd", "--", "--home=/h"}, []string{"--cwd", "--"}, "/h"},
		{"separator", []string{"--cwd", "/w", "--", "--home", "/x"}, nil, ""},
		{"separator equals", []string{"--", "--store-path=/x"}, nil, ""},
		{"id owns global", []string{"--instance-id", "--store-path=/x"}, nil, ""},
		{"text owns global", []string{"--text", "--tmux-command", "/x"}, nil, ""},
		{"text owns boolean global", []string{"--text", "--create-if-missing=false"}, nil, ""},
		{"text owns missing global", []string{"--text", "--home"}, nil, ""},
		{"operand", []string{"stray", "--home", "/x"}, nil, ""},
		{"boolean leaves operand", []string{"--switch", "false", "--home", "/x"}, nil, ""},
		{"unknown flag", []string{"--unknown", "--home", "/x"}, nil, ""},
		{"unknown equals flag", []string{"--unknown=x", "--home=/x"}, nil, ""},
		{"malformed flag", []string{"---text", "--home=/x"}, nil, ""},
		{"malformed name", []string{"-=text", "--home=/x"}, nil, ""},
		{"single-dash global is unknown", []string{"-home", "/x", "--home=/x"}, nil, ""},
		{"dash operand", []string{"-", "--home=/x"}, nil, ""},
		{"empty operand", []string{"", "--home=/x"}, nil, ""},
		{"missing native value", []string{"--text"}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("verb", flag.ContinueOnError)
			fs.String("cwd", "", "cwd")
			fs.String("instance-id", "", "id")
			text := fs.String("text", "unchanged", "text")
			fs.Bool("switch", false, "boolean")
			fs.Var(scanOnlyBool{}, "optional", "custom boolean")
			before := slices.Clone(tc.argv)
			g, out, err := clisetup.ParseVerbGlobalFlags(fs, clisetup.GlobalFlags{}, tc.argv)
			want := clisetup.GlobalFlags{}
			if tc.home != "" {
				want.Home, want.HomeSet = tc.home, true
			}
			wantArgv := tc.out
			if wantArgv == nil {
				wantArgv = tc.argv
			}
			if err != nil || g != want || !slices.Equal(out, wantArgv) {
				t.Errorf("flags=%+v argv=%q err=%v; want %+v, %q", g, out, err, want, wantArgv)
			}
			if !slices.Equal(tc.argv, before) || fs.Parsed() || fs.NFlag() != 0 || *text != "unchanged" {
				t.Fatal("scanner mutated argv or parsed/set a verb flag")
			}
		})
	}
}

// Set must never run during a read-only scan, including for a custom boolean.
type scanOnlyBool struct{}

func (scanOnlyBool) IsBoolFlag() bool { return true }
func (scanOnlyBool) String() string   { return "false" }
func (scanOnlyBool) Set(string) error { panic("global scanner called Set") }

// Exercise both stages for the global value/validation tables. This test
// grammar includes only the native flags those tables use; CLI tests use
// the binaries' actual builders.
func parseGlobalFlagStages(argv []string) (clisetup.GlobalFlags, []string, error) {
	g, rest, err := clisetup.ParseGlobalFlags(argv)
	if err != nil || len(rest) == 0 {
		return g, rest, err
	}
	fs := flag.NewFlagSet(rest[0], flag.ContinueOnError)
	for _, name := range []string{"instance-id", "cwd", "label"} {
		fs.String(name, "", name)
	}
	g, tail, err := clisetup.ParseVerbGlobalFlags(fs, g, rest[1:])
	if err != nil {
		return g, nil, err
	}
	return g, append([]string{rest[0]}, tail...), nil
}

// b.t1b: suffix values merge with the prefix without clearing Set sentinels;
// invalid earlier copies must still fail even when a later copy is valid.
func TestGlobalFlagStagesMerge(t *testing.T) {
	g, out, err := parseGlobalFlagStages([]string{"--create-if-missing=true", "--store-path", "/s", "list", "--home=/h", "--create-if-missing", "false"})
	want := clisetup.GlobalFlags{CreateIfMissingSet: true, StorePath: "/s", StorePathSet: true, Home: "/h", HomeSet: true}
	if err != nil || !reflect.DeepEqual(g, want) || !slices.Equal(out, []string{"list"}) {
		t.Fatalf("flags=%+v argv=%q err=%v; want %+v and [list]", g, out, err, want)
	}
	g, _, err = parseGlobalFlagStages([]string{"--create-if-missing=true", "list", "--home=/h"})
	if err != nil || !g.CreateIfMissingSet || !g.CreateIfMissing {
		t.Fatalf("suffix cleared prefix boolean: %+v, %v", g, err)
	}
	_, _, err = parseGlobalFlagStages([]string{"list", "--create-if-missing=no", "--create-if-missing=false"})
	if err == nil {
		t.Fatal("invalid earlier suffix value was ignored")
	}
}
