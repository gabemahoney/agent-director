package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// captured holds the argv a fake runner observed plus a programmable
// response. Each test point-instantiates one so cases can assert what the
// client would have handed to tmux without ever running tmux.
type captured struct {
	args   []string
	stdout []byte
	err    error
}

func (c *captured) runner() runner {
	return func(name string, args ...string) ([]byte, error) {
		c.args = append([]string{name}, args...)
		return c.stdout, c.err
	}
}

func TestNewSessionArgvComposition(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(*Client) error
		wantCmd []string
	}{
		{
			name: "new-session with env vars (sorted) and command",
			fn: func(c *Client) error {
				return c.NewSession("foo", "/cwd",
					map[string]string{"B": "2", "A": "1"},
					[]string{"bash", "-l"})
			},
			wantCmd: []string{"tmux", "new-session", "-d", "-s", "foo",
				"-c", "/cwd", "-e", "A=1", "-e", "B=2", "--", "bash", "-l"},
		},
		{
			name: "new-session with no env and a single-arg command",
			fn: func(c *Client) error {
				return c.NewSession("bare", "/x", nil, []string{"claude"})
			},
			wantCmd: []string{"tmux", "new-session", "-d", "-s", "bare",
				"-c", "/x", "--", "claude"},
		},
		{
			name: "has-session",
			fn: func(c *Client) error {
				_, err := c.HasSession("foo")
				return err
			},
			wantCmd: []string{"tmux", "has-session", "-t", "foo"},
		},
		{
			name: "kill-session",
			fn: func(c *Client) error {
				return c.KillSession("foo")
			},
			wantCmd: []string{"tmux", "kill-session", "-t", "foo"},
		},
		{
			name: "list-panes",
			fn: func(c *Client) error {
				_, err := c.ListPanes("foo")
				return err
			},
			wantCmd: []string{"tmux", "list-panes", "-t", "foo", "-F", "#{pane_pid}"},
		},
		{
			name: "send-keys with a space-bearing string lands as one argv element",
			fn: func(c *Client) error {
				return c.SendKeys("foo", "hello world")
			},
			wantCmd: []string{"tmux", "send-keys", "-t", "foo:0.0", "hello world"},
		},
		{
			name: "send-keys preserves an embedded literal newline in one argv element",
			fn: func(c *Client) error {
				return c.SendKeys("foo", "multi\nline\ntext")
			},
			wantCmd: []string{"tmux", "send-keys", "-t", "foo:0.0", "multi\nline\ntext"},
		},
		{
			name: "send-keys passes the literal Enter token through as the text argv",
			fn: func(c *Client) error {
				// The verb layer composes Enter as a separate send-keys
				// call; this case pins that the client does not transform
				// the string "Enter" — tmux interprets it as the keysym.
				return c.SendKeys("foo", "Enter")
			},
			wantCmd: []string{"tmux", "send-keys", "-t", "foo:0.0", "Enter"},
		},
		{
			name: "capture-pane with n=25 emits -S -25 (not omitted, not -0)",
			fn: func(c *Client) error {
				_, err := c.CapturePane("foo", 25)
				return err
			},
			wantCmd: []string{"tmux", "capture-pane", "-p", "-t", "foo:0.0", "-S", "-25"},
		},
		{
			name: "capture-pane with a large n widens the scrollback verbatim",
			fn: func(c *Client) error {
				_, err := c.CapturePane("foo", 1000)
				return err
			},
			wantCmd: []string{"tmux", "capture-pane", "-p", "-t", "foo:0.0", "-S", "-1000"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &captured{}
			c := &Client{run: cap.runner()}
			if err := tc.fn(c); err != nil {
				t.Fatalf("op failed: %v", err)
			}
			if !reflect.DeepEqual(cap.args, tc.wantCmd) {
				t.Fatalf("argv mismatch\n got=%v\nwant=%v", cap.args, tc.wantCmd)
			}
		})
	}
}

// TestNewSessionWithoutCommand confirms that when a caller passes an empty
// command slice the tmux argv stops at the cwd/-e options — tmux without a
// trailing command is a valid invocation (it spawns the user's default
// shell, which the launch path never relies on but the client must support
// for symmetry with future Epics).
func TestNewSessionWithoutCommand(t *testing.T) {
	cap := &captured{}
	c := &Client{run: cap.runner()}
	if err := c.NewSession("plain", "/tmp", nil, nil); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	want := []string{"tmux", "new-session", "-d", "-s", "plain", "-c", "/tmp"}
	if !reflect.DeepEqual(cap.args, want) {
		t.Fatalf("argv mismatch\n got=%v\nwant=%v", cap.args, want)
	}
}

func TestHasSessionFalseOnNonzeroExit(t *testing.T) {
	// A non-zero *exec.ExitError is tmux's "session not found" signal —
	// the client maps it to (false, nil) so callers can treat the probe as
	// boolean without unpacking the exit code.
	cap := &captured{err: &exec.ExitError{}}
	c := &Client{run: cap.runner()}
	ok, err := c.HasSession("absent")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatalf("HasSession returned true for ExitError; want false")
	}
}

func TestNewSessionMapsExecMissingToTmuxNotAvailable(t *testing.T) {
	// Wrap an *exec.Error like defaultRunner would when PATH lookup fails,
	// then drive NewSession through the seam and confirm the canonical
	// ErrTmuxNotAvailable sentinel surfaces — the install gate depends on
	// this exact unwrap chain.
	cap := &captured{err: fmt.Errorf("%w: %v",
		ErrTmuxNotAvailable, &exec.Error{Name: "tmux", Err: exec.ErrNotFound})}
	c := &Client{run: cap.runner()}
	err := c.NewSession("x", "/tmp", nil, []string{"claude"})
	if !errors.Is(err, ErrTmuxNotAvailable) {
		t.Fatalf("err = %v; want ErrTmuxNotAvailable", err)
	}
}

func TestNewSessionMapsTmuxExitToSessionCreate(t *testing.T) {
	// Simulate tmux running and reporting failure: caller should see
	// ErrTmuxSessionCreate with the tmux stderr blob in the error message.
	cap := &captured{
		stdout: []byte("duplicate session: foo\n"),
		err:    &exec.ExitError{},
	}
	c := &Client{run: cap.runner()}
	err := c.NewSession("foo", "/tmp", nil, []string{"claude"})
	if !errors.Is(err, ErrTmuxSessionCreate) {
		t.Fatalf("err = %v; want ErrTmuxSessionCreate", err)
	}
	if !strings.Contains(err.Error(), "duplicate session") {
		t.Fatalf("err message %q does not include tmux stderr blob", err.Error())
	}
}

func TestListPanesParsesPids(t *testing.T) {
	cap := &captured{stdout: []byte("123\n456\n\nbroken\n789\n")}
	c := &Client{run: cap.runner()}
	pids, err := c.ListPanes("foo")
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	want := []int{123, 456, 789}
	if !reflect.DeepEqual(pids, want) {
		t.Fatalf("pids = %v; want %v", pids, want)
	}
}

func TestCapturePaneReturnsStdout(t *testing.T) {
	// Capture's job is to surface the raw pane text — pin that the bytes
	// returned are exactly what tmux wrote on stdout, including a trailing
	// newline. ANSI handling happens at a higher layer.
	cap := &captured{stdout: []byte("first line\nsecond line\n")}
	c := &Client{run: cap.runner()}
	got, err := c.CapturePane("foo", 25)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if got != "first line\nsecond line\n" {
		t.Fatalf("got %q; want exact stdout passthrough", got)
	}
}

func TestSendKeysWrapsTmuxFailure(t *testing.T) {
	// A non-zero tmux exit should surface as ErrTmuxSendKeys with the
	// stderr blob in the chain — the verb layer's state-precondition
	// errors are distinct, so callers must be able to errors.Is the
	// transport failure cleanly.
	cap := &captured{
		stdout: []byte("can't find pane: foo:0.0"),
		err:    &exec.ExitError{},
	}
	c := &Client{run: cap.runner()}
	err := c.SendKeys("foo", "hi")
	if !errors.Is(err, ErrTmuxSendKeys) {
		t.Fatalf("err = %v; want ErrTmuxSendKeys", err)
	}
	if !strings.Contains(err.Error(), "can't find pane") {
		t.Fatalf("err message %q does not include tmux stderr blob", err.Error())
	}
}

func TestCapturePaneWrapsTmuxFailure(t *testing.T) {
	cap := &captured{
		stdout: []byte("can't find session: ghost"),
		err:    &exec.ExitError{},
	}
	c := &Client{run: cap.runner()}
	_, err := c.CapturePane("ghost", 25)
	if !errors.Is(err, ErrTmuxCaptureFailed) {
		t.Fatalf("err = %v; want ErrTmuxCaptureFailed", err)
	}
}

func TestSendKeysMapsExecMissingToTmuxNotAvailable(t *testing.T) {
	cap := &captured{err: fmt.Errorf("%w: %v",
		ErrTmuxNotAvailable, &exec.Error{Name: "tmux", Err: exec.ErrNotFound})}
	c := &Client{run: cap.runner()}
	err := c.SendKeys("foo", "hi")
	if !errors.Is(err, ErrTmuxNotAvailable) {
		t.Fatalf("err = %v; want ErrTmuxNotAvailable", err)
	}
}

func TestCapturePaneMapsExecMissingToTmuxNotAvailable(t *testing.T) {
	cap := &captured{err: fmt.Errorf("%w: %v",
		ErrTmuxNotAvailable, &exec.Error{Name: "tmux", Err: exec.ErrNotFound})}
	c := &Client{run: cap.runner()}
	_, err := c.CapturePane("foo", 25)
	if !errors.Is(err, ErrTmuxNotAvailable) {
		t.Fatalf("err = %v; want ErrTmuxNotAvailable", err)
	}
}

func TestKillSessionWrapsTmuxFailure(t *testing.T) {
	cap := &captured{
		stdout: []byte("can't find session: ghost"),
		err:    &exec.ExitError{},
	}
	c := &Client{run: cap.runner()}
	err := c.KillSession("ghost")
	if !errors.Is(err, ErrTmuxKillFailed) {
		t.Fatalf("err = %v; want ErrTmuxKillFailed", err)
	}
}

// TestClientPackageHasNoShellReferences is a structural guard against the
// SRD §4.3 invariant: no /bin/sh anywhere in this package's own code path.
// Reading the source for the substring is intentional — a code-review grep
// would catch the same thing but the test pins the invariant at CI time.
func TestClientPackageHasNoShellReferences(t *testing.T) {
	// The runner uses exec.Command which exec.LookPaths the binary; if a
	// future maintainer introduces a sh fallback we want the test to fail.
	// Read this very file's sibling client.go and assert no /bin/sh literal.
	// Using a test-internal helper keeps the assertion self-contained.
	const banned = "/bin/sh"
	src := mustReadSibling(t, "client.go")
	if strings.Contains(src, banned) {
		t.Fatalf("client.go contains banned substring %q (SRD §4.3 invariant)", banned)
	}
}

// mustReadSibling reads a file in the same package directory. Tests that
// assert on source content use this to keep the path resolution explicit.
func mustReadSibling(t *testing.T, name string) string {
	t.Helper()
	b, err := readFileAtTestData(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
