//go:build !linux

package cursortranscript

import "fmt"

func Verify(string) error       { return fmt.Errorf("protected Cursor transcript unsupported") }
func VerifyAppend(string) error { return fmt.Errorf("protected Cursor transcript unsupported") }
