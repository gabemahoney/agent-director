//go:build linux

package cursorhost

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
	master, slave := terminalPair(t)
	events := make(chan terminalInput, 8)
	var idle atomic.Bool
	idle.Store(true)
	restore := terminalInputLoop(slave, io.Discard, events, func() bool { return idle.CompareAndSwap(true, false) })
	defer restore()
	if _, err := master.Write([]byte("first\rsecond\r/exit\r")); err != nil {
		t.Fatal(err)
	}
	first, second, exit := terminalEvent(t, events), terminalEvent(t, events), terminalEvent(t, events)
	if !first.accepted || second.accepted || exit.accepted || exit.text != "/exit" {
		t.Fatalf("idle reservation or local exit failed: %+v %+v %+v", first, second, exit)
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
