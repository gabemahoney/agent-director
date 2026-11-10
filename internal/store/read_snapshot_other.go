//go:build !linux && !darwin

package store

import (
	"context"
	"fmt"
)

func copyReadSnapshot(context.Context, string, string) error {
	return fmt.Errorf("readonly SQLite snapshot locking is unsupported on this platform")
}
