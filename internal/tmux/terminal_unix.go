//go:build linux || darwin

package tmux

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// terminalRaw reads the mode of the terminal tty. O_NOCTTY keeps it from
// becoming this process's controlling terminal and O_NONBLOCK keeps the open
// from waiting on the line.
func terminalRaw(tty string) (bool, error) {
	f, err := os.OpenFile(tty, os.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	term, err := unix.IoctlGetTermios(int(f.Fd()), termiosRead)
	if err != nil {
		return false, fmt.Errorf("read the terminal mode of %s: %w", tty, err)
	}
	return term.Lflag&(unix.ICANON|unix.ECHO) == 0 && term.Iflag&unix.ICRNL == 0, nil
}
