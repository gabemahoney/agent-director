package main_test

// The hook verb with no usable HOME (b.4uz): the store's "~/" path is refused,
// so the hook opens and creates no store under the passwd home or the cwd.

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// TestHookWithoutHOMEOpensNoStore: with HOME empty or unset the hook exits 0, writes nothing under the
// passwd home or the cwd, logs one line, and prints only a relayed PermissionRequest's deny (b.4uz).
func TestHookWithoutHOMEOpensNoStore(t *testing.T) {
	const (
		permission   = `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`
		sessionStart = `{"hook_event_name":"SessionStart","source":"startup"}`
	)
	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	relayOn := []string{hook.EnvRelayMode + "=" + hook.RelayModeOn}
	for _, tc := range []struct {
		name    string
		unset   bool
		env     []string
		payload string
		want    string // the whole stdout
	}{
		{"PermissionRequest relay on HOME empty", false, relayOn, permission, deny},
		{"PermissionRequest relay on HOME unset", true, relayOn, permission, deny},
		{"PermissionRequest relay off", false, nil, permission, ""},
		{"SessionStart relay on", false, relayOn, sessionStart, ""},
		{"SessionStart relay off HOME unset", true, nil, sessionStart, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwdDir := passwdAgentDir(t)
			cwd := t.TempDir()
			environ := append([]string{"PATH=" + os.Getenv("PATH")}, tc.env...)
			if !tc.unset {
				environ = append(environ, "HOME=")
			}
			stdout, stderr, code := mustRun(t, cliOpts{dir: cwd, env: environ, stdin: tc.payload, deadline: surfaceDeadline}, "hook")
			if code != 0 || stdout != tc.want {
				t.Errorf("hook exit=%d stdout=%q; want 0 and %q (stderr=%q)", code, stdout, tc.want, stderr)
			}
			if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 ||
				!strings.Contains(lines[0], "hook: open store: ") || !strings.Contains(lines[0], "no home directory") {
				t.Errorf("stderr = %q; want exactly one hook: open store line naming no home directory", stderr)
			}
			if _, err := os.Lstat(passwdDir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("hook created %s under the passwd home (Lstat: %v)", passwdDir, err)
			}
			assertHomeTree(t, cwd)
		})
	}
}
