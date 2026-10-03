package main_test

// advice_follow_config_cli_test.go (b.fji G2, CLI half): a refused [tmux]
// config reaches a CLI caller as an error envelope carrying the advice, and
// following it lets the verb run. The drop-or-zero matrix, its known-broken
// case included, is internal/config's TestAdviceFollow_G2_TmuxRefusalMissingOrZeroGivesDefault.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestAdviceFollow_G2_CLITmuxRefusalMissingKeyGivesDefault: G2 "refused [tmux]
// values: ... . A missing key, or 0, gives the default." Dropping the refused key lets list run.
func TestAdviceFollow_G2_CLITmuxRefusalMissingKeyGivesDefault(t *testing.T) {
	const advice = "A missing key, or 0, gives the default."
	key := config.TmuxStoppingWindowSeconds
	home := t.TempDir()
	cfgPath := filepath.Join(directorDir(home), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath, apitest.TmuxInt(key, config.MinStoppingWindowSeconds-1))

	stdout, stderr, code := runCLIWithHome(t, home, "list")
	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrConfigMalformed")
	for _, want := range []string{"[tmux] " + key.Name() + " ", advice} {
		if !strings.Contains(env.ErrDescription, want) {
			t.Fatalf("description %q lacks %q", env.ErrDescription, want)
		}
	}

	apitest.WriteTmuxConfig(t, cfgPath) // the refused key, the file's only one, dropped
	stdout, stderr, code = runCLIWithHome(t, home, "list")

	if code != 0 || stderr != "" {
		t.Fatalf("list after following %q: exit=%d stderr=%q stdout=%q", advice, code, stderr, stdout)
	}
}
