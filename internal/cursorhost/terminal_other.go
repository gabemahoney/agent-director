//go:build !linux

package cursorhost

import (
	"io"
	"os"
)

type terminalInput struct {
	accepted  bool
	text      string
	interrupt bool
	err       error
}

func terminalInputLoop(*os.File, io.Writer, chan<- terminalInput, func() bool) func() {
	return func() {}
}

const maxInputRunes = 256 * 1024
