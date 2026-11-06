package adminapi

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// journalFileID is a file's identity, its device and inode. The device is
// Darwin's 32-bit dev_t, the value lsof prints, so a Linux st_dev is cut to
// the same width and both sides of a comparison agree on either host.
type journalFileID struct {
	dev, ino uint64
}

// journalStat is what the verifiers read of a file: its identity, its kind
// (the S_IFMT bits), its permission bits, its owner and its status-change
// time.
type journalStat struct {
	id    journalFileID
	mode  uint32
	perm  uint32
	uid   uint32
	ctime time.Time
}

// journalImageFile is an image opened for its Mach-O headers and build
// metadata.
type journalImageFile interface {
	io.ReaderAt
	io.Closer
}

// journalFS is every file-system lookup the Darwin verifier, the shared path
// check (verifyJournalPathIn) and the Darwin containment rule make. Tests on
// Linux wrap osJournalFS over real temporary files to fold case, or to refuse
// or block a lookup, so none of them calls os or unix directly for these
// lookups.
type journalFS interface {
	// Lstat reads path itself, never following a link.
	Lstat(path string) (journalStat, error)
	// ReadDir lists the names in the directory at path, sorted; an error
	// means the directory cannot be opened or read.
	ReadDir(path string) ([]string, error)
	// Open opens path read-only, never following a link or blocking on a
	// FIFO, and reads the opened file's own status.
	Open(path string) (journalImageFile, journalStat, error)
	// EvalSymlinks is path with every link resolved.
	EvalSymlinks(path string) (string, error)
	// KernelPath is the paths the kernel reports for an open descriptor of
	// path, none on a host that reports none.
	KernelPath(path string) ([]string, error)
}

// osJournalFS is the production journalFS. It has no build tag: x/sys's
// Stat_t has Dev, Ino, Mode and Ctim under the same names on Linux and
// Darwin.
type osJournalFS struct{}

func journalStatOf(st *unix.Stat_t) journalStat {
	return journalStat{
		id:    journalFileID{dev: uint64(uint32(st.Dev)), ino: uint64(st.Ino)},
		mode:  uint32(st.Mode) & unix.S_IFMT,
		perm:  uint32(st.Mode) & 0o777,
		uid:   st.Uid,
		ctime: time.Unix(st.Ctim.Unix()),
	}
}

func (osJournalFS) Lstat(path string) (journalStat, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return journalStat{}, &fs.PathError{Op: "lstat", Path: path, Err: err}
	}
	return journalStatOf(&st), nil
}

// ReadDir reads names only, so an entry it cannot stat never fails the read.
func (osJournalFS) ReadDir(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (osJournalFS) Open(path string) (journalImageFile, journalStat, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, journalStat{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, journalStat{}, &fs.PathError{Op: "fstat", Path: path, Err: err}
	}
	return f, journalStatOf(&st), nil
}

func (osJournalFS) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func (osJournalFS) KernelPath(path string) ([]string, error) {
	return journalKernelPath(path)
}

// journalLookupAbsent reports a lookup that found nothing, one through a
// regular file included, as the store's inputPathAbsent does.
func journalLookupAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR)
}

// journalLookupRefused reports a lookup the system refused for permission or
// privacy.
func journalLookupRefused(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)
}

// journalChainIDs is the identities of path and of each lexical parent up to
// /. For a path with no link in it, its lexical parents are its physical ones.
func journalChainIDs(fsys journalFS, path string) ([]journalFileID, error) {
	var ids []journalFileID
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		st, err := fsys.Lstat(current)
		if err != nil {
			return nil, err
		}
		ids = append(ids, st.id)
		if current == filepath.Dir(current) {
			return ids, nil
		}
	}
}

// journalAncestorIDs is the ancestor test's evidence for a listed name: the
// identities of its deepest existing ancestor, the name itself when it
// exists, and of that ancestor's parents. lsof's names are kernel vnode paths
// with no links, so the lexical parents are physical. A component the system
// refuses to look up counts as absent, and the test moves on to its parent,
// because every root's own path was read in full before the listing.
func journalAncestorIDs(fsys journalFS, name string) ([]journalFileID, error) {
	current := filepath.Clean(name)
	for {
		_, err := fsys.Lstat(current)
		if err == nil {
			return journalChainIDs(fsys, current)
		}
		if !journalLookupAbsent(err) && !journalLookupRefused(err) || current == filepath.Dir(current) {
			return nil, err
		}
		current = filepath.Dir(current)
	}
}

// journalPathElems is a clean absolute path's names; / has none.
func journalPathElems(path string) []string {
	if path == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

// journalUnderForm reports whether the listed name is the root form or lies
// under it, each name compared case-insensitively.
func journalUnderForm(name, form string) bool {
	if !filepath.IsAbs(name) {
		return false
	}
	names, formNames := journalPathElems(filepath.Clean(name)), journalPathElems(form)
	if len(names) < len(formNames) {
		return false
	}
	for i, formName := range formNames {
		if !strings.EqualFold(names[i], formName) {
			return false
		}
	}
	return true
}

// darwinRoot is a legacy root as the verifier found it after the listing:
// its identity when it exists, and the forms its listed paths may take.
type darwinRoot struct {
	exists bool
	id     journalFileID
	// forms are the root's own path, its EvalSymlinks form and its kernel
	// forms, each once. The ancestor test runs only for a name under one.
	forms []string
}

// journalDeepestExisting is the deepest of path and its lexical parents that
// exists, found with Lstat, its status, and the names below it.
func journalDeepestExisting(fsys journalFS, path string) (string, journalStat, []string, error) {
	var rest []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		st, err := fsys.Lstat(current)
		if err == nil {
			return current, st, rest, nil
		}
		if !journalLookupAbsent(err) || current == filepath.Dir(current) {
			return "", journalStat{}, nil, err
		}
		rest = append([]string{filepath.Base(current)}, rest...)
	}
}

// journalRootOf reads one legacy root after the listing: its identity, and
// that of every entry a walk of it reaches into walked, and its forms. An
// absent root's EvalSymlinks and kernel forms are those of its deepest
// existing ancestor, with the remaining names joined on. Any lookup that
// fails refuses legacy_root_unreadable, because verifyJournalPathIn has
// already read every component; the end of ctx refuses inventory_deadline.
func journalRootOf(ctx context.Context, fsys journalFS, root string, walked map[journalFileID]bool) (darwinRoot, error) {
	r := darwinRoot{forms: []string{root}}
	ancestor, st, rest, err := journalDeepestExisting(fsys, root)
	if err != nil {
		return r, journalRefusal("legacy_root_unreadable")
	}
	if len(rest) == 0 {
		r.exists, r.id = true, st.id
		if err := journalWalk(ctx, fsys, root, st, walked); err != nil {
			return r, err
		}
	}
	resolved, err := fsys.EvalSymlinks(ancestor)
	if err != nil {
		return r, journalRefusal("legacy_root_unreadable")
	}
	kernel, err := fsys.KernelPath(ancestor)
	if err != nil {
		return r, journalRefusal("legacy_root_unreadable")
	}
	for _, base := range append([]string{resolved}, kernel...) {
		form := filepath.Join(append([]string{base}, rest...)...)
		if !slices.Contains(r.forms, form) {
			r.forms = append(r.forms, form)
		}
	}
	return r, nil
}

// journalWalk records the identity of root and of every entry under it,
// never following a link. A directory it cannot read is recorded but not
// entered, and an entry it cannot read is left out: the ancestor test covers
// both. The end of ctx between entries refuses inventory_deadline.
func journalWalk(ctx context.Context, fsys journalFS, root string, st journalStat, walked map[journalFileID]bool) error {
	walked[st.id] = true
	if st.mode != unix.S_IFDIR {
		return nil
	}
	dirs := []string{root}
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		names, err := fsys.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, name := range names {
			if ctx.Err() != nil {
				return journalRefusal("inventory_deadline")
			}
			path := filepath.Join(dir, name)
			entry, err := fsys.Lstat(path)
			if err != nil {
				continue
			}
			walked[entry.id] = true
			if entry.mode == unix.S_IFDIR {
				dirs = append(dirs, path)
			}
		}
	}
	return nil
}

// journalACLEntry is one access-control entry line of `/bin/ls -led`:
// `<n>: <principal> [inherited] allow|deny <perms>`, the principal being
// user:<name>, group:<name> or a UUID.
var journalACLEntry = regexp.MustCompile(`^ *[0-9]+: (user:\S+|group:\S+|[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})( inherited)? (allow|deny) \S+$`)

// journalACLPrivate reads `/bin/ls -led` output for one path and reports
// whether its access-control entries are known and allow no principal but
// the owner. Line 1 is the ls -l line, its mode and owner the first and third
// fields; every later line must be an entry. A mode ending in + with no entry
// lines is not known. The + alone is no evidence either way: macOS shows @
// instead when the file also has extended attributes, so the entry lines are
// the evidence.
func journalACLPrivate(out []byte) bool {
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	fields := strings.Fields(lines[0])
	if len(fields) < 3 {
		return false
	}
	mode, owner := fields[0], fields[2]
	entries := lines[1:]
	if len(entries) == 0 {
		return !strings.HasSuffix(mode, "+")
	}
	for _, line := range entries {
		m := journalACLEntry.FindStringSubmatch(line)
		if m == nil {
			return false
		}
		if m[3] == "allow" && m[1] != "user:"+owner {
			return false
		}
	}
	return true
}

// containmentLookupError is a pathWithinByIdentity lookup that failed, on
// the directory's side or the path's.
type containmentLookupError struct {
	dirSide bool
	err     error
}

func (e *containmentLookupError) Error() string { return e.err.Error() }

func (e *containmentLookupError) Unwrap() error { return e.err }

// journalResolveExisting resolves path to its deepest existing ancestor with
// every link resolved, trying EvalSymlinks on it and then on each lexical
// parent, and the names below that ancestor. A lookup that finds nothing
// moves up; any other error is returned.
func journalResolveExisting(fsys journalFS, path string) (string, []string, error) {
	var rest []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		resolved, err := fsys.EvalSymlinks(current)
		if err == nil {
			return resolved, rest, nil
		}
		if !journalLookupAbsent(err) || current == filepath.Dir(current) {
			return "", nil, err
		}
		rest = append([]string{filepath.Base(current)}, rest...)
	}
}

// pathWithinByIdentity reports whether path is dir or lies under it, by file
// identity (SRD SR-2.15 "Limits"), so a case variant cannot escape it. An
// existing dir must be among the identities of path's resolved deepest
// existing ancestor and that ancestor's parents. A dir that does not exist
// yet must share path's deepest existing ancestor, with path's remaining
// names beginning with dir's, compared case-insensitively. A failed lookup
// is a *containmentLookupError naming its side. Must use: every Darwin check
// that a path lies inside a directory goes through it.
func pathWithinByIdentity(fsys journalFS, path, dir string) (bool, error) {
	pathAncestor, pathRest, err := journalResolveExisting(fsys, path)
	if err != nil {
		return false, &containmentLookupError{err: err}
	}
	dirAncestor, dirRest, err := journalResolveExisting(fsys, dir)
	if err != nil {
		return false, &containmentLookupError{dirSide: true, err: err}
	}
	dirStat, err := fsys.Lstat(dirAncestor)
	if err != nil {
		return false, &containmentLookupError{dirSide: true, err: err}
	}
	if len(dirRest) == 0 {
		chain, err := journalChainIDs(fsys, pathAncestor)
		if err != nil {
			return false, &containmentLookupError{err: err}
		}
		return slices.Contains(chain, dirStat.id), nil
	}
	pathStat, err := fsys.Lstat(pathAncestor)
	if err != nil {
		return false, &containmentLookupError{err: err}
	}
	if pathStat.id != dirStat.id || len(pathRest) < len(dirRest) {
		return false, nil
	}
	for i, name := range dirRest {
		if !strings.EqualFold(pathRest[i], name) {
			return false, nil
		}
	}
	return true, nil
}

// backupInsideRootByIdentity is Darwin's backupInsideRoot: whether the backup
// is a root or lies under one, by file identity. A failed lookup refuses
// backup_unreadable on the backup's side and legacy_root_unreadable on a
// root's.
func backupInsideRootByIdentity(fsys journalFS, backup string, roots []string) (bool, error) {
	for _, root := range roots {
		inside, err := pathWithinByIdentity(fsys, backup, root)
		var lookup *containmentLookupError
		if errors.As(err, &lookup) && lookup.dirSide {
			return false, journalRefusal("legacy_root_unreadable")
		}
		if err != nil {
			return false, journalRefusal("backup_unreadable")
		}
		if inside {
			return true, nil
		}
	}
	return false, nil
}
