package clisetup_test

import (
	"errors"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// TestOpenErrorIs: an OpenError matches the sentinel its Name names and no
// other clisetup sentinel, and its cause still matches (b.vma, b.cm7).
func TestOpenErrorIs(t *testing.T) {
	cause := errors.New("the cause")
	sentinels := map[string]error{
		"ErrConfigMalformed":         clisetup.ErrConfigMalformed,
		"ErrStoreOpen":               clisetup.ErrStoreOpen,
		"ErrSchemaMismatch":          clisetup.ErrSchemaMismatch,
		"ErrSchemaMigrationRequired": clisetup.ErrSchemaMigrationRequired,
		"ErrUnknownVerb":             clisetup.ErrUnknownVerb,
		"ErrJSONMarshal":             clisetup.ErrJSONMarshal,
		"ErrTrailWrite":              clisetup.ErrTrailWrite,
	}
	for _, name := range []string{"ErrConfigMalformed", "ErrStoreOpen", "ErrSchemaMismatch", "ErrSchemaMigrationRequired"} {
		t.Run(name, func(t *testing.T) {
			err := &clisetup.OpenError{Name: name, Err: cause}
			for target, sentinel := range sentinels {
				if got, want := errors.Is(err, sentinel), target == name; got != want {
					t.Errorf("errors.Is(%s) = %t, want %t", target, got, want)
				}
			}
			if !errors.Is(err, cause) {
				t.Error("errors.Is(cause) = false, want true")
			}
		})
	}
}
