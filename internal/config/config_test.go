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
		{"Defaults.DisableAskUser", d.Defaults.DisableAskUser, false},
		{"Defaults.InjectHelpHook", d.Defaults.InjectHelpHook, false},
		{"Relay.PollBaseMs", d.Relay.PollBaseMs, 100},
		{"Relay.PollJitterMs", d.Relay.PollJitterMs, 100},
		{"Relay.TimeoutSeconds", d.Relay.TimeoutSeconds, 86400},
		{"Relay.PermissionRequestCap", d.Relay.PermissionRequestCap, 1000},
		{"Pause.TimeoutSeconds", d.Pause.TimeoutSeconds, 30},
		{"PreTrust.LockWaitSeconds", d.PreTrust.LockWaitSeconds, 12},
		{"Store.DbPath", d.Store.DbPath, "~/.config/agent-director/state.db"},
		{"Store.BusyTimeoutMs", d.Store.BusyTimeoutMs, 10000},
		{"State.SettleMs", d.State.SettleMs, 0}, // b.3ie: off by default
		{"Log.ErrorLogPath", d.Log.ErrorLogPath, "~/.config/agent-director/errors.log"},
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
			func(c *config.Config) { c.Store.DbPath, c.Store.DbPathSet = "/x.db", true }},
		// install.sh's busy_timeout_ms reader refuses this spelling, which the decoder reads (b.c7f).
		{"busy_timeout_ms in another letter case", makeConfigFile(t, "[STORE]\nBUSY_TIMEOUT_MS = 2500\n"),
			func(c *config.Config) { c.Store.BusyTimeoutMs = 2500 }},
		{"settle_ms set", makeConfigFile(t, "[state]\nsettle_ms = 1500\n"), func(c *config.Config) { c.State.SettleMs = 1500 }},
		{"settle_ms in another letter case", makeConfigFile(t, "[STATE]\nSETTLE_MS = 2000\n"),
			func(c *config.Config) { c.State.SettleMs = 2000 }},
		// install.sh appends a [defaults] to a config holding [Defaults].
		{"two table spellings setting other keys", makeConfigFile(t, "[Defaults]\nrelay_mode = \"on\"\n\n[defaults]\ninject_help_hook = true\n"),
			func(c *config.Config) { c.Defaults.RelayMode, c.Defaults.InjectHelpHook = "on", true }},
		{"empty table beside another spelling", makeConfigFile(t, "[Store]\n\n[store]\ndb_path = \"/x.db\"\n"),
			func(c *config.Config) { c.Store.DbPath, c.Store.DbPathSet = "/x.db", true }},
		{"unknown key in two spellings ignored", makeConfigFile(t, "[foo]\nX = 1\nx = 2\n"), func(*config.Config) {}},
		{"disable_ask_user true", makeConfigFile(t, "[defaults]\ndisable_ask_user = true\n"),
			func(c *config.Config) { c.Defaults.DisableAskUser = true }},
		{"disable_ask_user false", makeConfigFile(t, "[defaults]\ndisable_ask_user = false\n"),
			func(c *config.Config) { c.Defaults.DisableAskUser = false }},
		{"disable_ask_user in another letter case", makeConfigFile(t, "[DEFAULTS]\nDISABLE_ASK_USER = true\n"),
			func(c *config.Config) { c.Defaults.DisableAskUser = true }},
		{"disable_ask_user as an inline table", makeConfigFile(t, "defaults = { disable_ask_user = true }\n"),
			func(c *config.Config) { c.Defaults.DisableAskUser = true }},
		{"disable_ask_user as a dotted key", makeConfigFile(t, "defaults.disable_ask_user = true\n"),
			func(c *config.Config) { c.Defaults.DisableAskUser = true }},
		{"unrelated unknown [defaults] key ignored", makeConfigFile(t, "[defaults]\ndisable_something_else = true\nrelay_mode = \"on\"\n"),
			func(c *config.Config) { c.Defaults.RelayMode = "on" }},
		{"claude_path absolute", makeConfigFile(t, "[launch]\nclaude_path = \"/opt/bin/claude\"\n"),
			func(c *config.Config) { c.Launch.ClaudePath = "/opt/bin/claude" }},
		{"codex_path absolute", makeConfigFile(t, "[launch]\ncodex_path = \"/opt/bin/codex\"\n"),
			func(c *config.Config) { c.Launch.CodexPath = "/opt/bin/codex" }},
		{"both paths absolute", makeConfigFile(t, "[launch]\nclaude_path = \"/a/claude\"\ncodex_path = \"/b/codex\"\n"),
			func(c *config.Config) { c.Launch.ClaudePath, c.Launch.CodexPath = "/a/claude", "/b/codex" }},
		{"launch paths joined onto HOME", makeConfigFile(t, "[launch]\nclaude_path = \"~/bin/claude\"\ncodex_path = \"~/.local/bin/codex\"\n"),
			func(c *config.Config) {
				c.Launch.ClaudePath, c.Launch.CodexPath = filepath.Join(home, "bin/claude"), filepath.Join(home, ".local/bin/codex")
			}},
		{"empty launch paths load as unset", makeConfigFile(t, "[launch]\nclaude_path = \"\"\ncodex_path = \"\"\n"), func(*config.Config) {}},
		{"launch table and keys in another letter case", makeConfigFile(t, "[LAUNCH]\nCLAUDE_PATH = \"/a/claude\"\nCodex_Path = \"/b/codex\"\n"),
			func(c *config.Config) { c.Launch.ClaudePath, c.Launch.CodexPath = "/a/claude", "/b/codex" }},
		{"unknown [launch] key ignored", makeConfigFile(t, "[launch]\nclaude_paths = \"relative\"\ngemini_path = \"x\"\nclaude_path = \"/a/claude\"\n"),
			func(c *config.Config) { c.Launch.ClaudePath = "/a/claude" }},
		{"old key name outside [defaults] ignored as any unknown key", makeConfigFile(t, "[relay]\ndisable_askuserquestion = true\n"),
			func(*config.Config) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(tc.path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := config.Default()
			want.Store.DbPath = filepath.Join(home, ".config/agent-director/state.db")
			want.Log.ErrorLogPath = filepath.Join(home, ".config/agent-director/errors.log")
			tc.edit(&want)
			if cfg != want {
				t.Errorf("Load:\n got %+v\nwant %+v", cfg, want)
			}
		})
	}
}

// TestLoadResolvesPathFields: "~/" expands against HOME, a relative path
// resolves under <HOME>/.config/agent-director, an absolute path and a "$HOME"
// literal are kept as written.
func TestLoadResolvesPathFields(t *testing.T) {
	home := homeDir(t)
	abs := filepath.Join(t.TempDir(), "absolute.db")
	cases := []struct{ db, log, wantDB, wantLog string }{
		{"~/foo.db", "~/bar.log", filepath.Join(home, "foo.db"), filepath.Join(home, "bar.log")},
		{"foo.db", "logs/errors.log", filepath.Join(home, ".config", "agent-director", "foo.db"), filepath.Join(home, ".config", "agent-director", "logs", "errors.log")},
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
		{"empty", "", home, false, filepath.Join(home, ".config", "agent-director", "state.db"), false},
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

// TestDefaultsIgnoreXDGConfigHome: the default directory is <HOME>/.config/agent-director
// from $HOME alone, so XDG_CONFIG_HOME, set elsewhere, changes no default and no
// relative join (SR-11.7).
func TestDefaultsIgnoreXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "agent-director")
	for _, xdg := range []string{"", filepath.Join(t.TempDir(), "xdg")} {
		t.Run("XDG_CONFIG_HOME="+xdg, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", xdg)

			missing, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))
			if err != nil {
				t.Fatalf("Load of a missing file: %v", err)
			}
			relative, err := config.Load(makeConfigFile(t, "[store]\ndb_path = \"foo.db\"\n[log]\nerror_log_path = \"logs/e.log\"\n"))
			if err != nil {
				t.Fatalf("Load of relative paths: %v", err)
			}
			effective, err := config.Store{}.EffectiveDbPath()
			if err != nil {
				t.Fatalf("EffectiveDbPath: %v", err)
			}
			for _, tc := range []struct{ name, got, want string }{
				{"Load default db_path", missing.Store.DbPath, filepath.Join(dir, "state.db")},
				{"Load default error_log_path", missing.Log.ErrorLogPath, filepath.Join(dir, "errors.log")},
				{"relative db_path", relative.Store.DbPath, filepath.Join(dir, "foo.db")},
				{"relative error_log_path", relative.Log.ErrorLogPath, filepath.Join(dir, "logs", "e.log")},
				{"EffectiveDbPath of empty", effective, filepath.Join(dir, "state.db")},
				{"TemplatesDir", config.TemplatesDir(), filepath.Join(dir, "templates")},
			} {
				if tc.got != tc.want {
					t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
				}
			}
		})
	}
}

// TestLoadRecordsWhetherDbPathIsSet: Store.DbPathSet is true exactly when the file
// sets [store] db_path (any value, the default's spelling and "" included, in
// any letter case), and false for a missing file, a file without the key, one
// that sets only other [store] keys, and a malformed one. The old-layout refusal
// reads it to skip the check for a configured store.
func TestLoadRecordsWhetherDbPathIsSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"missing file", filepath.Join(t.TempDir(), "absent.toml"), false},
		{"empty file", makeConfigFile(t, ""), false},
		{"other table only", makeConfigFile(t, "[relay]\npoll_base_ms = 250\n"), false},
		{"other [store] key only", makeConfigFile(t, "[store]\nbusy_timeout_ms = 2500\n"), false},
		{"absolute path", makeConfigFile(t, "[store]\ndb_path = \"/x.db\"\n"), true},
		{"relative path", makeConfigFile(t, "[store]\ndb_path = \"x.db\"\n"), true},
		{"the default's own text", makeConfigFile(t, "[store]\ndb_path = \"~/.config/agent-director/state.db\"\n"), true},
		{"empty string", makeConfigFile(t, "[store]\ndb_path = \"\"\n"), true},
		{"table and key in another letter case", makeConfigFile(t, "[STORE]\nDB_PATH = \"/x.db\"\n"), true},
		{"malformed file", makeConfigFile(t, "[store\ndb_path = \"/x.db\"\n"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := config.Load(tc.path)

			if cfg.Store.DbPathSet != tc.want {
				t.Errorf("Store.DbPathSet = %t, want %t", cfg.Store.DbPathSet, tc.want)
			}
		})
	}
}

// quoted wraps s in TOML double-quoted-string syntax with minimal escaping.
func quoted(s string) string {
	return "\"" + strings.ReplaceAll(s, "\\", "\\\\") + "\""
}
