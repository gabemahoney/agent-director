package errnames_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// TestClassifyKnown verifies that Classify correctly identifies every
// entry in Catalog when the sentinel is wrapped with %w.
func TestClassifyKnown(t *testing.T) {
	for _, entry := range errnames.Catalog {
		entry := entry // capture loop var
		t.Run(entry.Name, func(t *testing.T) {
			wrapped := fmt.Errorf("%w: some context", entry.Err)
			name, desc := errnames.Classify(wrapped)
			if name != entry.Name {
				t.Errorf("Classify(%q wrapped): name = %q, want %q", entry.Name, name, entry.Name)
			}
			if desc != wrapped.Error() {
				t.Errorf("Classify(%q wrapped): description = %q, want %q", entry.Name, desc, wrapped.Error())
			}
		})
	}
}

// TestClassifyUnknown verifies that an unrecognized error collapses to
// "ErrInternal" with the original error message as description.
func TestClassifyUnknown(t *testing.T) {
	err := errors.New("something completely unexpected")
	name, desc := errnames.Classify(err)
	if name != "ErrInternal" {
		t.Errorf("Classify(unknown): name = %q, want %q", name, "ErrInternal")
	}
	if desc != err.Error() {
		t.Errorf("Classify(unknown): description = %q, want %q", desc, err.Error())
	}
}

// TestClassifyNil verifies that Classify(nil) returns empty strings.
func TestClassifyNil(t *testing.T) {
	name, desc := errnames.Classify(nil)
	if name != "" || desc != "" {
		t.Errorf("Classify(nil) = (%q, %q), want (\"\", \"\")", name, desc)
	}
}

// TestTrimNamePrefix is table-driven, covering the four documented cases.
func TestTrimNamePrefix(t *testing.T) {
	cases := []struct {
		desc        string
		name        string
		description string
		want        string
	}{
		{
			desc:        "with prefix",
			name:        "ErrCwdMissing",
			description: "ErrCwdMissing: cwd is required",
			want:        "cwd is required",
		},
		{
			desc:        "without prefix",
			name:        "ErrCwdMissing",
			description: "cwd is required",
			want:        "cwd is required",
		},
		{
			desc:        "prefix only (no remainder)",
			name:        "ErrFoo",
			description: "ErrFoo:",
			want:        "",
		},
		{
			desc:        "empty description",
			name:        "ErrFoo",
			description: "",
			want:        "",
		},
		{
			desc:        "prefix with leading space in remainder",
			name:        "ErrCwdNotAPath",
			description: "ErrCwdNotAPath:   extra spaces",
			want:        "extra spaces",
		},
		{
			desc:        "different name does not strip",
			name:        "ErrFoo",
			description: "ErrBar: some message",
			want:        "ErrBar: some message",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			got := errnames.TrimNamePrefix(tc.name, tc.description)
			if got != tc.want {
				t.Errorf("TrimNamePrefix(%q, %q) = %q, want %q", tc.name, tc.description, got, tc.want)
			}
		})
	}
}

// TestCatalogNonEmpty ensures the Catalog has at least one entry
// (guards against an accidental empty initialization).
func TestCatalogNonEmpty(t *testing.T) {
	if len(errnames.Catalog) == 0 {
		t.Fatal("errnames.Catalog is empty")
	}
}
