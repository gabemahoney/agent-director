package tmux

import "golang.org/x/sys/unix"

// termiosRead is the ioctl that reads a terminal's mode.
const termiosRead = unix.TCGETS
