package manifest_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api/manifest"
)

// TestVerbsHasExactlyOneEntry asserts the manifest is bootstrap-sized. When
// Epic 2+ workers add their first verb this will fail — that's the intended
// signal to update this expectation and the rest of the test surface.
func TestVerbsHasExactlyOneEntry(t *testing.T) {
	if got, want := len(manifest.Verbs), 1; got != want {
		t.Fatalf("len(manifest.Verbs) = %d, want %d", got, want)
	}
	if got, want := manifest.Verbs[0].Name, "help"; got != want {
		t.Fatalf("manifest.Verbs[0].Name = %q, want %q", got, want)
	}
}

// TestLookup covers the hit and miss paths of Lookup against the real
// registry. No hand-constructed entries.
func TestLookup(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		v, ok := manifest.Lookup("help")
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", "help")
		}
		if v.Name != "help" {
			t.Fatalf("Lookup(%q).Name = %q, want %q", "help", v.Name, "help")
		}
	})
	t.Run("miss", func(t *testing.T) {
		v, ok := manifest.Lookup("nonexistent")
		if ok {
			t.Fatalf("Lookup(%q) ok = true, want false", "nonexistent")
		}
		if !reflect.DeepEqual(v, manifest.VerbDef{}) {
			t.Fatalf("Lookup miss returned non-zero VerbDef: %+v", v)
		}
	})
}

// TestHelpVerbRequiredFields is a table-driven check that the help entry
// carries every field downstream consumers (CLI dispatch, MCP schema, doc
// generator) expect to be populated.
func TestHelpVerbRequiredFields(t *testing.T) {
	v, ok := manifest.Lookup("help")
	if !ok {
		t.Fatalf("Lookup(%q) ok = false, want true", "help")
	}
	cases := []struct {
		field   string
		nonZero bool
	}{
		{"Name", v.Name != ""},
		{"Description", v.Description != ""},
		{"ResultFields", len(v.ResultFields) > 0},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			if !c.nonZero {
				t.Fatalf("help.%s is empty/zero; want populated", c.field)
			}
		})
	}
}

// TestHelpErrorNamesEmptyNonNil enforces the JSON-stability invariant: help
// has no error conditions, so ErrorNames must marshal as [] not null.
func TestHelpErrorNamesEmptyNonNil(t *testing.T) {
	v, ok := manifest.Lookup("help")
	if !ok {
		t.Fatalf("Lookup(%q) ok = false, want true", "help")
	}
	if v.ErrorNames == nil {
		t.Fatalf("help.ErrorNames is nil; want empty non-nil slice")
	}
	if len(v.ErrorNames) != 0 {
		t.Fatalf("len(help.ErrorNames) = %d, want 0", len(v.ErrorNames))
	}
}
