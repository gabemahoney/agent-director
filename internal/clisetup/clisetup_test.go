package clisetup_test

import (
	"errors"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// TestOpenErrorIs: an OpenError matches the sentinel its Name names and no
// other, a schema-named one matches neither, and its cause still matches (b.vma).
func TestOpenErrorIs(t *testing.T) {
	cause := errors.New("the cause")
	for _, tc := range []struct {
		name                  string
		wantConfig, wantStore bool
	}{
		{"ErrConfigMalformed", true, false},
		{"ErrStoreOpen", false, true},
		{"ErrSchemaMismatch", false, false},
		{"ErrSchemaMigrationRequired", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &clisetup.OpenError{Name: tc.name, Err: cause}
			if got := errors.Is(err, clisetup.ErrConfigMalformed); got != tc.wantConfig {
				t.Errorf("errors.Is(ErrConfigMalformed) = %t, want %t", got, tc.wantConfig)
			}
			if got := errors.Is(err, clisetup.ErrStoreOpen); got != tc.wantStore {
				t.Errorf("errors.Is(ErrStoreOpen) = %t, want %t", got, tc.wantStore)
			}
			if !errors.Is(err, cause) {
				t.Error("errors.Is(cause) = false, want true")
			}
		})
	}
}
