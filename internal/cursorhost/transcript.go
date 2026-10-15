package cursorhost

import (
	"os"
	"strings"
)

// transcriptFile keeps the same protected identity, write and sync contract
// while allowing deterministic I/O barriers without alternate production paths.
type transcriptFile interface {
	Stat() (os.FileInfo, error)
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func splitPath(path string) []string { return strings.Split(strings.TrimPrefix(path, "/"), "/") }
