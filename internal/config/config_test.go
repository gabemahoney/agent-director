package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// makeConfigFile writes content into a temp directory and returns the path.
func makeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

// homeDir returns the current home and fails the test if it cannot be
// resolved, matching the lookup Load itself performs.
func homeDir(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return h
}

func TestDefaultMatchesSRD(t *testing.T) {
	d := config.Default()
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"Defaults.RelayMode", d.Defaults.RelayMode, "off"},
		{"Defaults.ExpireRetentionDays", d.Defaults.ExpireRetentionDays, 31},
		{"Defaults.DisableAskUserQuestion", d.Defaults.DisableAskUserQuestion, false},
		{"Defaults.InjectHelpHook", d.Defaults.InjectHelpHook, false},
		{"Relay.PollBaseMs", d.Relay.PollBaseMs, 100},
		{"Relay.PollJitterMs", d.Relay.PollJitterMs, 100},
		{"Relay.TimeoutSeconds", d.Relay.TimeoutSeconds, 600},
		{"Pause.TimeoutSeconds", d.Pause.TimeoutSeconds, 30},
		{"Store.DbPath", d.Store.DbPath, "~/.agent-director/state.db"},
		{"Log.ErrorLogPath", d.Log.ErrorLogPath, "~/.agent-director/errors.log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}

func TestLoadMissingFileReturnsResolvedDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.toml")
	cfg, err := config.Load(missing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Fatalf("UserHomeDir: %v", herr)
	}
	// Default() values use "~/" placeholders that Load must expand on the
	// missing-file branch too — un-resolved tilde paths reaching store.Open
	// or log output would be a real-world bug.
	wantDB := filepath.Join(home, ".agent-director/state.db")
	wantLog := filepath.Join(home, ".agent-director/errors.log")
	if cfg.Store.DbPath != wantDB {
		t.Errorf("Store.DbPath = %q, want %q", cfg.Store.DbPath, wantDB)
	}
	if cfg.Log.ErrorLogPath != wantLog {
		t.Errorf("Log.ErrorLogPath = %q, want %q", cfg.Log.ErrorLogPath, wantLog)
	}
	// All non-path defaults must still match Default() unchanged.
	def := config.Default()
	if cfg.Defaults != def.Defaults || cfg.Relay != def.Relay || cfg.Pause != def.Pause {
		t.Errorf("non-path defaults drifted from Default():\n got=%+v\nwant=%+v", cfg, def)
	}
}

func TestLoadPartialOverridePreservesDefaults(t *testing.T) {
	path := makeConfigFile(t, "[relay]\npoll_base_ms = 250\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Relay.PollBaseMs != 250 {
		t.Errorf("PollBaseMs override not applied: got %d", cfg.Relay.PollBaseMs)
	}
	// Untouched fields keep defaults.
	if cfg.Relay.PollJitterMs != 100 {
		t.Errorf("PollJitterMs default lost: got %d", cfg.Relay.PollJitterMs)
	}
	if cfg.Relay.TimeoutSeconds != 600 {
		t.Errorf("Relay.TimeoutSeconds default lost: got %d", cfg.Relay.TimeoutSeconds)
	}
	if cfg.Defaults.RelayMode != "off" {
		t.Errorf("Defaults.RelayMode default lost: got %q", cfg.Defaults.RelayMode)
	}
	if cfg.Pause.TimeoutSeconds != 30 {
		t.Errorf("Pause.TimeoutSeconds default lost: got %d", cfg.Pause.TimeoutSeconds)
	}
}

func TestLoadExpandsTildeInPathFields(t *testing.T) {
	home := homeDir(t)
	path := makeConfigFile(t, `
[store]
db_path = "~/foo.db"

[log]
error_log_path = "~/bar.log"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDB := filepath.Join(home, "foo.db")
	if cfg.Store.DbPath != wantDB {
		t.Errorf("Store.DbPath: got %q, want %q", cfg.Store.DbPath, wantDB)
	}
	wantLog := filepath.Join(home, "bar.log")
	if cfg.Log.ErrorLogPath != wantLog {
		t.Errorf("Log.ErrorLogPath: got %q, want %q", cfg.Log.ErrorLogPath, wantLog)
	}
}

func TestLoadPreservesDollarVarLiteral(t *testing.T) {
	path := makeConfigFile(t, `
[store]
db_path = "$HOME/foo.db"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cfg.Store.DbPath, "$HOME") {
		t.Errorf("expected literal $HOME preserved, got %q", cfg.Store.DbPath)
	}
}

func TestLoadResolvesRelativePathAgainstHome(t *testing.T) {
	home := homeDir(t)
	path := makeConfigFile(t, `
[store]
db_path = "foo.db"

[log]
error_log_path = "logs/errors.log"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDB := filepath.Join(home, ".agent-director", "foo.db")
	if cfg.Store.DbPath != wantDB {
		t.Errorf("Store.DbPath: got %q, want %q", cfg.Store.DbPath, wantDB)
	}
	wantLog := filepath.Join(home, ".agent-director", "logs", "errors.log")
	if cfg.Log.ErrorLogPath != wantLog {
		t.Errorf("Log.ErrorLogPath: got %q, want %q", cfg.Log.ErrorLogPath, wantLog)
	}
}

func TestLoadPreservesAbsolutePath(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "absolute.db")
	path := makeConfigFile(t, "[store]\ndb_path = "+quoted(abs)+"\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Store.DbPath != abs {
		t.Errorf("Store.DbPath: got %q, want %q (absolute path should be unchanged)", cfg.Store.DbPath, abs)
	}
}

// quoted wraps s in TOML double-quoted-string syntax with minimal escaping.
func quoted(s string) string {
	return "\"" + strings.ReplaceAll(s, "\\", "\\\\") + "\""
}
