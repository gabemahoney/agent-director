//go:build !linux

package cursorhost

import (
	"fmt"
	"os"
)

type ownedProcess struct {
	pid           int
	start         string
	input, output *os.File
	done          chan error
}

func startProcess(string, []string, string, []string) (*ownedProcess, error) {
	return nil, fmt.Errorf("Cursor protected runtime is unsupported on this platform")
}
func (p *ownedProcess) terminate() error {
	return fmt.Errorf("Cursor protected runtime is unsupported on this platform")
}

func (p *ownedProcess) protocolInput() (*os.File, error) {
	return nil, fmt.Errorf("Cursor protected runtime is unsupported on this platform")
}
