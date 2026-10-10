package main

import (
	"flag"
	"io"
	"testing"
)

// TestDecimalIntFlag: a newDecimalInt flag keeps its default when absent and
// reads base 10 only ("010" is 10); a hex, octal or binary prefix, an
// underscore, a leading + or an out-of-range value fails fs.Parse, which every
// verb reports as ErrInvalidFlags (b.c4n). A leading - parses; the verb refuses it.
func TestDecimalIntFlag(t *testing.T) {
	parse := func(args ...string) (int, error) {
		var n int
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Var(newDecimalInt(&n, 25), "n", "count")
		err := fs.Parse(args)
		return n, err
	}
	if n, err := parse(); n != 25 || err != nil {
		t.Errorf("absent flag = %d, %v; want the default 25", n, err)
	}
	const notDecimal, plus, rng = "not a base-10 integer", "leading + not allowed", "value out of range"
	for _, tc := range []struct {
		in, err string // err: Set's refusal; "" when the value parses
		want    int
	}{
		{"10", "", 10},
		{"010", "", 10},
		{"0", "", 0},
		{"-3", "", -3},
		{"0x10", notDecimal, 0},
		{"0X10", notDecimal, 0},
		{"0o7", notDecimal, 0},
		{"0b1", notDecimal, 0},
		{"1_000", notDecimal, 0},
		{"1e3", notDecimal, 0},
		{" 5", notDecimal, 0},
		{"", notDecimal, 0},
		{"+5", plus, 0},
		{"+0x10", plus, 0},
		{"9223372036854775808", rng, 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			n, err := parse("-n", tc.in)
			if tc.err == "" {
				if err != nil || n != tc.want {
					t.Errorf("-n %q = %d, %v; want %d", tc.in, n, err, tc.want)
				}
				return
			}
			want := `invalid value "` + tc.in + `" for flag -n: ` + tc.err
			if err == nil || err.Error() != want {
				t.Errorf("-n %q = %d, %v; want the error %q", tc.in, n, err, want)
			}
		})
	}
}
