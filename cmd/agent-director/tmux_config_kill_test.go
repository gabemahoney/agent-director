package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The configured query timeout bounds kill's lookup (SR-20.6, AC-CFG-02), and a
// running `serve --stdio` keeps the value it started with (SR-4.1).

// hungKillRow seeds a live row whose socket's lookup hangs and writes
// query_timeout_ms; it returns the HOME, the row's id and the config path.
func hungKillRow(t *testing.T, timeout time.Duration) (home, id, cfgPath string) {
	t.Helper()
	home, id, socket := seedKillRow(t, store.StateWaiting)
	faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(tmux.CallLookup).Bound(fakeHangBound))
	cfgPath = filepath.Join(directorDir(home), "config.toml")
	writeQueryTimeout(t, cfgPath, timeout)
	return home, id, cfgPath
}

// writeQueryTimeout writes a config whose only setting is query_timeout_ms.
func writeQueryTimeout(t *testing.T, cfgPath string, d time.Duration) {
	t.Helper()
	apitest.WriteTmuxConfig(t, cfgPath, apitest.TmuxInt(config.TmuxQueryTimeoutMs, d.Milliseconds()))
}

// assertHungLookup checks an ErrTmuxUnresponsive whose description names the
// lookup and timeout.
func assertHungLookup(t *testing.T, errName, desc string, timeout time.Duration) {
	t.Helper()
	if errName != "ErrTmuxUnresponsive" {
		t.Errorf("err_name = %q; want ErrTmuxUnresponsive (description %q)", errName, desc)
	}
	apitest.AssertDescription(t, desc, apitest.DescCallTimeout(tmux.CallLookup, timeout))
}

// TestTmuxConfigKillCLIHungLookup: a CLI kill whose lookup hangs fails with the
// configured 0.3 s, well before the default query timeout (AC-CFG-02).
func TestTmuxConfigKillCLIHungLookup(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const timeout = 300 * time.Millisecond
	home, id, _ := hungKillRow(t, timeout)

	began := time.Now()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", id)
	elapsed := time.Since(began)
	if code != 1 || stdout != "" {
		t.Fatalf("kill exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	env := parseEnvelope(t, stderr)
	assertHungLookup(t, env.ErrName, env.ErrDescription, timeout)
	if def := (config.Tmux{}).EffectiveQueryTimeout(); elapsed >= def {
		t.Errorf("kill took %v; want well under the default query timeout %v", elapsed, def)
	}
	assertInvocationKinds(t, home, "list-sessions")
}

// TestTmuxConfigKillServeKeepsStartupTimeout: a running serve's MCP kill names
// the query timeout it started with after a rewrite, and the new one after a restart.
func TestTmuxConfigKillServeKeepsStartupTimeout(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const startup, rewritten = 300 * time.Millisecond, 500 * time.Millisecond
	home, id, cfgPath := hungKillRow(t, startup)
	env := []string{
		"PATH=" + fakeDir + ":" + os.Getenv("PATH"),
		"TMUX_TMPDIR=" + spawnTmuxTmpdir(t, home),
	}

	var srv *serveSession
	t.Cleanup(func() { srv.kill() })
	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"startup_value", func(t *testing.T) {
			srv = startServe(t, home, env...)
			srv.initialize(t)
			srv.killNamesTimeout(t, id, startup)
		}},
		{"keeps_startup_value_after_rewrite", func(t *testing.T) {
			writeQueryTimeout(t, cfgPath, rewritten)
			srv.killNamesTimeout(t, id, startup)
		}},
		{"new_value_after_restart", func(t *testing.T) {
			srv.stop(t)
			srv = startServe(t, home, env...)
			srv.initialize(t)
			srv.killNamesTimeout(t, id, rewritten)
			srv.stop(t)
		}},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.run) {
			t.FailNow()
		}
	}
}

// killNamesTimeout calls the MCP kill tool on id and checks it fails on the hung
// lookup naming timeout.
func (s *serveSession) killNamesTimeout(t *testing.T, id string, timeout time.Duration) {
	t.Helper()
	s.nextID++
	r := s.request(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"kill","arguments":{"claude_instance_id":%q}}}`+"\n", s.nextID, id))
	if r.Error == nil {
		t.Fatalf("kill tool result = %+v; want an error", r.Result)
	}
	assertHungLookup(t, r.Error.Data.ErrName, r.Error.Message, timeout)
}
