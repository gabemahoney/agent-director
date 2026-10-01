package main_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The configured action timeout bounds read-pane's capture (SR-20.6,
// AC-CFG-02); a capture timeout makes no follow-up lookup (SR-7.3).

// TestTmuxConfigReadPaneCLIHungCapture: a CLI read-pane whose capture hangs
// fails naming the capture and the configured 0.3 s, well before the default.
func TestTmuxConfigReadPaneCLIHungCapture(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const timeout = 300 * time.Millisecond
	home, id, socket := seedKillRow(t, store.StateWaiting)
	token, _, storeID := launchIdentity(t, home, id)
	name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
	faketmuxfix.Tables{}.Write(t, socket, killTable(
		readPaneSession("$3", name, token, id, storeID, apitest.TestPaneID, apitest.TestPanePID, "")))
	faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(tmux.CallCapture).Bound(fakeHangBound))
	apitest.WriteTmuxConfig(t, filepath.Join(directorDir(home), "config.toml"),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, timeout.Milliseconds()))

	began := time.Now()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "read-pane", "--claude-instance-id", id)
	elapsed := time.Since(began)
	if code != 1 || stdout != "" {
		t.Fatalf("read-pane exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrTmuxUnresponsive" {
		t.Errorf("err_name = %q; want ErrTmuxUnresponsive (description %q)", env.ErrName, env.ErrDescription)
	}
	apitest.AssertDescription(t, env.ErrDescription, apitest.DescCallTimeout(tmux.CallCapture, timeout))
	if def := (config.Tmux{}).EffectiveActionTimeout(); elapsed >= def {
		t.Errorf("read-pane took %v; want well under the default action timeout %v", elapsed, def)
	}
	assertInvocationKinds(t, home, "list-sessions", "list-panes", "capture-pane")
}
