//go:build !linux

package cursorhost

import (
	"io"
	"os"
)

func terminalInputLoop(*os.File, io.Writer, chan<- terminalInput, func() bool, ...func(rune) (string, string, bool)) func() {
	return func() {}
}

const maxInputRunes = 256 * 1024

func terminalLoop(*os.File, io.Writer, func(terminalInput), func() bool, ...func(rune) (string, string, bool)) func() {
	return func() {}
}
