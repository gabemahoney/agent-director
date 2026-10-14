//go:build !linux

package cursorhost

import (
	"fmt"
	"github.com/gabemahoney/agent-director/internal/store"
)

func VerifyRuntime(store.CursorRuntime, bool) error {
	return fmt.Errorf("Cursor protected runtime is unsupported on this platform")
}

func processPipe(int, int) (string, error) { return "", fmt.Errorf("unsupported platform") }
