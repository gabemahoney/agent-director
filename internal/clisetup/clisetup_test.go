package clisetup_test

import (
	"errors"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// TestAPIOptions: the overrides' store path and tmux command pass through, and
// a missing store is created unless --create-if-missing false set it off (Pin 1, b.78b).
func TestAPIOptions(t *testing.T) {
	cases := []struct {
		name   string
		o      clisetup.Overrides
		create bool
	}{
		{"no overrides", clisetup.Overrides{}, true},
		{"paths, no --create-if-missing", clisetup.Overrides{StorePath: "/s.db", TmuxCommand: "/bin/tmux"}, true},
		{"--create-if-missing true", clisetup.Overrides{CreateIfMissing: true, CreateIfMissingSet: true}, true},
		{"--create-if-missing false", clisetup.Overrides{StorePath: "/s.db", CreateIfMissingSet: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := pkgapi.Options{ConfigPath: clisetup.ConfigPath, CreateIfMissing: tc.create,
				StorePath: tc.o.StorePath, TmuxCommand: tc.o.TmuxCommand}
			if got := clisetup.APIOptions(tc.o); got != want {
				t.Errorf("APIOptions(%+v) = %+v; want %+v", tc.o, got, want)
			}
		})
	}
}

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
