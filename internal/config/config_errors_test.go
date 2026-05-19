package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/claude-director/internal/config"
)

func TestLoadMalformedReturnsTypedError(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "unclosed_array",
			content: "key = [unclosed\n",
		},
		{
			name: "duplicate_key_in_same_table",
			content: `
[defaults]
relay_mode = "off"
relay_mode = "on"
`,
		},
		{
			name:    "non_toml_garbage",
			content: "<<< this is not toml >>>\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bad.toml")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write temp config: %v", err)
			}

			cfg, err := config.Load(path)
			if err == nil {
				t.Fatalf("expected error, got nil (cfg=%+v)", cfg)
			}
			var cerr *config.ConfigError
			if !errors.As(err, &cerr) {
				t.Fatalf("expected *config.ConfigError, got %T: %v", err, err)
			}
			if cerr.Path != path {
				t.Errorf("ConfigError.Path: got %q, want %q", cerr.Path, path)
			}
			if cerr.Unwrap() == nil {
				t.Errorf("ConfigError should wrap the underlying parse error, got nil")
			}
			// On parse failure Load returns Default() (post path-resolution) so
			// callers can keep running with usable, expanded paths.
			home, herr := os.UserHomeDir()
			if herr != nil {
				t.Fatalf("UserHomeDir: %v", herr)
			}
			wantDB := filepath.Join(home, ".claude-director/state.db")
			wantLog := filepath.Join(home, ".claude-director/errors.log")
			def := config.Default()
			if cfg.Store.DbPath != wantDB || cfg.Log.ErrorLogPath != wantLog {
				t.Errorf("path fields not resolved on error: %+v", cfg)
			}
			if cfg.Defaults != def.Defaults || cfg.Relay != def.Relay || cfg.Pause != def.Pause {
				t.Errorf("non-path defaults drifted on error: got=%+v want=%+v", cfg, def)
			}
		})
	}
}

func TestLoadIgnoresUnknownKey(t *testing.T) {
	// BurntSushi/toml ignores unknown top-level keys by default. This test
	// pins that behavior so a future opt-in to strict mode is a conscious
	// choice rather than an accidental regression.
	path := makeConfigFile(t, `
unknown_top_level_key = 42

[relay]
poll_base_ms = 150
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Relay.PollBaseMs != 150 {
		t.Errorf("known field still applied: got PollBaseMs=%d, want 150", cfg.Relay.PollBaseMs)
	}
}
