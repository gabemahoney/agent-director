//go:build linux

package cursorhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"golang.org/x/sys/unix"
)

// ownedProcess keeps the accepting launcher chain ptraced with EXITKILL.
// Trace ownership is established at the first exec stop, before the launcher
// runs, and inherited at fork/clone stops before descendants can run. Helpers
// identified as independent Cursor workers are detached at exec; shared workers are never killed
// by process-group operations or treated as conversation liveness.
type ownedProcess struct {
	pid    int
	start  string
	input  *os.File
	output *os.File
	done   chan error
	stop   chan chan error
	// Published before done is closed; later stop calls retain the same
	// terminal failure after another observer consumed the channel value.
	exitErr error
}

// protocolInput gives the transport a separately closeable, pollable writer.
// Keep the registered descriptor open until end reporting has verified it.
func (p *ownedProcess) protocolInput() (*os.File, error) {
	fd, err := unix.FcntlInt(p.input.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("Cursor protocol writer unavailable")
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("Cursor protocol writer unavailable")
	}
	// NewFile registers a nonblocking pipe with Go's poller. Do not call Fd
	// on this handle: Fd switches a pipe back to blocking I/O.
	return os.NewFile(uintptr(fd), "cursor-acp-input"), nil
}

type processReady struct {
	p   *ownedProcess
	err error
}

func startProcess(program string, args []string, cwd string) (*ownedProcess, error) {
	ready := make(chan processReady, 1)
	go traceProcess(program, args, cwd, ready)
	r := <-ready
	return r.p, r.err
}
func traceProcess(program string, args []string, cwd string, ready chan<- processReady) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	childIn, parentIn, err := os.Pipe()
	if err != nil {
		ready <- processReady{err: err}
		return
	}
	parentOut, childOut, err := os.Pipe()
	if err != nil {
		childIn.Close()
		parentIn.Close()
		ready <- processReady{err: err}
		return
	}
	cmd := exec.Command(program, args...)
	cmd.Dir = cwd
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	// Runtime stderr may contain internal/auth diagnostics. The gateway shows
	// its own bounded outcomes; no raw runtime diagnostics reach the pane.
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Pdeathsig: syscall.SIGKILL}
	if err = cmd.Start(); err != nil {
		childIn.Close()
		childOut.Close()
		parentIn.Close()
		parentOut.Close()
		reason := "Cursor runtime could not start; cause unknown"
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			reason = "Cursor runtime executable or working directory unavailable; check cursor_path and project cwd"
		} else if errors.Is(err, os.ErrPermission) {
			reason = "Cursor runtime start denied by the operating system; check executable and project directory permissions"
		}
		ready <- processReady{err: fmt.Errorf("%s", reason)}
		return
	}
	childIn.Close()
	childOut.Close()
	pid := cmd.Process.Pid
	defer cmd.Process.Release()
	var status unix.WaitStatus
	if _, err = unix.Wait4(pid, &status, 0, nil); err != nil || !status.Stopped() {
		unix.Kill(pid, unix.SIGKILL)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor lifetime protection could not be established")}
		return
	}
	options := unix.PTRACE_O_EXITKILL | unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEEXEC
	if err = unix.PtraceSetOptions(pid, options); err != nil {
		unix.Kill(pid, unix.SIGKILL)
		unix.PtraceCont(pid, 0)
		unix.Wait4(pid, &status, 0, nil)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor protected lifetime is unsupported")}
		return
	}
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive {
		unix.Kill(pid, unix.SIGKILL)
		unix.PtraceCont(pid, 0)
		unix.Wait4(pid, &status, 0, nil)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor process identity unavailable")}
		return
	}
	p := &ownedProcess{pid: pid, start: start, input: parentIn, output: parentOut, done: make(chan error, 1), stop: make(chan chan error)}
	tracked := map[int]bool{pid: true}
	initial := map[int]bool{}
	in, _ := pipeAt(pid, 0)
	out, _ := pipeAt(pid, 1)
	err = unix.PtraceCont(pid, 0)
	if err != nil {
		unix.Kill(pid, unix.SIGKILL)
		unix.PtraceCont(pid, 0)
		unix.Wait4(pid, &status, 0, nil)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor protected launch failed")}
		return
	}
	ready <- processReady{p: p}
	var stopReply chan error
	var failure error
	rootGone := false
	for len(tracked) > 0 {
		select {
		case reply := <-p.stop:
			stopReply = reply
		default:
		}
		if stopReply != nil || rootGone || failure != nil {
			collectTracees(tracked, initial)
			for tid := range tracked {
				if e := unix.Kill(tid, unix.SIGKILL); e != nil && e != unix.ESRCH {
					failure = fmt.Errorf("Cursor termination failed")
				}
			}
		}
		for tid := range tracked {
			got, e := unix.Wait4(tid, &status, unix.WNOHANG|unix.WALL, nil)
			if e == unix.EINTR {
				continue
			}
			if e != nil {
				failure = fmt.Errorf("Cursor process evidence lost")
				delete(tracked, tid)
				continue
			}
			if got == 0 {
				continue
			}
			if status.Exited() || status.Signaled() {
				if tid == pid {
					collectTracees(tracked, initial)
				}
				delete(tracked, tid)
				delete(initial, tid)
				if tid == pid {
					rootGone = true
					for other := range tracked {
						unix.Kill(other, unix.SIGKILL)
					}
				}
				continue
			}
			if !status.Stopped() {
				continue
			}
			sig := int(status.StopSignal())
			if initial[tid] {
				delete(initial, tid)
				sig = 0
			}
			event := status.TrapCause()
			if event == unix.PTRACE_EVENT_FORK || event == unix.PTRACE_EVENT_VFORK || event == unix.PTRACE_EVENT_CLONE {
				child, e := unix.PtraceGetEventMsg(tid)
				if e != nil {
					failure = fmt.Errorf("Cursor descendant protection failed")
					unix.Kill(pid, unix.SIGKILL)
				} else {
					tracked[int(child)] = true
					initial[int(child)] = true
				}
				sig = 0
			}
			if event == unix.PTRACE_EVENT_EXEC {
				sig = 0
				if tid != pid {
					ci, _ := pipeAt(tid, 0)
					co, _ := pipeAt(tid, 1)
					if ci == in && co == out {
						failure = fmt.Errorf("Cursor launcher has a second ACP owner; use an exec launcher")
						for owned := range tracked {
							unix.Kill(owned, unix.SIGKILL)
						}
					} else if ci != in && co != out && cursorWorker(tid, pid) {
						if e := unix.PtraceDetach(tid); e == nil {
							delete(tracked, tid)
							continue
						} else {
							failure = fmt.Errorf("Cursor worker isolation failed")
							unix.Kill(pid, unix.SIGKILL)
						}
					}
				}
			}
			if sig == int(unix.SIGTRAP) || sig == int(unix.SIGSTOP) {
				sig = 0
			}
			if e := unix.PtraceCont(tid, sig); e != nil && e != unix.ESRCH {
				failure = fmt.Errorf("Cursor protection failed")
				unix.Kill(pid, unix.SIGKILL)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !rootGone && failure == nil {
		failure = fmt.Errorf("Cursor exit was not confirmed")
	}
	p.exitErr = failure
	p.done <- failure
	close(p.done)
	if stopReply != nil {
		stopReply <- failure
	}
}

// SIGKILL can overtake a fork/clone event before its child ID is reported.
// That automatically attached child still needs its initial ptrace stop
// consumed, or a thread can prevent the leader from completing group exit.
// Discover only processes/tasks that this exact tracer thread owns; waiting
// for arbitrary children would steal unrelated statuses. Process enumeration
// also finds a traced fork child reparented when its launcher was killed.
func collectTracees(tracked, initial map[int]bool) {
	tracer := unix.Gettid()
	add := func(tid int) {
		if tracked[tid] {
			return
		}
		owner, err := procStatus(tid, "TracerPid")
		if err == nil && owner == tracer {
			tracked[tid] = true
			initial[tid] = true
		}
	}
	processes, _ := os.ReadDir("/proc")
	for _, process := range processes {
		child, err := strconv.Atoi(process.Name())
		if err == nil {
			add(child)
		}
	}
	for tid := range tracked {
		tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", tid))
		for _, task := range tasks {
			child, err := strconv.Atoi(task.Name())
			if err == nil {
				add(child)
			}
		}
	}
}

func (p *ownedProcess) terminate() error {
	reply := make(chan error, 1)
	select {
	case p.stop <- reply:
	case err, ok := <-p.done:
		if !ok {
			return p.exitErr
		}
		return err
	case <-time.After(3 * time.Second):
		return fmt.Errorf("Cursor termination could not be requested")
	}
	select {
	case err := <-reply:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("Cursor termination is not confirmed")
	}
}

// Cursor's documented local worker is independent from the ACP conversation
// and may be shared by other launches. Only that explicit role, with the same
// executable as the selected runtime and no ACP pipe, is detached. Unknown
// descendants remain protected by EXITKILL rather than inferred independent.
func cursorWorker(pid, owner int) bool {
	workerExe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil {
		return false
	}
	ownerExe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", owner))
	if e != nil || workerExe != ownerExe {
		return false
	}
	raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if e != nil || len(raw) > 64*1024 {
		return false
	}
	args := strings.Split(string(raw), "\x00")
	// Installed Cursor's Node worker argv is node <runtime index.js>
	// worker-server. Native Cursor builds may use <runtime> worker-server.
	if len(args) == 3 && args[1] == "worker-server" && args[2] == "" {
		return true
	}
	if len(args) != 4 || !strings.HasSuffix(args[1], "/index.js") || args[2] != "worker-server" || args[3] != "" {
		return false
	}
	ownerRaw, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", owner))
	if e != nil || len(ownerRaw) > 64*1024 {
		return false
	}
	for _, arg := range strings.Split(string(ownerRaw), "\x00") {
		if arg == args[1] {
			return true
		}
	}
	return false
}
