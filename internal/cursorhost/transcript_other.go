//go:build !linux

package cursorhost

import (
	"fmt"
	"github.com/gabemahoney/agent-director/internal/store"
	"os"
)

type transcript struct {
	file    *os.File
	path    string
	binding store.CursorRuntime
}

func openTranscript(string, store.CursorRuntime) (*transcript, error) {
	return nil, fmt.Errorf("protected Cursor transcripts unsupported")
}
func (*transcript) record(string, any) error {
	return fmt.Errorf("protected Cursor transcripts unsupported")
}
func randomID() (string, error)         { return "", fmt.Errorf("protected Cursor runtime unsupported") }
func VerifyTranscriptPath(string) error { return fmt.Errorf("protected Cursor transcript unsupported") }
