package driverscripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPaneHookStderrPipe (Task t2.xis.oo.u8): the line pane-hook.sh types runs
// the hook as the pane shell's direct child, with its stdout in out, its
// stderr in err and its exit status in rc. With --stderr-pipe the hook's
// stderr is a pipe, as Claude Code gives it (busy send's owned wake writes
// only to a stream that takes a write deadline, which a regular file does
// not), and rc follows a complete err; without it the line is as before. A
// fake tmux keeps the typed line, and bash runs it as the pane's shell would,
// with a stub agent-director.
func TestPaneHookStderrPipe(t *testing.T) {
	db := newStore(t)
	if r := run(t, nil, "", "sqlite3", db, `INSERT INTO spawns VALUES ('id-p', '%7', '');`); r.code != 0 {
		t.Fatalf("seed row: %s", r)
	}
	bin := t.TempDir()
	writeFile(t, bin, "tmux", `#!/bin/bash
# fake tmux: keeps the text send-keys -l types
[[ " $* " == *" -l -- "* ]] && printf '%s' "${@: -1}" > "$TYPED"
exit 0
`, 0o755)
	writeFile(t, bin, "agent-director", `#!/bin/bash
# stub hook: reports its stderr's kind and its parent, writes to stderr, exits 2
if [ -p /dev/stderr ]; then echo "pipe $PPID"; else echo "file $PPID"; fi
printf 'to stderr' >&2
exit 2
`, 0o755)
	for _, tc := range []struct {
		name  string
		flags []string
		kind  string
	}{
		{"default", nil, "file"},
		{"--stderr-pipe", []string{"--stderr-pipe"}, "pipe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typed := filepath.Join(t.TempDir(), "typed")
			env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "PANE_HOOK_DB=" + db, "TYPED=" + typed}
			r := run(t, env, "", driverScript(t, "pane-hook.sh"), append(tc.flags, "--no-wait", "id-p", "{}")...)
			job := strings.TrimSpace(r.stdout)
			line, err := os.ReadFile(typed)
			if r.code != 0 || !strings.HasPrefix(job, "/tmp/pane-hook.") || err != nil {
				t.Fatalf("pane-hook.sh %v: %s; typed %q, %v", tc.flags, r, line, err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(job) })
			pane := exec.Command("bash", "--noprofile", "--norc", "-c", string(line))
			pane.Env = env
			if out, err := pane.CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("the pane shell ran %q: %v, %q", line, err, out)
			}
			for name, want := range map[string]string{
				"out": tc.kind + " " + strconv.Itoa(pane.Process.Pid) + "\n", "err": "to stderr", "rc": "2\n",
			} {
				if got, err := os.ReadFile(filepath.Join(job, name)); err != nil || string(got) != want {
					t.Errorf("%s = %q, %v; want %q", name, got, err, want)
				}
			}
		})
	}
}
