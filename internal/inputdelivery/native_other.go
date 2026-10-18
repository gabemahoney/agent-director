//go:build !linux

package inputdelivery

import (
	"fmt"
)

const nativeTerminalSupported = false

func (s *nativeSession) Queue(Identity) (int, error) {
	return 0, fmt.Errorf("terminal probing unsupported")
}
func ownedTranscripts(Identity) ([]string, error) {
	return nil, fmt.Errorf("transcript ownership probing unsupported")
}
