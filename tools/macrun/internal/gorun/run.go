// Package gorun builds and runs one Go package's test binary on the Mac as go
// test would, and collects its test2json events as case results of macrun's
// result model. The build, the run and the collection are separate pieces,
// and every go command goes through an Executor, so tests drive them without
// running go. It is reusable: macrun code that builds or runs a Go package's
// tests on the Mac, or reads a test2json stream, must use it, never its own
// go test call or event reader.
package gorun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Cmd is one command line.
type Cmd struct {
	// Path is the program, by absolute path.
	Path string
	// Args are its arguments.
	Args []string
	// Dir is its working directory.
	Dir string
	// Env is its whole environment.
	Env []string
	// ProcessGroup runs it in a process group of its own, so stopping it
	// stops everything it started.
	ProcessGroup bool
	// Stderr receives its stderr, for a command whose stdout is read; nil
	// discards it.
	Stderr io.Writer
}

// Executor runs commands. Exec is the real one.
type Executor interface {
	// Output runs cmd to its end and returns its stdout and stderr together
	// and whether it exited 0. err is only for a command that couldn't run.
	Output(ctx context.Context, cmd Cmd) (output []byte, ok bool, err error)
	// Start starts cmd and returns its stdout. Read stdout to its end, then
	// call wait, whose error is only for a fault, never for an exit status.
	// Ending ctx stops cmd, its whole process group with ProcessGroup.
	Start(ctx context.Context, cmd Cmd) (stdout io.Reader, wait func() error, err error)
}

// Runner builds and runs packages' test binaries with one go command.
type Runner struct {
	// Go is the go command, by absolute path.
	Go string
	// Exec runs the commands.
	Exec Executor
}

// RunTests runs the test binary built from the package at dir, relative to
// root, as go test -json does: through go tool test2json, from the package's
// directory, with -test.run selecting exactly names, no -test.short and no
// -test.timeout. It gives the binary env unchanged, by absolute path so it
// can run itself again as os.Args[0], in a process group of its own. It
// collects the stream as Collect does. test2json folds the binary's stderr
// into its stream; go's own stderr is mirrored after the run, so only one
// goroutine writes the mirror. err is only for a fault starting the run,
// reading its stream or writing the mirror, not for failing cases.
func (r Runner) RunTests(ctx context.Context, root, dir, binary string, names []string, env []string, hooks Hooks) (Collected, error) {
	var stderr bytes.Buffer
	cmd := Cmd{
		Path: r.Go,
		Args: []string{
			"tool", "test2json", "-t", "-p", dir, binary,
			"-test.v=test2json", "-test.paniconexit0", "-test.run", RunPattern(names),
		},
		Dir:          filepath.Join(root, filepath.FromSlash(dir)),
		Env:          env,
		ProcessGroup: true,
		Stderr:       &stderr,
	}
	stdout, wait, err := r.Exec.Start(ctx, cmd)
	if err != nil {
		return Collected{}, fmt.Errorf("run tests of %s: %w", dir, err)
	}
	mirror := newLineWriter(hooks.Mirror)
	collected, readErr := collect(stdout, dir, hooks, mirror)
	waitErr := wait()
	mirror.write(stderr.String())
	mirror.endLine()
	if err := errors.Join(readErr, waitErr, mirror.err); err != nil {
		return collected, fmt.Errorf("run tests of %s: %w", dir, err)
	}
	return collected, nil
}

// RunPattern returns the -test.run expression that selects exactly the
// top-level tests and Examples named names.
func RunPattern(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(?:" + strings.Join(quoted, "|") + ")$"
}

// waitDelay bounds how long Exec waits for a stopped command's output to
// close.
const waitDelay = 5 * time.Second

// Exec is the Executor that runs commands with os/exec.
type Exec struct{}

// Output implements Executor.
func (Exec) Output(ctx context.Context, cmd Cmd) ([]byte, bool, error) {
	out, err := command(ctx, cmd).CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	return out, true, nil
}

// Start implements Executor.
func (Exec) Start(ctx context.Context, cmd Cmd) (io.Reader, func() error, error) {
	c := command(ctx, cmd)
	c.Stderr = cmd.Stderr
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := c.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() error {
		err := c.Wait()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return err
	}
	return stdout, wait, nil
}

// command makes cmd's exec.Cmd, which ending ctx kills, with its process
// group when it has its own.
func command(ctx context.Context, cmd Cmd) *exec.Cmd {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.WaitDelay = waitDelay
	if cmd.ProcessGroup {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	}
	return c
}
