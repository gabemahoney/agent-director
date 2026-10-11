package layout

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// oldDirName is the name of the directory agent-director used before the
// move to DirRel. It appears in this file only: the presence rules below are
// the one place that looks at it.
const oldDirName = ".agent-director"

// OldDir returns the old directory under home.
func OldDir(home string) string { return filepath.Join(home, oldDirName) }

// OldPresent reports whether the old directory is present under home
// (SR-11.7 "Which directory is there"): anything exists at that name, a
// dangling link included, except a link whose target is AD's directory (a
// compatibility link), judged from the link whether or not its target
// exists. The link's raw target is read, taken relative to home when
// relative, cleaned, and compared lexically with Dir(home).
//
// A home that is not an absolute path has no old directory, so nothing is
// ever looked up relative to the working directory. A name that cannot be
// examined reads as absent.
func OldPresent(home string) bool {
	if !filepath.IsAbs(home) {
		return false
	}
	old := OldDir(home)
	fi, err := os.Lstat(old)
	if err != nil {
		return false
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(old)
		if err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(home, target)
			}
			if filepath.Clean(target) == Dir(home) {
				return false
			}
		}
	}
	return true
}

// NewPresent reports whether AD's directory is present under home: a
// directory, or a link that resolves to one.
func NewPresent(home string) bool {
	if !filepath.IsAbs(home) {
		return false
	}
	fi, err := os.Stat(Dir(home))
	return err == nil && fi.IsDir()
}

// OnlyOldPresent reports whether the old directory is present and AD's
// directory is not (SR-11.7): the state in which the trail writes nothing.
func OnlyOldPresent(home string) bool {
	return OldPresent(home) && !NewPresent(home)
}

// StoreExists reports whether a store file exists at the default path under
// home (a link counts when its target exists). An error other than
// not-exist, such as a permission refusal, counts as existing: the open
// reports it better than the old-layout refusal would.
func StoreExists(home string) bool {
	if !filepath.IsAbs(home) {
		return false
	}
	_, err := os.Stat(Join(home, StoreFile))
	if err == nil {
		return true
	}
	return !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR)
}
