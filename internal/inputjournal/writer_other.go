//go:build !linux && !darwin

package inputjournal

import (
	"fmt"
	"os"
)

// LockWriter nonblockingly acquires the original shared launch lock.
// Runtime and importer must reuse this lock, never replace its inode.
func LockWriter(*os.File) error {
	return fmt.Errorf("exclusive input writer unsupported on this platform")
}

func openEvidence(string) (*os.File, error) {
	return nil, fmt.Errorf("native evidence unsupported on this platform")
}
