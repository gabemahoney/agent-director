package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// Private test-child modes use the same cached test executable. They run before
// TestMain/flag parsing, since scheduleClaudeStartupWake supplies production argv.
func init() {
	if path := os.Getenv("AD_IP_LOCK_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			panic(err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			panic(err)
		}
		fmt.Fprintln(os.Stdout, "held")
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = f.Close()
		os.Exit(0)
	}
	if path := os.Getenv("AD_IP_CAPTURE_RECEIPT"); path != "" && len(os.Args) > 2 && os.Args[1] == "hook" {
		f, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		_ = json.NewEncoder(f).Encode(struct {
			Args []string
			Env  string
		}{os.Args, os.Getenv("AD_IP_CAPTURE_VALUE")})
		_ = f.Close()
		os.Exit(0)
	}
}

func TestCapturedStartupWakeExecutable(t *testing.T) {
	dir := t.TempDir()
	// The low-level Darwin publication/ready-layout matrix is capture_test.go's.
	// Here the captured path is a real child, so both exec target and argv[0],
	// along with the unchanged payload/environment, are observed at execution.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	immutable := filepath.Join(dir, "captured-A")
	if err := os.WriteFile(immutable, data, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(dir, "receipt")
	if err := syscall.Mkfifo(receipt, 0600); err != nil {
		t.Fatal(err)
	}
	fifo, err := os.OpenFile(receipt, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	diagnostic, err := os.Create(filepath.Join(dir, "diagnostic"))
	if err != nil {
		t.Fatal(err)
	}
	defer diagnostic.Close()
	t.Setenv("AD_IP_CAPTURE_RECEIPT", receipt)
	t.Setenv("AD_IP_CAPTURE_VALUE", "original environment")
	saved := startupWakeExecutable
	t.Cleanup(func() { startupWakeExecutable = saved })
	startupWakeExecutable = func() (string, error) { return immutable, nil }
	wake := hook.ClaudeStartupWake{InstanceID: "instance-A", LaunchToken: "launch-A", SessionID: "session-A", Deadline: time.Now().Add(5 * time.Second)}
	storePath := filepath.Join(dir, "custom-store.db")
	if err := scheduleClaudeStartupWake(storePath, log.New(diagnostic, "", 0), wake); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args []string
		Env  string
	}
	done := make(chan error, 1)
	go func() { done <- json.NewDecoder(fifo).Decode(&got) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no captured child receipt")
	}
	if len(got.Args) != 4 || !reflect.DeepEqual(got.Args[:3], []string{immutable, "hook", claudeStartupWakeArg}) || got.Env != "original environment" {
		t.Fatalf("child receipt %+v", got)
	}
	var job claudeStartupWakeJob
	if err := json.Unmarshal([]byte(got.Args[3]), &job); err != nil {
		t.Fatal(err)
	}
	if job.StorePath != storePath || job.HookPID != os.Getpid() || job.Wake.InstanceID != wake.InstanceID || job.Wake.LaunchToken != wake.LaunchToken || job.Wake.SessionID != wake.SessionID || !job.Wake.Deadline.Equal(wake.Deadline) {
		t.Fatalf("changed startup payload: %+v", job)
	}
	refusal := errors.New("no ready own-image executable")
	startupWakeExecutable = func() (string, error) { return "", refusal }
	if err := scheduleClaudeStartupWake(storePath, log.New(diagnostic, "", 0), wake); !errors.Is(err, refusal) {
		t.Fatalf("capture refusal = %v", err)
	}
}

type startupDiagnosticWriter func([]byte) (int, error)

func (write startupDiagnosticWriter) Write(p []byte) (int, error) { return write(p) }

// CR1 / t2.xis.oo.67: the hard deadline and timer cleanup must remain
// independent of both a blocked diagnostic sink and a held logger mutex.
func TestClaudeStartupWakeDeadlineIgnoresBlockedDiagnostic(t *testing.T) {
	for _, heldMutex := range []bool{false, true} {
		name := "blocked sink"
		if heldMutex {
			name = "held logger mutex"
		}
		t.Run(name, func(t *testing.T) {
			release, entered := make(chan struct{}), make(chan struct{})
			var once sync.Once
			logger := log.New(startupDiagnosticWriter(func(p []byte) (int, error) {
				once.Do(func() { close(entered) })
				<-release
				return len(p), nil
			}), "", 0)
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					t.Fatal("deadline callback or cleanup waited for the blocked diagnostic")
				}
			}
			var holderDone chan struct{}
			if heldMutex {
				holderDone = make(chan struct{})
				go func() { defer close(holderDone); logger.Print("another log call") }()
			}
			started, finished := make(chan struct{}), make(chan struct{})
			var stop func()
			t.Cleanup(func() {
				close(release)
				if stop != nil {
					stop()
					wait(finished)
				}
				if holderDone != nil {
					wait(holderDone)
				}
			})
			if heldMutex {
				wait(entered) // the earlier log call owns the mutex and is in Write
			}
			exit := make(chan int, 1)
			stop = armClaudeStartupWakeDeadline(time.Now().Add(500*time.Millisecond), func() {
				close(started)
				logger.Print("startup job deadline")
				close(finished)
			}, func(code int) { exit <- code })
			wait(started)
			if !heldMutex {
				wait(entered)
			}
			select {
			case code := <-exit:
				if code != hookExitCode {
					t.Errorf("deadline exit = %d; want %d", code, hookExitCode)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("hard deadline waited for diagnostic I/O or the logger mutex")
			}
			select {
			case <-finished:
				t.Fatal("diagnostic finished before its sink was released")
			default:
			}
			stopped := make(chan struct{})
			go func() { stop(); close(stopped) }()
			wait(stopped) // stop must not join a callback still blocked in Print
		})
	}
}
