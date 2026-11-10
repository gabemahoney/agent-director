package adminapi

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// verifyJournalPath is the path check over the host's own file system, as
// the Linux verifier makes it (verifyJournalPathIn).
func verifyJournalPath(path string) error {
	return verifyJournalPathIn(osJournalFS{}, path)
}

// Verify every existing path component without following a link. An absent
// root is known absent only after its existing parent was readable; a path
// through a regular file is absent too, since nothing can exist under it.
// Private roots establish which unprivileged user can hold operational
// authority. Every lookup goes through fsys, so the Darwin verifier runs it
// under its deadline. Must use: this is the one path check, shared by both
// verifiers; never add a second.
func verifyJournalPathIn(fsys journalFS, path string) error {
	if !filepath.IsAbs(path) {
		return journalRefusal("legacy_root_not_absolute")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		st, err := fsys.Lstat(current)
		if err != nil && !journalLookupAbsent(err) {
			return journalRefusal("legacy_root_unreadable")
		}
		if err == nil {
			if st.mode == unix.S_IFLNK {
				return journalRefusal("legacy_root_symlink")
			}
			if current == path && (st.uid != uint32(os.Geteuid()) || st.perm&0o077 != 0) {
				return journalRefusal("legacy_root_access_unestablished")
			}
			if st.mode == unix.S_IFDIR {
				if _, err := fsys.ReadDir(current); err != nil {
					return journalRefusal("legacy_root_unreadable")
				}
			}
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}
