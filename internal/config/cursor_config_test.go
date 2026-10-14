package config_test

import (
	"github.com/gabemahoney/agent-director/internal/config"
	"path/filepath"
	"testing"
)

func TestCursorLaunchPathIsWholeAndResolved(t *testing.T) {
	home := homeDir(t)
	for _, tc := range []struct{ path, want string }{{"", ""}, {"~/cursor agent", filepath.Join(home, "cursor agent")}, {"/opt/cursor agent --flag", "/opt/cursor agent --flag"}} {
		t.Run(tc.path, func(t *testing.T) {
			cfg, err := config.Load(makeConfigFile(t, "[launch]\ncursor_path = "+quoted(tc.path)+"\n"))
			if err != nil || cfg.Launch.CursorPath != tc.want {
				t.Fatalf("cursor_path %q %v; want %q", cfg.Launch.CursorPath, err, tc.want)
			}
		})
	}
}
