package main

import (
	"strings"
	"testing"
)

// TestCheck runs Check over each fixture against a three-verb list, so no
// real manifest is needed; errContains "" means the fixture must pass.
func TestCheck(t *testing.T) {
	verbs := []string{"alpha", "bravo", "charlie"}
	for _, tc := range []struct{ name, fixture, errContains string }{
		{"aligned", "testdata/aligned.json", ""},
		{"missing key", "testdata/missing.json", "charlie"},
		{"extraneous key", "testdata/extraneous.json", "delta"},
		{"malformed JSON", "testdata/malformed.json", "parse"},
		{"zero-key object", "testdata/empty.json", "zero keys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(verbs, tc.fixture)
			if tc.errContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("err = %v; want one containing %q", err, tc.errContains)
			}
		})
	}
}
