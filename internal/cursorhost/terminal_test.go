//go:build linux

package cursorhost

import (
	"bytes"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// b.x9o: observe the original decoder in an actual kernel wait, including
// refills after it consumed an incomplete UTF-8 rune or Escape sequence.
func TestTerminalCleanupWakesBlockedDecoder(t *testing.T) {
	for _, prefix := range []string{"", "\xe2\x82", "\x1b["} {
		t.Run(fmt.Sprintf("prefix=%q", prefix), func(t *testing.T) {
			master, slave := terminalPair(t)
			old, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			flags, err := unix.FcntlInt(slave.Fd(), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			pane := &observedWire{}
			polls := make(chan terminalPollCall, 4)
			reads := make(chan string, 4)
			var wake [2]uintptr
			var events, admissions, choices atomic.Int32
			restore := terminalLoopWithReader(slave, pane, func(terminalInput, <-chan struct{}) { events.Add(1) }, func() bool { admissions.Add(1); return true }, func(r *terminalReader) {
				wake = [2]uintptr{r.wakeRead.Fd(), r.wakeWrite.Fd()}
				poll, read := r.poll, r.read
				r.poll = func(p []unix.PollFd, timeout int) (int, error) {
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					call := terminalPollCall{tid: unix.Gettid(), timeout: timeout, returned: make(chan struct{})}
					polls <- call
					defer close(call.returned)
					// Hold the finite Escape wait at real poll until stop; its
					// requested 80ms is asserted separately from injected expiry.
					for {
						n, err := poll(p, -1)
						if err != unix.EINTR {
							return n, err
						}
					}
				}
				r.read = func(fd int, p []byte) (int, error) {
					n, err := read(fd, p)
					if n > 0 {
						reads <- string(p[:n])
					}
					return n, err
				}
			}, func(rune) (string, string, bool) { choices.Add(1); return "", "", false })
			defer restore()
			call := terminalPoll(t, polls)
			if call.timeout != -1 {
				t.Fatalf("idle read polled on a timer: %d", call.timeout)
			}
			awaitTerminalKernelPoll(t, call)
			if prefix != "" {
				if _, err := master.Write([]byte(prefix)); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-reads:
					if got != prefix {
						t.Fatalf("actual partial read %q; want %q", got, prefix)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("partial bytes never reached original decoder")
				}
				call = terminalPoll(t, polls)
				want := -1
				if prefix[0] == '\x1b' {
					want = 80
				}
				if call.timeout != want {
					t.Fatalf("partial wait=%d want=%d", call.timeout, want)
				}
				awaitTerminalKernelPoll(t, call)
			}
			_, returned := startTerminalCleanup(restore, call.returned)
			awaitTerminalCleanup(t, returned)
			for _, fd := range wake {
				if _, err := unix.FcntlInt(fd, unix.F_GETFD, 0); err != unix.EBADF {
					t.Fatalf("owned wake fd %d remains open: %v", fd, err)
				}
			}
			got, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *got != *old {
				t.Fatalf("termios not restored: got=%+v want=%+v err=%v", got, old, err)
			}
			gotFlags, err := unix.FcntlInt(slave.Fd(), unix.F_GETFL, 0)
			if err != nil || gotFlags != flags {
				t.Fatalf("borrowed fd flags changed: got=%#x want=%#x err=%v", gotFlags, flags, err)
			}
			terminalReadByNextOwner(t, master, slave)
			if events.Load() != 0 || admissions.Load() != 0 || choices.Load() != 0 || string(pane.bytes()) != "\x1b[?2004h\x1b[?2004l" {
				t.Fatalf("retired partial input escaped: events=%d admissions=%d choices=%d output=%q", events.Load(), admissions.Load(), choices.Load(), pane.bytes())
			}
		})
	}
}

type terminalPollCall struct {
	tid, timeout int
	returned     chan struct{}
}

func terminalPoll(t *testing.T, polls <-chan terminalPollCall) terminalPollCall {
	t.Helper()
	select {
	case call := <-polls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("original decoder did not enter poll")
		return terminalPollCall{}
	}
}

// The notification before poll is insufficient: wchan must show this locked
// OS thread actually sleeping in Linux poll, with its return still unobserved.
func awaitTerminalKernelPoll(t *testing.T, call terminalPollCall) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		select {
		case <-call.returned:
			t.Fatal("poll returned before a kernel wait was observed")
		default:
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/wchan", call.tid))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "poll") {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("thread %d never blocked in poll: wchan=%q", call.tid, raw)
		}
		runtime.Gosched()
	}
}

// b.x9o: callbacks that entered before stop must finish before restoration;
// no buffered successor, post-stop result, or second redraw write may escape.
func TestTerminalCleanupJoinsCallbacks(t *testing.T) {
	for _, kind := range []string{"admission", "choice", "redraw"} {
		t.Run(kind, func(t *testing.T) {
			master, slave := terminalPair(t)
			entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			block := func() { close(entered); <-release; close(completed) }
			var stop <-chan struct{}
			var admissions, choices, events atomic.Int32
			pane := &observedWire{}
			keys, want := "a\rbuffered\r", "\x1b[?2004h\r\x1b[2K> a\r\n\x1b[?2004l"
			admit := func() bool { admissions.Add(1); block(); return true }
			var choose []func(rune) (string, string, bool)
			if kind == "choice" {
				keys, want = "xbuffered\r", "\x1b[?2004h\x1b[?2004l"
				choose = append(choose, func(rune) (string, string, bool) { choices.Add(1); block(); return "request", "option", true })
			}
			if kind == "redraw" {
				keys = "ab\x01buffered\r"
				want = "\x1b[?2004h\r\x1b[2K> a\r\x1b[2K> ab\r\x1b[2K> ab\x1b[?2004l"
				matches := 0
				pane.observe = func(raw []byte) {
					if string(raw) == "\r\x1b[2K> ab" {
						matches++
						if matches == 2 {
							block()
						}
					}
				}
			}
			restore := terminalLoopWithReader(slave, pane, func(terminalInput, <-chan struct{}) { events.Add(1) }, admit, func(r *terminalReader) { stop = r.stop }, choose...)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				restore()
			})
			if _, err := master.Write([]byte(keys)); err != nil {
				t.Fatal(err)
			}
			awaitTerminalSignal(t, entered, "entered "+kind)
			caller, returned := startTerminalCleanup(restore, completed)
			awaitTerminalSignal(t, stop, "cleanup requested stop")
			awaitTerminalCleanupJoin(t, caller, returned)
			close(release)
			awaitTerminalCleanup(t, returned)
			terminalReadByNextOwner(t, master, slave)
			wantAdmissions, wantChoices := int32(0), int32(0)
			if kind == "admission" {
				wantAdmissions = 1
			}
			if kind == "choice" {
				wantChoices = 1
			}
			if admissions.Load() != wantAdmissions || choices.Load() != wantChoices || events.Load() != 0 || string(pane.bytes()) != want {
				t.Fatalf("late callback/output: admissions=%d choices=%d events=%d output=%q want=%q", admissions.Load(), choices.Load(), events.Load(), pane.bytes(), want)
			}
		})
	}
}

func TestTerminalEscapeContinuationWaits(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split=%v", split), func(t *testing.T) {
			master, slave := terminalPair(t)
			waits, advance := make(chan int), make(chan bool)
			events := make(chan terminalInput, 1)
			restore := terminalLoopWithReader(slave, io.Discard, func(v terminalInput, stop <-chan struct{}) {
				select {
				case events <- v:
				case <-stop:
				}
			}, func() bool { return true }, func(r *terminalReader) {
				poll := r.poll
				r.poll = func(p []unix.PollFd, timeout int) (int, error) {
					if timeout < 0 {
						return poll(p, timeout)
					}
					select {
					case waits <- timeout:
					case <-r.stop:
						return 0, os.ErrClosed
					}
					select {
					case ready := <-advance:
						if !ready {
							return 0, nil
						}
						return poll(p, -1)
					case <-r.stop:
						return 0, os.ErrClosed
					}
				}
			})
			defer restore()
			if _, err := master.Write([]byte("a🐝c\x1b")); err != nil {
				t.Fatal(err)
			}
			chunks := []string{""}
			if split {
				chunks = []string{"[", "D"}
			}
			for _, chunk := range chunks {
				select {
				case timeout := <-waits:
					if timeout != 80 {
						t.Fatalf("each empty Escape continuation gets 80ms, got %d", timeout)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("Escape continuation did not wait")
				}
				if chunk != "" {
					if _, err := master.Write([]byte(chunk)); err != nil {
						t.Fatal(err)
					}
				}
				advance <- chunk != ""
			}
			if split {
				if _, err := master.Write([]byte("\x7fb\r")); err != nil {
					t.Fatal(err)
				}
			}
			got := terminalEvent(t, events)
			if got.err != nil || (!split && (!got.interrupt || got.text != "")) || (split && (got.text != "abc" || got.interrupt || !got.accepted)) {
				t.Fatalf("Escape decoding changed: %+v", got)
			}
		})
	}
}

func TestTerminalReadFailureCleanup(t *testing.T) {
	for _, fault := range []error{io.EOF, unix.EIO} {
		t.Run(fault.Error(), func(t *testing.T) {
			master, slave := terminalPair(t)
			events := make(chan terminalInput, 1)
			pane := &observedWire{}
			var wake [2]uintptr
			restore := terminalLoopWithReader(slave, pane, func(v terminalInput, _ <-chan struct{}) { events <- v }, func() bool { t.Error("read failure admitted input"); return false }, func(r *terminalReader) {
				wake = [2]uintptr{r.wakeRead.Fd(), r.wakeWrite.Fd()}
				read := r.read
				r.read = func(fd int, p []byte) (int, error) {
					n, err := read(fd, p)
					if err != nil || n == 0 {
						return n, err
					}
					if fault == io.EOF {
						return 0, nil
					}
					return 0, fault
				}
			})
			t.Cleanup(restore)
			if _, err := master.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if got := terminalEvent(t, events); !errors.Is(got.err, fault) {
				t.Fatalf("read error=%v want=%v", got.err, fault)
			}
			restore()
			for _, fd := range wake {
				if _, err := unix.FcntlInt(fd, unix.F_GETFD, 0); err != unix.EBADF {
					t.Fatalf("wake fd %d leaked: %v", fd, err)
				}
			}
			terminalReadByNextOwner(t, master, slave)
			if string(pane.bytes()) != "\x1b[?2004h\x1b[?2004l" {
				t.Fatalf("read failure output=%q", pane.bytes())
			}
		})
	}
}

// b.x9o: returning a borrowed terminal must be safe to repeat. In particular,
// a second caller must not write restoration bytes into the next owner's output.
func TestTerminalCleanupRestoresOnce(t *testing.T) {
	master, slave := terminalPair(t)
	old, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(slave.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	pane := &observedWire{}
	events := make(chan terminalInput, 1)
	restore := terminalInputLoop(slave, pane, events, func() bool { return true })
	t.Cleanup(restore)
	if _, err := master.Write([]byte("before\r")); err != nil {
		t.Fatal(err)
	}
	if got := terminalEvent(t, events); got.text != "before" || !got.accepted || got.err != nil {
		t.Fatalf("initial input: %+v", got)
	}
	restore()
	after := pane.bytes()
	restore()
	if !bytes.Equal(pane.bytes(), after) || bytes.Count(after, []byte("\x1b[?2004l")) != 1 {
		t.Fatal("repeated cleanup wrote terminal restoration again")
	}
	got, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *got != *old {
		t.Fatalf("borrowed terminal settings not restored: got=%+v want=%+v err=%v", got, old, err)
	}
	gotFlags, err := unix.FcntlInt(slave.Fd(), unix.F_GETFL, 0)
	if err != nil || gotFlags != flags {
		t.Fatalf("borrowed terminal flags changed: got=%#x want=%#x err=%v", gotFlags, flags, err)
	}
}

// b.x9o: cleanup must join an entered delivery and discard its buffered successor.
func TestTerminalCleanupJoinsDelivery(t *testing.T) {
	master, slave := terminalPair(t)
	pane := &observedWire{}
	entered, stopping, delivered := make(chan terminalInput, 1), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var admissions, calls atomic.Int32
	restore := terminalLoopWithStop(slave, pane, func(v terminalInput, stop <-chan struct{}) {
		calls.Add(1)
		entered <- v
		<-stop
		close(stopping)
		<-release
		close(delivered)
	}, func() bool { admissions.Add(1); return true })
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		restore()
	})
	if _, err := master.Write([]byte("first\rbuffered-successor\r")); err != nil {
		t.Fatal(err)
	}
	if got := terminalEvent(t, entered); got.text != "first" || !got.accepted || got.err != nil {
		t.Fatalf("delivery boundary: %+v", got)
	}
	caller, returned := startTerminalCleanup(restore, delivered)
	awaitTerminalSignal(t, stopping, "delivery observed stop")
	awaitTerminalCleanupJoin(t, caller, returned)
	if bytes.Contains(pane.bytes(), []byte("\x1b[?2004l")) {
		t.Fatal("terminal restored before entered delivery completed")
	}
	returns := []<-chan bool{returned}
	for n := 0; n < 3; n++ {
		_, returned := startTerminalCleanup(restore, delivered)
		returns = append(returns, returned)
	}
	close(release)
	for _, returned := range returns {
		awaitTerminalCleanup(t, returned)
	}
	before := pane.bytes()
	if bytes.Count(before, []byte("\x1b[?2004l")) != 1 || admissions.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("buffered successor or repeated restoration: admissions=%d calls=%d output=%q", admissions.Load(), calls.Load(), before)
	}
	terminalReadByNextOwner(t, master, slave)
	if !bytes.Equal(pane.bytes(), before) || admissions.Load() != 1 || calls.Load() != 1 {
		t.Fatal("retired reader consumed later input or produced callbacks/output")
	}
}

func awaitTerminalSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal(label + ": timed out")
	}
}

// Record completion in the cleanup caller at return, without a later grace wait.
func startTerminalCleanup(restore func(), completed <-chan struct{}) (string, <-chan bool) {
	caller, returned := make(chan string), make(chan bool, 1)
	go func() {
		stack := make([]byte, 256)
		n := runtime.Stack(stack, false)
		caller <- strings.Join(strings.Fields(string(stack[:n]))[:2], " ") + " "
		restore()
		select {
		case <-completed:
			returned <- true
		default:
			returned <- false
		}
	}()
	return <-caller, returned
}

// Observe this caller receiving inside cleanup itself, excluding other readers,
// sync.Once contenders and output locks. The decoder remains held throughout.
func awaitTerminalCleanupJoin(t *testing.T, caller string, returned <-chan bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	stack := make([]byte, 1<<20)
	for {
		select {
		case <-returned:
			t.Fatal("cleanup returned before its reader join was observed")
		default:
		}
		n := runtime.Stack(stack, true)
		for _, g := range strings.Split(string(stack[:n]), "\n\n") {
			lines := strings.Split(g, "\n")
			if len(lines) >= 3 && lines[0] == caller+"[chan receive]:" &&
				strings.Contains(lines[1], ".terminalLoopWithReader.func1.1(") &&
				strings.Contains(lines[2], "/terminal_linux.go:") {
				return
			}
		}
		if time.Now().After(until) {
			t.Fatalf("%snever blocked in the reader join:\n%s", caller, stack[:n])
		}
		runtime.Gosched()
	}
}

func awaitTerminalCleanup(t *testing.T, returned <-chan bool) {
	t.Helper()
	select {
	case completed := <-returned:
		if !completed {
			t.Fatal("cleanup returned before held operation completed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal cleanup timed out")
	}
}

// terminalReadByNextOwner is reusable by terminal and Run cleanup tests. Call
// after cleanup with both PTY handles still owned and held open by the caller.
// The legitimate owner reads distinct later bytes; observe output separately.
func terminalReadByNextOwner(t *testing.T, master, slave *os.File) {
	t.Helper()
	const later = "next-owner-input\n"
	if _, err := master.Write([]byte(later)); err != nil {
		t.Fatal(err)
	}
	poll := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 1000); err != nil || n != 1 || poll[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("borrowed fd lost later input: poll=%+v n=%d err=%v", poll, n, err)
	}
	buf := make([]byte, 128)
	n, err := unix.Read(int(slave.Fd()), buf)
	if err != nil {
		t.Fatalf("borrowed fd read failed: %v", err)
	}
	if string(buf[:n]) != later {
		t.Fatalf("later input consumed or borrowed fd unusable: got=%q err=%v", buf[:n], err)
	}
}

// b.x9o: the real channel adapter must release an unread event, including a
// setup error. Observing its blocked select prevents a stop-before-start pass.
func TestTerminalChannelDeliveryCleanup(t *testing.T) {
	for _, keys := range []string{"prompt\r", string([]byte{0xff}), "not-a-terminal"} {
		t.Run(fmt.Sprintf("%q", keys), func(t *testing.T) {
			master, in := terminalPair(t)
			if keys == "not-a-terminal" {
				var err error
				in, err = os.CreateTemp(t.TempDir(), "borrowed")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { in.Close() })
			}
			pane := &observedWire{}
			events := make(chan terminalInput)
			restore := terminalInputLoop(in, pane, events, func() bool { return true })
			t.Cleanup(restore)
			if keys != "not-a-terminal" {
				if _, err := master.Write([]byte(keys)); err != nil {
					t.Fatal(err)
				}
			}
			// A runtime stack observes the actual adapter's blocked select, not
			// merely a notification just before it might start delivering.
			until := time.Now().Add(3 * time.Second)
			for {
				stack := make([]byte, 1<<20)
				n := runtime.Stack(stack, true)
				blocked := false
				for _, g := range strings.Split(string(stack[:n]), "\n\n") {
					blocked = blocked || (strings.Contains(g, "[select]:") && strings.Contains(g, "terminalInputLoop"))
				}
				if blocked {
					break
				}
				if time.Now().After(until) {
					t.Fatal("channel adapter never blocked in delivery")
				}
				runtime.Gosched()
			}
			returned := make(chan struct{})
			go func() { restore(); close(returned) }()
			awaitTerminalSignal(t, returned, "blocked channel cleanup")
			before := pane.bytes()
			if keys == "not-a-terminal" {
				if len(before) != 0 {
					t.Fatalf("failed setup changed terminal output: %q", before)
				}
				if _, err := in.WriteString("next-owner"); err != nil {
					t.Fatal("failed setup closed borrowed file: ", err)
				}
			} else {
				terminalReadByNextOwner(t, master, in)
				if bytes.Count(before, []byte("\x1b[?2004l")) != 1 {
					t.Fatalf("missing restoration: %q", before)
				}
			}
			select {
			case e := <-events:
				t.Fatalf("retired adapter delivered %+v", e)
			default:
			}
			if !bytes.Equal(pane.bytes(), before) {
				t.Fatal("retired adapter wrote output")
			}
		})
	}
}

func terminalPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "master")
	t.Cleanup(func() { master.Close() })
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}
func terminalEvent(t *testing.T, events <-chan terminalInput) terminalInput {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(time.Second):
		t.Fatal("terminal submission unavailable")
		return terminalInput{}
	}
}
func TestEditableTerminalSubmission(t *testing.T) {
	for _, tc := range []struct {
		name, keys, text   string
		interrupt, invalid bool
	}{
		{"multiline unicode", "第一\t🦋\nsecond\tcafé\r", "第一\t🦋\nsecond\tcafé", false, false},
		{"bracketed paste", "\x1b[200~line\tone\rline\ttwo\x1b[201~\r", "line\tone\nline\ttwo", false, false},
		{"typed tab", "a\tb\r", "a\tb", false, false},
		{"utf8 cursor edit", "a🐝c\x1b[D\x7fb\r", "abc", false, false},
		{"delete at cursor", "abc\x1b[D\x1b[3~\r", "ab", false, false},
		{"clear before local exit", "stale\ndraft\x15/exit\r", "/exit", false, false},
		{"interrupt", "draft\x03", "", true, false},
		{"lone escape", "\x1b", "", true, false},
		{"eight byte escape bound", "\x1b[1234567z\r", "z", false, false},
		{"invalid utf8", string([]byte{0xff}), "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave := terminalPair(t)
			events := make(chan terminalInput, 8)
			restore := terminalInputLoop(slave, io.Discard, events, func() bool { return true })
			defer restore()
			if _, err := master.Write([]byte(tc.keys)); err != nil {
				t.Fatal(err)
			}
			got := terminalEvent(t, events)
			if tc.invalid {
				if got.err == nil {
					t.Fatal("invalid UTF8 accepted")
				}
				return
			}
			if got.err != nil || got.text != tc.text || got.interrupt != tc.interrupt {
				t.Fatalf("terminal event %+v; want %q interrupt %v", got, tc.text, tc.interrupt)
			}
		})
	}
}
func TestTerminalAdmissionReservesOneIdleTurn(t *testing.T) {
	for _, handoff := range []bool{false, true} {
		t.Run(fmt.Sprintf("waiting_handoff=%v", handoff), func(t *testing.T) {
			master, slave := terminalPair(t)
			events := make(chan terminalInput, 8)
			var admission promptAdmission
			if handoff {
				admission.waiting(func() bool { return true })
			} else {
				admission.confirm()
			}
			restore := terminalInputLoop(slave, io.Discard, events, admission.take)
			defer restore()
			if _, err := master.Write([]byte("first\r")); err != nil {
				t.Fatal(err)
			}
			first := terminalEvent(t, events)
			// Confirm after consumption, before any turn processor can disable
			// admission: a claimed handoff must not rearm the next Enter.
			if handoff {
				admission.confirm()
			}
			if _, err := master.Write([]byte("second\r/exit\r")); err != nil {
				t.Fatal(err)
			}
			second, exit := terminalEvent(t, events), terminalEvent(t, events)
			if !first.accepted || second.accepted || exit.accepted || exit.text != "/exit" {
				t.Fatalf("idle reservation or local exit failed: %+v %+v %+v", first, second, exit)
			}
		})
	}
}
func TestPaneRenderingEscapesControlText(t *testing.T) {
	for _, raw := range []string{"ordinary 多行 🦋\nnext", "\x1b[2Jclear", "\x07bell", "\rrewrite", "\x1b]52;c;secret\x07"} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			got := safeText(raw)
			for _, r := range strings.ReplaceAll(got, "\r\n", "\n") {
				if r < ' ' && r != '\n' && r != '\t' {
					t.Fatalf("unsafe pane control %q", got)
				}
			}
		})
	}
}
