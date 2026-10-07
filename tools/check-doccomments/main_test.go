// Package main — tests for check-doccomments, run against the fixture
// packages under testdata/. They cover ticket t3.qe2.9x.6a.5f's acceptance
// criteria: (a) a fully documented package gives no diagnostics
// (TestCheckDocumented); (b-d) an undocumented exported func, struct field or
// sentinel error var is named in a diagnostic (TestCheckMissing).
package main

import (
	"strings"
	"testing"
)

// TestCheckDocumented: every declaration kind in the fixture is documented, so
// check reports nothing (AC a).
func TestCheckDocumented(t *testing.T) {
	got, err := check("testdata/documented")
	if err != nil || len(got) != 0 {
		t.Errorf("check(documented) = %q, %v; want no diagnostics", got, err)
	}
}

// TestCheckMissing: one diagnostic per undocumented export, naming it, and
// none for the documented ones (AC b, c, d).
func TestCheckMissing(t *testing.T) {
	got, err := check("testdata/missing")
	if err != nil {
		t.Fatalf("check(missing): %v", err)
	}
	out := strings.Join(got, "\n")
	want := []string{"UndocumentedConst", "UndocumentedVar", "UndocumentedFunc", "UndocumentedType",
		"DocumentedStructUndocumentedField.UndocumentedField", "ErrUndocumented"}
	if len(got) != len(want) {
		t.Errorf("check(missing): %d diagnostics, want %d:\n%s", len(got), len(want), out)
	}
	for _, name := range want {
		if !strings.Contains(out, name) {
			t.Errorf("check(missing): no diagnostic names %s:\n%s", name, out)
		}
	}
	for _, name := range []string{"DocumentedConst", "DocumentedFunc", "DocumentedType"} {
		if strings.Contains(out, name) {
			t.Errorf("check(missing): documented %s flagged:\n%s", name, out)
		}
	}
}

// TestCheckNonexistent: a missing directory is an error, never zero diagnostics.
func TestCheckNonexistent(t *testing.T) {
	if _, err := check("testdata/does-not-exist"); err == nil {
		t.Error("check(nonexistent): want an error, got nil")
	}
}
