package main

import (
	"strings"
	"testing"
)

// fixtureVerbs is the canonical three-verb list used by every test case.
// Tests pass this slice to Check directly so no real manifest is needed.
var fixtureVerbs = []string{"alpha", "bravo", "charlie"}

func TestCheck(t *testing.T) {
	tests := []struct {
		name        string
		verbs       []string
		fixture     string // relative path under testdata/
		wantErr     bool
		errContains string // substring that must appear in the error message
	}{
		{
			name:    "all-aligned: fixture matches verb list exactly",
			verbs:   fixtureVerbs,
			fixture: "testdata/aligned.json",
			wantErr: false,
		},
		{
			name:        "missing-key: fixture omits charlie",
			verbs:       fixtureVerbs,
			fixture:     "testdata/missing.json",
			wantErr:     true,
			errContains: "charlie",
		},
		{
			name:        "extraneous-key: fixture contains delta not in verb list",
			verbs:       fixtureVerbs,
			fixture:     "testdata/extraneous.json",
			wantErr:     true,
			errContains: "delta",
		},
		{
			name:        "malformed JSON: parse error includes byte offset hint",
			verbs:       fixtureVerbs,
			fixture:     "testdata/malformed.json",
			wantErr:     true,
			errContains: "parse",
		},
		{
			name:        "empty file: zero-key object fails with a clear message",
			verbs:       fixtureVerbs,
			fixture:     "testdata/empty.json",
			wantErr:     true,
			errContains: "zero keys",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.verbs, tc.fixture)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && tc.errContains != "" {
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain expected substring %q", err.Error(), tc.errContains)
				}
			}
		})
	}
}
