package installversion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/version"
)

// CaptureExecutable binds a helper or delivery hook to this running image.
// On Darwin, Executable may name the invocation symlink after publication has
// changed it. Only this image's compiled stamp may select a retained version;
// resolving the symlink alone does not capture the running generation.
func CaptureExecutable() (string, error) {
	return captureExecutable(os.Executable, filepath.EvalSymlinks, version.Version)
}

// The low-level readers are parameters so tests can model Darwin's invocation
// path and a publication before resolution without replacing this algorithm.
func captureExecutable(executable func() (string, error), resolve func(string) (string, error), stamp string) (string, error) {
	path, err := executable()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, resolveErr := resolve(abs)
	// Keep the invocation's layout even when its mutable entry is now broken
	// or points elsewhere. Such a path must never become an outside build.
	root, err := executableLayout(abs)
	if err != nil {
		return "", err
	}
	if root == "" && resolveErr == nil {
		root, err = executableLayout(real)
		if err != nil {
			return "", err
		}
	}
	if root == "" {
		if resolveErr != nil {
			return "", resolveErr
		}
		return filepath.Abs(real)
	}
	root, err = canonicalRoot(root)
	if err != nil {
		return "", fmt.Errorf("capture running version: %w", err)
	}
	// A ready own-stamp directory is the only in-layout answer, whether the
	// initial resolution reached it, another generation, or a mutable entry.
	// readVersion checks the record, ready marker and immutable file inventory
	// without current, publication history, installation locks or store access.
	own, _, err := readVersion(root, layout.VersionID(stamp), true)
	if err != nil {
		return "", fmt.Errorf("capture running version: no safe ready executable for this image: %w", err)
	}
	return own.RuntimeExecutable, nil
}

// executableLayout recognizes managed executable paths, including incomplete
// layouts. A plain build's bin directory is not installation evidence.
func executableLayout(path string) (string, error) {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == layout.VersionsDir && validDigest(filepath.Base(dir)) {
			return filepath.Dir(parent), nil
		}
		// An established layout also owns paths outside versions, including
		// an overwritten permanent entry. Renaming an executable within the
		// layout cannot turn an unsafe lookup into an outside-layout build.
		for _, name := range []string{layout.InstallContract, layout.InstallInitialization} {
			_, err := os.Lstat(filepath.Join(dir, name))
			if err == nil {
				return dir, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		// A directory merely named current is not AD evidence. The selecting
		// link's managed target is evidence even before the anchor is present;
		// broken/mismatched links in established layouts are caught above by
		// their contract/initialization markers, never treated as build paths.
		current := filepath.Join(dir, layout.CurrentVersion)
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err != nil {
				return "", err
			}
			id := strings.TrimPrefix(target, layout.VersionsDir+"/")
			if validDigest(id) && target == layout.VersionsDir+"/"+id {
				return dir, nil
			}
		}
		if parent == dir {
			break
		}
	}
	return "", nil
}
