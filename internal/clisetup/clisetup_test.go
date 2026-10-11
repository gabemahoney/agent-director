package clisetup_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/store"
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

// TestConfigPathIsUnderNewDir: the config path every command binary loads is
// under ~/.config/agent-director, and the pkg/api options carry it (SR-11.7).
func TestConfigPathIsUnderNewDir(t *testing.T) {
	const want = "~/.config/agent-director/config.toml"
	if clisetup.ConfigPath != want {
		t.Errorf("ConfigPath = %q; want %q", clisetup.ConfigPath, want)
	}
	if got := clisetup.APIOptions(clisetup.Overrides{}).ConfigPath; got != want {
		t.Errorf("APIOptions ConfigPath = %q; want %q", got, want)
	}
}

// TestNewOpenErrorNamesOldLayout: a pkg/api.New failure that wraps ErrOldLayout
// is named ErrOldLayout, matches the sentinel, and keeps the cause's text; other
// failures still read ErrStoreOpen (P-88: handled as the schema errors are).
func TestNewOpenErrorNamesOldLayout(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantName string
	}{
		{"store sentinel", store.ErrOldLayout, "ErrOldLayout"},
		{"wrapped store sentinel", fmt.Errorf("api: %w: detail", store.ErrOldLayout), "ErrOldLayout"},
		{"pkg/api re-export", fmt.Errorf("%w: detail", pkgapi.ErrOldLayout), "ErrOldLayout"},
		{"schema mismatch is its own name", fmt.Errorf("%w: detail", store.ErrSchemaMismatch), "ErrSchemaMismatch"},
		{"any other failure", errors.New("disk full"), "ErrStoreOpen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oe := clisetup.NewOpenError(tc.err)
			if oe.Name != tc.wantName {
				t.Errorf("Name = %q; want %q", oe.Name, tc.wantName)
			}
			if oe.Error() != tc.err.Error() {
				t.Errorf("Error() = %q; want the cause's %q", oe.Error(), tc.err.Error())
			}
			if got, want := errors.Is(oe, clisetup.ErrOldLayout), tc.wantName == "ErrOldLayout"; got != want {
				t.Errorf("errors.Is(ErrOldLayout) = %t; want %t", got, want)
			}
		})
	}
}

// homeTree lists every path under home, relative and sorted, with a trailing
// slash on directories, for the before/after comparison of a refused open.
func homeTree(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, path)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", home, err)
	}
	sort.Strings(out)
	return out
}

// oldOnlyHome returns a throwaway HOME (set as $HOME) holding only the old
// directory, with a file in it.
func oldOnlyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := filepath.Join(home, ".agent-director")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatalf("mkdir old dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, "state.db"), []byte("old store"), 0o600); err != nil {
		t.Fatalf("write old store: %v", err)
	}
	return home
}

// TestOpenRefusesOldLayoutAndCreatesNothing: with HOME holding only the old
// directory and nothing steering the open off the default store, Open returns an
// *OpenError named ErrOldLayout before it creates errors.log, the new directory
// or a store (SR-11.7, Pin 4).
func TestOpenRefusesOldLayoutAndCreatesNothing(t *testing.T) {
	home := oldOnlyHome(t)
	before := homeTree(t, home)

	client, _, err := clisetup.Open(clisetup.Overrides{})
	if client != nil {
		_ = client.Close()
		t.Fatal("Open on an old-layout-only HOME succeeded; want ErrOldLayout")
	}
	var oe *clisetup.OpenError
	if !errors.As(err, &oe) || oe.Name != "ErrOldLayout" {
		t.Fatalf("Open error = %v (%T); want an *OpenError named ErrOldLayout", err, err)
	}
	if !errors.Is(err, clisetup.ErrOldLayout) {
		t.Errorf("errors.Is(err, ErrOldLayout) = false; err = %v", err)
	}
	if after := homeTree(t, home); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("refused Open changed HOME:\n before %v\n after  %v", before, after)
	}
}

// TestOpenSkipsOldLayoutCheckOffTheDefaultStore: the check applies only when
// about to open the default store. With --store-path, with a [store] db_path in
// the config file, or with a store already at the default path, Open goes on
// with the old directory present.
func TestOpenSkipsOldLayoutCheckOffTheDefaultStore(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home string) clisetup.Overrides
	}{
		{"--store-path", func(t *testing.T, home string) clisetup.Overrides {
			return clisetup.Overrides{StorePath: filepath.Join(t.TempDir(), "state.db")}
		}},
		{"[store] db_path in the config file", func(t *testing.T, home string) clisetup.Overrides {
			dir := filepath.Join(home, ".config", "agent-director")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(t.TempDir(), "elsewhere.db")
			cfg := fmt.Sprintf("[store]\ndb_path = %q\n", db)
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			return clisetup.Overrides{}
		}},
		{"a store already at the default path", func(t *testing.T, home string) clisetup.Overrides {
			dir := filepath.Join(home, ".config", "agent-director")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			c, err := pkgapi.New(pkgapi.Options{CreateIfMissing: true, StorePath: filepath.Join(dir, "state.db")})
			if err != nil {
				t.Fatalf("seed the default store: %v", err)
			}
			_ = c.Close()
			return clisetup.Overrides{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := oldOnlyHome(t)
			o := tc.setup(t, home)
			client, _, err := clisetup.Open(o)
			if err != nil {
				t.Fatalf("Open with the old directory present = %v; want success (the check does not apply)", err)
			}
			_ = client.Close()
		})
	}
}
