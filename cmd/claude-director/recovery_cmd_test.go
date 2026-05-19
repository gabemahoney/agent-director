package main

import (
	"testing"
	"time"
)

// TestParseDaysOrDuration pins the SRD §11 retention-flag matrix: trailing-d
// days, Go-duration forms, malformed strings, and (critically) negative
// durations — which the store would otherwise interpret as "delete every
// terminal row", a footgun if --older-than took a typo'd negative value.
func TestParseDaysOrDuration(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"days_seven", "7d", 7 * 24 * time.Hour, false},
		{"days_zero", "0d", 0, false},
		{"days_one", "1d", 24 * time.Hour, false},
		{"hours", "12h", 12 * time.Hour, false},
		{"minutes", "30m", 30 * time.Minute, false},
		{"seconds", "45s", 45 * time.Second, false},
		{"compound", "1h30m", 90 * time.Minute, false},
		{"negative_hours_rejected", "-2h", 0, true},
		{"negative_compound_rejected", "-1h30m", 0, true},
		{"days_non_digit", "7.5d", 0, true},
		{"days_with_negative_prefix", "-7d", 0, true},
		{"empty", "", 0, true},
		{"bare_letter", "d", 0, true},
		{"unknown_suffix", "5x", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDaysOrDuration(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDaysOrDuration(%q) = %v, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDaysOrDuration(%q) unexpected err: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseDaysOrDuration(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}
