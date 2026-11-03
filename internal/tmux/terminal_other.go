//go:build !linux && !darwin

package tmux

import "errors"

func terminalRaw(string) (bool, error) {
	return false, errors.New("reading a terminal's mode is unsupported on this platform")
}
