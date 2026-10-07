package api_test

// pretrust_lock_wait_test.go pins b.kr4 at the Client: plain spawn, spawn
// with reuse and resume each wait for a held .claude.json lock for the loaded
// config's [pre_trust] lock_wait_seconds. Trust files are
// pretrust_fixture_test.go's; rows and runners the kill and reuse fixtures'.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ptwHoldLock makes c's lock dir as a live holder of Claude Code's lock
// would, refreshed now.
func ptwHoldLock(t *testing.T, c trustConfig) {
	t.Helper()
	if err := os.Mkdir(c.path+".lock", 0o700); err != nil {
		t.Fatalf("hold lock: %v", err)
	}
}

// TestLaunchPathsUseConfiguredLockWait: with lock_wait_seconds = 1 and the
// lock held fresh throughout, each launch path gives up after 1 s, reports
// pre_trust failed and still launches. The 12 s default would outlast the
// 10 s stale limit, break the lock and report ok.
func TestLaunchPathsUseConfiguredLockWait(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	cases := []struct {
		name   string
		launch func(t *testing.T, e *killEnv, c *api.Client) (preTrust string, trust trustConfig, cwd string)
	}{
		{"plain spawn", func(t *testing.T, e *killEnv, c *api.Client) (string, trustConfig, string) {
			advKillServer(t, e)
			trust, cwd := seedTrustConfig(t, t.TempDir(), trustLacksEntry), t.TempDir()
			ptwHoldLock(t, trust)
			res, err := c.Spawn(api.SpawnParams{ClaudeInstanceID: "ptw-" + uuid.NewString()[:8], CWD: cwd,
				ExtraEnv: trust.extraEnv()})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			return res.PreTrust, trust, cwd
		}},
		{"spawn with reuse", func(t *testing.T, e *killEnv, c *api.Client) (string, trustConfig, string) {
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			ptwHoldLock(t, r.Trust)
			res, err := c.Spawn(reuseParams(t, r, reuseRequest{}))
			if err != nil {
				t.Fatalf("reuse Spawn: %v", err)
			}
			return res.PreTrust, r.Trust, r.CWD
		}},
		{"resume", func(t *testing.T, e *killEnv, c *api.Client) (string, trustConfig, string) {
			r := e.seedResumable(t, time.Hour, agentGone)
			ptwHoldLock(t, r.Trust)
			res, err := c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			return res.PreTrust, r.Trust, r.CWD
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
			e := newReuseEnv(t)
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			apitest.WriteKeysConfig(t, cfgPath, apitest.ConfigKeys{PreTrustLockWaitSeconds: 1})
			c, logs, err := e.clientFor(t, cfgPath)
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}
			start := time.Now()

			got, trust, cwd := tc.launch(t, e, c)
			took := time.Since(start)

			if got != "failed" {
				t.Errorf("pre_trust = %q after %s; want failed once the configured 1 s ran out (log %q)",
					got, took, logs)
			}
			if took < time.Second {
				t.Errorf("launch took %s; want at least the configured 1 s, so failed comes from the lock wait (log %q)",
					took, logs)
			}
			trust.check(t, cwd, false, "after the launch")
			if _, err := os.Stat(trust.path + ".lock"); err != nil {
				t.Errorf("lock dir: %v; want the holder's left in place", err)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("creates = %d; want 1 (pre-trust never stops the launch)", n)
			}
		})
	}
}
