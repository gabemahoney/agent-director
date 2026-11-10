package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReadSnapshot copies a consistent database and its committed WAL into an
// existing empty private directory. It never opens the source through SQLite:
// even a readonly SQLite connection can create or rewrite source sidecars.
// Future preflight consumers must reuse ReadSnapshot to leave the source untouched.
// The caller owns directory and removes it after querying the returned path.
// Call it only while the calling process holds no other descriptor or SQLite
// connection of the source database, such as from a separate process or before
// the process opens the store: closing any descriptor of an inode releases
// that process's POSIX locks.
func ReadSnapshot(ctx context.Context, source, directory string, busyTimeoutMs int) (string, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("readonly snapshot requires absolute source and directory paths")
	}
	if busyTimeoutMs <= 0 || busyTimeoutMs > 2147483647 {
		return "", fmt.Errorf("readonly snapshot busy timeout is out of range")
	}
	dir, err := os.Lstat(directory)
	if err != nil || dir == nil || !dir.IsDir() || dir.Mode().Perm() != 0700 {
		return "", fmt.Errorf("readonly snapshot requires an existing private directory (mode 0700)")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	if len(entries) != 0 {
		return "", fmt.Errorf("readonly snapshot directory is not empty")
	}
	// SQLite resolves the database's symlink before choosing its sidecar names.
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(busyTimeoutMs)*time.Millisecond)
	defer cancel()
	destination := filepath.Join(directory, "state.db")
	if err := copyReadSnapshot(ctx, source, destination); err != nil {
		return "", err
	}
	return destination, nil
}
