package api_test

import (
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestVersionConfigDir: Version and Client.Version report config_dir as the
// literal "~/.config/agent-director" whatever HOME is, set, relative, odd or
// unset, and read no file.
func TestVersionConfigDir(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	const want = "~/.config/agent-director"
	dir := t.TempDir()
	apitest.SeedStore(t, filepath.Join(dir, "state.db"))
	for name, home := range map[string]string{"absolute": dir, "unset": "", "relative": "rel/home", "with spaces": "/tmp/a b"} {
		t.Run(name, func(t *testing.T) {
			if home == "" {
				unsetenvIfSet(t, "HOME")
			} else {
				t.Setenv("HOME", home)
			}
			res, err := api.Version()
			if err != nil || res.ConfigDir != want {
				t.Errorf("Version() = %+v, %v; want ConfigDir %q", res, err, want)
			}
			c, err := api.New(api.Options{ConfigPath: filepath.Join(dir, "none.toml"), StorePath: filepath.Join(dir, "state.db")})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer c.Close()
			if cres, err := c.Version(); err != nil || cres != res {
				t.Errorf("Client.Version() = %+v, %v; want %+v", cres, err, res)
			}
		})
	}
}
