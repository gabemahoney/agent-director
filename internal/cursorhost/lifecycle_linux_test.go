//go:build linux

package cursorhost

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func awaitProcessGone(t *testing.T, pid int) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return
		}
		end := strings.LastIndex(string(raw), ")")
		if end >= 0 && strings.HasPrefix(string(raw)[end+2:], "Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remains accepting", pid)
}
func TestOwnedRuntimeProtectedAndTerminated(t *testing.T) {
	for _, chain := range []string{"direct", "two exec launchers"} {
		t.Run(chain, func(t *testing.T) {
			r := cursorfix.New(t)
			program := r.Program
			if chain == "two exec launchers" {
				for i := 0; i < 2; i++ {
					wrapper := fmt.Sprintf("%s/launcher-%d", t.TempDir(), i)
					if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", program)), 0700); err != nil {
						t.Fatal(err)
					}
					program = wrapper
				}
			}
			p, err := startProcess(program, []string{"acp"}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer p.input.Close()
			defer p.output.Close()
			defer p.terminate()
			start, alive, known := probe.NewProcChecker().StartTime(p.pid)
			if !known || !alive || start != p.start {
				t.Fatal("runtime identity not established")
			}
			rpc := newProtocol(p.input, p.output)
			t.Cleanup(rpc.close)
			if _, err = rpc.request("initialize", map[string]any{"protocolVersion": 1}); err != nil {
				t.Fatal(err)
			}
			f := nextFrame(t, rpc)
			if f.err != nil {
				t.Fatal(f.err)
			}
			if _, err = rpc.response(f.frame); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if err = p.terminate(); err != nil {
					t.Fatalf("repeated successful termination %d: %v", i, err)
				}
			}
			awaitProcessGone(t, p.pid)
		})
	}
}
func TestOwnedRuntimeUnexpectedDeathAndTransportEOF(t *testing.T) {
	for _, mode := range []string{"exit", "EOF live child", "child death with open pipe"} {
		t.Run(mode, func(t *testing.T) {
			script := "#!/bin/sh\n"
			if mode == "exit" {
				script += "exit 0\n"
			} else if mode == "EOF live child" {
				script += "exec 1>&-\nexec sleep 30\n"
			} else {
				script += "exec sleep 30\n"
			}
			path := t.TempDir() + "/agent"
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			p, err := startProcess(path, nil, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer p.terminate()
			defer p.input.Close()
			defer p.output.Close()
			if mode == "child death with open pipe" {
				holder, e := os.OpenFile(fmt.Sprintf("/proc/%d/fd/1", p.pid), os.O_WRONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer holder.Close()
				if e = syscall.Kill(p.pid, syscall.SIGKILL); e != nil {
					t.Fatal(e)
				}
				select {
				case <-p.done:
				case <-time.After(3 * time.Second):
					t.Fatal("open pipe concealed child death")
				}
			} else if mode == "exit" {
				select {
				case <-p.done:
				case <-time.After(3 * time.Second):
					t.Fatal("child loss not detected")
				}
			} else {
				rpc := newProtocol(p.input, p.output)
				t.Cleanup(rpc.close)
				if f := nextFrame(t, rpc); f.err == nil {
					t.Fatal("EOF did not fail transport")
				}
				if _, alive, known := probe.NewProcChecker().StartTime(p.pid); !known || !alive {
					t.Fatal("EOF fixture lost child unexpectedly")
				}
			}
		})
	}
}
func TestGatewayLossKillsProtectedRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("process crash regression")
	}
	r := cursorfix.New(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestGatewayCrashHelper$")
	cmd.Env = append(os.Environ(), "AD_CURSOR_CRASH_HELPER="+r.Program)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	line := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(out)
		if s.Scan() {
			line <- s.Text()
		}
	}()
	var pid int
	select {
	case raw := <-line:
		pid, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway helper did not establish protection")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	awaitProcessGone(t, pid)
}
func TestGatewayCrashHelper(t *testing.T) {
	path := os.Getenv("AD_CURSOR_CRASH_HELPER")
	if path == "" {
		t.Skip("subprocess helper")
	}
	p, err := startProcess(path, []string{"acp"}, os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(p.pid)
	for {
		time.Sleep(time.Hour)
	}
}
func TestFailedTerminationNeverReportsSuccess(t *testing.T) {
	p := &ownedProcess{done: make(chan error, 1)}
	p.done <- fmt.Errorf("termination evidence unavailable")
	if err := p.terminate(); err == nil {
		t.Fatal("termination failure reported success")
	}
}
func TestSecondACPOwnerRefusedAndSentinelSurvives(t *testing.T) {
	sentinel := exec.Command("sleep", "30")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { sentinel.Process.Kill(); sentinel.Wait() }()
	path := t.TempDir() + "/agent"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n: AfterChild\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := startProcess(path, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.input.Close()
	defer p.output.Close()
	defer p.terminate()
	select {
	case err = <-p.done:
		if err == nil || !strings.Contains(err.Error(), "second ACP owner") {
			t.Fatalf("second pipe owner not refused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second ACP owner remained live")
	}
	terminalErr := err
	// t2.jf1.3e.d6: Run consumes the notification before deferred cleanup.
	select {
	case _, open := <-p.done:
		if open {
			t.Fatal("supervisor published more than one terminal result")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not close terminal notification")
	}
	for i := 0; i < 3; i++ {
		if got := p.terminate(); got != terminalErr {
			t.Fatalf("cleanup %d lost real supervisor failure: got %v, want %v", i, got, terminalErr)
		}
	}
	if err = sentinel.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated sentinel killed: %v", err)
	}
}

func TestRuntimeBindingRequiresIndependentKernelFacts(t *testing.T) {
	r := cursorfix.New(t)
	p, err := startProcess(r.Program, []string{"acp"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.terminate()
	defer p.input.Close()
	defer p.output.Close()
	gatewayStart, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
	if !alive || !known {
		t.Fatal("gateway identity unavailable")
	}
	in, err := pipeAt(p.pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pipeAt(p.pid, 1)
	if err != nil {
		t.Fatal(err)
	}
	b := store.CursorRuntime{GatewayPID: os.Getpid(), GatewayStart: gatewayStart, RuntimePID: p.pid, RuntimeStart: p.start, InputPipe: in, OutputPipe: out, InputFD: int(p.input.Fd()), OutputFD: int(p.output.Fd())}
	for _, tc := range []struct {
		name   string
		change func(*store.CursorRuntime)
	}{
		{"current", func(*store.CursorRuntime) {}},
		{"missing gateway", func(b *store.CursorRuntime) { b.GatewayPID = 99999999 }},
		{"gateway reused", func(b *store.CursorRuntime) { b.GatewayStart += "changed" }},
		{"runtime reused", func(b *store.CursorRuntime) { b.RuntimeStart += "changed" }},
		{"input replaced", func(b *store.CursorRuntime) { b.InputPipe = "pipe:[missing]" }},
		{"output fd replaced", func(b *store.CursorRuntime) { b.OutputFD = int(p.input.Fd()) }},
		{"runtime missing", func(b *store.CursorRuntime) { b.RuntimePID = 99999999 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := b
			tc.change(&sample)
			err := VerifyRuntime(sample, false)
			if (err == nil) != (tc.name == "current") {
				t.Fatalf("runtime attribution %v", err)
			}
		})
	}
	writer, err := p.protocolInput()
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = VerifyRuntime(b, false); err != nil {
		t.Fatalf("transport close destroyed registered provenance: %v", err)
	}
	if err = VerifyRuntime(b, true); err == nil {
		t.Fatal("live child accepted as terminated")
	}
	if err = p.terminate(); err != nil {
		t.Fatal(err)
	}
	if err = VerifyRuntime(b, true); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentCursorWorkerSurvivesRuntimeTermination(t *testing.T) {
	r := cursorfix.NewWithWorker(t, true)
	p, err := startProcess(r.Program, []string{"acp"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.terminate()
	defer p.input.Close()
	defer p.output.Close()
	deadline := time.Now().Add(5 * time.Second)
	workerPID := 0
	for time.Now().Before(deadline) {
		raw, e := os.ReadFile(r.WorkerPID)
		if e == nil {
			workerPID, _ = strconv.Atoi(string(raw))
			if workerPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workerPID == 0 {
		t.Fatal("worker did not start")
	}
	t.Cleanup(func() { syscall.Kill(workerPID, syscall.SIGKILL) })
	if !cursorWorker(workerPID, p.pid) {
		t.Fatal("same-runtime independent worker unidentified")
	}
	if err = p.terminate(); err != nil {
		t.Fatal(err)
	}
	awaitProcessGone(t, p.pid)
	if _, alive, known := probe.NewProcChecker().StartTime(workerPID); !known || !alive {
		t.Fatal("independent worker was killed with conversation")
	}
	if err = os.WriteFile(r.WorkerRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitProcessGone(t, workerPID)
}
