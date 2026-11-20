package droidfix

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Guard is the shared Path List and recording Guard stand-in for launch tests.
// Tests needing a Path List and Guard stand-in must use NewGuard or CreateGuard.
// It is independent of spawn; callers install Path with its selection seam.
// See docs/architecture.md's droidfix fixture registration.
type Guard struct {
	Path, List string
	program    *Program
	started    string
}

const (
	GuardPass   Mode = "guard-pass" // AskUser blocks, TodoWrite passes
	Signal      Mode = "signal"
	Text        Mode = "text"
	Newlines    Mode = "newlines"
	NUL         Mode = "nul"
	StderrFlood Mode = "stderr-flood"
)

// CreateGuard is the testing-independent setup used by the TS smoke helper.
// The caller owns root and removes it after use. Entries are invented paths;
// neither this fixture nor a launch reads the listed paths.
func CreateGuard(root string) (*Guard, error) {
	g := &Guard{Path: filepath.Join(root, "guard program"), List: filepath.Join(root, "secret-paths.txt")}
	g.program = &Program{Path: g.Path, root: root}
	if err := os.MkdirAll(filepath.Join(root, "calls"), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(g.List, []byte("/invented/private-vault\n"), 0600); err != nil {
		return nil, err
	}
	if err := g.Set(GuardPass); err != nil {
		return nil, err
	}
	return g, nil
}

// NewGuard creates a Path List and recording Guard stand-in in a test-owned
// temporary directory, with cleanup registered through t.
func NewGuard(t *testing.T) *Guard {
	t.Helper()
	g, err := CreateGuard(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Set replaces the program atomically. Every call records argv and stdin
// before answering; FailOutput on stderr is deliberately private test data.
func (g *Guard) Set(mode Mode) error {
	answer, ok := map[Mode]string{
		GuardPass: `case "$input" in *'"tool_name":"AskUser"'*) exit 2;; *) exit 0;; esac`,
		Pass:      "exit 0", Fail: "exit 1", Signal: "kill -KILL $$",
		Text: "printf 'private stdout'", Newlines: "printf '\\n\\n'", NUL: "printf '\\000'",
		Flood:       "head -c 1048577 /dev/zero",
		StderrFlood: "head -c 8192 /dev/zero >&2\nexit 2",
		Hang:        "exec sleep 3600",
	}[mode]
	if !ok {
		return fmt.Errorf("unknown guard mode %q", mode)
	}
	signal := ""
	if g.started != "" {
		signal = "printf x > " + quote(g.started) + "\n"
	}
	script := `#!/bin/sh
seen=$(command -p env)
PATH=/usr/bin:/bin
export PATH
umask 077
calls=` + quote(filepath.Join(g.program.root, "calls")) + `
n=0
while [ -e "$calls/$n" ] || [ -e "$calls/$n.part" ]; do n=$((n + 1)); done
part=$calls/$n.part
mkdir "$part"
printf '%s\n' "$@" > "$part/args"
printf '%s\n' "$seen" > "$part/env"
cat > "$part/input"
input=$(cat "$part/input")
printf '%s' "$$" > "$part/pid"
mv "$part" "$calls/$n"
printf '%s\n' ` + quote(FailOutput) + ` >&2
` + signal + answer + "\n"
	if err := os.WriteFile(g.Path+".new", []byte(script), 0700); err != nil {
		return err
	}
	return os.Rename(g.Path+".new", g.Path)
}

// Calls shares the Rule Set fixture's call representation and recording reader.
func (g *Guard) Calls(t *testing.T) []Call { return g.program.Calls(t) }

// ConfigLine belongs inside [launch]. Built CLI tests use their real Guard;
// in-process tests additionally install Path through the selection seam.
func (g *Guard) ConfigLine() string { return fmt.Sprintf("droid_secret_paths_file = %q\n", g.List) }

// Started signals after the next call has published its complete record.
// It lets cancellation tests wait causally, without sleeping or polling.
func (g *Guard) Started(t *testing.T) <-chan struct{} {
	t.Helper()
	g.started = filepath.Join(g.program.root, "started")
	if err := syscall.Mkfifo(g.started, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(g.started, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	ready := make(chan struct{})
	go func() {
		var b [1]byte
		if _, err := f.Read(b[:]); err == nil {
			close(ready)
		}
	}()
	return ready
}

// PID is the recorded process identity; Hang execs sleep without changing it.
func (g *Guard) PID(t *testing.T, call int) int {
	t.Helper()
	var pid int
	if _, err := fmt.Sscanf(string(read(t, filepath.Join(g.program.root, "calls", fmt.Sprint(call)), "pid")), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	return pid
}
