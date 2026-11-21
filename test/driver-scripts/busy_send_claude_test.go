package driverscripts_test

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPaneHookPipes (Task t2.xis.oo.u8; Subtask t3.xis.wm.hg.k1): the line
// pane-hook.sh types runs the hook as the pane shell's direct child, with its
// stdout in out, its stderr in err and its exit status in rc. With
// --stderr-pipe the hook's stderr is a pipe, and with --stdout-pipe its
// stdout, as Claude Code gives them (busy send's owned wake and steer write
// only to a stream that takes a write deadline, which a regular file does
// not), the other stream staying a file; without either the line is as
// before. A fake tmux keeps the typed line, and bash runs it as the pane's
// shell would, with a stub agent-director.
func TestPaneHookPipes(t *testing.T) {
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
# stub hook: reports its stdout's and stderr's kinds and its parent, writes to stderr, exits 2
o=file; [ -p /dev/stdout ] && o=pipe
e=file; [ -p /dev/stderr ] && e=pipe
echo "$o $e $PPID"
printf 'to stderr' >&2
exit 2
`, 0o755)
	for _, tc := range []struct {
		name  string
		flags []string
		kind  string // stdout's, then stderr's
	}{
		{"default", nil, "file file"},
		{"--stderr-pipe", []string{"--stderr-pipe"}, "file pipe"},
		{"--stdout-pipe", []string{"--stdout-pipe"}, "pipe file"},
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

// The mixed-version hook stays the pane's direct child while observation
// descriptors and either delayed stream remain separate. Background mode must
// leave the pane available before the pending hook's release (TR4-1/TR4-2).
func TestPaneHookObservedFIFO(t *testing.T) {
	for _, mode := range []string{"foreground", "background"} {
		for _, stream := range []string{"stdout", "stderr"} {
			t.Run(mode+"/"+stream, func(t *testing.T) {
				db := newStore(t)
				if r := run(t, nil, "", "sqlite3", db, `INSERT INTO spawns VALUES ('id-m', '%8', '');`); r.code != 0 {
					t.Fatal(r)
				}
				bin, observation := t.TempDir(), t.TempDir()
				writeFile(t, bin, "tmux", `#!/bin/bash
[[ " $* " == *" -l -- "* ]] && printf '%s\n' "${@: -1}" > "$PANE_COMMANDS"
exit 0
`, 0o755)
				writeFile(t, bin, "agent-director", "#!/bin/bash\nexit 91\n", 0o755)
				executable := filepath.Join(bin, "captured A's runtime")
				writeFile(t, bin, filepath.Base(executable), `#!/bin/bash
read -r plan <&198
printf '%s %s %s\n' "$plan" "$PPID" "$$" >&200
read -r release <&199
[[ "$release" == release ]] || exit 92
printf 'captured %s\n' "$PPID"
printf 'whole\nstream' >&2
exit 2
`, 0o755)
				writeFile(t, observation, "plan.json", "plan\n", 0o600)
				pipes := map[string]*os.File{}
				for _, name := range []string{"release", "events", "stream", "commands", "free"} {
					path := filepath.Join(observation, name)
					if r := run(t, nil, "", "mkfifo", path); r.code != 0 {
						t.Fatal(r)
					}
					f, err := os.OpenFile(path, os.O_RDWR, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { f.Close() })
					pipes[name] = f
				}
				readLine := func(name string) (string, error) {
					if err := pipes[name].SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					return bufio.NewReader(pipes[name]).ReadString('\n')
				}
				env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "PANE_HOOK_DB=" + db,
					"PANE_COMMANDS=" + filepath.Join(observation, "commands")}
				pane := exec.Command("bash", "--noprofile", "--norc")
				pane.Env, pane.Stdin = env, pipes["commands"]
				if err := pane.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				job := ""
				go func() { done <- pane.Wait() }()
				t.Cleanup(func() {
					pipes["release"].WriteString("release\n")
					pipes["commands"].WriteString("wait\nexit\n")
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						pane.Process.Kill()
						<-done
					}
					if job != "" {
						os.RemoveAll(job)
					}
				})
				args := []string{"--executable", executable, "--observe-commit", observation,
					"--" + stream + "-pipe", "--stream-fifo", filepath.Join(observation, "stream"), "--no-wait"}
				if mode == "background" {
					args = append(args, "--background")
				}
				r := run(t, env, "", driverScript(t, "pane-hook.sh"), append(args, "id-m", "{}")...)
				job = strings.TrimSpace(r.stdout)
				if r.code != 0 || !strings.HasPrefix(job, "/tmp/pane-hook.") {
					t.Fatalf("prepare: %s", r)
				}
				event, err := readLine("events")
				fields := strings.Fields(event)
				if err != nil || len(fields) != 3 || fields[0] != "plan" || fields[1] != strconv.Itoa(pane.Process.Pid) {
					t.Fatalf("hook identity: %q %v", event, err)
				}
				if mode == "background" {
					if _, err := pipes["commands"].WriteString("printf 'pane-free\\n' > " + filepath.Join(observation, "free") + "\n"); err != nil {
						t.Fatal(err)
					}
					if free, err := readLine("free"); err != nil || free != "pane-free\n" {
						t.Fatalf("pane serialized a pending hook: %q %v", free, err)
					}
					pid, err := os.ReadFile(filepath.Join(job, "pid"))
					if err != nil || strings.TrimSpace(string(pid)) != fields[2] {
						t.Fatalf("background PID: %q %v; hook %s", pid, err, fields[2])
					}
				}
				if _, err := pipes["release"].WriteString("release\n"); err != nil {
					t.Fatal(err)
				}
				got := run(t, env, "", driverScript(t, "pane-hook.sh"), "--timeout", "3", "--wait", job)
				captured := "captured " + strconv.Itoa(pane.Process.Pid) + "\n"
				wantOut, wantErr, wantFIFO := captured, "", "whole\nstream"
				if stream == "stdout" {
					wantOut, wantErr, wantFIFO = "", "whole\nstream", captured
				}
				if got.code != 2 || got.stdout != wantOut || got.stderr != wantErr {
					t.Fatalf("collect: %s; want exit 2, stdout %q, stderr %q", got, wantOut, wantErr)
				}
				if err := pipes["stream"].SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				bytes := make([]byte, len(wantFIFO))
				if _, err := io.ReadFull(pipes["stream"], bytes); err != nil || string(bytes) != wantFIFO {
					t.Fatalf("FIFO: %q %v; want %q", bytes, err, wantFIFO)
				}
			})
		}
	}
}
