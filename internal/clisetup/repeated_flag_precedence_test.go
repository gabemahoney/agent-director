package clisetup_test

import (
	"flag"
	"io"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// b.51h, CR-1: the read-only globals scan must leave mixed-error verb
// arguments to their real parser, preserving its help/unknown/value result.
func TestB51hVerbScanPreservesParsePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"help", []string{"--directory", "/x", "--directory", "/y", "--help"}, flag.ErrHelp.Error()},
		{"old-name", []string{"--instance-id", "A", "--instance-id", "B", "--claude-instance-id", "C"}, "flag provided but not defined: -claude-instance-id"},
		{"invalid-int", []string{"--busy-timeout-ms=bad", "--directory", "/x", "--directory", "/y"}, `invalid value "bad" for flag -busy-timeout-ms: parse error`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := precedenceFlagSet()
			g, out, err := clisetup.ParseVerbGlobalFlags(fs, clisetup.GlobalFlags{}, tc.args, func(tail []string) error {
				return fs.Parse(tail) // disposable values, as in production
			})
			if err != nil || g != (clisetup.GlobalFlags{}) || !slices.Equal(out, tc.args) {
				t.Fatalf("scan: globals=%+v argv=%q err=%v; want untouched verb args and no scan error", g, out, err)
			}
			real := precedenceFlagSet()
			if err := real.Parse(out); err == nil || err.Error() != tc.want {
				t.Errorf("parse error=%v; want %q", err, tc.want)
			}
		})
	}
}

func precedenceFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("verb", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("directory", "", "directory")
	fs.String("instance-id", "", "id")
	fs.Int("busy-timeout-ms", 10000, "timeout")
	return fs
}
