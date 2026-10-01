package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// TestSocketlessArgvRefused checks argv without -u is logged and refused with
// the default socket's no-server reply; only the bare call and has-session answer.
func TestSocketlessArgvRefused(t *testing.T) {
	defaultSocket := filepath.Join(t.TempDir(), "default")
	t.Setenv("TMUX", defaultSocket+",1,0")
	noServer := tmuxfix.NoServer(defaultSocket)
	cases := []struct {
		name   string
		argv   []string
		want   tmuxfix.Entry
		logged bool
	}{
		{name: "bare"},
		{name: "has-session", argv: []string{"has-session", "-t", "legacy"}, want: tmuxfix.Entry{Exit: 1}},
		{name: "new-session", argv: []string{"new-session", "-d", "-s", "legacy"}, want: noServer, logged: true},
		{name: "send-keys", argv: []string{"send-keys", "-t", "legacy:0.0", "hi"}, want: noServer, logged: true},
		{name: "capture-pane", argv: []string{"capture-pane", "-p", "-t", "legacy"}, want: noServer, logged: true},
		{name: "kill-session", argv: []string{"kill-session", "-t", "legacy"}, want: noServer, logged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
			t.Setenv(faketmuxfix.EnvLog, logPath)
			cmd := exec.Command(faketmuxfix.Binary(t), tc.argv...)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run()
			if code := cmd.ProcessState.ExitCode(); code != tc.want.Exit || stdout.String() != tc.want.Stdout || stderr.String() != tc.want.Stderr {
				t.Errorf("exit %d stdout %q stderr %q, want %d %q %q",
					code, stdout.String(), stderr.String(), tc.want.Exit, tc.want.Stdout, tc.want.Stderr)
			}
			var recs [][]string
			if _, err := os.Stat(logPath); err == nil {
				recs = readLog(t, logPath)
			}
			switch {
			case !tc.logged && len(recs) != 0:
				t.Errorf("log = %q, want nothing logged", recs)
			case tc.logged && (len(recs) != 1 || !reflect.DeepEqual(recs[0][1:], tc.argv)):
				t.Errorf("log = %q, want one record with argv %q", recs, tc.argv)
			}
		})
	}
}
