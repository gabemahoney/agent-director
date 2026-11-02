package tmux_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/ptyfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"golang.org/x/sys/unix"
)

// b.qcf: raw input requires all three tty flags off, including ICRNL.
func TestPaneTerminalRawModes(t *testing.T) {
	_, slave := ptyfix.Open(t)
	term, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		canonical, echo bool
		crToNL, raw     bool
	}{
		{name: "cooked", canonical: true, echo: true, crToNL: true},
		{name: "raw", raw: true},
		{name: "raw with ICRNL", crToNL: true},
		{name: "raw with ICANON", canonical: true},
		{name: "raw with ECHO", echo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := *term
			mode.Lflag &^= unix.ICANON | unix.ECHO
			mode.Iflag &^= unix.ICRNL
			if tc.canonical {
				mode.Lflag |= unix.ICANON
			}
			if tc.echo {
				mode.Lflag |= unix.ECHO
			}
			if tc.crToNL {
				mode.Iflag |= unix.ICRNL
			}
			if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, &mode); err != nil {
				t.Fatal(err)
			}
			c, runner := newScripted(t, exitZero(slave.Name()+"\n"))
			if raw, err := c.PaneTerminalRaw(testSocket, "%3", testTimeouts.Action+testTimeouts.WaitDelay); err != nil || raw != tc.raw {
				t.Errorf("PaneTerminalRaw = %v, %v; want %v, nil", raw, err, tc.raw)
			}
			runner.Only()
		})
	}
}

// b.qcf: an openable device with no termios must report an error, not ready.
func TestPaneTerminalRawUnreadableMode(t *testing.T) {
	c, _ := newScripted(t, exitZero("/dev/null\n"))
	if raw, err := c.PaneTerminalRaw(testSocket, "%3", testTimeouts.Action+testTimeouts.WaitDelay); raw || !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("PaneTerminalRaw = %v, %v; want false, ENOTTY", raw, err)
	}
}

// b.qcf: an unclean path resolving to a real tty must be refused before open.
func TestPaneTerminalRawRejectsBeforeOpen(t *testing.T) {
	_, slave := ptyfix.Open(t)
	watch, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(watch) })
	if _, err := unix.InotifyAddWatch(watch, slave.Name(), unix.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	unclean := "/dev/pts/../pts/" + filepath.Base(slave.Name())
	c, _ := newScripted(t, exitZero(unclean+"\n"), exitZero(slave.Name()+"\n"))
	_, err = c.PaneTerminalRaw(testSocket, "%3", testTimeouts.Action+testTimeouts.WaitDelay)
	parseWantFailure(t, err, tmux.CallPaneTerminal, tmux.FailUnrecognized)
	var events [4096]byte
	if n, err := unix.Read(watch, events[:]); n > 0 || !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("rejected path opened the device: events=%d err=%v", n, err)
	}
	// The same call with the clean path must reach the device, so this also
	// proves the watch observes opens of this particular tty.
	if _, err := c.PaneTerminalRaw(testSocket, "%3", testTimeouts.Action+testTimeouts.WaitDelay); err != nil {
		t.Fatal(err)
	}
	if n, err := unix.Read(watch, events[:]); n <= 0 || err != nil {
		t.Fatalf("clean path did not open the device: events=%d err=%v", n, err)
	}
}
