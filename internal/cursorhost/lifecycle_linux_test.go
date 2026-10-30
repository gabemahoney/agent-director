//go:build linux

package cursorhost

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"golang.org/x/sys/unix"
)

// TestOwnedDescendantHelper runs only in the owned subprocess tree.
func TestOwnedDescendantHelper(t *testing.T) {
	role := os.Getenv("AD_TEST_OWNED_DESCENDANT")
	if role == "" {
		t.Skip("subprocess helper")
	}
	if role == "child" {
		var ready sync.WaitGroup
		ready.Add(8)
		for i := 0; i < 8; i++ {
			go func() { runtime.LockOSThread(); ready.Done(); select {} }()
		}
		ready.Wait()
		os.Exit(0)
	}
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		if input.Text() == "children" {
			for i := 0; i < 16; i++ {
				child := exec.Command(os.Args[0], "-test.run=^TestOwnedDescendantHelper$")
				child.Env = append(os.Environ(), "AD_TEST_OWNED_DESCENDANT=child")
				if err := child.Run(); err != nil {
					t.Fatal(err)
				}
			}
			fmt.Println("all children waited")
		} else {
			fmt.Println("responsive:" + input.Text())
		}
	}
}

func TestOwnedRuntimeReapsNormalDescendantsBeforeTermination(t *testing.T) {
	p, err := startProcess(os.Args[0], []string{"-test.run=^TestOwnedDescendantHelper$"}, t.TempDir(), append(os.Environ(), "AD_TEST_OWNED_DESCENDANT=root"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.terminate()
	defer p.input.Close()
	defer p.output.Close()
	lines := ownedProcessLines(p)
	for _, exchange := range []struct{ request, response string }{{"children", "all children waited"}, {"ping", "responsive:ping"}} {
		if _, err := fmt.Fprintln(p.input, exchange.request); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-lines:
			if got != exchange.response {
				t.Fatalf("response=%q want %q", got, exchange.response)
			}
		case err := <-p.done:
			t.Fatalf("accepting root exited before normal descendant waits: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("owned root did not complete %q before termination", exchange.request)
		}
	}
}

func TestOwnedRuntimeForkVforkAndNonleaderExec(t *testing.T) {
	program := cursorfix.TraceLifecycle(t)
	for _, mode := range []string{"burst", "nonleader"} {
		t.Run(mode, func(t *testing.T) {
			p, err := startProcess(program, []string{mode}, t.TempDir(), os.Environ())
			if err != nil {
				t.Fatal(err)
			}
			defer p.input.Close()
			defer p.output.Close()
			defer p.terminate()
			lines := ownedProcessLines(p)
			if mode == "nonleader" {
				var pid, tid int
				line := ownedProcessLine(t, lines)
				if _, err := fmt.Sscanf(line, "exec-thread %d %d", &pid, &tid); err != nil || pid != p.pid || tid == pid {
					t.Fatalf("not a real nonleader: %q pid=%d tid=%d err=%v", line, pid, tid, err)
				}
				if _, err := p.input.WriteString("x"); err != nil {
					t.Fatal(err)
				}
				if got, want := ownedProcessLine(t, lines), fmt.Sprintf("after-exec %d %d", p.pid, p.pid); got != want {
					t.Fatalf("nonleader exec remap=%q, want %q", got, want)
				}
				if start, alive, known := probe.NewProcChecker().StartTime(p.pid); !known || !alive || start != p.start {
					t.Fatal("exec lost the pinned leader identity")
				}
			} else {
				if _, err := p.input.WriteString("b"); err != nil {
					t.Fatal(err)
				}
				if got := ownedProcessLine(t, lines); got != "waited 16 exit=17" {
					t.Fatalf("owned descendants not independently waitable: %q", got)
				}
			}
			if _, err := p.input.WriteString("q"); err != nil {
				t.Fatal(err)
			}
			select {
			case err, ok := <-p.done:
				if !ok || err != nil {
					t.Fatalf("terminal result=%v open=%t", err, ok)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("real trace events did not drain to completion")
			}
			awaitProcessGone(t, p.pid)
			if _, ok := <-p.done; ok {
				t.Fatal("published a second terminal result")
			}
			if err := p.terminate(); err != nil {
				t.Fatalf("completed owner lost successful result: %v", err)
			}
		})
	}
}

// Output is a child-controlled barrier, never a guessed delay before a kernel event.
func ownedProcessLines(p *ownedProcess) <-chan string {
	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(p.output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

func ownedProcessLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("tracee output closed before its receipt")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("tracee did not reach its event barrier")
		return ""
	}
}

func TestOwnedRuntimeSubscriptionsAndUnrelatedWait(t *testing.T) {
	// The second owner remains live across both failed startup and normal retirement.
	p, err := startProcess(os.Args[0], []string{"-test.run=^TestOwnedDescendantHelper$"}, t.TempDir(), append(os.Environ(), "AD_TEST_OWNED_DESCENDANT=root"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.input.Close()
	defer p.output.Close()
	defer p.terminate()
	lines := ownedProcessLines(p)
	for _, program := range []string{t.TempDir() + "/missing", cursorfix.TraceLifecycle(t)} {
		other, err := startProcess(program, []string{"nonleader"}, t.TempDir(), os.Environ())
		if other == nil {
			if err == nil || !strings.HasSuffix(program, "/missing") {
				t.Fatalf("unexpected start result: %s %v", program, err)
			}
		} else {
			defer other.input.Close()
			defer other.output.Close()
			defer other.terminate()
			otherLines := ownedProcessLines(other)
			var pid, tid int
			if line := ownedProcessLine(t, otherLines); !strings.HasPrefix(line, "exec-thread ") {
				t.Fatalf("second owner did not reach nonleader exec: %q", line)
			} else if _, err := fmt.Sscanf(line, "exec-thread %d %d", &pid, &tid); err != nil || pid != other.pid || tid == pid {
				t.Fatalf("second owner did not establish a distinct exec thread: %q", line)
			}
			if _, err := other.input.WriteString("x"); err != nil {
				t.Fatal(err)
			}
			if got := ownedProcessLine(t, otherLines); got != fmt.Sprintf("after-exec %d %d", other.pid, other.pid) {
				t.Fatalf("second owner's real nonleader exec failed: %q", got)
			}
			if err := other.terminate(); err != nil {
				t.Fatal(err)
			}
			awaitProcessGone(t, other.pid)
		}
		// No owner may consume this exec.Cmd's status, even on coalesced SIGCHLD.
		unrelated := exec.Command("/bin/sh", "-c", "exit 23")
		if err := unrelated.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(p.input, "children"); err != nil {
			t.Fatal(err)
		}
		if got := ownedProcessLine(t, lines); got != "all children waited" {
			t.Fatalf("remaining owner's subscription lost: %q", got)
		}
		if err := unrelated.Wait(); err == nil || unrelated.ProcessState == nil || unrelated.ProcessState.ExitCode() != 23 {
			t.Fatalf("unrelated wait status lost: %v %v", err, unrelated.ProcessState)
		}
	}
	if err := p.terminate(); err != nil {
		t.Fatal(err)
	}
	awaitProcessGone(t, p.pid)
}

func TestOwnedWaitCannotConsumeSiblingThreadChild(t *testing.T) {
	// A stale numeric nonleader TID must not acquire a sibling thread's child.
	// Pin both callers so this exercises the kernel boundary, not a flag mock.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	child := exec.Command("/bin/sh", "-c", "exit 23")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Wait()
	awaitProcessGone(t, child.Process.Pid)
	type result struct {
		pid int
		err error
	}
	finished := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var status unix.WaitStatus
		pid, err := waitTrace(child.Process.Pid, &status, unix.WNOHANG, nil)
		finished <- result{pid, err}
	}()
	select {
	case got := <-finished:
		if got.pid > 0 || got.err != unix.ECHILD {
			t.Errorf("unrelated child status crossed owner threads: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exact owner wait did not return")
	}
	if err := child.Wait(); err == nil || child.ProcessState == nil || child.ProcessState.ExitCode() != 23 {
		t.Fatalf("unrelated exec.Cmd lost its actual status: %v %v", err, child.ProcessState)
	}
}

func TestOwnedRuntimeCausalForkAndExecRaces(t *testing.T) {
	program := cursorfix.TraceLifecycle(t)
	for _, mode := range []string{"fork-stop", "exec-child", "nonleader"} {
		t.Run(mode, func(t *testing.T) {
			type event struct {
				tid, child int
				err        error
			}
			entered, release := make(chan event, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var root atomic.Int64
			terminals := map[int]bool{}
			held := false
			hooks := &processTraceHooks{observe: func(point string, tid int, status unix.WaitStatus) {
				if point != "status" {
					return
				}
				if status.Exited() || status.Signaled() {
					terminals[tid] = true
				}
				if held || !status.Stopped() || root.Load() == 0 {
					return
				}
				wanted := status.TrapCause() == unix.PTRACE_EVENT_EXEC
				if mode == "fork-stop" {
					wanted = status.TrapCause() == unix.PTRACE_EVENT_FORK
				}
				if !wanted {
					return
				}
				held = true
				e := event{tid: tid, child: tid}
				if mode == "fork-stop" {
					// Diagnostic only, before any kill: capture the child for the
					// independent death assertion, not to grant owner authority.
					child, err := unix.PtraceGetEventMsg(tid)
					e.child, e.err = int(child), err
				}
				entered <- e
				<-release
			}}
			p, err := startProcessWithHooks(program, []string{mode}, t.TempDir(), os.Environ(), hooks)
			if err != nil {
				t.Fatal(err)
			}
			defer p.input.Close()
			defer p.output.Close()
			defer p.terminate()
			defer unblock()
			root.Store(int64(p.pid))
			lines := ownedProcessLines(p)
			ready := ownedProcessLine(t, lines)
			command := "f"
			if mode == "nonleader" {
				if !strings.HasPrefix(ready, "exec-thread ") {
					t.Fatalf("nonleader not ready: %q", ready)
				}
				command = "x"
			} else if ready != "fork-ready" {
				t.Fatalf("fork not ready: %q", ready)
			}
			if _, err := p.input.WriteString(command); err != nil {
				t.Fatal(err)
			}
			var observed event
			select {
			case observed = <-entered:
				if observed.err != nil || observed.child <= 0 {
					t.Fatalf("event identity: %+v", observed)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("real event barrier not reached")
			}
			switch mode {
			case "fork-stop":
				if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				unblock()
			case "exec-child":
				if _, err := p.input.WriteString("k"); err != nil {
					t.Fatal(err)
				}
				if got := ownedProcessLine(t, lines); got != "kill-sent" {
					t.Fatalf("runtime did not kill its exec-stopped child: %q", got)
				}
				unblock()
				if got := ownedProcessLine(t, lines); got != "waited child signal=9" {
					t.Fatalf("lost actual descendant wait result: %q", got)
				}
				if _, err := p.input.WriteString("p"); err != nil {
					t.Fatal(err)
				}
				if got := ownedProcessLine(t, lines); got != "responsive" {
					t.Fatalf("overtaken EXEC killed healthy root: %q", got)
				}
				if _, err := p.input.WriteString("q"); err != nil {
					t.Fatal(err)
				}
			case "nonleader":
				requested := make(chan time.Duration, 2)
				stopped := make(chan error, 1)
				go func() {
					stopped <- p.terminateWithTimer(func(d time.Duration) <-chan time.Time {
						requested <- d
						return time.After(d)
					})
				}()
				if d := <-requested; d != 3*time.Second {
					t.Fatalf("stop request allowance=%s", d)
				}
				unblock()
				if err := <-stopped; err != nil {
					t.Fatalf("stop raced nonleader remap: %v", err)
				}
			}
			select {
			case err := <-p.done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("causal statuses stranded the owner")
			}
			if !terminals[p.pid] || !terminals[observed.child] {
				t.Fatalf("terminal publication preceded exact waits: %+v", terminals)
			}
			awaitProcessGone(t, p.pid)
			awaitProcessGone(t, observed.child)
		})
	}
}

func TestOwnedRuntimeProcEvidenceScope(t *testing.T) {
	program := cursorfix.TraceLifecycle(t)
	for _, fault := range []string{"unrelated status", "proc root", "owned tasks"} {
		t.Run(fault, func(t *testing.T) {
			unrelated := exec.Command("/bin/sh", "-c", "read value; exit 23")
			input, err := unrelated.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { input.Close(); unrelated.Wait() }()
			var root, injected atomic.Int64
			hooks := &processTraceHooks{
				procStatus: func(tid int) ([]byte, error) {
					if fault == "unrelated status" && tid == unrelated.Process.Pid {
						injected.Add(1)
						return nil, os.ErrPermission
					}
					return os.ReadFile(fmt.Sprintf("/proc/%d/status", tid))
				},
				readDir: func(path string) ([]os.DirEntry, error) {
					match := fault == "proc root" && path == "/proc" || fault == "owned tasks" && path == fmt.Sprintf("/proc/%d/task", root.Load())
					if match && injected.CompareAndSwap(0, 1) {
						return nil, os.ErrPermission
					}
					return os.ReadDir(path)
				},
			}
			p, err := startProcessWithHooks(program, []string{"burst"}, t.TempDir(), os.Environ(), hooks)
			if err != nil {
				t.Fatal(err)
			}
			defer p.input.Close()
			defer p.output.Close()
			defer p.terminate()
			root.Store(int64(p.pid))
			lines := ownedProcessLines(p)
			if _, err := p.input.WriteString("b"); err != nil {
				t.Fatal(err)
			}
			if fault == "unrelated status" {
				if got := ownedProcessLine(t, lines); got != "waited 16 exit=17" {
					t.Fatalf("unrelated unreadable entry killed runtime: %q", got)
				}
				if err := p.terminate(); err != nil {
					t.Fatal(err)
				}
			} else {
				select {
				case failure := <-p.done:
					if failure == nil || failure.Error() != "Cursor process evidence lost" {
						t.Fatalf("required evidence failure=%v", failure)
					}
					if got := p.terminate(); got != failure {
						t.Fatalf("consumed terminal result lost failure: %v want %v", got, failure)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("required evidence failure did not retire owner")
				}
			}
			if injected.Load() == 0 {
				t.Fatal("fault boundary was not reached")
			}
			awaitProcessGone(t, p.pid)
			if _, err := fmt.Fprintln(input, "release"); err != nil {
				t.Fatal(err)
			}
			if err := unrelated.Wait(); err == nil || unrelated.ProcessState == nil || unrelated.ProcessState.ExitCode() != 23 {
				t.Fatalf("unrelated waitability changed: %v %v", err, unrelated.ProcessState)
			}
		})
	}
}

func TestOwnedIdentitySnapshotCannotAuthorizeReplacement(t *testing.T) {
	// b.x9o review-04: separate proc reads and group-zero fallbacks must not
	// grant signal authority to a reused number owned by another task.
	for _, boundary := range []string{"admission", "missing group admission", "stop fallback", "stopped fallback"} {
		t.Run(boundary, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			// The snapshot swap scripts numeric reuse; the replacement is a real
			// unrelated child so a wrong group grant also loses its exact exit 23.
			unrelated := exec.Command("/bin/sh", "-c", "read value; exit 23")
			input, err := unrelated.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { input.Close(); unrelated.Wait() }()
			const root = 1 << 30
			candidate := unrelated.Process.Pid
			p := &ownedProcess{pid: root, done: make(chan error, 1), stop: make(chan chan error)}
			o := &processTrace{process: p, tracked: map[int]*ownedTracee{root: {group: root, killed: true}}}
			reads, foreignSignals := 0, 0
			o.hooks = &processTraceHooks{
				procStatus: func(tid int) ([]byte, error) {
					if tid == root {
						return []byte(fmt.Sprintf("TracerPid:\t%d\nTgid:\t%d\n", unix.Gettid(), root)), nil
					}
					if tid != candidate {
						return nil, unix.ESRCH
					}
					reads++
					if reads == 1 {
						switch boundary {
						case "admission":
							return []byte(fmt.Sprintf("TracerPid:\t%d\nTgid:\t%d\n", unix.Gettid(), root)), nil
						case "missing group admission":
							return []byte(fmt.Sprintf("TracerPid:\t%d\n", unix.Gettid())), nil
						}
					}
					return []byte(fmt.Sprintf("TracerPid:\t0\nTgid:\t%d\n", candidate)), nil
				},
				readDir: func(string) ([]os.DirEntry, error) { return nil, nil },
				wait: func(tid int, _ *unix.WaitStatus, _ int) (int, error) {
					if tid == candidate {
						return -1, unix.ECHILD
					}
					return 0, nil
				},
				observe: func(point string, tid int, _ unix.WaitStatus) {
					if point == "signal" && tid == candidate && o.tracked[tid].group == candidate {
						foreignSignals++
					}
				},
			}
			switch boundary {
			case "admission":
				old := &ownedTracee{group: root}
				o.tracked[candidate] = old
				o.admitTracee(candidate, true)
				if o.tracked[candidate] != old || old.group != root {
					t.Fatal("separate snapshots combined old ownership with the foreign group")
				}
				o.admitTracee(candidate, true) // a complete foreign snapshot cannot replace it
				if o.tracked[candidate] != old || old.group != root {
					t.Fatal("foreign replacement displaced captured cleanup authority")
				}
			case "missing group admission":
				o.admitTracee(candidate, false)
				if o.tracked[candidate] != nil || o.failure == nil {
					t.Fatal("partial known-owned identity granted ownership or lost uncertainty")
				}
			default:
				o.tracked[candidate] = &ownedTracee{initial: true}
				if boundary == "stopped fallback" {
					o.status(candidate, o.tracked[candidate], unix.WaitStatus(int(unix.SIGSTOP)<<8|0x7f))
				}
			}
			o.stop()
			if current := o.tracked[candidate]; foreignSignals != 0 || current != nil && current.group == candidate {
				t.Fatalf("foreign replacement gained signal authority: %+v signals=%d", current, foreignSignals)
			}
			o.drain()
			if reads == 0 || o.rootGone || len(p.done) != 0 {
				t.Fatal("unavailable identity or ECHILD certified terminal completion")
			}
			if boundary != "admission" && (o.failure == nil || o.failure.Error() != "Cursor process evidence lost") {
				t.Fatalf("unknown identity lost its retained failure: %v", o.failure)
			}
			if _, err := fmt.Fprintln(input, "release"); err != nil {
				t.Fatal(err)
			}
			if err := unrelated.Wait(); err == nil || unrelated.ProcessState == nil || unrelated.ProcessState.ExitCode() != 23 {
				t.Fatalf("foreign replacement was signalled or reaped: %v %v", err, unrelated.ProcessState)
			}
		})
	}
}

func TestOwnedDrainCoalescedAndLateHints(t *testing.T) {
	for _, boundary := range []string{"during drain", "before block", "later hint", "lost wait evidence"} {
		t.Run(boundary, func(t *testing.T) {
			// Unallocated TIDs keep this syscall-fault unit separate from the
			// real Linux cases above. It runs the production owner, not a model.
			const root, child = 1 << 30, 1<<30 + 1
			type result struct {
				status unix.WaitStatus
				err    error
			}
			rootEvents, childEvents := make(chan result, 8), make(chan result, 8)
			stop := func(event int) unix.WaitStatus { return unix.WaitStatus(event<<16 | int(unix.SIGTRAP)<<8 | 0x7f) }
			rootEvents <- result{err: unix.EINTR}
			if boundary == "lost wait evidence" {
				rootEvents <- result{err: unix.ECHILD}
			} else {
				rootEvents <- result{status: stop(unix.PTRACE_EVENT_EXIT)}
			}
			childEvents <- result{status: stop(unix.PTRACE_EVENT_VFORK)}
			childEvents <- result{status: 17 << 8}
			wake := make(chan os.Signal, 1)
			p := &ownedProcess{pid: root, done: make(chan error, 1), stop: make(chan chan error)}
			counts := map[string]int{}
			checkpoints := make(chan [3]int, 4)
			hooks := &processTraceHooks{}
			hooks.observe = func(point string, _ int, _ unix.WaitStatus) {
				counts[point]++
				if point == "block" {
					checkpoints <- [3]int{counts["wait"], counts["collect"], counts["signal"]}
					if boundary == "before block" {
						rootEvents <- result{}
						wake <- syscall.SIGCHLD
					}
				}
			}
			hooks.wait = func(tid int, status *unix.WaitStatus, options int) (int, error) {
				if options&(unix.WALL|unix.WNOTHREAD|unix.WNOHANG) != unix.WALL|unix.WNOTHREAD|unix.WNOHANG {
					t.Errorf("exact nonblocking owner wait flags=%#x", options)
				}
				events := rootEvents
				if tid == child {
					events = childEvents
				} else if tid != root {
					t.Errorf("wait broadened beyond exact captured TIDs: %d", tid)
					return -1, unix.ECHILD
				}
				select {
				case event := <-events:
					*status = event.status
					if tid == child && event.status.Exited() && boundary == "during drain" {
						rootEvents <- result{}
					}
					if event.err != nil {
						return -1, event.err
					}
					return tid, nil
				default:
					return 0, nil
				}
			}
			o := &processTrace{process: p, hooks: hooks, tracked: map[int]*ownedTracee{root: {group: root, killed: true}, child: {group: child, killed: true}}}
			joined := make(chan struct{})
			go func() {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				defer close(joined)
				o.run(wake)
			}()
			defer func() {
				rootEvents <- result{}
				childEvents <- result{}
				close(wake)
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Error("owner did not retire after test cleanup wake")
				}
			}()
			if boundary == "later hint" {
				var first [3]int
				select {
				case first = <-checkpoints:
				case <-time.After(5 * time.Second):
					t.Fatal("exit stop did not drain to idle")
				}
				select {
				case <-p.done:
					t.Fatal("PTRACE_EVENT_EXIT was mistaken for death")
				default:
				}
				wake <- syscall.SIGCHLD // duplicate hint with no status grants no authority
				var second [3]int
				select {
				case second = <-checkpoints:
				case <-time.After(5 * time.Second):
					t.Fatal("duplicate hint did not return to idle")
				}
				if second != [3]int{first[0] + 1, first[1], 0} {
					t.Fatalf("empty hint scanned/signalled or did not quiesce: %v -> %v", first, second)
				}
				rootEvents <- result{} // retained evidence, deliberately no wake yet
				select {
				case <-p.done:
					t.Fatal("unobserved status or hint alone certified death")
				default:
				}
				wake <- syscall.SIGCHLD
				select {
				case wake <- syscall.SIGCHLD:
				default: // notifications may coalesce
				}
			}
			select {
			case <-joined:
			case <-time.After(5 * time.Second):
				t.Fatal("available exact statuses needed a nonexistent additional wake")
			}
			err := <-p.done
			if boundary == "lost wait evidence" {
				if err == nil || err.Error() != "Cursor process evidence lost" || o.rootGone {
					t.Fatalf("lost status became confirmed exit: %v rootGone=%t", err, o.rootGone)
				}
			} else if err != nil || !o.rootGone || counts["status"] != 4 {
				t.Fatalf("drain outcome=%v rootGone=%t counts=%v", err, o.rootGone, counts)
			}
			if _, open := <-p.done; open || p.terminate() != err {
				t.Fatal("terminal publication or retained result changed")
			}
		})
	}
}

func TestOwnedExecMessageCannotReplaceWaitEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		group   int
		message uint
		err     error
	}{
		{"ESRCH", 17, 0, unix.ESRCH},
		{"zero", 17, 0, nil},
		{"leader message collision", 9, 9, nil},
		{"same group member alias", 17, 9, nil},
		{"other group member alias", 17, 31, nil},
		{"two sequential execs", 17, 0, nil},
		{"two losses in one exec", 17, 0, nil},
		{"unexplained error", 17, 0, unix.EIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			const root, former, nextFormer, other = 1 << 30, 27, 28, 31
			p := &ownedProcess{pid: root, done: make(chan error, 1), stop: make(chan chan error)}
			tracked := map[int]*ownedTracee{
				root:     {group: root, killed: true},
				tc.group: {group: tc.group, killed: true},
				former:   {group: tc.group, killed: true},
				other:    {group: other, killed: true},
			}
			if tc.name == "same group member alias" {
				tracked[9] = &ownedTracee{group: tc.group, killed: true}
			}
			if tc.name == "two losses in one exec" {
				tracked[nextFormer] = &ownedTracee{group: tc.group, killed: true}
			}
			waited := map[int]int{}
			terminals := map[int]unix.WaitStatus{}
			terminal := tc.name != "two sequential execs"
			hooks := &processTraceHooks{
				eventMsg: func(int) (uint, error) { return tc.message, tc.err },
				// Synthetic kernel facts: 27 vanished during exec; alias 9 must
				// supply its own terminal status, including if EXEC drains it early.
				procStatus: func(tid int) ([]byte, error) {
					if tid == former || tid == nextFormer || terminals[tid] != 0 {
						return nil, unix.ESRCH
					}
					if task := tracked[tid]; task != nil {
						return []byte(fmt.Sprintf("TracerPid:\t%d\nTgid:\t%d\n", unix.Gettid(), task.group)), nil
					}
					return nil, unix.ESRCH
				},
				readDir: func(string) ([]os.DirEntry, error) { return nil, nil },
				wait: func(tid int, status *unix.WaitStatus, _ int) (int, error) {
					waited[tid]++
					if tid == former || tid == nextFormer || terminals[tid] != 0 {
						return -1, unix.ECHILD
					}
					if terminal && (tid == tc.group || tc.name == "same group member alias" && tid == 9) {
						*status = unix.WaitStatus(unix.SIGKILL)
						terminals[tid] = *status
						return tid, nil
					}
					return 0, nil
				},
			}
			o := &processTrace{process: p, hooks: hooks, tracked: tracked}
			o.status(tc.group, tracked[tc.group], unix.WaitStatus(unix.PTRACE_EVENT_EXEC<<16|int(unix.SIGTRAP)<<8|0x7f))
			if o.rootGone || tracked[other] == nil || tc.name == "same group member alias" && tracked[9] == nil && terminals[9] != unix.WaitStatus(unix.SIGKILL) {
				t.Fatalf("mutable EXEC message discarded required wait evidence: %+v", tracked)
			}
			if !terminal {
				o.drain() // first former reaches ECHILD while the group stays alive
				if o.failure != nil || waited[former] == 0 {
					t.Fatalf("first pending exec lost before second: %v waited=%v", o.failure, waited)
				}
				// A new captured thread execs before the first former is retired.
				// Its transition may explain one more ECHILD, never replace the old one.
				tracked[nextFormer] = &ownedTracee{group: tc.group, killed: true}
				o.status(tc.group, tracked[tc.group], unix.WaitStatus(unix.PTRACE_EVENT_EXEC<<16|int(unix.SIGTRAP)<<8|0x7f))
				terminal = true
			}
			o.drain()
			if tc.err == unix.EIO || tc.name == "two losses in one exec" {
				if o.failure == nil || o.failure.Error() != "Cursor process evidence lost" {
					t.Fatalf("unexplained evidence failure lost: %v", o.failure)
				}
			} else if o.failure != nil {
				t.Fatalf("overtaken EXEC killed healthy root: %v", o.failure)
			}
			if o.rootGone || tracked[root] == nil || tracked[other] == nil || terminals[tc.group] != unix.WaitStatus(unix.SIGKILL) || waited[former] == 0 {
				t.Fatalf("wrong group retired or required waits skipped: waited=%v tracked=%+v", waited, tracked)
			}
			if tc.name == "same group member alias" && terminals[9] != unix.WaitStatus(unix.SIGKILL) {
				t.Fatal("same-group alias lost its independent wait status")
			}
			if tc.name == "two sequential execs" && (waited[nextFormer] == 0 || tracked[former] != nil || tracked[nextFormer] != nil) {
				t.Fatalf("separate exec transitions did not reconcile both former TIDs: waited=%v tracked=%v", waited, tracked)
			}
		})
	}
}

func TestOwnedExecReplacementKeepsTerminalObligations(t *testing.T) {
	for _, boundary := range []string{"before ECHILD", "after ECHILD", "during member drain", "two old losses"} {
		t.Run(boundary, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			// Unallocated IDs make the signal calls harmless; only kernel-facing
			// evidence is scripted. The real owner still admits stop and publishes.
			const root, reused, extra, forker = 1 << 30, 1<<30 + 1, 1<<30 + 2, 1<<30 + 3
			dir := t.TempDir()
			for _, tid := range []int{reused, extra} {
				if err := os.WriteFile(fmt.Sprintf("%s/%d", dir, tid), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			p := &ownedProcess{pid: root, done: make(chan error, 1), stop: make(chan chan error)}
			o := &processTrace{process: p, tracked: map[int]*ownedTracee{root: {group: root, killed: true}, reused: {group: root, killed: true}}}
			if boundary == "two old losses" || boundary == "during member drain" {
				o.tracked[extra] = &ownedTracee{group: root, killed: true}
			}
			replacementTID := reused
			replacement, replacementGone := false, false
			rootEvents, childEvents := make(chan unix.WaitStatus, 1), make(chan unix.WaitStatus, 1)
			wake := make(chan os.Signal, 1)
			signals, blocks, childStops, childTerminals, oldECHILD := 0, 0, 0, 0, 0
			memberTerminals := 0
			o.hooks = &processTraceHooks{
				eventMsg: func(int) (uint, error) { return 0, nil },
				procStatus: func(tid int) ([]byte, error) {
					if tid == root || tid == replacementTID && replacement && !replacementGone {
						return []byte(fmt.Sprintf("TracerPid:\t%d\nTgid:\t%d\n", unix.Gettid(), tid)), nil
					}
					return nil, unix.ESRCH
				},
				readDir: func(path string) ([]os.DirEntry, error) {
					if path == "/proc" && replacement && !replacementGone {
						return entries, nil
					}
					return nil, nil
				},
				wait: func(tid int, status *unix.WaitStatus, _ int) (int, error) {
					if boundary == "during member drain" && !replacement && (tid == reused || tid == extra) {
						// Whichever old member is drained first exits exactly. Its
						// causal scan discovers the other's replacement, so the
						// displaced obligation must already be bound in either order.
						replacementTID = extra
						if tid == extra {
							replacementTID = reused
						}
						replacement = true
						memberTerminals++
						*status = unix.WaitStatus(unix.SIGKILL)
						return tid, nil
					}
					if (tid == extra || tid == reused) && (!replacement || tid != replacementTID) {
						if tid == reused {
							oldECHILD++
						}
						return -1, unix.ECHILD
					}
					events := rootEvents
					if tid == replacementTID {
						events = childEvents
					}
					select {
					case *status = <-events:
						if tid == replacementTID && status.Signaled() {
							replacementGone = true
						}
						return tid, nil
					default:
						return 0, nil
					}
				},
				observe: func(point string, tid int, status unix.WaitStatus) {
					if tid == replacementTID && point == "signal" {
						signals++
						if signals == 1 {
							rootEvents <- unix.WaitStatus(unix.SIGKILL)
							childEvents <- unix.WaitStatus(int(unix.SIGSTOP)<<8 | 0x7f)
						}
					}
					if tid == replacementTID && point == "status" {
						if status.Stopped() {
							childStops++
						} else if status.Signaled() {
							childTerminals++
						}
					}
					if point == "block" && o.stopping {
						blocks++
						current := o.tracked[replacementTID]
						if !o.rootGone || current == nil || current.group != replacementTID || current.initial || signals != 1 || childStops != 1 || childTerminals != 0 || len(p.done) != 0 {
							t.Errorf("replacement lost before its terminal wait: rootGone=%t child=%+v signals=%d stops=%d terminals=%d", o.rootGone, current, signals, childStops, childTerminals)
						}
						childEvents <- unix.WaitStatus(unix.SIGKILL)
						wake <- syscall.SIGCHLD
					}
				},
			}
			if boundary == "before ECHILD" {
				// Another captured group can fork while the root's EXEC awaits
				// handling. Discovery must replace the old numeric claim before
				// any old ECHILD, preserving evidence for the pending EXEC.
				replacement = true
				o.tracked[forker] = &ownedTracee{group: forker, killed: true}
				o.status(forker, o.tracked[forker], unix.WaitStatus(unix.PTRACE_EVENT_FORK<<16|int(unix.SIGTRAP)<<8|0x7f))
				o.status(forker, o.tracked[forker], 0)
			}
			o.status(root, o.tracked[root], unix.WaitStatus(unix.PTRACE_EVENT_EXEC<<16|int(unix.SIGTRAP)<<8|0x7f))
			if boundary == "before ECHILD" || boundary == "during member drain" {
				if oldECHILD != 0 {
					t.Fatal("before-ECHILD schedule consumed the old wait first")
				}
				if boundary == "during member drain" && memberTerminals != 1 {
					t.Fatal("member terminal status did not trigger replacement discovery")
				}
			} else {
				o.drain()
				if oldECHILD == 0 {
					t.Fatal("after-ECHILD schedule never consumed the old wait")
				}
				replacement = true
				o.status(root, o.tracked[root], unix.WaitStatus(unix.PTRACE_EVENT_FORK<<16|int(unix.SIGTRAP)<<8|0x7f))
			}
			if child := o.tracked[replacementTID]; child == nil || child.group != replacementTID || !child.initial {
				t.Fatalf("causal discovery hid a newly inherited generation: %+v", child)
			}
			reply := make(chan error, 1)
			requestDone, abandon := make(chan struct{}), make(chan struct{})
			defer func() { close(abandon); <-requestDone }()
			go func() {
				defer close(requestDone)
				select {
				case p.stop <- reply:
				case <-abandon:
				}
			}()
			o.run(wake)
			result := <-p.done
			if boundary == "two old losses" {
				if result == nil || result.Error() != "Cursor process evidence lost" {
					t.Fatalf("replacement erased prior transition's extra loss: %v", result)
				}
			} else if result != nil {
				t.Fatalf("explained same-owner reuse failed: %v", result)
			}
			var replyErr error
			select {
			case replyErr = <-reply:
			default:
				t.Fatal("owner completed without replying to the explicit stop")
			}
			if blocks != 1 || childTerminals != 1 || len(o.tracked) != 0 || replyErr != result || p.terminate() != result {
				t.Fatalf("completion skipped exact replacement death or lost result: blocks=%d terminals=%d tracked=%v", blocks, childTerminals, o.tracked)
			}
		})
	}
}

func TestOwnedIdleDoesNotProbeAndStopWakes(t *testing.T) {
	// b.x9o: run the production owner with a controlled wake channel and exact
	// wait results. No real subscription can introduce an unrelated child hint.
	const root = 1 << 30 // unallocated Linux PID; no real process can be signalled
	p := &ownedProcess{pid: root, done: make(chan error, 1), stop: make(chan chan error)}
	wake := make(chan os.Signal)
	terminal := make(chan unix.WaitStatus, 1)
	blocked, joined := make(chan struct{}, 1), make(chan struct{})
	var waits, scans, signals, statuses atomic.Int64
	hooks := &processTraceHooks{
		observe: func(point string, tid int, status unix.WaitStatus) {
			switch point {
			case "wait":
				waits.Add(1)
			case "collect":
				scans.Add(1)
			case "signal":
				signals.Add(1)
			case "status":
				statuses.Add(1)
				if tid != root || status != unix.WaitStatus(unix.SIGKILL) {
					t.Errorf("terminal evidence=%d/%#x, want exact root SIGKILL", tid, status)
				}
			case "block":
				// Never hold the owner in this callback: the interval below must
				// allow the actual select (including a regressed timer) to run.
				select {
				case blocked <- struct{}{}:
				default:
				}
			}
		},
		wait: func(tid int, status *unix.WaitStatus, options int) (int, error) {
			if tid != root || options != unix.WALL|unix.WNOTHREAD|unix.WNOHANG {
				t.Errorf("idle owner wait target/flags=%d/%#x", tid, options)
			}
			select {
			case *status = <-terminal:
				return tid, nil
			default:
				return 0, nil
			}
		},
	}
	o := &processTrace{process: p, hooks: hooks, tracked: map[int]*ownedTracee{root: {group: root}}}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(joined)
		o.run(wake)
	}()
	defer func() {
		select {
		case terminal <- unix.WaitStatus(unix.SIGKILL):
		default:
		}
		close(wake)
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("owner did not retire after cleanup wake")
		}
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not drain to its idle select")
	}
	counts := func() [4]int64 { return [4]int64{waits.Load(), scans.Load(), signals.Load(), statuses.Load()} }
	before := counts()
	if before != [4]int64{1, 0, 0, 0} {
		t.Fatalf("initial drain did not establish idle: %v", before)
	}
	terminal <- unix.WaitStatus(unix.SIGKILL) // available evidence alone is not a causal wake
	// This timer measures absence of idle activity, not ordering. The checkpoint
	// above establishes the empty drain; stop below is an unbuffered handshake.
	// A finite window detects polling within it, not every possible longer period.
	interval := time.NewTimer(100 * time.Millisecond)
	defer interval.Stop()
	select {
	case <-p.done:
		t.Fatal("idle owner consumed terminal evidence without a causal wake")
	case <-interval.C:
	}
	if after := counts(); after != before {
		t.Fatalf("idle wait/scan/signal/status activity: %v -> %v", before, after)
	}
	// No SIGCHLD has been supplied. Only this explicit request can wake the owner.
	if err := p.terminate(); err != nil {
		t.Fatalf("stop from established idle: %v", err)
	}
	<-joined
	if !o.rootGone || len(o.tracked) != 0 || signals.Load() != 1 || statuses.Load() != 1 {
		t.Fatalf("stop lost exact terminal evidence or signalled twice: rootGone=%t tracked=%v counts=%v", o.rootGone, o.tracked, counts())
	}
	if err, open := <-p.done; !open || err != nil {
		t.Fatalf("terminal publication=%v open=%t", err, open)
	}
	if _, open := <-p.done; open || p.terminate() != nil {
		t.Fatal("idle stop did not retain its single successful result")
	}
}

func TestOwnedStartupFailureReapsChildAndClosesPipes(t *testing.T) {
	// b.x9o: fail after a real exec stop, before protection is installed. Keep
	// cleanup's wait/kill operations real; a cmd.Start failure cannot cover them.
	borrowedIn, borrowedOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer borrowedIn.Close()
	defer borrowedOut.Close()
	retired := make(chan struct{})
	var lifecycle []string
	var pid, calls, terminals int
	var start, inputPipe, outputPipe string
	var initial unix.WaitStatus
	parentFDs := map[string]int{}
	hooks := &processTraceHooks{
		observe: func(point string, tid int, status unix.WaitStatus) {
			switch point {
			case "subscribed", "launch", "unsubscribed":
				lifecycle = append(lifecycle, point)
				if point == "unsubscribed" {
					close(retired)
				}
			case "status":
				if status.Exited() || status.Signaled() {
					terminals++
					if tid != pid || status != unix.WaitStatus(unix.SIGKILL) {
						t.Errorf("failed-start terminal evidence=%d/%#x, want %d/SIGKILL", tid, status, pid)
					}
				}
			}
		},
		wait: func(tid int, status *unix.WaitStatus, options int) (int, error) {
			calls++
			if options != unix.WALL|unix.WNOTHREAD {
				t.Errorf("startup/cleanup wait flags=%#x", options)
			}
			if calls == 2 {
				return -1, unix.EINTR // cleanup must retry, then consume real terminal evidence
			}
			got, err := unix.Wait4(tid, status, options, nil)
			if calls != 1 {
				if tid != pid {
					t.Errorf("cleanup waited for %d instead of captured %d", tid, pid)
				}
				return got, err
			}
			pid, initial = tid, *status
			if err != nil || got != tid || !status.Stopped() {
				t.Errorf("initial real wait=%d/%#x/%v", got, *status, err)
			}
			var alive, known bool
			start, alive, known = probe.NewProcChecker().StartTime(pid)
			if !known || !alive || start == "" {
				t.Error("failed start had no independent live identity")
			}
			inputPipe, err = pipeAt(pid, 0)
			if err != nil {
				t.Error(err)
			}
			outputPipe, err = pipeAt(pid, 1)
			if err != nil {
				t.Error(err)
			}
			entries, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Error(err)
			}
			for _, entry := range entries {
				link, err := os.Readlink("/proc/self/fd/" + entry.Name())
				if err == nil && (link == inputPipe || link == outputPipe) {
					parentFDs[link]++
				}
			}
			return -1, unix.EIO
		},
	}
	p, err := startProcessWithHooks(cursorfix.TraceLifecycle(t), []string{"burst"}, t.TempDir(), os.Environ(), hooks)
	if p != nil || err == nil || err.Error() != "Cursor lifetime protection could not be established" {
		t.Errorf("post-launch failure=%v/%v", p, err)
		if p != nil {
			defer p.input.Close()
			defer p.output.Close()
			defer p.terminate()
		}
	}
	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("failed protected startup retained its subscription")
	}
	if got := strings.Join(lifecycle, ","); got != "subscribed,launch,unsubscribed" {
		t.Errorf("failed protected startup subscription lifetime=%s", got)
	}
	if pid <= 0 || !initial.Stopped() || calls < 3 || terminals != 1 {
		t.Fatalf("post-launch cleanup omitted real evidence/retry: pid=%d initial=%#x calls=%d terminals=%d", pid, initial, calls, terminals)
	}
	awaitProcessGone(t, pid)
	if current, alive, known := probe.NewProcChecker().StartTime(pid); !known || (alive && current == start) {
		t.Fatalf("failed launch identity still live or unknown: %q %t %t", current, alive, known)
	}
	if inputPipe == "" || outputPipe == "" || inputPipe == outputPipe || parentFDs[inputPipe] != 1 || parentFDs[outputPipe] != 1 {
		t.Fatalf("did not capture both owned parent pipe endpoints: in=%q out=%q descriptors=%v", inputPipe, outputPipe, parentFDs)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		link, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if err == nil && (link == inputPipe || link == outputPipe) {
			t.Errorf("failed start leaked owned descriptor %s -> %s", entry.Name(), link)
		}
	}
	if _, err := borrowedOut.WriteString("unrelated"); err != nil {
		t.Fatalf("cleanup closed unrelated writer: %v", err)
	}
	buf := make([]byte, len("unrelated"))
	if n, err := borrowedIn.Read(buf); err != nil || n != len(buf) || string(buf) != "unrelated" {
		t.Fatalf("cleanup damaged unrelated reader: %q %v", buf[:n], err)
	}
}

func TestOwnedTerminationDeadlinesAndAbandonedCaller(t *testing.T) {
	program := cursorfix.TraceLifecycle(t)
	for _, phase := range []string{"request", "confirmation"} {
		t.Run(phase, func(t *testing.T) {
			type timer struct {
				duration time.Duration
				fire     chan time.Time
			}
			timers := make(chan timer, 2)
			after := func(d time.Duration) <-chan time.Time {
				fire := make(chan time.Time, 1)
				timers <- timer{d, fire}
				return fire
			}
			nextTimer := func(want time.Duration) timer {
				t.Helper()
				select {
				case got := <-timers:
					if got.duration != want {
						t.Fatalf("termination allowance=%s want %s", got.duration, want)
					}
					return got
				case <-time.After(5 * time.Second):
					t.Fatal("termination did not reach timer boundary")
					return timer{}
				}
			}
			p := &ownedProcess{done: make(chan error, 1), stop: make(chan chan error)}
			release, retired := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var signals atomic.Int64
			var lifecycle []string
			if phase == "confirmation" {
				hooks := &processTraceHooks{observe: func(point string, _ int, _ unix.WaitStatus) {
					switch point {
					case "subscribed", "launch":
						lifecycle = append(lifecycle, point)
					case "signal":
						signals.Add(1)
						<-release
					case "unsubscribed":
						lifecycle = append(lifecycle, point)
						close(retired)
					}
				}}
				var err error
				p, err = startProcessWithHooks(program, []string{"burst"}, t.TempDir(), os.Environ(), hooks)
				if err != nil {
					t.Fatal(err)
				}
				defer p.input.Close()
				defer p.output.Close()
				defer p.terminate()
				defer unblock()
			}
			result := make(chan error, 1)
			go func() { result <- p.terminateWithTimer(after) }()
			target := nextTimer(3 * time.Second)
			want := "Cursor termination could not be requested"
			if phase == "confirmation" {
				target = nextTimer(5 * time.Second)
				want = "Cursor termination is not confirmed"
			}
			target.fire <- time.Time{}
			if err := <-result; err == nil || err.Error() != want {
				t.Fatalf("timeout changed meaning: %v want %q", err, want)
			}
			select {
			case <-p.done:
				t.Fatal("timeout invented terminal evidence")
			default:
			}
			if phase == "request" {
				return
			}
			unblock()
			select {
			case <-retired: // owner replied to the abandoned caller and released its subscription
			case <-time.After(5 * time.Second):
				t.Fatal("abandoned caller blocked terminal publication or owner disposal")
			}
			if got := strings.Join(lifecycle, ","); got != "subscribed,launch,unsubscribed" || signals.Load() != 1 {
				t.Fatalf("subscription/one-shot idle stop: %s signals=%d", got, signals.Load())
			}
			if err, open := <-p.done; !open || err != nil {
				t.Fatalf("real terminal publication: %v open=%t", err, open)
			}
			awaitProcessGone(t, p.pid)
			for i := 0; i < 2; i++ {
				go func() { result <- p.terminate() }()
			}
			for i := 0; i < 2; i++ {
				if err := <-result; err != nil {
					t.Fatalf("later termination changed result: %v", err)
				}
			}
			if _, open := <-p.done; open {
				t.Fatal("more than one terminal result")
			}
		})
	}
	retired := make(chan struct{})
	var lifecycle []string
	hooks := &processTraceHooks{observe: func(point string, _ int, _ unix.WaitStatus) {
		if point == "subscribed" || point == "launch" || point == "unsubscribed" {
			lifecycle = append(lifecycle, point)
		}
		if point == "unsubscribed" {
			close(retired)
		}
	}}
	if p, err := startProcessWithHooks(t.TempDir()+"/absent", nil, t.TempDir(), os.Environ(), hooks); p != nil || err == nil {
		t.Fatalf("failed startup result=%v %v", p, err)
	}
	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("failed startup retained its signal subscription")
	}
	if got := strings.Join(lifecycle, ","); got != "subscribed,launch,unsubscribed" {
		t.Fatalf("failed startup subscription lifetime=%s", got)
	}
}

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
			p, err := startProcess(program, []string{"acp"}, t.TempDir(), os.Environ())
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
			p, err := startProcess(path, nil, t.TempDir(), os.Environ())
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
	p, err := startProcess(path, []string{"acp"}, os.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(p.pid)
	for {
		time.Sleep(time.Hour)
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
	p, err := startProcess(path, nil, t.TempDir(), os.Environ())
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
	p, err := startProcess(r.Program, []string{"acp"}, t.TempDir(), os.Environ())
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
	p, err := startProcess(r.Program, []string{"acp"}, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer p.terminate()
	defer p.input.Close()
	defer p.output.Close()
	workerPID := ownedWorkerPID(t, r)
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

func ownedWorkerPID(t *testing.T, r cursorfix.Runtime) int {
	t.Helper()
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
	start, alive, known := probe.NewProcChecker().StartTime(workerPID)
	if !known || !alive {
		t.Fatal("worker identity unavailable")
	}
	t.Cleanup(func() {
		if current, alive, known := probe.NewProcChecker().StartTime(workerPID); known && alive && current == start {
			syscall.Kill(workerPID, syscall.SIGKILL)
		}
	})
	return workerPID
}

func TestOwnedRuntimeWorkerExemptionRequiresExactRoleAndNeitherPipe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options cursorfix.WorkerOptions
	}{
		{"input retained", cursorfix.WorkerOptions{Stdio: []string{"inherit", "ignore", "ignore"}}},
		{"output retained", cursorfix.WorkerOptions{Stdio: []string{"ignore", "inherit", "ignore"}}},
		{"other role", cursorfix.WorkerOptions{Args: []string{"synthetic-other-role"}}},
		{"ambiguous arguments", cursorfix.WorkerOptions{Args: []string{"worker-server", "extra"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cursorfix.NewWithWorker(t, true, tc.options)
			p, err := startProcess(r.Program, []string{"acp"}, t.TempDir(), os.Environ())
			if err != nil {
				t.Fatal(err)
			}
			defer p.input.Close()
			defer p.output.Close()
			defer p.terminate()
			worker := ownedWorkerPID(t, r)
			if err := p.terminate(); err != nil {
				t.Fatal(err)
			}
			awaitProcessGone(t, p.pid)
			awaitProcessGone(t, worker)
		})
	}
}

func TestCursorExecStartCauseAndLiteralRepair(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"missing executable", "executable or working directory unavailable; check cursor_path and project cwd"},
		{"missing cwd", "executable or working directory unavailable; check cursor_path and project cwd"},
		{"permission", "start denied by the operating system; check executable and project directory permissions"},
		{"unknown", "could not start; cause unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cursorfix.New(t)
			program, cwd := r.Program, t.TempDir()
			switch tc.name {
			case "missing executable":
				program += "-absent"
			case "missing cwd":
				cwd += "/absent"
			case "permission":
				if err := os.Chmod(program, 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.WriteFile(program, []byte("SYNTHETIC-SECRET\nlogin required\x1b"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			p, err := startProcess(program, nil, cwd, os.Environ())
			if p != nil || err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "SYNTHETIC-SECRET") || strings.Contains(err.Error(), program) || strings.ContainsAny(err.Error(), "\n\r\x1b") {
				t.Fatalf("start=%v err=%v", p, err)
			}
			if tc.name == "unknown" {
				return
			}
			// Follow the actual path/cwd/permission advice against the synthetic runtime.
			program = r.Program
			cwd = t.TempDir()
			if err := os.Chmod(program, 0700); err != nil {
				t.Fatal(err)
			}
			p, err = startProcess(program, []string{"acp"}, cwd, os.Environ())
			if err != nil {
				t.Fatal(err)
			}
			defer p.terminate()
			defer p.input.Close()
			defer p.output.Close()
			rpc := newProtocol(p.input, p.output)
			defer rpc.close()
			if _, err = rpc.request("initialize", map[string]any{"protocolVersion": 1}); err != nil {
				t.Fatal(err)
			}
			f := nextFrame(t, rpc)
			if f.err != nil {
				t.Fatal(f.err)
			}
			raw, err := rpc.response(f.frame)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = initializeCapabilities(raw); err != nil {
				t.Fatalf("literal repair failed: %v", err)
			}
		})
	}
}
