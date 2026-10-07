package tmux

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Tests for the name-based HasSession (run seam: combined output,
// *exec.ExitError), the New(binary, Timeouts) constructor and package-level
// structural guards. The socket-taking call set is covered by its own files.

// captured records every argv the fake name-based runner observed and
// returns a programmable response for each call.
type captured struct {
	calls  [][]string
	stdout []byte
	err    error
}

func (c *captured) runner() runner {
	return func(name string, args ...string) ([]byte, error) {
		c.calls = append(c.calls, append([]string{name}, args...))
		return c.stdout, c.err
	}
}

// TestHasSessionArgv pins the exact argv HasSession hands tmux; a name ending
// in ";" is escaped so it cannot end the command (b.ukw).
func TestHasSessionArgv(t *testing.T) {
	for _, c := range []struct{ name, target string }{{"foo", "foo"}, {"n;", `n\;`}} {
		cap := &captured{}
		if _, err := (&Client{run: cap.runner()}).HasSession(c.name); err != nil {
			t.Fatalf("HasSession(%q) failed: %v", c.name, err)
		}
		want := [][]string{{"tmux", "has-session", "-t", c.target}}
		if !reflect.DeepEqual(cap.calls, want) {
			t.Errorf("HasSession(%q) argv mismatch\n got=%q\nwant=%q", c.name, cap.calls, want)
		}
	}
}

// TestHasSessionFalseOnNonzeroExit pins that a non-zero tmux exit is the
// boolean "no" answer, not an error.
func TestHasSessionFalseOnNonzeroExit(t *testing.T) {
	cap := &captured{err: &exec.ExitError{}}
	ok, err := (&Client{run: cap.runner()}).HasSession("absent")
	if err != nil || ok {
		t.Fatalf("HasSession = (%v, %v); want (false, nil)", ok, err)
	}
}

// TestNewBinaryIsProgramRun pins SR-2.6 for the name-based HasSession:
// New("") runs tmux, New(bin) runs bin. PATH is an empty dir, so nothing runs.
func TestNewBinaryIsProgramRun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, bc := range []struct{ binary, wantProgram string }{
		{"", "tmux"},
		{"custom-tmux", "custom-tmux"},
	} {
		t.Run(fmt.Sprintf("binary=%q", bc.binary), func(t *testing.T) {
			_, err := New(bc.binary, Timeouts{}).HasSession("x")
			if !errors.Is(err, ErrTmuxNotAvailable) {
				t.Fatalf("err = %v; want ErrTmuxNotAvailable", err)
			}
			if want := "exec: " + strconv.Quote(bc.wantProgram) + ":"; !strings.Contains(err.Error(), want) {
				t.Fatalf("err %q does not name program %q", err.Error(), bc.wantProgram)
			}
		})
	}
}

// TestClientPackageHasNoShellReferences guards the SRD §4.3 invariant: no
// /bin/sh in client.go's code path.
func TestClientPackageHasNoShellReferences(t *testing.T) {
	const banned = "/bin/sh"
	if strings.Contains(mustReadSibling(t, "client.go"), banned) {
		t.Fatalf("client.go contains banned substring %q (SRD §4.3 invariant)", banned)
	}
}

// TestPackageDoesNotImportConfig guards SR-2.4: no production file of
// internal/tmux imports internal/config (timeouts arrive through New).
func TestPackageDoesNotImportConfig(t *testing.T) {
	const banned = "github.com/gabemahoney/agent-director/internal/config"
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parser.ParseDir: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("no production files parsed in internal/tmux")
	}
	for _, pkg := range pkgs {
		for file, f := range pkg.Files {
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if path == banned || strings.HasPrefix(path, banned+"/") {
					t.Errorf("%s imports %s (SR-2.4: internal/tmux never imports internal/config)", file, path)
				}
			}
		}
	}
}

// mustReadSibling reads a file in this package directory or fails the test.
func mustReadSibling(t *testing.T, name string) string {
	t.Helper()
	b, err := readFileAtTestData(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
