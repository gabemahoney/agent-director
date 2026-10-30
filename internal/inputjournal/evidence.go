package inputjournal

import (
	"fmt"
	"io"
)

// ReadEvidence reads only a regular, non-symlink file under a byte bound.
// Runtime proof and migration must reuse it to refuse blocking special files.
func ReadEvidence(path string, limit int64) ([]byte, error) {
	f, err := openEvidence(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("native evidence is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("native evidence exceeds read bound")
	}
	return data, err
}
