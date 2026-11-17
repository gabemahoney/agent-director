//go:build linux || darwin

package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
)

// buildBusyOldRuntime is a single scratch build shared by the wake-only and
// host-writer groups. It preserves the real Claude-only compiled coverage.
func buildBusyOldRuntime(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "agent-director-old")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin,
		"-ldflags=-X github.com/gabemahoney/agent-director/internal/version.Version=dj-old-runtime -X github.com/gabemahoney/agent-director/internal/version.Commit=dj-runtime-test", "./cmd/agent-director")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scratch runtime build: %v\n%s", err, out)
	}
	return bin
}

type busyRuntimeReply struct {
	stdout, stderr string
	code           int
	elapsed        time.Duration
}

// runBusyRuntime gives a real runtime only scratch paths. The executable trap
// records any accidental provider/tmux/delegation entry before refusing it.
func runBusyRuntime(t *testing.T, binary, home, db, trap string, args ...string) busyRuntimeReply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	argv := append([]string{}, args...)
	argv = append(argv, "--home", home, "--store-path", db, "--tmux-command", trap)
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Dir(trap), "TMUX_TMPDIR=" + filepath.Join(home, "tmux")}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	start := time.Now()
	err := cmd.Run()
	reply := busyRuntimeReply{out.String(), stderr.String(), 0, time.Since(start)}
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			reply.code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("runtime exceeded original call plus startup allowance: %v; %s", ctx.Err(), reply.stderr)
	}
	return reply
}

func (r busyRuntimeReply) errorName(t *testing.T, want string) {
	t.Helper()
	var envelope struct {
		Name        string          `json:"err_name"`
		Description string          `json:"err_description"`
		Details     json.RawMessage `json:"err_details"`
	}
	if r.code != 1 || r.stdout != "" || json.Unmarshal([]byte(strings.Split(r.stderr, "\n")[0]), &envelope) != nil || envelope.Name != want || envelope.Description == "" {
		t.Fatalf("runtime error = code %d out %q stderr %q; want %s", r.code, r.stdout, r.stderr, want)
	}
	if want == "ErrStoreBusy" && len(envelope.Details) != 0 {
		t.Fatalf("busy error invented details: %s", envelope.Details)
	}
}

func busyRuntimeHome(t *testing.T) (home, trap, marker string) {
	t.Helper()
	home = t.TempDir()
	if err := os.MkdirAll(layout.Dir(home), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.Join(home, layout.ConfigFile), []byte("[store]\nbusy_timeout_ms = 40\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "tools")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker, trap = filepath.Join(home, "unexpected-input"), filepath.Join(dir, "tmux")
	for _, name := range []string{"tmux", "claude", "codex", "agent-director"} {
		// home is a testing-generated path; quote it for the POSIX builtin.
		script := "#!/bin/sh\nprintf '%s\\n' invoked >> '" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'\nexit 89\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return
}

// holdBusyResource owns exactly its child process. Readiness proves the real
// SQLite/advisory lock is acquired; release is causal, never a timed sleep.
func holdBusyResource(t *testing.T, kind, path string) func() {
	t.Helper()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestBusySendResourceHolderProcess$")
	cmd.Env = append(os.Environ(), "AD_DJ_HOLDER_KIND="+kind, "AD_DJ_HOLDER_PATH="+path)
	cmd.ExtraFiles = []*os.File{readyW, releaseR}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyW.Close()
	_ = releaseR.Close()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			// Only this exec.Cmd's unreaped process is signalled; no PID scan.
			_ = cmd.Process.Signal(syscall.SIGCONT)
			_, _ = releaseW.Write([]byte{1})
			_ = releaseW.Close()
			_ = readyR.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("holder: %v\n%s", err, output.String())
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("owned holder failed causal release")
			}
		})
	}
	t.Cleanup(stop)
	ready := make(chan error, 1)
	go func() { var b [1]byte; _, err := io.ReadFull(readyR, b[:]); ready <- err }()
	select {
	case err := <-ready:
		if err != nil {
			stop()
			t.Fatalf("holder readiness: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		stop()
		t.Fatal("holder never acquired its lock")
	}
	if kind == "sqlite" {
		if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
	}
	return stop
}

func TestBusySendResourceHolderProcess(t *testing.T) {
	// Serial: subprocess fixture; no-op unless this exact test was re-executed.
	kind, path := os.Getenv("AD_DJ_HOLDER_KIND"), os.Getenv("AD_DJ_HOLDER_PATH")
	if kind == "" {
		t.Skip("owned holder subprocess only")
	}
	if strings.HasPrefix(kind, "resume-") {
		resumeTransportProcess(t, kind, path)
		os.Exit(0) // protocol-only stdout, without the test runner's PASS line
	}
	ready, release := os.NewFile(3, "ready"), os.NewFile(4, "release")
	defer ready.Close()
	defer release.Close()
	if kind == "sqlite" {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
		defer db.Exec("ROLLBACK")
	} else if kind == "install" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(fmt.Errorf("unknown holder %q", kind))
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(release, b[:]); err != nil {
		t.Fatal(err)
	}
}

type resumeTransportEvent struct {
	Kind           string
	PID, ParentPID int
	Request        resumedelegate.Request
}

// resumeTransportProcess extends the owned-process fixture with a control
// socket separate from the real sender's stdio. No timed sleeps or fake runner.
func resumeTransportProcess(t *testing.T, kind, path string) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	event := resumeTransportEvent{Kind: kind, PID: os.Getpid(), ParentPID: os.Getppid()}
	if kind != "resume-pipe-holder" {
		event.Request, err = resumedelegate.DecodeRequest(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(filepath.Dir(path), "invocations"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("delegate\n")
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := json.NewEncoder(conn).Encode(event); err != nil {
		t.Fatal(err)
	}
	if kind == "resume-pipe-holder" {
		ready := os.NewFile(3, "ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_ = ready.Close()
		for {
			var command [1]byte
			if _, err := io.ReadFull(conn, command[:]); err != nil {
				return
			}
			if command[0] == 'r' {
				return
			}
			if command[0] != 'p' {
				t.Fatalf("unknown pipe-holder command %q", command)
			}
			if _, err := conn.Write([]byte{'h'}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var command [1]byte
	if _, err := io.ReadFull(conn, command[:]); err != nil {
		return
	}
	if kind == "resume-stalled" || command[0] == 'r' {
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self, "-test.run=^TestBusySendResourceHolderProcess$")
	child.Env = append(os.Environ(), "AD_DJ_HOLDER_KIND=resume-pipe-holder", "GORACE=atexit_sleep_ms=0")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.ExtraFiles = []*os.File{readyW}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyW.Close()
	if _, err := io.ReadFull(readyR, command[:]); err != nil {
		t.Fatal(err)
	}
	_ = readyR.Close()
	// The test's isolated subreaper owns the descendant once this process exits.
	if kind == "resume-valid-pipes" {
		var params struct {
			InstanceID string `json:"instance_id"`
		}
		if err := json.Unmarshal(event.Request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stdout.Write(delegateResult(params.InstanceID)); err != nil {
			t.Fatal(err)
		}
	}
}
