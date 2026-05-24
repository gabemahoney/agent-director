// Package main — tests for check-doccomments.
//
// These tests exercise the check() function directly against fixture packages
// under testdata/.  The table-driven TestACCases function formally covers the
// four Acceptance Criteria required by ticket t3.qe2.9x.6a.5f:
//
//	(a) Fully-documented package → 0 diagnostics (exit 0).
//	(b) Missing doc on an exported func → diagnostic names the function.
//	(c) Missing doc on an exported struct field → diagnostic names the field.
//	(d) Missing doc on an exported sentinel error var → diagnostic names the var.
package main

import (
	"strings"
	"testing"
)

// acCase describes one acceptance-criterion scenario.
type acCase struct {
	// name is the human-readable label shown on test failure.
	name string
	// dir is the fixture directory passed to check().
	dir string
	// wantZero asserts that check() returns no diagnostics (AC a).
	wantZero bool
	// wantSubstrings lists identifier substrings that MUST appear somewhere in
	// the diagnostics (used for AC b, c, d).
	wantSubstrings []string
	// forbidSubstrings lists identifier substrings that must NOT appear in
	// diagnostics (sanity: documented identifiers must stay clean).
	forbidSubstrings []string
}

// TestACCases is the primary table-driven acceptance test for check().
// Each row maps to one of the four AC cases from ticket t3.qe2.9x.6a.5f.
func TestACCases(t *testing.T) {
	cases := []acCase{
		{
			// AC(a): a package where every exported identifier has a doc comment
			// must produce zero diagnostics so the tool exits 0.
			name:     "AC(a): fully documented package exits 0",
			dir:      "testdata/documented",
			wantZero: true,
		},
		{
			// AC(b): a package with an exported function that has no doc comment
			// must produce a diagnostic that includes the function name.
			name:           "AC(b): missing func doc names the function",
			dir:            "testdata/missing",
			wantSubstrings: []string{"UndocumentedFunc"},
			// Documented items in the same fixture must stay clean.
			forbidSubstrings: []string{"DocumentedFunc", "DocumentedConst", "DocumentedType"},
		},
		{
			// AC(c): a package with an exported struct field that has no doc
			// comment must produce a diagnostic containing "StructType.FieldName".
			name:           "AC(c): missing struct field doc names the field",
			dir:            "testdata/missing",
			wantSubstrings: []string{"DocumentedStructUndocumentedField.UndocumentedField"},
		},
		{
			// AC(d): a package with an exported sentinel error variable that has
			// no doc comment must produce a diagnostic naming the variable.
			name:           "AC(d): missing sentinel error var doc names the variable",
			dir:            "testdata/missing",
			wantSubstrings: []string{"ErrUndocumented"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := check(tc.dir)
			if err != nil {
				t.Fatalf("check(%q): unexpected error: %v", tc.dir, err)
			}

			if tc.wantZero && len(got) != 0 {
				t.Errorf("check(%q): expected 0 diagnostics, got %d:\n%s",
					tc.dir, len(got), strings.Join(got, "\n"))
			}

			output := strings.Join(got, "\n")

			for _, sub := range tc.wantSubstrings {
				if !strings.Contains(output, sub) {
					t.Errorf("check(%q): expected diagnostic containing %q but none found.\nFull output:\n%s",
						tc.dir, sub, output)
				}
			}

			for _, sub := range tc.forbidSubstrings {
				if strings.Contains(output, sub) {
					t.Errorf("check(%q): unexpected diagnostic for documented identifier %q.\nFull output:\n%s",
						tc.dir, sub, output)
				}
			}
		})
	}
}

// TestCheckDocumented verifies that a fully-documented package produces no
// diagnostics. This also checks that all declaration kinds (Const, Var, Func,
// Method, Type, Interface, exported struct fields) are covered by the fixture.
func TestCheckDocumented(t *testing.T) {
	got, err := check("testdata/documented")
	if err != nil {
		t.Fatalf("check(documented): unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("check(documented): expected 0 missing comments, got %d:\n%s",
			len(got), strings.Join(got, "\n"))
	}
}

// TestCheckMissing verifies that a package with intentionally undocumented
// exports produces one diagnostic per missing comment and does not flag the
// documented identifiers.
func TestCheckMissing(t *testing.T) {
	got, err := check("testdata/missing")
	if err != nil {
		t.Fatalf("check(missing): unexpected error: %v", err)
	}

	// Expected diagnostics:
	//   UndocumentedConst, UndocumentedVar, UndocumentedFunc, UndocumentedType
	//   DocumentedStructUndocumentedField.UndocumentedField, ErrUndocumented
	const wantCount = 6
	if len(got) != wantCount {
		t.Errorf("check(missing): expected %d diagnostics, got %d:\n%s",
			wantCount, len(got), strings.Join(got, "\n"))
	}

	// Every undocumented identifier must appear in the output.
	for _, name := range []string{
		"UndocumentedConst",
		"UndocumentedVar",
		"UndocumentedFunc",
		"UndocumentedType",
		"UndocumentedField",  // field name in the struct diagnostic
		"ErrUndocumented",
	} {
		found := false
		for _, line := range got {
			if strings.Contains(line, name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("check(missing): expected diagnostic for %s but none found in:\n%s",
				name, strings.Join(got, "\n"))
		}
	}

	// Documented identifiers must NOT be flagged.
	for _, name := range []string{
		"DocumentedConst",
		"DocumentedFunc",
		"DocumentedType",
	} {
		for _, line := range got {
			if strings.Contains(line, name) {
				t.Errorf("check(missing): unexpected diagnostic for documented identifier %s: %s", name, line)
			}
		}
	}
}

// TestCheckNonexistent verifies that a non-existent directory returns an error
// rather than silently producing zero diagnostics.
func TestCheckNonexistent(t *testing.T) {
	_, err := check("testdata/does-not-exist")
	if err == nil {
		t.Error("check(nonexistent): expected error, got nil")
	}
}
