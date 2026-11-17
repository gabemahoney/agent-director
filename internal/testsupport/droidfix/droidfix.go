// Package droidfix is the fake droid program for tests of the Droid Rule Set
// check (spawn.ResolveDroidRules), shared by internal/spawn and pkg/api tests:
// a real executable shell script named droid in a test temp directory. Each
// call records its arguments, its environment, and its --file input with that
// file's and its directory's permission bits as the call saw them, then
// answers in the program's Mode. SetMode replaces the program in place, as an
// operator fixing the droid program would; LockCopyDir makes the caller's
// removal of the --file directory fail.
//
// It is a leaf package (standard library only). Reusable test fixture:
// docs/architecture.md "droidfix: the fake droid program".
package droidfix

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Mode is how a call answers once it has recorded itself.
type Mode string

const (
	Pass  Mode = "pass"  // exit 0
	Fail  Mode = "fail"  // print FailOutput on stdout and stderr, then exit 1
	Hang  Mode = "hang"  // never end; only the caller's deadline stops it
	Flood Mode = "flood" // write 1 MiB + 1 bytes to stdout, then exit 0
	Hold  Mode = "hold"  // signal Held, wait for Release, then exit 0
)

// FailOutput is what a Fail call prints, so a test can show it is never echoed.
const FailOutput = "droidfix: planted rules check output"

// RuleSet is a Rule Set File agent-director's own checks accept: one allow
// rule for tail whose operator id is id.
func RuleSet(id string) string {
	return `{"version":1,"rules":[{"id":"` + id + `","decision":"allow","match":{"prefix":["tail"]},"tests":{"match":["tail -n 1 x"],"noMatch":["head x"]}}]}`
}

// Program is one fake droid; Path is the executable.
type Program struct {
	Path    string
	root    string // calls/, held and release
	mode    Mode
	lock    bool
	held    chan struct{}
	release *os.File
}

// New writes a fake droid answering in mode.
func New(t *testing.T, mode Mode) *Program {
	t.Helper()
	p := &Program{Path: filepath.Join(t.TempDir(), "droid"), root: t.TempDir(), held: make(chan struct{}, 16)}
	if err := os.Mkdir(filepath.Join(p.root, "calls"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Opened read-write, a FIFO never blocks its opener and keeps both ends
	// open, so a call's own open of either FIFO never blocks.
	var fifos [2]*os.File
	for i, name := range []string{"held", "release"} {
		path := filepath.Join(p.root, name)
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		fifos[i] = f
	}
	p.release = fifos[1]
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := fifos[0].Read(buf); err != nil {
				return
			}
			p.held <- struct{}{}
		}
	}()
	p.mode = mode
	p.write(t)
	return p
}

// SetMode makes the next call answer in mode.
func (p *Program) SetMode(t *testing.T, mode Mode) {
	t.Helper()
	p.mode = mode
	p.write(t)
}

// LockCopyDir makes each later call, once recorded, remove write permission
// from its --file's directory (mode 0500), so a non-root caller cannot remove
// the copy; off restores the plain program.
func (p *Program) LockCopyDir(t *testing.T, on bool) {
	t.Helper()
	p.lock = on
	p.write(t)
}

// Held receives once for each Hold call that has recorded itself and waits.
func (p *Program) Held() <-chan struct{} { return p.held }

// Release lets one waiting (or the next) Hold call exit 0.
func (p *Program) Release(t *testing.T) {
	t.Helper()
	if _, err := p.release.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
}

// Call is one recorded call.
type Call struct {
	Args              []string          // the arguments after the program
	Env               map[string]string // its environment
	File              string            // its --file argument; "" when none
	Input             []byte            // that file's content as the call read it
	FileMode, DirMode os.FileMode       // the permission bits of that file and its directory
}

// Calls returns the calls that finished recording, in order; a call stopped
// while recording (a Hang under a short deadline) is left out.
func (p *Program) Calls(t *testing.T) []Call {
	t.Helper()
	var calls []Call
	for n := 0; ; n++ {
		dir := filepath.Join(p.root, "calls", strconv.Itoa(n))
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Stat(dir + ".part"); err == nil {
				continue
			}
			return calls
		} else if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSuffix(string(read(t, dir, "args")), "\n"), "\n")
		c := Call{Args: args, Env: ReadEnv(t, filepath.Join(dir, "env"))}
		if file := read(t, dir, "file"); file != nil {
			c.File, c.Input = string(file), read(t, dir, "input")
			c.FileMode, c.DirMode = perm(read(t, dir, "file-mode")), perm(read(t, dir, "dir-mode"))
		}
		calls = append(calls, c)
	}
}

// ReadEnv parses an environment capture written by env(1), one KEY=value per
// line, as the fake records each call's; other executable stand-ins use it too.
func ReadEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		env[key] = value
	}
	return env
}

// read returns dir/name's content, nil when it is absent.
func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// perm reads the permission bits from the start of an `ls -ld` line.
func perm(line []byte) os.FileMode {
	var mode os.FileMode
	for i := 1; i < 10 && i < len(line); i++ {
		if line[i] != '-' {
			mode |= 1 << (9 - i)
		}
	}
	return mode
}

// write replaces the program at Path with one for p's mode and lock.
func (p *Program) write(t *testing.T) {
	t.Helper()
	answer := map[Mode]string{
		Pass:  "exit 0",
		Fail:  "printf '%s\\n' " + quote(FailOutput) + "\nprintf '%s\\n' " + quote(FailOutput) + " >&2\nexit 1",
		Hang:  "exec sleep 3600",
		Flood: "head -c 1048577 /dev/zero\nexit 0",
		Hold:  "printf x > " + quote(filepath.Join(p.root, "held")) + "\nread -r line < " + quote(filepath.Join(p.root, "release")) + "\nexit 0",
	}[p.mode]
	lock := ""
	if p.lock {
		lock = `[ -n "$file" ] && chmod 0500 "$(dirname "$file")"` + "\n"
	}
	// The environment is read before PATH is set for the script's own tools,
	// and the call publishes its record only once it is whole.
	script := `#!/bin/sh
seen=$(command -p env)
PATH=/usr/bin:/bin
export PATH
umask 077
calls=` + quote(filepath.Join(p.root, "calls")) + `
n=0
while [ -e "$calls/$n" ] || [ -e "$calls/$n.part" ]; do n=$((n + 1)); done
part=$calls/$n.part
mkdir "$part"
printf '%s\n' "$@" > "$part/args"
printf '%s\n' "$seen" > "$part/env"
file= prev=
for arg in "$@"; do
	[ "$prev" = --file ] && file=$arg
	prev=$arg
done
if [ -n "$file" ]; then
	cat "$file" > "$part/input"
	ls -ld "$file" > "$part/file-mode"
	ls -ld "$(dirname "$file")" > "$part/dir-mode"
	printf '%s' "$file" > "$part/file"
fi
mv "$part" "$calls/$n"
` + lock + answer + "\n"
	tmp := p.Path + ".new"
	if err := os.WriteFile(tmp, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Path); err != nil {
		t.Fatal(err)
	}
}

// quote is s as one single-quoted shell word.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
