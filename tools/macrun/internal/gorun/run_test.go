package gorun_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/golist"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/gorun"
)

// fakeExec records the commands it is given. Output answers with output, ok
// and err; Start writes stderr to the command's Stderr and answers with
// stdout.
type fakeExec struct {
	cmds           []gorun.Cmd
	output         string
	ok             bool
	err            error
	stdout, stderr string
}

func (f *fakeExec) Output(_ context.Context, cmd gorun.Cmd) ([]byte, bool, error) {
	f.cmds = append(f.cmds, cmd)
	return []byte(f.output), f.ok, f.err
}

func (f *fakeExec) Start(_ context.Context, cmd gorun.Cmd) (io.Reader, func() error, error) {
	f.cmds = append(f.cmds, cmd)
	if cmd.Stderr != nil {
		io.WriteString(cmd.Stderr, f.stderr)
	}
	return strings.NewReader(f.stdout), func() error { return nil }, nil
}

func TestBuild(t *testing.T) {
	t.Parallel()
	cgo := "CGO_ENABLED=0"
	if golist.DarwinCgoEnabled {
		cgo = "CGO_ENABLED=1"
	}
	env := []string{"PATH=/usr/bin:/bin", "GOFLAGS=-tags=integration", "GOOS=linux", "HOME=/Users/runner", "CGO_ENABLED=0", "GOARCH=amd64"}
	wantCmd := gorun.Cmd{
		Path: "/opt/go/bin/go",
		Args: []string{"test", "-c", "-o", "/work/pkg%2Fa.test", "./pkg/a"},
		Dir:  "/src/repo",
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/Users/runner", "GOOS=darwin", "GOARCH=arm64", cgo, "GOFLAGS="},
	}
	for _, tc := range []struct {
		name    string
		exec    fakeExec
		want    gorun.Build
		wantErr string
	}{
		{name: "built", exec: fakeExec{ok: true}, want: gorun.Build{Binary: "/work/pkg%2Fa.test", Built: true}},
		{
			name: "build failed",
			exec: fakeExec{output: "pkg/a/a_test.go:3:2: undefined: x\n"},
			want: gorun.Build{Output: "pkg/a/a_test.go:3:2: undefined: x\n"},
		},
		{
			name:    "go didn't run",
			exec:    fakeExec{err: errors.New("fork/exec /opt/go/bin/go: no such file or directory")},
			wantErr: "build tests of pkg/a: fork/exec /opt/go/bin/go: no such file or directory",
		},
	} {
		exec := tc.exec
		got, err := gorun.Runner{Go: "/opt/go/bin/go", Exec: &exec}.Build(context.Background(), "/src/repo", "pkg/a", "/work", env)
		if gotErr := fmtErr(err); got != tc.want || gotErr != tc.wantErr {
			t.Errorf("%s: Build = %+v, %q; want %+v, %q", tc.name, got, gotErr, tc.want, tc.wantErr)
		}
		if !reflect.DeepEqual(exec.cmds, []gorun.Cmd{wantCmd}) {
			t.Errorf("%s: ran %+v; want %+v", tc.name, exec.cmds, wantCmd)
		}
	}
}

// fmtErr returns err's text, or "" for no error.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestRunTests(t *testing.T) {
	t.Parallel()
	env := []string{"PATH=/usr/bin:/bin", "GOFLAGS=-tags=integration", "AGENT_DIRECTOR_TEST_BASH=/opt/homebrew/bin/bash"}
	exec := &fakeExec{stdout: stream(t, "pkg-b-testmain-fails.json"), stderr: "go: a note on stderr\n"}
	var mirror bytes.Buffer
	got, err := gorun.Runner{Go: "/opt/go/bin/go", Exec: exec}.RunTests(context.Background(),
		"/src/repo", "pkg/b", "/work/pkg%2Fb.test", []string{"TestOK", "ExampleOK"}, env, gorun.Hooks{Mirror: &mirror})
	if err != nil {
		t.Fatal(err)
	}
	if want := (gorun.Collected{Cases: pkgB}); !reflect.DeepEqual(got, want) {
		t.Errorf("RunTests =\n%+v\nwant\n%+v", got, want)
	}
	wantCmd := gorun.Cmd{
		Path: "/opt/go/bin/go",
		Args: []string{
			"tool", "test2json", "-t", "-p", "pkg/b", "/work/pkg%2Fb.test",
			"-test.v=test2json", "-test.paniconexit0", "-test.run", "^(?:TestOK|ExampleOK)$",
		},
		Dir:          "/src/repo/pkg/b",
		Env:          env,
		ProcessGroup: true,
	}
	if len(exec.cmds) != 1 || exec.cmds[0].Stderr == nil {
		t.Fatalf("ran %+v; want one command with a Stderr", exec.cmds)
	}
	exec.cmds[0].Stderr = nil
	if !reflect.DeepEqual(exec.cmds[0], wantCmd) {
		t.Errorf("ran\n%+v\nwant\n%+v", exec.cmds[0], wantCmd)
	}
	// go's own stderr is mirrored after the stream.
	const wantMirror = "    === RUN   TestOK\n    --- PASS: TestOK (0.00s)\n    PASS\n    TestMain: teardown failed\n    go: a note on stderr\n"
	if mirror.String() != wantMirror {
		t.Errorf("mirror =\n%s\nwant\n%s", mirror.String(), wantMirror)
	}
}

// TestExec runs the real Exec on /bin/sh, which Linux and the Mac both have.
func TestExec(t *testing.T) {
	t.Parallel()
	env := []string{"PATH=/usr/bin:/bin"}
	out, ok, err := gorun.Exec{}.Output(context.Background(), gorun.Cmd{Path: "/bin/sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}, Env: env})
	if string(out) != "out\nerr\n" || ok || err != nil {
		t.Errorf("Output of a command exiting 3 = %q, %v, %v; want its output, not ok, no error", out, ok, err)
	}
	if _, _, err := (gorun.Exec{}).Output(context.Background(), gorun.Cmd{Path: "/nonexistent/sh"}); err == nil {
		t.Error("Output of a missing program gave no error")
	}

	// A cancel stops the command's whole process group: the background
	// sleep, which holds stdout open, ends too, so stdout reaches its end.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout, wait, err := gorun.Exec{}.Start(ctx, gorun.Cmd{
		Path: "/bin/sh", Args: []string{"-c", "sleep 1000 & echo $!; wait"}, Env: env, ProcessGroup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	pid, convErr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || convErr != nil {
		t.Fatalf("first line %q: %v, %v; want the background sleep's pid", line, err, convErr)
	}
	cancel()
	ended := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(r)
		ended <- err
	}()
	select {
	case err := <-ended:
		if err != nil {
			t.Errorf("reading stdout after the cancel: %v", err)
		}
	case <-time.After(10 * time.Second):
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the background sleep (pid %d) outlived the cancel", pid)
	}
	if err := wait(); err != nil {
		t.Errorf("wait after a cancel = %v; want nil, a killed command being no fault", err)
	}
}

func TestRunPattern(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		names       []string
		want        string
		match, miss []string
	}{
		{
			names: []string{"TestA", "ExampleB"},
			want:  `^(?:TestA|ExampleB)$`,
			match: []string{"TestA", "ExampleB"},
			miss:  []string{"TestAB", "XTestA", "ExampleB_c"},
		},
		{names: []string{"Test.A+"}, want: `^(?:Test\.A\+)$`, match: []string{"Test.A+"}, miss: []string{"TestxA+", "Test.AA"}},
	} {
		got := gorun.RunPattern(tc.names)
		if got != tc.want {
			t.Errorf("RunPattern(%q) = %q; want %q", tc.names, got, tc.want)
		}
		re := regexp.MustCompile(got)
		for _, name := range tc.match {
			if !re.MatchString(name) {
				t.Errorf("RunPattern(%q) doesn't select %q", tc.names, name)
			}
		}
		for _, name := range tc.miss {
			if re.MatchString(name) {
				t.Errorf("RunPattern(%q) selects %q", tc.names, name)
			}
		}
	}
}
