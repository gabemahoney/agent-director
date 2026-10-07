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

// TestDefaultMatchesSRD pins Default() outside [tmux] (whose defaults
// TestTmuxConstantsMatchSRD and TestTmuxKeyDefinitions pin). The relay
// timeout is a full day so overnight human approvals do not time out (b.p48).
func TestDefaultMatchesSRD(t *testing.T) {
	d := config.Default()
	cases := []struct {
		name      string
		got, want any
	}{
		{"Defaults.RelayMode", d.Defaults.RelayMode, "off"},
		{"Defaults.ExpireRetentionDays", d.Defaults.ExpireRetentionDays, 31},
		{"Defaults.DisableAskUserQuestion", d.Defaults.DisableAskUserQuestion, false},
		{"Defaults.InjectHelpHook", d.Defaults.InjectHelpHook, false},
		{"Relay.PollBaseMs", d.Relay.PollBaseMs, 100},
		{"Relay.PollJitterMs", d.Relay.PollJitterMs, 100},
		{"Relay.TimeoutSeconds", d.Relay.TimeoutSeconds, 86400},
		{"Relay.PermissionRequestCap", d.Relay.PermissionRequestCap, 1000},
		{"Pause.TimeoutSeconds", d.Pause.TimeoutSeconds, 30},
		{"PreTrust.LockWaitSeconds", d.PreTrust.LockWaitSeconds, 12},
		{"Store.DbPath", d.Store.DbPath, "~/.agent-director/state.db"},
		{"Log.ErrorLogPath", d.Log.ErrorLogPath, "~/.agent-director/errors.log"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestLoadKeepsDefaults: a missing file loads Default(), and a file setting
// some keys changes only those (an unknown key is ignored, so a future strict
// mode is a conscious choice); either way the "~/" path defaults come back
// resolved against HOME. A table in another letter case, alone or beside a
// spelling setting other keys, loads, as does an unknown key in two (b.p8n).
func TestLoadKeepsDefaults(t *testing.T) {
	home := homeDir(t)
	cases := []struct {
		name string
		path string
		edit func(c *config.Config)
	}{
		{"missing file", filepath.Join(t.TempDir(), "does-not-exist.toml"), func(*config.Config) {}},
		{"poll_base_ms set", makeConfigFile(t, "[relay]\npoll_base_ms = 250\n"), func(c *config.Config) { c.Relay.PollBaseMs = 250 }},
		{"permission_request_cap set", makeConfigFile(t, "[relay]\npermission_request_cap = 500\n"),
			func(c *config.Config) { c.Relay.PermissionRequestCap = 500 }},
		{"unknown key ignored", makeConfigFile(t, "unknown_top_level_key = 42\n\n[relay]\npoll_base_ms = 150\n"),
			func(c *config.Config) { c.Relay.PollBaseMs = 150 }},
		{"table in another letter case", makeConfigFile(t, "[Store]\ndb_path = \"/x.db\"\n"),
			func(c *config.Config) { c.Store.DbPath = "/x.db" }},
		// install.sh appends a [defaults] to a config holding [Defaults].
		{"two table spellings setting other keys", makeConfigFile(t, "[Defaults]\nrelay_mode = \"on\"\n\n[defaults]\ninject_help_hook = true\n"),
			func(c *config.Config) { c.Defaults.RelayMode, c.Defaults.InjectHelpHook = "on", true }},
		{"empty table beside another spelling", makeConfigFile(t, "[Store]\n\n[store]\ndb_path = \"/x.db\"\n"),
			func(c *config.Config) { c.Store.DbPath = "/x.db" }},
		{"unknown key in two spellings ignored", makeConfigFile(t, "[foo]\nX = 1\nx = 2\n"), func(*config.Config) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(tc.path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := config.Default()
			want.Store.DbPath = filepath.Join(home, ".agent-director/state.db")
			want.Log.ErrorLogPath = filepath.Join(home, ".agent-director/errors.log")
			tc.edit(&want)
			if cfg != want {
				t.Errorf("Load:\n got %+v\nwant %+v", cfg, want)
			}
		})
	}
}

// TestLoadResolvesPathFields: "~/" expands against HOME, a relative path
// resolves under <HOME>/.agent-director, an absolute path and a "$HOME"
// literal are kept as written.
func TestLoadResolvesPathFields(t *testing.T) {
	home := homeDir(t)
	abs := filepath.Join(t.TempDir(), "absolute.db")
	cases := []struct{ db, log, wantDB, wantLog string }{
		{"~/foo.db", "~/bar.log", filepath.Join(home, "foo.db"), filepath.Join(home, "bar.log")},
		{"foo.db", "logs/errors.log", filepath.Join(home, ".agent-director", "foo.db"), filepath.Join(home, ".agent-director", "logs", "errors.log")},
		{abs, abs + ".log", abs, abs + ".log"},
	}
	for _, tc := range cases {
		cfg, err := config.Load(makeConfigFile(t, "[store]\ndb_path = "+quoted(tc.db)+"\n[log]\nerror_log_path = "+quoted(tc.log)+"\n"))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Store.DbPath != tc.wantDB || cfg.Log.ErrorLogPath != tc.wantLog {
			t.Errorf("db_path %q, error_log_path %q: got %q, %q; want %q, %q", tc.db, tc.log, cfg.Store.DbPath, cfg.Log.ErrorLogPath, tc.wantDB, tc.wantLog)
		}
	}
	cfg, err := config.Load(makeConfigFile(t, "[store]\ndb_path = \"$HOME/foo.db\"\n"))
	if err != nil || !strings.Contains(cfg.Store.DbPath, "$HOME") {
		t.Errorf("db_path \"$HOME/foo.db\": got %q, %v; want the literal $HOME kept", cfg.Store.DbPath, err)
	}
}

// TestEffectiveDbPath: a set db_path is returned unchanged, even without HOME;
// an empty one gives the default store under HOME, refused without HOME (b.8up).
func TestEffectiveDbPath(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		name, dbPath, home string
		unset              bool
		want               string
		wantErr            bool
	}{
		{"set", "/abs/x.db", home, false, "/abs/x.db", false},
		{"set without HOME", "~/x.db", "", true, "~/x.db", false},
		{"empty", "", home, false, filepath.Join(home, ".agent-director", "state.db"), false},
		{"empty with HOME empty", "", "", false, "", true},
		{"empty with HOME unset", "", "", true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			if tc.unset {
				if err := os.Unsetenv("HOME"); err != nil {
					t.Fatalf("Unsetenv HOME: %v", err)
				}
			}
			got, err := config.Store{DbPath: tc.dbPath}.EffectiveDbPath()
			if got != tc.want || (err != nil) != tc.wantErr || (err != nil && !strings.HasPrefix(err.Error(), "expand tilde: ")) {
				t.Errorf("EffectiveDbPath(%q) = %q, %v; want %q and an expand tilde error: %t", tc.dbPath, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// quoted wraps s in TOML double-quoted-string syntax with minimal escaping.
func quoted(s string) string {
	return "\"" + strings.ReplaceAll(s, "\\", "\\\\") + "\""
}
