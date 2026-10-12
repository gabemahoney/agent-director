package api_test

// spawn_pretrust_test.go covers plain spawn's pre-trust through the Go client
// (SR-22.6, SR-5.1; AC-SPN-08): SpawnParams.NoPreTrust is recorded on the
// inserted row's no_pre_trust, the trust entry is written into the spawn's
// CLAUDE_CONFIG_DIR only when pre-trust is allowed, and SpawnResult.PreTrust
// (JSON pre_trust) reports what the launch did. The spawnEnv fixture is in
// spawn_test.go; the .claude.json fixture is in pretrust_fixture_test.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// codexFailedWarning is the one line a failed Codex pre-trust prints, naming
// CODEX_HOME as given.
const codexFailedWarning = "agent-director: pre-trust failed (CODEX_HOME %q is not an absolute path); the agent may stop at Codex's folder-trust prompt\n"

// codexPreTrustCase is one CODEX_HOME form of a Codex launch: seed makes its
// folder under the test's home (and may move the working directory), and give
// replaces the extra env it gives (nil keeps folder.extraEnv()).
type codexPreTrustCase struct {
	name         string
	seed         func(t *testing.T, home string) codexFolder
	give         map[string]string
	noPreTrust   bool
	wantPreTrust string
	wantNamed    string // the CODEX_HOME the warning names, when failed
}

// codexPreTrustCases are the CODEX_HOME forms and opt-out of SR-5.4, shared by
// the spawn, reuse and resume tests.
func codexPreTrustCases() []codexPreTrustCase {
	abs := func(t *testing.T, home string) codexFolder { return seedCodexFolder(t, filepath.Join(home, "folder")) }
	dflt := func(t *testing.T, home string) codexFolder { return seedDefaultCodexFolder(t, home) }
	rel := func(t *testing.T, _ string) codexFolder { return seedRelativeCodexFolder(t) }
	tilde := seedTildeCodexFolder
	space := func(t *testing.T, _ string) codexFolder { return seedWhitespaceCodexFolder(t) }
	return []codexPreTrustCase{
		{name: "ok: absolute CODEX_HOME", seed: abs, wantPreTrust: "ok"},
		{name: "ok: CODEX_HOME unset, the default .codex", seed: dflt, wantPreTrust: "ok"},
		{name: "ok: CODEX_HOME empty", seed: dflt, give: map[string]string{"CODEX_HOME": ""}, wantPreTrust: "ok"},
		{name: "skipped: opted out, absolute", seed: abs, noPreTrust: true, wantPreTrust: "skipped"},
		{name: "skipped: opted out outranks a relative CODEX_HOME", seed: rel, noPreTrust: true, wantPreTrust: "skipped"},
		{name: "failed: relative CODEX_HOME", seed: rel, wantPreTrust: "failed", wantNamed: "rel"},
		{name: "failed: tilde CODEX_HOME", seed: tilde, wantPreTrust: "failed", wantNamed: "~/codex"},
		{name: "failed: whitespace-only CODEX_HOME", seed: space, wantPreTrust: "failed", wantNamed: "   "},
	}
}

// warning is what a launch of tc prints to the caller.
func (tc codexPreTrustCase) warning() string {
	if tc.wantPreTrust != "failed" {
		return ""
	}
	return fmt.Sprintf(codexFailedWarning, tc.wantNamed)
}

// TestSpawnCodexPreTrust (SR-5.4, SR-5.6): a Codex spawn reports pre_trust ok,
// skipped or failed by its CODEX_HOME and opt-out, a failed one prints its one
// pinned warning to the caller and the others print nothing, the launch goes
// ahead and records no_pre_trust, and neither the Codex folder's tree nor
// $HOME/.claude.json is touched.
func TestSpawnCodexPreTrust(t *testing.T) {
	// Serial: it sets HOME, TMUX, TMUX_TMPDIR and the working directory with t.Setenv, and redirects stderr.
	for _, tc := range codexPreTrustCases() {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			homeJSON := filepath.Join(env.home, ".claude.json")
			if err := os.WriteFile(homeJSON, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write home .claude.json: %v", err)
			}
			folder := tc.seed(t, env.home)
			extra := folder.extraEnv()
			if tc.give != nil {
				extra = tc.give
			}
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}

			var res api.SpawnResult
			warn := captureStderr(t, func() {
				res, err = env.c.Spawn(api.SpawnParams{Agent: "codex", CWD: cwd, InstanceID: "pretrust-" + uuid.NewString()[:8],
					ExtraEnv: extra, NoPreTrust: tc.noPreTrust})
			})

			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if res.PreTrust != tc.wantPreTrust {
				t.Errorf("SpawnResult.PreTrust = %q; want %q", res.PreTrust, tc.wantPreTrust)
			}
			checkPreTrustJSON(t, res, tc.wantPreTrust)
			if warn != tc.warning() {
				t.Errorf("warning = %q; want %q", warn, tc.warning())
			}
			row, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			wantCol := int64(0)
			if tc.noPreTrust {
				wantCol = 1
			}
			if row.NoPreTrust != wantCol {
				t.Errorf("no_pre_trust = %#v; want int64(%d)", row.NoPreTrust, wantCol)
			}
			if row.Agent != "codex" {
				t.Errorf("row agent = %q; want codex", row.Agent)
			}
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("creates = %d; want 1", n)
			}
			folder.assertUnchanged(t, "after the spawn")
			if b, err := os.ReadFile(homeJSON); err != nil || string(b) != "{}" {
				t.Errorf("home .claude.json = %q (err %v); want untouched {}", b, err)
			}
		})
	}
}

// TestSpawnReuseAsCodexPreTrust (SR-5.4, SR-5.6): a spawn with reuse that
// launches Codex reports pre_trust by the call's CODEX_HOME and opt-out as a
// plain spawn does and leaves the Codex folder's tree, and the Claude trust
// directory the finished row named, as they were. The row is a claude row and a
// codex row in turn.
func TestSpawnReuseAsCodexPreTrust(t *testing.T) {
	// Serial: it sets HOME and the working directory with t.Setenv, and redirects stderr.
	for _, rowAgent := range []string{"claude", "codex"} {
		for _, tc := range codexPreTrustCases() {
			t.Run(rowAgent+" row, "+tc.name, func(t *testing.T) {
				e := newReuseEnv(t)
				home := t.TempDir()
				t.Setenv("HOME", home)
				r := e.seedReusable(t, agentGone, reuseRowSpec{Opts: []apitest.SpawnOption{apitest.WithAgent(rowAgent)}})
				folder := tc.seed(t, home)
				p := reuseParams(t, r, reuseRequest{NoPreTrust: tc.noPreTrust})
				p.Agent = "codex"
				p.ExtraEnv = folder.extraEnv()
				if tc.give != nil {
					p.ExtraEnv = tc.give
				}

				var res api.SpawnResult
				var err error
				warn := captureStderr(t, func() { res, _, err = e.reuse(t, p) })

				if err != nil {
					t.Fatalf("reuse Spawn: %v", err)
				}
				if res.PreTrust != tc.wantPreTrust {
					t.Errorf("SpawnResult.PreTrust = %q; want %q", res.PreTrust, tc.wantPreTrust)
				}
				checkPreTrustJSON(t, res, tc.wantPreTrust)
				if warn != tc.warning() {
					t.Errorf("warning = %q; want %q", warn, tc.warning())
				}
				if cols := e.columns(t, r.ID); cols.Agent != "codex" {
					t.Errorf("row agent = %q; want codex after the reuse", cols.Agent)
				}
				if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
					t.Errorf("creates = %d; want 1", n)
				}
				folder.assertUnchanged(t, "after the reuse")
				r.Trust.check(t, r.CWD, false, "after the reuse (the row's Claude trust directory)")
			})
		}
	}
}

// TestSpawnRecordsNoPreTrust: Client.Spawn records NoPreTrust on the row,
// writes the trust entry into the spawn's CLAUDE_CONFIG_DIR only when allowed,
// and reports the outcome as pre_trust; a missing or unwritable .claude.json,
// or a relative CLAUDE_CONFIG_DIR (b.nje), still spawns. $HOME/.claude.json is
// never touched.
func TestSpawnRecordsNoPreTrust(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, the working directory and PWD, TMUX, TMUX_TMPDIR
	// with t.Setenv.
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
				Agent:      "claude",
				CWD:        cwd,
				InstanceID: "pretrust-" + uuid.NewString()[:8],
				ExtraEnv:   c.extraEnv(),
				NoPreTrust: tc.noPreTrust,
			})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if res.PreTrust != tc.wantPreTrust {
				t.Errorf("SpawnResult.PreTrust = %q; want %q", res.PreTrust, tc.wantPreTrust)
			}
			checkPreTrustJSON(t, res, tc.wantPreTrust)

			row, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID)
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
