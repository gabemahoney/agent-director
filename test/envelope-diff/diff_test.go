package envelope_diff

import (
	"testing"
)

// ── normalize ─────────────────────────────────────────────────────────────────

func TestNormalizeSortsKeys(t *testing.T) {
	input := []byte(`{"b":1,"a":2}`)
	got, err := normalize(input)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := `{"a":2,"b":1}`
	if string(got) != want {
		t.Errorf("sorted keys: got %s, want %s", got, want)
	}
	// Idempotent: normalize(normalize(x)) == normalize(x).
	got2, err := normalize(got)
	if err != nil {
		t.Fatalf("normalize idempotent: %v", err)
	}
	if string(got) != string(got2) {
		t.Errorf("not idempotent: first=%s second=%s", got, got2)
	}
}

func TestNormalizeWhitespace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"spaces around colon", `{ "a" : 1 }`, `{"a":1}`},
		{"newlines", "{\n  \"a\": 1\n}", `{"a":1}`},
		{"tabs", `{"a":	1}`, `{"a":1}`},
		{"pretty-printed", "{\n  \"x\": true,\n  \"y\": null\n}", `{"x":true,"y":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalize([]byte(tc.input))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNormalizeNested(t *testing.T) {
	// Nested object keys are sorted recursively; array element order is preserved.
	input := []byte(`{"z":{"b":2,"a":1},"y":[3,1,2]}`)
	got, err := normalize(input)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := `{"y":[3,1,2],"z":{"a":1,"b":2}}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
	// Idempotent.
	got2, err := normalize(got)
	if err != nil {
		t.Fatalf("normalize idempotent: %v", err)
	}
	if string(got) != string(got2) {
		t.Errorf("not idempotent: first=%s second=%s", got, got2)
	}
}

// ── structuralDiff ────────────────────────────────────────────────────────────

func TestDiffEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"identical scalars", `42`, `42`},
		{"identical objects", `{"a":1,"b":2}`, `{"a":1,"b":2}`},
		{"key order differs", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"identical arrays", `[1,2,3]`, `[1,2,3]`},
		{"null both", `null`, `null`},
		{"nested equal", `{"a":{"b":[1,2,3]}}`, `{"a":{"b":[1,2,3]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := structuralDiff([]byte(tc.a), []byte(tc.b), nil)
			if len(entries) != 0 {
				t.Errorf("expected zero diff entries, got %d: %v", len(entries), entries)
			}
		})
	}
}

func TestDiffScalarMismatch(t *testing.T) {
	entries := structuralDiff([]byte(`{"a":1}`), []byte(`{"a":2}`), nil)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
	}
	e := entries[0]
	if e.Path != ".a" {
		t.Errorf("path: got %q, want %q", e.Path, ".a")
	}
	if e.Want != float64(1) {
		t.Errorf("Want: got %v, want 1", e.Want)
	}
	if e.Got != float64(2) {
		t.Errorf("Got: got %v, want 2", e.Got)
	}
	if e.Kind != "value" {
		t.Errorf("Kind: got %q, want %q", e.Kind, "value")
	}
}

func TestDiffArrayMismatch(t *testing.T) {
	t.Run("value mismatch same length", func(t *testing.T) {
		entries := structuralDiff([]byte(`[1,2,3]`), []byte(`[1,2,4]`), nil)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
		}
		e := entries[0]
		if e.Path != "[2]" {
			t.Errorf("path: got %q, want %q", e.Path, "[2]")
		}
		if e.Want != float64(3) {
			t.Errorf("Want: got %v, want 3", e.Want)
		}
		if e.Got != float64(4) {
			t.Errorf("Got: got %v, want 4", e.Got)
		}
	})

	t.Run("length mismatch cli longer", func(t *testing.T) {
		// CLI has 3 elements, Client has 2 → index [2] is missing on client side.
		entries := structuralDiff([]byte(`[1,2,3]`), []byte(`[1,2]`), nil)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
		}
		e := entries[0]
		if e.Path != "[2]" {
			t.Errorf("path: got %q, want %q", e.Path, "[2]")
		}
		if e.Kind != "missing_client" {
			t.Errorf("Kind: got %q, want %q", e.Kind, "missing_client")
		}
	})
}

func TestDiffNestedMismatch(t *testing.T) {
	a := `{"a":{"b":[{"c":1}]}}`
	b := `{"a":{"b":[{"c":2}]}}`
	entries := structuralDiff([]byte(a), []byte(b), nil)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
	}
	e := entries[0]
	if e.Path != ".a.b[0].c" {
		t.Errorf("path: got %q, want %q", e.Path, ".a.b[0].c")
	}
}

func TestDiffTypeMismatch(t *testing.T) {
	// number vs string at the same key.
	entries := structuralDiff([]byte(`{"a":1}`), []byte(`{"a":"1"}`), nil)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
	}
	e := entries[0]
	if e.Path != ".a" {
		t.Errorf("path: got %q, want %q", e.Path, ".a")
	}
	if e.Kind != "type" {
		t.Errorf("Kind: got %q, want %q", e.Kind, "type")
	}
}

func TestDiffMissingKey(t *testing.T) {
	t.Run("missing_client: key in CLI only", func(t *testing.T) {
		entries := structuralDiff([]byte(`{"a":1,"b":2}`), []byte(`{"a":1}`), nil)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
		}
		e := entries[0]
		if e.Path != ".b" {
			t.Errorf("path: got %q, want %q", e.Path, ".b")
		}
		if e.Kind != "missing_client" {
			t.Errorf("Kind: got %q, want %q", e.Kind, "missing_client")
		}
	})

	t.Run("missing_cli: key in Client only", func(t *testing.T) {
		entries := structuralDiff([]byte(`{"a":1}`), []byte(`{"a":1,"b":2}`), nil)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
		}
		e := entries[0]
		if e.Path != ".b" {
			t.Errorf("path: got %q, want %q", e.Path, ".b")
		}
		if e.Kind != "missing_cli" {
			t.Errorf("Kind: got %q, want %q", e.Kind, "missing_cli")
		}
	})
}

func TestDiffIgnorePaths(t *testing.T) {
	t.Run("bare path suppresses single field", func(t *testing.T) {
		cli := `{"a":1,"timestamp":"2024-01-01T00:00:00Z"}`
		client := `{"a":1,"timestamp":"2024-12-31T23:59:59Z"}`

		entries := structuralDiff([]byte(cli), []byte(client), []string{".timestamp"})
		if len(entries) != 0 {
			t.Errorf("with ignore: expected zero entries, got %d: %v", len(entries), entries)
		}
		// Without ignore the difference is visible.
		if len(structuralDiff([]byte(cli), []byte(client), nil)) == 0 {
			t.Error("without ignore: expected at least one entry, got zero")
		}
	})

	t.Run("wildcard suppresses all matching nodes", func(t *testing.T) {
		cli := `{"items":[{"id":1,"timestamp":"A"},{"id":2,"timestamp":"B"}]}`
		client := `{"items":[{"id":1,"timestamp":"X"},{"id":2,"timestamp":"Y"}]}`

		entries := structuralDiff([]byte(cli), []byte(client), []string{"items[*].timestamp"})
		if len(entries) != 0 {
			t.Errorf("wildcard ignore: expected zero entries, got %d: %v", len(entries), entries)
		}
	})

	t.Run("index-specific suppresses only that index", func(t *testing.T) {
		cli := `{"items":[{"id":1,"timestamp":"A"},{"id":2,"timestamp":"B"}]}`
		client := `{"items":[{"id":1,"timestamp":"X"},{"id":2,"timestamp":"Y"}]}`

		// Ignore only items[0].timestamp; items[1].timestamp should still be reported.
		entries := structuralDiff([]byte(cli), []byte(client), []string{"items[0].timestamp"})
		if len(entries) != 1 {
			t.Fatalf("index-specific ignore: expected 1 entry, got %d: %v", len(entries), entries)
		}
		if entries[0].Path != ".items[1].timestamp" {
			t.Errorf("path: got %q, want %q", entries[0].Path, ".items[1].timestamp")
		}
	})
}
