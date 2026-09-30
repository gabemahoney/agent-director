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

// Tests for the pre-Phase-1 name-based methods (run seam: combined output,
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

// nameBasedOp drives one name-based method; exitErr is the sentinel a
// non-zero tmux exit maps to (nil for HasSession, whose "no" is not an error).
type nameBasedOp struct {
	name    string
	fn      func(*Client) error
	stderr  string
	exitErr error
}

// nameBasedOps is one call of every name-based method, shared by the error
// mapping and constructor tables.
var nameBasedOps = []nameBasedOp{
	{"HasSession", func(c *Client) error { _, err := c.HasSession("x"); return err }, "", nil},
	{"SendKeys", func(c *Client) error { return c.SendKeys("x", "hi", true) }, "can't find pane: x:0.0", ErrTmuxSendKeys},
}

// TestNameBasedArgv pins the exact argv each name-based method hands tmux,
// including literal-text-then-Enter for SendKeys.
func TestNameBasedArgv(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*Client) error
		want [][]string
	}{
		{
			name: "has-session",
			fn: func(c *Client) error {
				_, err := c.HasSession("foo")
				return err
			},
			want: [][]string{{"tmux", "has-session", "-t", "foo"}},
		},
		{
			name: "send-keys text-only is one literal (-l) call",
			fn:   func(c *Client) error { return c.SendKeys("foo", "hello world", false) },
			want: [][]string{{"tmux", "send-keys", "-t", "foo:0.0", "-l", "hello world"}},
		},
		{
			name: "send-keys keeps an embedded newline in one argv element",
			fn:   func(c *Client) error { return c.SendKeys("foo", "multi\nline\ntext", false) },
			want: [][]string{{"tmux", "send-keys", "-t", "foo:0.0", "-l", "multi\nline\ntext"}},
		},
		{
			name: "send-keys keysym-shaped text is forced literal by -l",
			fn:   func(c *Client) error { return c.SendKeys("foo", "Enter", false) },
			want: [][]string{{"tmux", "send-keys", "-t", "foo:0.0", "-l", "Enter"}},
		},
		{
			name: "send-keys with pressEnter sends literal text then a real Enter",
			fn:   func(c *Client) error { return c.SendKeys("foo", "Enter the password", true) },
			want: [][]string{
				{"tmux", "send-keys", "-t", "foo:0.0", "-l", "Enter the password"},
				{"tmux", "send-keys", "-t", "foo:0.0", "Enter"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &captured{}
			if err := tc.fn(&Client{run: cap.runner()}); err != nil {
				t.Fatalf("op failed: %v", err)
			}
			if !reflect.DeepEqual(cap.calls, tc.want) {
				t.Fatalf("argv mismatch\n got=%q\nwant=%q", cap.calls, tc.want)
			}
		})
	}
}

// TestNameBasedMissingBinaryMapsToNotAvailable pins that a runner-reported
// missing binary surfaces as ErrTmuxNotAvailable from every name-based method.
func TestNameBasedMissingBinaryMapsToNotAvailable(t *testing.T) {
	for _, op := range nameBasedOps {
		t.Run(op.name, func(t *testing.T) {
			cap := &captured{err: fmt.Errorf("%w: %v",
				ErrTmuxNotAvailable, &exec.Error{Name: "tmux", Err: exec.ErrNotFound})}
			err := op.fn(&Client{run: cap.runner()})
			if !errors.Is(err, ErrTmuxNotAvailable) {
				t.Fatalf("err = %v; want ErrTmuxNotAvailable", err)
			}
		})
	}
}

// TestNameBasedTmuxExitMapsToSentinel pins the sentinel and the tmux stderr
// blob each method returns when tmux exits non-zero.
func TestNameBasedTmuxExitMapsToSentinel(t *testing.T) {
	for _, op := range nameBasedOps {
		if op.exitErr == nil {
			continue
		}
		t.Run(op.name, func(t *testing.T) {
			cap := &captured{stdout: []byte(op.stderr), err: &exec.ExitError{}}
			err := op.fn(&Client{run: cap.runner()})
			if !errors.Is(err, op.exitErr) {
				t.Fatalf("err = %v; want %v", err, op.exitErr)
			}
			if blob := strings.TrimSpace(op.stderr); !strings.Contains(err.Error(), blob) {
				t.Fatalf("err %q does not include tmux stderr %q", err.Error(), blob)
			}
		})
	}
}

// TestSendKeysFirstCallFailureSkipsEnter pins that a failed literal-text call
// never issues the trailing Enter.
func TestSendKeysFirstCallFailureSkipsEnter(t *testing.T) {
	cap := &captured{err: &exec.ExitError{}, stdout: []byte("can't find pane")}
	if err := (&Client{run: cap.runner()}).SendKeys("foo", "hi", true); err == nil {
		t.Fatalf("SendKeys returned nil; want a tmux error")
	}
	if len(cap.calls) != 1 {
		t.Fatalf("got %d tmux calls after first-call failure; want 1 (%q)", len(cap.calls), cap.calls)
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

// TestNewBinaryIsProgramRun pins SR-2.6 for the name-based methods: New("")
// runs tmux, New(bin) runs bin. PATH is an empty dir, so nothing runs.
func TestNewBinaryIsProgramRun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, bc := range []struct{ binary, wantProgram string }{
		{"", "tmux"},
		{"custom-tmux", "custom-tmux"},
	} {
		for _, op := range nameBasedOps {
			t.Run(fmt.Sprintf("%s/binary=%q", op.name, bc.binary), func(t *testing.T) {
				err := op.fn(New(bc.binary, Timeouts{}))
				if !errors.Is(err, ErrTmuxNotAvailable) {
					t.Fatalf("err = %v; want ErrTmuxNotAvailable", err)
				}
				if want := "exec: " + strconv.Quote(bc.wantProgram) + ":"; !strings.Contains(err.Error(), want) {
					t.Fatalf("err %q does not name program %q", err.Error(), bc.wantProgram)
				}
			})
		}
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
