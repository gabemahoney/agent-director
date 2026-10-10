package main_test

import (
	"strings"
	"testing"
)

// TestCLIIntFlagRefusals: every int flag refuses a value MCP's JSON integers and
// the TS client's String(n) cannot send with ErrInvalidFlags, and list and
// read-pane refuse a negative count, writing nothing (b.c4n). The decimal
// parse is flags_test.go's; trail-emit's negative bytes are H7's.
func TestCLIIntFlagRefusals(t *testing.T) {
	relay := []string{"trail-emit", "relay-attempt", "--token", "tok-c4n", "--endpoint", "http://127.0.0.1:9/r",
		"--outcome", "200", "--instance-id", "inst-c4n"}
	flags := []struct {
		argv     []string
		flag     string
		negative string // the refusal of -1; "" leaves -1 to H7
	}{
		{[]string{"list"}, "limit", "limit = -1 is negative"},
		{[]string{"read-pane", "--claude-instance-id", "no-such-row"}, "n-lines", "n_lines = -1 is negative"},
		{relay, "bytes-sent", ""},
		{relay, "bytes-received", ""},
	}
	const notDecimal = "not a base-10 integer"
	values := []struct{ in, why string }{
		{"0x10", notDecimal}, {"0o7", notDecimal}, {"0b1", notDecimal}, {"1_000", notDecimal},
		{"+5", "leading + not allowed"}, {"-1", ""},
	}
	for _, f := range flags {
		for _, v := range values {
			want := `invalid value "` + v.in + `" for flag -` + f.flag + ": " + v.why
			if v.in == "-1" {
				if want = f.negative; want == "" {
					continue
				}
			}
			t.Run(f.flag+" "+v.in, func(t *testing.T) {
				home := t.TempDir()
				stdout, stderr, code := runCLIWithHome(t, home, append(append([]string{}, f.argv...), "--"+f.flag, v.in)...)
				if desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; !strings.Contains(desc, want) {
					t.Errorf("description %q lacks %q", desc, want)
				}
				if n := len(relayAttemptLines(trailOrNil(t, home))); n != 0 {
					t.Errorf("ad.relay_attempt.completed lines = %d; want none", n)
				}
			})
		}
	}
}
