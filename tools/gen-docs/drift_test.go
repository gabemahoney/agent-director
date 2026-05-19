package main

import (
	"strings"
	"testing"
)

// TestDriftCheck parameterizes byte-level drift detection: identical inputs
// must return nil; any byte difference must return an error whose message
// names the file path so CI output is actionable.
func TestDriftCheck(t *testing.T) {
	const path = "docs/cli-reference.md"
	cases := []struct {
		name    string
		gen     string
		disk    string
		wantErr bool
	}{
		{name: "identical", gen: "A\nB\n", disk: "A\nB\n", wantErr: false},
		{name: "one_char_diff", gen: "A\n", disk: "B\n", wantErr: true},
		{name: "empty_disk", gen: "A\n", disk: "", wantErr: true},
		{name: "trailing_newline", gen: "A", disk: "A\n", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := driftCheck(path, tc.gen, tc.disk)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not mention path %q", err.Error(), path)
				}
			} else if err != nil {
				t.Fatalf("want nil, got error: %v", err)
			}
		})
	}
}
