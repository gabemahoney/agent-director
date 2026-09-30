package api_test

// spawn_pretrust_test.go covers plain spawn's pre-trust through the Go client
// (SR-22.6, SR-5.1; AC-SPN-08): SpawnParams.NoPreTrust is recorded on the
// inserted row's no_pre_trust, and the trust entry is written into the
// spawn's CLAUDE_CONFIG_DIR only when pre-trust is allowed. The spawnEnv
// fixture is in spawn_test.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// trustEntry reports whether the .claude.json at path marks cwd's folder
// trust as accepted; the raw bytes are returned for failure messages.
func trustEntry(t *testing.T, path, cwd string) (bool, []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var top struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("parse %s: %v (%q)", path, err, b)
	}
	return top.Projects[cwd].HasTrustDialogAccepted, b
}

// TestSpawnRecordsNoPreTrust: Client.Spawn records NoPreTrust on the row and
// writes the trust entry into the spawn's CLAUDE_CONFIG_DIR only when allowed;
// a missing .claude.json still spawns. $HOME/.claude.json is never touched.
func TestSpawnRecordsNoPreTrust(t *testing.T) {
	cases := []struct {
		name       string
		noPreTrust bool
		seedConfig bool  // whether CLAUDE_CONFIG_DIR holds a .claude.json
		wantCol    int64 // the row's no_pre_trust
		wantEntry  bool
	}{
		{"allowed writes the entry", false, true, 0, true},
		{"opted out writes nothing", true, true, 1, false},
		{"allowed with .claude.json missing still spawns", false, false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			homeJSON := filepath.Join(env.home, ".claude.json")
			if err := os.WriteFile(homeJSON, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write home .claude.json: %v", err)
			}
			cfgDir := t.TempDir()
			cfgJSON := filepath.Join(cfgDir, ".claude.json")
			if tc.seedConfig {
				if err := os.WriteFile(cfgJSON, []byte(`{"keep":1}`), 0o600); err != nil {
					t.Fatalf("write config .claude.json: %v", err)
				}
			}
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}

			id := mustSpawn(t, env, api.SpawnParams{
				CWD:              cwd,
				ClaudeInstanceID: "pretrust-" + uuid.NewString()[:8],
				ExtraEnv:         map[string]string{"CLAUDE_CONFIG_DIR": cfgDir},
				NoPreTrust:       tc.noPreTrust,
			})

			row, err := apitest.ReadSpawnColumns(env.dbPath, id)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			if row.NoPreTrust != tc.wantCol {
				t.Errorf("no_pre_trust = %#v; want int64(%d)", row.NoPreTrust, tc.wantCol)
			}
			if !tc.seedConfig {
				if _, err := os.Stat(cfgJSON); !os.IsNotExist(err) {
					t.Errorf("config .claude.json stat err = %v; want not created", err)
				}
			} else {
				got, raw := trustEntry(t, cfgJSON, cwd)
				if got != tc.wantEntry {
					t.Errorf("trust entry for %s = %v; want %v (file %s)", cwd, got, tc.wantEntry, raw)
				}
				if !tc.wantEntry && string(raw) != `{"keep":1}` {
					t.Errorf("config .claude.json = %s; want untouched", raw)
				}
			}
			if b, err := os.ReadFile(homeJSON); err != nil || string(b) != "{}" {
				t.Errorf("home .claude.json = %q (err %v); want untouched {}", b, err)
			}
		})
	}
}
