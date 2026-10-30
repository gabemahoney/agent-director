//go:build linux

package cursorhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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

// Hooks are private to one owner. Nil fields retain the real kernel operations;
// observations run on the pinned thread without changing ownership or evidence.
type processTraceHooks struct {
	observe    func(string, int, unix.WaitStatus)
	wait       func(int, *unix.WaitStatus, int) (int, error)
	procStatus func(int) ([]byte, error)
	readDir    func(string) ([]os.DirEntry, error)
	eventMsg   func(int) (uint, error)
}

func (h *processTraceHooks) observed(event string, tid int, status unix.WaitStatus) {
	if h != nil && h.observe != nil {
		h.observe(event, tid, status)
	}
}

func (h *processTraceHooks) statusSnapshot(tid int) ([]byte, error) {
	if h != nil && h.procStatus != nil {
		return h.procStatus(tid)
	}
	return readProcStatus(tid)
}

func (h *processTraceHooks) statusField(tid int, field string) (int, error) {
	raw, err := h.statusSnapshot(tid)
	if err != nil {
		return 0, err
	}
	return procStatusField(raw, field)
}

func (h *processTraceHooks) ownedGroup(tid int) (group int, owned bool, err error) {
	// Both facts must describe the same captured task. Reopening a numeric
	// proc path for the second field can splice in a recycled TID's group.
	raw, err := h.statusSnapshot(tid)
	if err != nil {
		return 0, false, err
	}
	owner, err := procStatusField(raw, "TracerPid")
	if err != nil || owner != unix.Gettid() {
		return 0, false, err
	}
	group, err = procStatusField(raw, "Tgid")
	if err == nil && group <= 0 {
		err = fmt.Errorf("process evidence unavailable")
	}
	return group, true, err
}

func (h *processTraceHooks) directory(path string) ([]os.DirEntry, error) {
	if h != nil && h.readDir != nil {
		return h.readDir(path)
	}
	return os.ReadDir(path)
}

func (h *processTraceHooks) execMessage(tid int) (uint, error) {
	if h != nil && h.eventMsg != nil {
		return h.eventMsg(tid)
	}
	return unix.PtraceGetEventMsg(tid)
}

func startProcess(program string, args []string, cwd string, environ []string) (*ownedProcess, error) {
	return startProcessWithHooks(program, args, cwd, environ, nil)
}

func startProcessWithHooks(program string, args []string, cwd string, environ []string, hooks *processTraceHooks) (*ownedProcess, error) {
	ready := make(chan processReady, 1)
	go traceProcess(program, args, cwd, environ, ready, hooks)
	r := <-ready
	return r.p, r.err
}
func traceProcess(program string, args []string, cwd string, environ []string, ready chan<- processReady, hooks *processTraceHooks) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Register before launch, including the first exec stop. A buffered hint
	// survives a status arriving during a drain or just before we block.
	// Stop only this subscription: another runtime may have its own owner.
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGCHLD)
	hooks.observed("subscribed", unix.Gettid(), 0)
	defer func() {
		signal.Stop(wake)
		hooks.observed("unsubscribed", unix.Gettid(), 0)
	}()
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
	cmd.Env = environ
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	// Runtime stderr may contain internal/auth diagnostics. The gateway shows
	// its own bounded outcomes; no raw runtime diagnostics reach the pane.
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Pdeathsig: syscall.SIGKILL}
	hooks.observed("launch", unix.Gettid(), 0)
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
	if _, err = waitTrace(pid, &status, 0, hooks); err != nil || !status.Stopped() {
		// A terminal wait status already released the child; never signal that
		// numeric PID after reaping it. Unknown evidence is not success.
		if err != nil {
			if owner, e := hooks.statusField(pid, "TracerPid"); e == nil && owner == unix.Gettid() {
				stopUnstartedTracee(pid, hooks)
			}
		}
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor lifetime protection could not be established")}
		return
	}
	options := unix.PTRACE_O_EXITKILL | unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEEXEC | unix.PTRACE_O_TRACEEXIT
	if err = unix.PtraceSetOptions(pid, options); err != nil {
		stopUnstartedTracee(pid, hooks)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor protected lifetime is unsupported")}
		return
	}
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive {
		stopUnstartedTracee(pid, hooks)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor process identity unavailable")}
		return
	}
	p := &ownedProcess{pid: pid, start: start, input: parentIn, output: parentOut, done: make(chan error, 1), stop: make(chan chan error)}
	in, inErr := pipeAt(pid, 0)
	out, outErr := pipeAt(pid, 1)
	if inErr != nil || outErr != nil {
		stopUnstartedTracee(pid, hooks)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor process identity unavailable")}
		return
	}
	err = unix.PtraceCont(pid, 0)
	if err != nil {
		stopUnstartedTracee(pid, hooks)
		parentIn.Close()
		parentOut.Close()
		ready <- processReady{err: fmt.Errorf("Cursor protected launch failed")}
		return
	}
	ready <- processReady{p: p}
	owner := processTrace{
		process: p, inputPipe: in, outputPipe: out,
		tracked: map[int]*ownedTracee{pid: {group: pid}},
		hooks:   hooks,
	}
	owner.run(wake)
}

// waitTrace retries an interrupted syscall, never a timed status probe. All
// calls (including startup cleanup) stay on the locked ptrace owner thread.
func waitTrace(tid int, status *unix.WaitStatus, options int, hooks *processTraceHooks) (int, error) {
	// A former nonleader TID can be reused by a sibling owner's child. The
	// kernel must enforce this thread's ownership before consuming any status.
	options |= unix.WALL | unix.WNOTHREAD
	for {
		hooks.observed("wait", tid, 0)
		var got int
		var err error
		if hooks != nil && hooks.wait != nil {
			got, err = hooks.wait(tid, status, options)
		} else {
			got, err = unix.Wait4(tid, status, options, nil)
		}
		if err != unix.EINTR {
			if err == nil && got != 0 {
				hooks.observed("status", got, *status)
			}
			return got, err
		}
	}
}

// Before the first continue there can be no descendants. Even an early exit
// stop needs continuing and a real terminal wait; it is not death evidence.
func stopUnstartedTracee(pid int, hooks *processTraceHooks) {
	_ = unix.Kill(pid, unix.SIGKILL)
	for {
		var status unix.WaitStatus
		if _, err := waitTrace(pid, &status, 0, hooks); err != nil || status.Exited() || status.Signaled() {
			return
		}
		if status.Stopped() {
			_ = unix.PtraceCont(pid, 0)
		}
	}
}

type ownedTracee struct {
	initial bool
	group   int
	killed  bool
	// An EXEC binds only already-owned members of this group. Its
	// terminal status can explain one vanished former TID, never another group.
	exec *traceExec
}

type traceExec struct {
	terminal bool
	former   *ownedTracee
}

type processTrace struct {
	process *ownedProcess
	tracked map[int]*ownedTracee
	// Lost nonleaders retain evidence, not a claim on their reusable number.
	// Both keys and group owners are captured objects, not numeric TIDs.
	lost                  map[*ownedTracee]*ownedTracee
	inputPipe, outputPipe string
	stopping, rootGone    bool
	failure               error
	replies               []chan error
	hooks                 *processTraceHooks
}

func (o *processTrace) fail(reason string) {
	if o.failure == nil {
		o.failure = fmt.Errorf("%s", reason)
	}
}

func (o *processTrace) run(wake <-chan os.Signal) {
	for {
		// Startup handoff and every causal wake drain all immediately available
		// owned statuses. SIGCHLD is neither a count nor evidence of an exit.
		o.drain()
		if len(o.tracked) == 0 {
			if len(o.lost) != 0 {
				o.fail("Cursor process evidence lost")
			}
			break
		}
		o.hooks.observed("block", o.process.pid, 0)
		select {
		case <-wake:
		case reply := <-o.process.stop:
			o.replies = append(o.replies, reply)
			o.stop()
		}
	}
	if !o.rootGone && o.failure == nil {
		o.fail("Cursor exit was not confirmed")
	}
	o.process.exitErr = o.failure
	o.process.done <- o.failure
	close(o.process.done)
	for _, reply := range o.replies {
		// terminate supplies a buffered reply even if its caller times out.
		reply <- o.failure
	}
}

func (o *processTrace) stop() {
	if !o.stopping {
		o.stopping = true
		o.collectTracees()
	}
	for tid, tracee := range o.tracked {
		if tracee.killed {
			continue
		}
		if tracee.group == 0 {
			o.admitTracee(tid, true)
			if tracee.group == 0 {
				continue
			}
		}
		tracee.killed = true
		// A nonleader can exec between this call and its next wait. Bind the
		// signal to its captured thread group as well as TID, so a recycled
		// former TID in an unrelated process is never signalled. Membership
		// grants no ownership: only the already traced task is targeted.
		o.hooks.observed("signal", tid, 0)
		if err := unix.Tgkill(tracee.group, tid, unix.SIGKILL); err != nil && err != unix.ESRCH {
			o.fail("Cursor termination failed")
		}
	}
}

func (o *processTrace) drain() {
	o.hooks.observed("drain", o.process.pid, 0)
	for {
		// A busy descendant must not starve an explicit stop request.
		select {
		case reply := <-o.process.stop:
			o.replies = append(o.replies, reply)
			o.stop()
		default:
		}
		if o.rootGone || o.failure != nil || o.stopping {
			o.stop()
		}
		progress := false
		for tid, tracee := range o.tracked {
			// A vanished nonleader can be replaced before its first ECHILD.
			// Admit any newly inherited group before consuming that exact wait.
			if tracee.group != tid {
				o.admitTracee(tid, true)
				tracee = o.tracked[tid]
			}
			var status unix.WaitStatus
			got, err := waitTrace(tid, &status, unix.WNOHANG, o.hooks)
			if err != nil {
				if err == unix.ECHILD && tracee.group != tid {
					o.loseTracee(tid, tracee)
				} else {
					o.fail("Cursor process evidence lost")
					delete(o.tracked, tid)
					o.retireMissingThreads(tracee)
				}
				progress = true
				continue
			}
			if got != 0 {
				progress = true
				o.status(tid, tracee, status)
			}
		}
		// Revisit earlier TIDs and descendants added during this pass. A
		// single pass can strand a coalesced stop behind its parent's event.
		if !progress {
			return
		}
	}
}

func (o *processTrace) loseTracee(tid int, tracee *ownedTracee) {
	delete(o.tracked, tid)
	if tracee.exec != nil && tracee.exec.terminal {
		o.retireExecThread(tracee)
		return
	}
	leader := o.tracked[tracee.group]
	if leader == nil || tracee.group == tid {
		o.fail("Cursor process evidence lost")
		return
	}
	if o.lost == nil {
		o.lost = make(map[*ownedTracee]*ownedTracee)
	}
	o.lost[tracee] = leader
}

func (o *processTrace) retireMissingThreads(group *ownedTracee) {
	for tracee, leader := range o.lost {
		if leader == group {
			o.retireExecThread(tracee)
			delete(o.lost, tracee)
		}
	}
}

func (o *processTrace) retireExecThread(tracee *ownedTracee) {
	// ECHILD alone is not death. A captured EXEC followed by this same
	// group's terminal status explains at most one former nonleader TID.
	if tracee.exec == nil || !tracee.exec.terminal || (tracee.exec.former != nil && tracee.exec.former != tracee) {
		o.fail("Cursor process evidence lost")
	} else {
		tracee.exec.former = tracee
	}
}

// de_thread has exited the old nonleaders before reporting EXEC. Resolve
// their wait slots before continuing the surviving leader, so it cannot create
// a new same-group task behind an old numeric entry. Another owned group can
// already have reused a vanished TID; admit that task with fresh evidence.
func (o *processTrace) drainExecMember(tid int, tracee *ownedTracee) {
	o.admitTracee(tid, true)
	if o.tracked[tid] != tracee {
		return
	}
	var status unix.WaitStatus
	got, err := waitTrace(tid, &status, unix.WNOHANG, o.hooks)
	if err == unix.ECHILD {
		o.loseTracee(tid, tracee)
		return
	}
	if err == nil && got != 0 && (status.Exited() || status.Signaled()) {
		o.status(tid, tracee, status)
		return
	}
	// A replacement may have arrived between discovery and this exact wait.
	// Its initial stop belongs to its freshly observed group, not the old one.
	o.admitTracee(tid, true)
	if current := o.tracked[tid]; current != tracee {
		if err == nil && got != 0 && current != nil {
			o.status(tid, current, status)
		}
		return
	}
	// A still-running or stopped old nonleader contradicts the EXEC boundary.
	// Keep cleanup authority, but never turn that uncertainty into success.
	o.fail("Cursor process evidence lost")
}

func (o *processTrace) status(tid int, tracee *ownedTracee, status unix.WaitStatus) {
	if status.Exited() || status.Signaled() {
		if tracee.group == tid {
			// A group can exec again before an earlier former TID retires.
			// This exact terminal status completes each captured transition,
			// retaining its separate allowance for one vanished member.
			for _, member := range o.tracked {
				if member.group == tid && member.exec != nil {
					member.exec.terminal = true
				}
			}
			for member, leader := range o.lost {
				if leader == tracee && member.exec != nil {
					member.exec.terminal = true
				}
			}
		}
		delete(o.tracked, tid)
		o.retireMissingThreads(tracee)
		if tid == o.process.pid {
			o.rootGone = true
		}
		// Drop only the consumed identity before discovery: its number can
		// already name a new inherited child after this terminal wait.
		if tid == o.process.pid || status.Signaled() {
			o.collectTracees()
		}
		return
	}
	if !status.Stopped() {
		return
	}
	if tracee.group != tid {
		o.admitTracee(tid, true)
		if current := o.tracked[tid]; current != nil {
			tracee = current
		}
	}
	if tracee.group == 0 {
		o.fail("Cursor process evidence lost")
	}
	sig := int(status.StopSignal())
	if tracee.initial {
		tracee.initial = false
		sig = 0
	}
	switch status.TrapCause() {
	case unix.PTRACE_EVENT_EXIT:
		// The leader's final status can be withheld until inherited tasks'
		// initial stops have been drained. This early stop is not death.
		o.collectTracees()
		sig = 0
	case unix.PTRACE_EVENT_FORK, unix.PTRACE_EVENT_VFORK, unix.PTRACE_EVENT_CLONE:
		// Group exit can overtake this stop before GETEVENTMSG: the message
		// may already be an exit code, or the request may fail with ESRCH.
		// Discover inherited tasks by this thread's exact TracerPid instead
		// of treating that mutable message as a child identity or death proof.
		o.collectTracees()
		sig = 0
	case unix.PTRACE_EVENT_EXEC:
		sig = 0
		former, err := o.hooks.execMessage(tid)
		if tracee.group != tid || (err != nil && err != unix.ESRCH) {
			o.fail("Cursor process evidence lost")
		} else {
			// SIGKILL/group exit can replace the mutable event message, or
			// make GETEVENTMSG return ESRCH. Even a value naming the leader or
			// another captured member cannot identify the former execing TID.
			// Keep every captured nonleader for exact terminal reconciliation;
			// no message value can retire a task or select another group.
			transition := &traceExec{}
			reconcile := err != nil || former != uint(tid)
			for member, leader := range o.lost {
				if leader == tracee {
					if member.exec == nil {
						member.exec = transition
					}
					reconcile = true
				}
			}
			for memberTID, member := range o.tracked {
				if memberTID != tid && member.group == tid {
					// Earlier execs keep their own former-member allowance.
					if member.exec == nil {
						member.exec = transition
					}
					reconcile = true
				}
			}
			// Bind the full captured set before any terminal member wait can
			// discover a replacement and move another member into lost evidence.
			for memberTID, member := range o.tracked {
				if memberTID != tid && member.group == tid {
					o.drainExecMember(memberTID, member)
				}
			}
			// Only an ordinary leader exec with no pending group members
			// retains the existing worker-detach path below.
			if reconcile {
				tracee.exec = transition
				tracee.killed = false
			}
		}
		o.collectTracees()
		if tid != o.process.pid {
			ci, inErr := pipeAt(tid, 0)
			co, outErr := pipeAt(tid, 1)
			if inErr == nil && outErr == nil && ci == o.inputPipe && co == o.outputPipe {
				o.fail("Cursor launcher has a second ACP owner; use an exec launcher")
			} else if tracee.exec == nil && !o.stopping && o.failure == nil && ci != o.inputPipe && co != o.outputPipe && cursorWorker(tid, o.process.pid) {
				// A non-pipe descriptor is positive evidence of a different
				// endpoint; an unreadable descriptor is not a detach grant.
				in, ie := os.Readlink(fmt.Sprintf("/proc/%d/fd/0", tid))
				out, oe := os.Readlink(fmt.Sprintf("/proc/%d/fd/1", tid))
				if ie == nil && oe == nil && in != o.inputPipe && out != o.outputPipe {
					if err := unix.PtraceDetach(tid); err == nil {
						delete(o.tracked, tid)
						return
					}
					o.fail("Cursor worker isolation failed")
				}
			}
		}
	}
	if sig == int(unix.SIGTRAP) || sig == int(unix.SIGSTOP) {
		sig = 0
	}
	if o.stopping || o.failure != nil || o.rootGone {
		o.stop()
	}
	if err := unix.PtraceCont(tid, sig); err != nil && err != unix.ESRCH {
		o.fail("Cursor protection failed")
	}
}

// Group exit or SIGKILL can overtake a fork/clone announcement. Reconcile only
// at causal events, matching TracerPid to this exact locked owner thread.
// Neither process groups nor arbitrary-child waits establish ownership.
func (o *processTrace) admitTracee(tid int, required bool) {
	previous := o.tracked[tid]
	if previous != nil && previous.group == tid {
		return
	}
	group, owned, err := o.hooks.ownedGroup(tid)
	if err == nil && owned {
		if previous != nil {
			if previous.group == group {
				return
			}
			if previous.group == 0 {
				previous.group = group
				return
			}
			// The old nonleader's number now names another inherited task.
			// Preserve its transition obligation independently of the new owner.
			o.loseTracee(tid, previous)
		}
		o.tracked[tid] = &ownedTracee{initial: true, group: group}
	} else if (owned || required || previous != nil) && err != nil && !os.IsNotExist(err) && !errors.Is(err, unix.ESRCH) {
		o.fail("Cursor process evidence lost")
	}
}

func (o *processTrace) collectTracees() {
	o.hooks.observed("collect", o.process.pid, 0)
	tracer := unix.Gettid()
	processes, err := o.hooks.directory("/proc")
	if err != nil {
		o.fail("Cursor process evidence lost")
		return
	}
	for _, process := range processes {
		if child, err := strconv.Atoi(process.Name()); err == nil {
			// An unreadable global candidate is not evidence about this owner.
			o.admitTracee(child, false)
		}
	}
	for tid := range o.tracked {
		owner, err := o.hooks.statusField(tid, "TracerPid")
		if err != nil {
			if !os.IsNotExist(err) && !errors.Is(err, unix.ESRCH) {
				o.fail("Cursor process evidence lost")
			}
			continue
		}
		if owner != tracer {
			continue
		}
		tasks, err := o.hooks.directory(fmt.Sprintf("/proc/%d/task", tid))
		if err != nil && !os.IsNotExist(err) && !errors.Is(err, unix.ESRCH) {
			o.fail("Cursor process evidence lost")
		}
		for _, task := range tasks {
			if child, err := strconv.Atoi(task.Name()); err == nil {
				o.admitTracee(child, true)
			}
		}
	}
}

func (p *ownedProcess) terminate() error {
	return p.terminateWithTimer(time.After)
}

func (p *ownedProcess) terminateWithTimer(after func(time.Duration) <-chan time.Time) error {
	reply := make(chan error, 1)
	select {
	case p.stop <- reply:
	case err, ok := <-p.done:
		if !ok {
			return p.exitErr
		}
		return err
	case <-after(3 * time.Second):
		return fmt.Errorf("Cursor termination could not be requested")
	}
	select {
	case err := <-reply:
		return err
	case <-after(5 * time.Second):
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
