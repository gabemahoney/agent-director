package adminapi

import (
	"debug/buildinfo"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// journalModulePath is this module's path. A process whose image's build
// metadata names it is an agent-director mutator on every host.
const journalModulePath = "github.com/gabemahoney/agent-director"

// buildInfoNamesModule reports whether an image's build metadata names this
// module: as its path or main module, or as a dependency. It detects renamed
// binaries and Go callers embedding this module, even while their SQLite
// lease is closed. A Go test binary, whose path ends in ".test", never names
// it, whatever its main module and dependencies, because test binaries never
// touch real stores; callers' file-name test still applies to it. Go 1.22's
// test binaries carry no build metadata, so there this changes nothing.
// Must use: both verifiers recognise this module only through it.
func buildInfoNamesModule(bi *buildinfo.BuildInfo) bool {
	if strings.HasSuffix(bi.Path, ".test") {
		return false
	}
	if strings.Contains(bi.Path, journalModulePath) || bi.Main.Path == journalModulePath {
		return true
	}
	for _, dep := range bi.Deps {
		if dep.Path == journalModulePath {
			return true
		}
	}
	return false
}

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
