//go:build !darwin

package probe

import (
	"errors"
	"testing"
)

func TestUIDProcessReaderUnsupported(t *testing.T) {
	t.Parallel()
	for _, uid := range []int{-1, 0, 501} {
		pids, err := NewUIDProcessReader().PIDs(uid)
		if pids != nil || !errors.Is(err, errUIDSelectionUnsupported) {
			t.Errorf("PIDs(%d) = %v, %v", uid, pids, err)
		}
	}
}
