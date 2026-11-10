//go:build linux

package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type journalHelperReport struct {
	PID, PPID int
	Env       []string
	Stdin     string
}

// journalFixedHelperChild is TestMain's one fixed-path child implementation.
// It uses only the test binary, IPC under the test's temp directory and stdlib.
func journalFixedHelperChild(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "report":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 2
		}
		if err := json.NewEncoder(os.Stdout).Encode(journalHelperReport{PID: os.Getpid(), Env: os.Environ(), Stdin: string(input)}); err != nil {
			return 2
		}
		fmt.Fprint(os.Stderr, "separate stderr")
	case "exit":
		return 23
	case "output":
		if len(args) != 2 {
			return 2
		}
		left, err := strconv.Atoi(args[1])
		if err != nil {
			return 2
		}
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for left > 0 {
			n := min(left, len(chunk))
			if _, err := os.Stdout.Write(chunk[:n]); err != nil {
				return 2
			}
			left -= n
		}
	case "fork":
		if len(args) != 2 {
			return 2
		}
		child := exec.Command(os.Args[0], "--journal-fixed-helper", "grandchild", args[1])
		if err := child.Run(); err != nil {
			return 2
		}
	case "grandchild":
		if len(args) != 2 {
			return 2
		}
		conn, err := net.Dial("unix", args[1])
		if err != nil {
			return 2
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(journalHelperReport{PID: os.Getpid(), PPID: os.Getppid()}); err != nil {
			return 2
		}
		_, _ = io.Copy(io.Discard, conn) // stays in the helper's process group
	default:
		return 2
	}
	return 0
}

func TestDarwinJournalFixedHelperContract(t *testing.T) {
	// Serial: the parent PATH is deliberately unrelated to the executable.
	t.Setenv("PATH", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var pids []int
	out, stderr, err := runFixedHelper(ctx, exe, []string{"--journal-fixed-helper", "report"}, func(pid int) {
		pids = append(pids, pid)
		if err := unix.Kill(pid, 0); err != nil {
			t.Errorf("started child not alive: %v", err)
		}
		if group, err := unix.Getpgid(pid); err != nil || group != pid {
			t.Errorf("process group = %d, %v; want %d", group, err, pid)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var report journalHelperReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pids, []int{report.PID}) || !reflect.DeepEqual(report.Env, []string{"LC_ALL=C"}) || report.Stdin != "" || string(stderr) != "separate stderr" {
		t.Fatalf("helper report = %+v, started = %v, stderr = %q", report, pids, stderr)
	}
	if _, _, err := runFixedHelper(ctx, filepath.Base(exe), nil, func(int) { t.Error("relative helper started") }); err == nil {
		t.Fatal("relative helper path accepted")
	}
	if _, _, err := runFixedHelper(ctx, exe, []string{"--journal-fixed-helper", "exit"}, func(int) {}); err == nil {
		t.Fatal("non-zero helper exit accepted")
	}
}

func TestDarwinJournalFixedHelperOutputCap(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{64 << 20, (64 << 20) + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, stderr, err := runFixedHelper(ctx, exe, []string{"--journal-fixed-helper", "output", strconv.Itoa(size)}, func(int) {})
			if len(stderr) != 0 {
				t.Fatalf("stderr = %q", stderr)
			}
			if size == 64<<20 {
				if err != nil || len(out) != size {
					t.Fatalf("exact cap: bytes=%d err=%v", len(out), err)
				}
			} else if err == nil || len(out) > 64<<20 {
				t.Fatalf("over cap: bytes=%d err=%v", len(out), err)
			}
		})
	}
}

func TestDarwinJournalFixedHelperKillsProcessGroup(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "ready.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan int, 1)
	result := make(chan error, 1)
	go func() {
		_, _, err := runFixedHelper(ctx, exe, []string{"--journal-fixed-helper", "fork", socket}, func(pid int) { started <- pid })
		result <- err
	}()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var child journalHelperReport
	if err := json.NewDecoder(conn).Decode(&child); err != nil {
		t.Fatal(err)
	}
	parent := <-started
	if child.PPID != parent {
		t.Fatalf("grandchild parent = %d, helper = %d", child.PPID, parent)
	}
	// pidfds identify the actual processes through reparenting and PID reuse.
	// Poll waits for death without a sleep or a helper process of its own.
	var descriptors []unix.PollFd
	for _, pid := range []int{parent, child.PID} {
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); _ = unix.Close(fd) })
		descriptors = append(descriptors, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
	}
	before := time.Now()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled helper succeeded")
		}
	case <-time.After(journalHelperWaitDelay):
		t.Fatal("helper exceeded WaitDelay after cancellation")
	}
	for _, descriptor := range descriptors {
		for {
			remaining := journalHelperWaitDelay - time.Since(before)
			if remaining <= 0 {
				t.Fatal("process group exceeded WaitDelay")
			}
			fds := []unix.PollFd{descriptor}
			n, err := unix.Poll(fds, int(remaining.Milliseconds()))
			if err == unix.EINTR {
				continue
			}
			if err != nil || n != 1 || fds[0].Revents&unix.POLLIN == 0 {
				t.Fatalf("process %d survived group cancellation: %v, %v", descriptor.Fd, fds, err)
			}
			break
		}
	}
}
