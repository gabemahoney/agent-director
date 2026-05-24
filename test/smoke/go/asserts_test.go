package smoke_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// capturingT is a testing.T substitute that records failure messages without
// actually failing the outer test. Used to assert that AssertResultMatchesManifest
// and AssertExpectedError fire (or don't fire) on specific inputs.
type capturingT struct {
	testing.TB
	errors []string
}

func (c *capturingT) Helper() {}
func (c *capturingT) Errorf(format string, args ...any) {
	c.errors = append(c.errors, fmt.Sprintf(format, args...))
}

// newCapturingT returns a *capturingT that captures errors without actually
// failing. The parent *testing.T is embedded so required TB methods compile.
func newCapturingT(t *testing.T) *capturingT {
	return &capturingT{TB: t}
}

// assertNoErrors calls t.Errorf for every captured error on the inner recorder.
func assertNoErrors(t *testing.T, ct *capturingT, label string) {
	t.Helper()
	for _, msg := range ct.errors {
		t.Errorf("%s: unexpected failure: %s", label, msg)
	}
}

// assertHasError fails the test if the recorder has no errors.
func assertHasError(t *testing.T, ct *capturingT, label string) {
	t.Helper()
	if len(ct.errors) == 0 {
		t.Errorf("%s: expected a failure but got none", label)
	}
}

// ── AssertResultMatchesManifest table tests ───────────────────────────────────

func TestAssertResultMatchesManifest_RequiredNonZero(t *testing.T) {
	vd := manifest.VerbDef{
		Name: "test-verb",
		ResultFields: []manifest.FieldDef{
			{Name: "id", Type: "string", Nullable: false, AllowEmpty: false},
		},
	}

	t.Run("non-zero passes", func(t *testing.T) {
		type result struct {
			ID string `json:"id"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{ID: "abc"})
		assertNoErrors(t, ct, "non-zero passes")
	})

	t.Run("zero fails", func(t *testing.T) {
		type result struct {
			ID string `json:"id"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{ID: ""})
		assertHasError(t, ct, "zero fails")
	})
}

func TestAssertResultMatchesManifest_Nullable(t *testing.T) {
	vd := manifest.VerbDef{
		Name: "test-verb",
		ResultFields: []manifest.FieldDef{
			{Name: "ended_at", Type: "timestamp?", Nullable: true, AllowEmpty: false},
		},
	}

	t.Run("nil pointer passes", func(t *testing.T) {
		type result struct {
			EndedAt *int `json:"ended_at,omitempty"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{EndedAt: nil})
		assertNoErrors(t, ct, "nil pointer nullable")
	})

	t.Run("non-nil pointer also passes", func(t *testing.T) {
		type result struct {
			EndedAt *int `json:"ended_at,omitempty"`
		}
		v := 42
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{EndedAt: &v})
		assertNoErrors(t, ct, "non-nil pointer nullable")
	})

	t.Run("zero string passes (nullable)", func(t *testing.T) {
		type result struct {
			EndedAt string `json:"ended_at"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{EndedAt: ""})
		assertNoErrors(t, ct, "zero string nullable")
	})
}

func TestAssertResultMatchesManifest_AllowEmpty(t *testing.T) {
	vd := manifest.VerbDef{
		Name: "test-verb",
		ResultFields: []manifest.FieldDef{
			{Name: "ids", Type: "[]string", Nullable: false, AllowEmpty: true},
		},
	}

	t.Run("empty non-nil slice passes", func(t *testing.T) {
		type result struct {
			IDs []string `json:"ids"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{IDs: []string{}})
		assertNoErrors(t, ct, "empty slice AllowEmpty")
	})

	t.Run("nil slice fails (AllowEmpty does not allow nil)", func(t *testing.T) {
		type result struct {
			IDs []string `json:"ids"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{IDs: nil})
		assertHasError(t, ct, "nil slice AllowEmpty")
	})

	t.Run("non-empty slice passes", func(t *testing.T) {
		type result struct {
			IDs []string `json:"ids"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{IDs: []string{"a", "b"}})
		assertNoErrors(t, ct, "non-empty slice AllowEmpty")
	})
}

func TestAssertResultMatchesManifest_AllowedValues(t *testing.T) {
	vd := manifest.VerbDef{
		Name: "test-verb",
		ResultFields: []manifest.FieldDef{
			{Name: "state", Type: "string", AllowedValues: []string{"on", "off"}},
		},
	}

	t.Run("allowed value passes", func(t *testing.T) {
		type result struct {
			State string `json:"state"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{State: "on"})
		assertNoErrors(t, ct, "allowed value")
	})

	t.Run("disallowed value fails", func(t *testing.T) {
		type result struct {
			State string `json:"state"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{State: "maybe"})
		assertHasError(t, ct, "disallowed value")
	})
}

func TestAssertResultMatchesManifest_MissingField(t *testing.T) {
	vd := manifest.VerbDef{
		Name: "test-verb",
		ResultFields: []manifest.FieldDef{
			{Name: "claude_instance_id", Type: "string", Nullable: false},
		},
	}

	t.Run("struct missing json tag fails", func(t *testing.T) {
		type result struct {
			// deliberately wrong json tag
			ID string `json:"id"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{ID: "abc"})
		assertHasError(t, ct, "missing json tag")
	})

	t.Run("struct with matching json tag passes", func(t *testing.T) {
		type result struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		ct := newCapturingT(t)
		AssertResultMatchesManifest(ct, vd, result{ClaudeInstanceID: "abc"})
		assertNoErrors(t, ct, "matching json tag")
	})
}

func TestAssertResultMatchesManifest_EmptyResultFields(t *testing.T) {
	// Verbs like kill, send-keys have no result fields — nothing to check.
	vd := manifest.VerbDef{Name: "kill", ResultFields: []manifest.FieldDef{}}
	ct := newCapturingT(t)
	AssertResultMatchesManifest(ct, vd, api.KillResult{})
	assertNoErrors(t, ct, "empty result fields")
}

// ── AssertExpectedError table tests ───────────────────────────────────────────

func TestAssertExpectedError(t *testing.T) {
	vd := manifest.VerbDef{
		Name:       "status",
		ErrorNames: []string{"ErrSpawnNotFound"},
	}

	t.Run("matching sentinel passes", func(t *testing.T) {
		ct := newCapturingT(t)
		AssertExpectedError(ct, vd, api.ErrSpawnNotFound)
		assertNoErrors(t, ct, "matching sentinel")
	})

	t.Run("wrapped matching sentinel passes", func(t *testing.T) {
		wrapped := fmt.Errorf("outer: %w", api.ErrSpawnNotFound)
		ct := newCapturingT(t)
		AssertExpectedError(ct, vd, wrapped)
		assertNoErrors(t, ct, "wrapped matching sentinel")
	})

	t.Run("unrelated error fails", func(t *testing.T) {
		ct := newCapturingT(t)
		AssertExpectedError(ct, vd, errors.New("some other error"))
		assertHasError(t, ct, "unrelated error")
	})

	t.Run("nil error fails", func(t *testing.T) {
		ct := newCapturingT(t)
		AssertExpectedError(ct, vd, nil)
		assertHasError(t, ct, "nil error")
	})
}
