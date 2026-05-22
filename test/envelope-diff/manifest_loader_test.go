package envelope_diff

import (
	"strings"
	"testing"
)

func TestLoadManifestValid(t *testing.T) {
	data := []byte(`{"spawn":[".claude_instance_id"]}`)
	m, err := loadNonDetManifestFromBytes(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.HasVerb("spawn") {
		t.Error("HasVerb(spawn) = false, want true")
	}
	sels, err := m.Selectors("spawn")
	if err != nil {
		t.Fatalf("Selectors(spawn): %v", err)
	}
	if len(sels) != 1 || sels[0] != ".claude_instance_id" {
		t.Errorf("Selectors(spawn): got %v, want [.claude_instance_id]", sels)
	}
}

func TestLoadManifestEmpty(t *testing.T) {
	m, err := loadNonDetManifestFromBytes([]byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty manifest, got %v", m)
	}
}

func TestLoadManifestMalformedFailsLoud(t *testing.T) {
	_, err := loadNonDetManifestFromBytes([]byte(`{ not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "nondeterministic.json") {
		t.Errorf("error should mention nondeterministic.json; got: %v", err)
	}
}

func TestManifestHasVerb(t *testing.T) {
	m, err := loadNonDetManifestFromBytes([]byte(`{"spawn":[],"version":["commit"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cases := []struct {
		verb string
		want bool
	}{
		{"spawn", true},
		{"version", true},
		{"unknown", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.verb+"_"+boolStr(tc.want), func(t *testing.T) {
			got := m.HasVerb(tc.verb)
			if got != tc.want {
				t.Errorf("HasVerb(%q) = %v, want %v", tc.verb, got, tc.want)
			}
		})
	}
}

func TestManifestSelectors(t *testing.T) {
	m, err := loadNonDetManifestFromBytes([]byte(`{"spawn":["a","b"],"expire":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("known verb with selectors", func(t *testing.T) {
		sels, err := m.Selectors("spawn")
		if err != nil {
			t.Fatalf("Selectors(spawn): %v", err)
		}
		if len(sels) != 2 || sels[0] != "a" || sels[1] != "b" {
			t.Errorf("got %v, want [a b]", sels)
		}
	})

	t.Run("known verb with empty slice", func(t *testing.T) {
		sels, err := m.Selectors("expire")
		if err != nil {
			t.Fatalf("Selectors(expire): %v", err)
		}
		if len(sels) != 0 {
			t.Errorf("expected empty slice, got %v", sels)
		}
	})

	t.Run("unknown verb returns error", func(t *testing.T) {
		_, err := m.Selectors("unknown")
		if err == nil {
			t.Fatal("expected error for unknown verb, got nil")
		}
	})
}

// boolStr converts a bool to a short string for use in subtest names.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
