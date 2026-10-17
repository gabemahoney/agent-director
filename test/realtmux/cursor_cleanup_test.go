package realtmux_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// A transport reply can precede process retirement. Keep each identity alive
// for its first observation, then consult the real process reader.
type retiringCursorProcesses struct {
	tmux.ProcChecker
	identities map[int]string
	reads      map[int]int
	unknown    bool
}

func (p *retiringCursorProcesses) StartTime(pid int) (string, bool, bool) {
	if start, ok := p.identities[pid]; ok {
		p.reads[pid]++
		if p.reads[pid] == 1 {
			if p.unknown {
				return "", false, false
			}
			return start, true, true
		}
	}
	return p.ProcChecker.StartTime(pid)
}

type cursorCleanupErrors struct {
	testing.TB
	errors []string
}

func (t *cursorCleanupErrors) Errorf(format string, args ...any) {
	t.errors = append(t.errors, fmt.Sprintf(format, args...))
}

// t2.jf1.6d.7g: reproduce the full-suite cleanup transport/death race using
// the existing private-server fixture and raw tmux binary seam.
// Serial: changes HOME, PATH, TMUX_TMPDIR and the raw tmux binary.
func TestCursorCleanupRetirementWaitsForCurrentGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux cleanup regression")
	}
	for _, tc := range []struct {
		name       string
		stdout     string
		stderr     string
		exit       int
		unknown    bool
		wantErrors bool
	}{
		{name: "retiring", stderr: "server exited unexpectedly\n", exit: 1},
		{name: "unknown-process-needs-proof", stderr: "server exited unexpectedly\n", exit: 1, unknown: true},
		{name: "arbitrary-error", stderr: "permission denied\n", exit: 1, wantErrors: true},
		{name: "wrong-exit", stderr: "server exited unexpectedly\n", exit: 2, wantErrors: true},
		{name: "unexpected-output", stdout: "untrusted output\n", stderr: "server exited unexpectedly\n", exit: 1, wantErrors: true},
		{name: "other-socket", stderr: "no server running on /other/socket\n", exit: 1, wantErrors: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkCursorCleanupRetirement(t, tc.stdout, tc.stderr, tc.exit, tc.unknown, tc.wantErrors)
		})
	}
}

func checkCursorCleanupRetirement(t *testing.T, stdout, stderr string, exit int, unknown, wantErrors bool) {
	t.Helper()
	f := newSpawnFix(t)
	original := f.mustSpawn(t, newInstanceID("old"))
	current := f.mustSpawn(t, newInstanceID("new"))
	// Model a resumed row publishing a new gateway while launch still holds
	// the original identity. Both real panes belong to this private server.
	original.ID = current.ID
	pc := &retiringCursorProcesses{ProcChecker: f.PC, identities: map[int]string{
		int(original.Row.TmuxServerPID.(int64)): original.Row.TmuxServerStarttime.(string),
		int(current.Row.PanePID.(int64)):        current.Row.PaneStarttime.(string),
	}, reads: map[int]int{}, unknown: unknown}
	f.PC = pc
	actualTmux := tmuxPath
	defer func() { tmuxPath = actualTmux }()
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	wrapper := filepath.Join(dir, "tmux")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$4" in
list-panes) printf '%%b' %q; printf '%%b' %q >&2; exit %d ;;
kill-server) %q "$@"; printf 'no server running on %%s\n' "$3" >&2; exit 1 ;;
esac
exec %q "$@"
`, log, stdout, stderr, exit, actualTmux, actualTmux)
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	tmuxPath = wrapper
	observed := &cursorCleanupErrors{TB: t}
	stopCursorPaneGateways(observed, f, original)
	if wantErrors {
		if len(observed.errors) != 1 || !strings.Contains(observed.errors[0], "snapshot fixture panes:") {
			t.Errorf("unexpected transport must remain a cleanup failure: %v", observed.errors)
		}
	} else if len(observed.errors) != 0 {
		t.Errorf("retirement transport cleanup errors: %v", observed.errors)
	}
	for pid := range pc.identities {
		if pc.reads[pid] < 2 || !pidGone(pid) {
			t.Errorf("cleanup returned before retirement proof: pid=%d reads=%d %s", pid, pc.reads[pid], procState(pid))
		}
	}
	if !pidGone(int(original.Row.PanePID.(int64))) {
		t.Error("cleanup returned with original gateway alive")
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("injected retirement transport commands:\n%s", commands)
}
