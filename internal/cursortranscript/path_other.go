//go:build !linux

package cursortranscript

import (
	"fmt"
	"os"
)

func Verify(string) error       { return fmt.Errorf("protected Cursor transcript unsupported") }
func VerifyAppend(string) error { return fmt.Errorf("protected Cursor transcript unsupported") }
func OpenRead(string) (*os.File, error) {
	return nil, fmt.Errorf("protected Cursor transcript unsupported")
}
