//go:build !linux && !darwin

package tmux

import "fmt"

func runtimeLaunchBudget() (uint64, error) {
	return 0, fmt.Errorf("launch exec budget is unsupported on this platform")
}
