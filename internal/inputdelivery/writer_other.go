//go:build !linux && !darwin

package inputdelivery

import (
	"fmt"
	"os"
)

func lockWriter(*os.File) error {
	return fmt.Errorf("exclusive input writer unsupported on this platform")
}
