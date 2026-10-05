package main_test

// The hook verb with no usable HOME (b.4uz): the store's "~/" path is refused,
// so the hook opens and creates no store under the passwd home or the cwd.

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// passwdAgentDir returns the passwd home's .agent-director, failing unless it is
// absent and removing it at cleanup; outside the sandbox (the real store) it skips.
func passwdAgentDir(t *testing.T) string {
	t.Helper()
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("runs only in the sandbox (%s=1): a regression writes the passwd home's store", sandboxguard.EnvVar)
	}
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		t.Fatalf("user.Current() = %v, %v; want an absolute passwd home", u, err)
	}
	dir := filepath.Join(u.HomeDir, ".agent-director")
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s must be absent before the hook runs (Lstat: %v); the sandbox image has none", dir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

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
			stdout, stderr, code, timedOut := runBoundedIn(t, cwd, environ, tc.payload, false, noExecFormDeadline, "hook")
			if timedOut {
				t.Fatalf("hook still running after %s; stderr=%q", noExecFormDeadline, stderr)
			}
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
