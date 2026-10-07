package api_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// writeConfig writes TOML content to path, creating parent dirs as needed.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("writeConfig MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeConfig WriteFile: %v", err)
	}
}

// TestNewDefaultPaths pins api.New under HOME: with no store at
// ~/.agent-director/state.db it is ErrStoreNotInitialized and creates nothing
// (H1) unless CreateIfMissing; an existing store opens; "~/" paths expand
// against $HOME; concurrent Closes all return nil (SR-1.2).
func TestNewDefaultPaths(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	def := filepath.Join(".agent-director", "state.db")
	cases := []struct {
		name    string
		seed    string // the store seeded under HOME first; "" none
		opts    api.Options
		wantErr error
	}{
		{"missing store", "", api.Options{}, store.ErrStoreNotInitialized},
		{"missing store, CreateIfMissing", "", api.Options{CreateIfMissing: true}, nil},
		{"existing store", def, api.Options{}, nil},
		{"tilde StorePath and ConfigPath", "tilde.db", api.Options{StorePath: "~/tilde.db", ConfigPath: "~/tilde.toml"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if tc.seed != "" {
				apitest.SeedStore(t, filepath.Join(home, tc.seed))
			}
			writeConfig(t, filepath.Join(home, "tilde.toml"), "")
			c, err := api.New(tc.opts)
			if !errors.Is(err, tc.wantErr) || (c == nil) != (err != nil) {
				t.Fatalf("New = %v, %v; want error %v", c, err, tc.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(home, def)); (statErr == nil) != (tc.seed == def || tc.opts.CreateIfMissing) {
				t.Errorf("default store: stat %v; want it present only when seeded or created", statErr)
			}
			if c == nil {
				return
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := c.Close(); err != nil {
						t.Errorf("concurrent Close: %v", err)
					}
				}()
			}
			wg.Wait()
		})
	}
}

// TestExpandTildeHonorsHOMEEnv is the b.6k1 regression: expandTilde resolves
// "~/" with $HOME, not the passwd entry.
func TestExpandTildeHonorsHOMEEnv(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	tmpdir := t.TempDir()
	t.Setenv("HOME", tmpdir)
	if got, err := api.ExpandTildeForTest("~/foo"); err != nil || got != filepath.Join(tmpdir, "foo") {
		t.Errorf("expandTilde = %q, %v; want %q (HOME=%q)", got, err, filepath.Join(tmpdir, "foo"), tmpdir)
	}
}

// TestNewStorePath pins H2: [store] db_path names the store when StorePath is
// empty; StorePath wins over it, leaving it untouched; a store of another
// schema version is ErrSchemaMismatch.
func TestNewStorePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath, fromCfg, override := filepath.Join(dir, "cfg.toml"), filepath.Join(dir, "cfg.db"), filepath.Join(dir, "override.db")
	writeConfig(t, cfgPath, fmt.Sprintf("[store]\ndb_path = %q\n", fromCfg))
	open := func(opts api.Options) error {
		c, err := api.New(opts)
		if err == nil {
			err = c.Close()
		}
		return err
	}
	apitest.SeedStore(t, override)
	if err := open(api.Options{StorePath: override, ConfigPath: cfgPath}); err != nil {
		t.Fatalf("New with StorePath: %v", err)
	}
	if _, err := os.Stat(fromCfg); !os.IsNotExist(err) {
		t.Errorf("config's db_path %s: stat %v; want it never created when StorePath overrides it", fromCfg, err)
	}
	apitest.SeedStore(t, fromCfg)
	if err := open(api.Options{ConfigPath: cfgPath}); err != nil {
		t.Fatalf("New with the config's db_path: %v", err)
	}
	db, err := sql.Open("sqlite", fromCfg)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	_ = db.Close()
	if err := open(api.Options{ConfigPath: cfgPath}); !errors.Is(err, store.ErrSchemaMismatch) {
		t.Errorf("New on a user_version 99 store: %v; want ErrSchemaMismatch", err)
	}
}

// TestNewRefusesConfig pins SR-4.1, b.8q2 and b.c7f at the Go surface: a
// config that parses but holds a refused [tmux], [relay], [pause] or [store]
// value makes api.New return no Client and that file's *config.ConfigError
// before the store is opened, so even with CreateIfMissing nothing is created;
// the same file without the value constructs a Client. 9223372036 is the relay
// value whose guard cutoff wrapped; 9223372037 wrapped the relay and pause
// windows.
func TestNewRefusesConfig(t *testing.T) {
	t.Parallel()
	type refused struct {
		keys     apitest.ConfigKeys
		refusal  *apitest.ConfigRefusal // the description, when checked whole
		settings []apitest.TmuxSetting
		key      config.TmuxKey // the [tmux] key the description must name, when namesKey
		namesKey bool
	}
	cases := map[string]refused{
		"tmux below minimum": {settings: []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, config.MinStoppingWindowSeconds-1)},
			key: config.TmuxStoppingWindowSeconds, namesKey: true},
		"tmux negative": {settings: []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxQueryTimeoutMs, -1)},
			key: config.TmuxQueryTimeoutMs, namesKey: true},
		"tmux non-integer": {settings: []apitest.TmuxSetting{apitest.TmuxString(config.TmuxStartingSessionSeconds, "soon")}},
	}
	for _, v := range []int64{-1, 2147484, 9223372036, 9223372037} {
		cases[fmt.Sprintf("relay %d", v)] = refused{keys: apitest.ConfigKeys{RelayTimeoutSeconds: v},
			refusal: &apitest.ConfigRefusal{RelayTimeout: true, Value: v}}
	}
	for _, v := range []int64{-1, 9223372037} {
		cases[fmt.Sprintf("pause %d", v)] = refused{keys: apitest.ConfigKeys{PauseTimeoutSeconds: v},
			refusal: &apitest.ConfigRefusal{PauseTimeout: true, Value: v}}
	}
	for _, v := range []int64{-1, int64(config.MaxStoreBusyTimeoutMs) + 1} {
		cases[fmt.Sprintf("store busy timeout %d", v)] = refused{keys: apitest.ConfigKeys{StoreBusyTimeoutMs: v},
			refusal: &apitest.ConfigRefusal{StoreBusyTimeout: true, Value: v}}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath, storeDir := filepath.Join(dir, "config.toml"), filepath.Join(dir, "store")
			opts := api.Options{ConfigPath: cfgPath, StorePath: filepath.Join(storeDir, "state.db"), CreateIfMissing: true}
			apitest.WriteKeysConfig(t, cfgPath, tc.keys, tc.settings...)
			c, err := api.New(opts)
			var ce *config.ConfigError
			if c != nil || !errors.As(err, &ce) || ce.Path != cfgPath || ce.Err == nil {
				t.Fatalf("New = %v, %v; want no Client and a *config.ConfigError for %s", c, err, cfgPath)
			}
			if tc.namesKey && !strings.Contains(ce.Err.Error(), tc.key.Name()) {
				t.Errorf("description %q does not name %s", ce.Err, tc.key.Name())
			}
			if tc.refusal != nil {
				apitest.AssertDescription(t, ce.Error(), apitest.DescConfigRefused(cfgPath, *tc.refusal))
			}
			if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
				t.Errorf("store directory: stat %v; want nothing created", err)
			}
			apitest.WriteKeysConfig(t, cfgPath, apitest.ConfigKeys{})
			if c, err = api.New(opts); err != nil {
				t.Fatalf("New once the value is removed: %v", err)
			}
			_ = c.Close()
			if _, err := os.Stat(opts.StorePath); err != nil {
				t.Errorf("store after the fixed config: %v; want it created", err)
			}
		})
	}
}
