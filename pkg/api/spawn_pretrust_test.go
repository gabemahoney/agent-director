package api_test

// spawn_pretrust_test.go covers plain spawn's pre-trust through the Go client
// (SR-22.6, SR-5.1; AC-SPN-08): SpawnParams.NoPreTrust is recorded on the
// inserted row's no_pre_trust, the trust entry is written into the spawn's
// CLAUDE_CONFIG_DIR only when pre-trust is allowed, and SpawnResult.PreTrust
// (JSON pre_trust) reports what the launch did. The spawnEnv fixture is in
// spawn_test.go; the .claude.json fixture is in pretrust_fixture_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSpawnRecordsNoPreTrust: Client.Spawn records NoPreTrust on the row,
// writes the trust entry into the spawn's CLAUDE_CONFIG_DIR only when allowed,
// and reports the outcome as pre_trust; a missing or unwritable .claude.json,
// or a relative CLAUDE_CONFIG_DIR (b.nje), still spawns. $HOME/.claude.json is
// never touched.
func TestSpawnRecordsNoPreTrust(t *testing.T) {
	cases := []struct {
		name         string
		file         trustFile
		noPreTrust   bool
		wantCol      int64 // the row's no_pre_trust
		wantEntry    bool  // the entry is written; otherwise the file is left as seeded
		wantPreTrust string
		relative     bool // CLAUDE_CONFIG_DIR is "rel" (seedRelativeTrustConfig)
	}{
		{"ok: allowed writes the entry", trustLacksEntry, false, 0, true, "ok", false},
		{"skipped: opted out writes nothing", trustLacksEntry, true, 1, false, "skipped", false},
		{"failed: .claude.json missing still spawns", trustMissing, false, 0, false, "failed", false},
		{"failed: .claude.json unwritable still spawns", trustUnwritable, false, 0, false, "failed", false},
		{"failed: relative CLAUDE_CONFIG_DIR is refused, still spawns", trustLacksEntry, false, 0, false, "failed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			homeJSON := filepath.Join(env.home, ".claude.json")
			if err := os.WriteFile(homeJSON, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write home .claude.json: %v", err)
			}
			var c trustConfig
			if tc.relative {
				c = seedRelativeTrustConfig(t, tc.file)
			} else {
				c = seedTrustConfig(t, t.TempDir(), tc.file)
			}
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}

			res, err := env.c.Spawn(api.SpawnParams{
				CWD:              cwd,
				ClaudeInstanceID: "pretrust-" + uuid.NewString()[:8],
				ExtraEnv:         c.extraEnv(),
				NoPreTrust:       tc.noPreTrust,
			})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if res.PreTrust != tc.wantPreTrust {
				t.Errorf("SpawnResult.PreTrust = %q; want %q", res.PreTrust, tc.wantPreTrust)
			}
			checkPreTrustJSON(t, res, tc.wantPreTrust)

			row, err := apitest.ReadSpawnColumns(env.dbPath, res.ClaudeInstanceID)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			if row.NoPreTrust != tc.wantCol {
				t.Errorf("no_pre_trust = %#v; want int64(%d)", row.NoPreTrust, tc.wantCol)
			}
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("creates = %d; want 1", n)
			}
			c.check(t, cwd, tc.wantEntry, "after the spawn")
			if b, err := os.ReadFile(homeJSON); err != nil || string(b) != "{}" {
				t.Errorf("home .claude.json = %q (err %v); want untouched {}", b, err)
			}
		})
	}
}
