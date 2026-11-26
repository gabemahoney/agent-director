package realtmux_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// b.6xx: exercise the executable boundary, since library callers retain their
// own umask. Each mask starts without install.sh or a pre-created socket dir.
func TestCLIRestrictiveUmask(t *testing.T) {
	binDir := t.TempDir()
	bin := buildHookCLI(t, binDir)
	admin := filepath.Join(binDir, "agent-director-admin")
	if out, err := exec.Command("timeout", "120", "go", "build", "-o", admin,
		"github.com/gabemahoney/agent-director/cmd/agent-director-admin").CombinedOutput(); err != nil {
		t.Fatalf("build admin: %v\n%s", err, out)
	}
	baseline := map[string]fs.FileMode{}
	for _, mask := range []string{"022", "077", "0777", "0200"} {
		t.Run(mask, func(t *testing.T) {
			rt := newRealTmux(t)
			// Let production create tmux-<uid>; the harness normally creates it.
			if err := os.Remove(rt.UserDir); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			t.Cleanup(func() { restoreModes(home) })
			t.Setenv("HOME", home)
			unsetEnv(t, "CLAUDE_CONFIG_DIR", "AGENT_DIRECTOR_INSTANCE_ID", "AGENT_DIRECTOR_AGENT")
			stub := t.TempDir()
			channel := newInstanceID("umask")
			// A real pane signals only after the stand-in agent has started.
			body := fmt.Sprintf("#!/bin/sh\nif [ \"${1-}\" = --version ]; then echo 2.1.999; exit; fi\ntmux -S %s wait-for -S %s\nexec sleep 600\n", rt.Socket, channel)
			if err := os.WriteFile(filepath.Join(stub, "claude"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", stub+":"+binDir+":"+os.Getenv("PATH"))
			run := func(program string, args ...string) string {
				t.Helper()
				outPath := filepath.Join(t.TempDir(), "output")
				out, err := os.Create(outPath)
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("bash", append([]string{"-c", "umask \"$1\"; shift; exec timeout -k 2 40 \"$@\"", "umask-test", mask, program}, args...)...)
				cmd.Env = withoutPrefix(os.Environ(), "AGENT_DIRECTOR_")
				cmd.Stdout, cmd.Stderr = out, out
				err = cmd.Run()
				_ = out.Close()
				data, readErr := os.ReadFile(outPath)
				if err != nil || readErr != nil {
					t.Fatalf("%s %v: %v (read %v)\n%s", filepath.Base(program), args, err, readErr, data)
				}
				return string(data)
			}
			if got := run(bin, "list"); strings.TrimSpace(got) != `{"spawns":[]}` {
				t.Fatalf("fresh list = %s", got)
			}
			run(bin, "make-template", "--name", "t1", "--agent", "claude")
			run(bin, "make-template", "--name", "t1", "--agent", "claude", "--overwrite")
			run(bin, "spawn", "--agent", "claude", "--cwd", stub, "--instance-id", newInstanceID("umask"), "--no-pre-trust")
			rt.must(t, "wait-for", channel)
			run(bin, "list")
			// Admin must initialize a separate fresh store under the same mask.
			adminHome := t.TempDir()
			t.Cleanup(func() { restoreModes(adminHome) })
			run(admin, "--home", adminHome, "delete", "--instance-id", "absent")
			for i, root := range []string{filepath.Join(home, ".config"), filepath.Join(adminHome, ".config"), rt.UserDir} {
				err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					info, err := d.Info()
					if err != nil {
						return err
					}
					rel, err := filepath.Rel(root, path)
					if err != nil {
						return err
					}
					key := fmt.Sprintf("%d/%s", i, rel)
					mode := info.Mode().Perm()
					if mask == "022" {
						baseline[key] = mode
					}
					want, ok := baseline[key]
					owner := fs.FileMode(0o600)
					if d.IsDir() {
						owner = 0o700
					}
					if !ok || mode&^want != 0 || mode&owner != owner || mode&0o077 != 0 {
						t.Errorf("%s mode %04o, base mode %04o (present %v); require owner access and no wider permissions", path, mode, want, ok)
					}
					return nil
				})
				if err != nil {
					t.Error(err)
				}
			}
			for _, rel := range []string{"state.db", "state.db.initlock", "errors.log", "templates/t1.toml"} {
				if _, err := os.Stat(filepath.Join(home, ".config/agent-director", rel)); err != nil {
					t.Errorf("required path %s: %v", rel, err)
				}
			}
		})
	}
}
