package api_test

import (
	"testing"
	"time"

	api "github.com/gabemahoney/agent-director/pkg/api"
)

// TestParseOlderThan pins the older_than forms the CLI and MCP share (b.hxn):
// trailing-d days and Go durations at or above zero parse; a negative value,
// which Expire would read as "every finished row", and junk are refused.
func TestParseOlderThan(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"7d", 7 * 24 * time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"0d", 0, true},
		{"0s", 0, true},
		{"12h", 12 * time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"1h30m", 90 * time.Minute, true},
		{"-2h", 0, false},
		{"-1s", 0, false},
		{"-1h30m", 0, false},
		{"-7d", 0, false},
		{"-0d", 0, false},
		{"7.5d", 0, false},
		{"7xd", 0, false},
		{"", 0, false},
		{"d", 0, false},
		{"5x", 0, false},
		{"soon", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := api.ParseOlderThan(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ParseOlderThan(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
