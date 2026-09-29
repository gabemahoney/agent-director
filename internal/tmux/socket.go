package tmux

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// SocketDirReason says why a socket directory is unusable (SRD SR-3.3,
// SR-1.4 row "an unusable socket directory"; RN-5 R3d, R4a to R4c).
type SocketDirReason int

// The reasons a socket directory is refused. Each but SocketDirNotCreatable
// is one of tmux's own refusals (tmux 3.2a and 3.3a, make_label); tmux words
// SocketDirSymlink like SocketDirNotDirectory, and SocketDirNotOwned like
// SocketDirUnsafePermissions, and the kinds keep them apart.
const (
	// SocketDirCreateFailed: the per-user directory could not be created
	// ("couldn't create directory <dir> (<reason>)"). A TMUX_TMPDIR naming a
	// regular file gives this with "Not a directory" (RN-5 R3d).
	SocketDirCreateFailed SocketDirReason = iota + 1
	// SocketDirUnreadable: the directory could not be read with lstat
	// ("couldn't read directory <dir> (<reason>)"). Also the working
	// directory, Dir ".", when a relative TMUX socket cannot be made absolute.
	SocketDirUnreadable
	// SocketDirSymlink: the per-user directory is a symlink (RN-5 R4b).
	SocketDirSymlink
	// SocketDirNotDirectory: the per-user directory is not a directory.
	SocketDirNotDirectory
	// SocketDirNotOwned: the per-user directory is not owned by the real uid
	// (RN-5 R4c).
	SocketDirNotOwned
	// SocketDirUnsafePermissions: the per-user directory has permission bits
	// for others (RN-5 R4a).
	SocketDirUnsafePermissions
	// SocketDirNotCreatable: a recorded socket's directory is missing and is
	// not a per-user directory agent-director may create (EnsureSocketDir).
	// tmux has no wording for it.
	SocketDirNotCreatable
)

// String is the reason in short words, for descriptions built from the
// fields (SR-1.4).
func (r SocketDirReason) String() string {
	switch r {
	case SocketDirCreateFailed:
		return "could not be created"
	case SocketDirUnreadable:
		return "could not be read"
	case SocketDirSymlink:
		return "is a symlink"
	case SocketDirNotDirectory:
		return "is not a directory"
	case SocketDirNotOwned:
		return "is not owned by this user"
	case SocketDirUnsafePermissions:
		return "has unsafe permissions"
	case SocketDirNotCreatable:
		return "is missing and is not a per-user directory agent-director may create"
	default:
		return "unknown reason " + strconv.Itoa(int(r))
	}
}

// SocketDirError is every refusal of ResolveSocket and EnsureSocketDir: the
// socket's directory cannot be created or fails tmux's own check (SR-3.3).
// Under errors.Is it matches ErrTmuxNotAvailable and no other sentinel (SR-1.1
// class ENVIRONMENT; SR-1.4; SR-1.5).
//
// It is deliberately not a *CallError, although Appendix F.1's ResolveSocket
// sketch says so: a CallError wraps no sentinel (SR-1.5) and resolution makes
// no tmux call. It has no Unwrap, so the OS error in Err never makes it match
// anything else; Err is read as a field.
type SocketDirError struct {
	// Socket is the socket path concerned: the one resolution would return,
	// or the recorded socket given to EnsureSocketDir.
	Socket string
	// Dir is the directory concerned (usually the per-user directory).
	Dir string
	// Reason is why the directory was refused.
	Reason SocketDirReason
	// Err is the underlying OS error, when there is one.
	Err error
}

// Error is tmux's own reason, in tmux's words where tmux has them (tmux 3.3a
// make_label), naming the directory; the OS reason is worded as C's strerror
// words it. It never reads as a missing binary: errnames.Classify uses it as
// the description.
func (e *SocketDirError) Error() string {
	switch e.Reason {
	case SocketDirCreateFailed:
		return "couldn't create directory " + e.Dir + " (" + strerror(e.Err) + ")"
	case SocketDirUnreadable:
		return "couldn't read directory " + e.Dir + " (" + strerror(e.Err) + ")"
	case SocketDirSymlink, SocketDirNotDirectory:
		return e.Dir + " is not a directory"
	case SocketDirNotOwned, SocketDirUnsafePermissions:
		return "directory " + e.Dir + " has unsafe permissions"
	default:
		return "directory " + e.Dir + " of socket " + e.Socket + " " + e.Reason.String()
	}
}

// Is reports whether target is ErrTmuxNotAvailable, the only sentinel a
// socket-directory refusal matches (SR-1.4 row "an unusable socket
// directory").
func (e *SocketDirError) Is(target error) bool {
	return target == ErrTmuxNotAvailable
}

// strerror words an OS error as C's strerror does for tmux's messages
// ("Not a directory"): the errno's text with its first letter capitalised,
// or the error's own text when it carries no errno.
func strerror(err error) string {
	if err == nil {
		return "unknown error"
	}
	var errno syscall.Errno
	msg := err.Error()
	if errors.As(err, &errno) {
		msg = errno.Error()
	}
	r, size := utf8.DecodeRuneInString(msg)
	return string(unicode.ToUpper(r)) + msg[size:]
}

// socketEnv is the seam for the inputs of socket resolution: the environment,
// the working directory, the real uid and the default base directory. The
// exported functions use productionSocketEnv; tests build their own through
// export_test.go, so no case needs root or the real /tmp/tmux-<uid>.
type socketEnv struct {
	lookupEnv   func(key string) (string, bool)
	getwd       func() (string, error)
	getuid      func() int
	defaultBase string
}

// defaultSocketBase is tmux's default base directory (_PATH_TMP).
const defaultSocketBase = "/tmp"

// defaultSocketName is the socket's name in the per-user directory (tmux's
// default label).
const defaultSocketName = "default"

// userDirMode is the mode tmux creates the per-user directory with.
const userDirMode = 0o700

func productionSocketEnv() socketEnv {
	return socketEnv{
		lookupEnv:   os.LookupEnv,
		getwd:       os.Getwd,
		getuid:      os.Getuid,
		defaultBase: defaultSocketBase,
	}
}

// ResolveSocket returns the socket path tmux itself would use in this
// process's environment, reproducing tmux 3.2a and 3.3a as RN-5 measured them
// (SRD SR-3.3 steps 1 to 3; rn5-tmux-check.md "What SR-3.3 must say"):
//
//  1. TMUX, when its first comma-separated field is not empty: that field, as
//     given (a server now dead still counts; nothing is checked). A relative
//     field, which tmux never writes, is joined to the working directory as
//     text only, with no symlink resolution and no existence check, so it
//     names the same file; a working directory that cannot be read is a
//     SocketDirUnreadable refusal. A TMUX that is unset, empty or whose
//     first field is empty (",123,0") is ignored (R3a).
//  2. Otherwise the base directory: TMUX_TMPDIR, when set, not empty and its
//     real path can be taken (any existing path, a regular file included),
//     as that real path: symlinks resolved, a trailing slash dropped, a
//     relative value resolved against the working directory (R1, R3c). A
//     value containing ':' is one path. A value whose real path cannot be
//     taken, for any reason, falls back silently to /tmp, with no error and
//     nothing created (R3b). /tmp too is taken as its real path (on darwin
//     /private/tmp), or as written when even that fails (tmux 3.3a
//     expand_paths).
//  3. The per-user directory <base>/tmux-<real uid>. With create, a missing
//     one is made with mode 0700 (a concurrent creation is not an error).
//     An existing one is then checked as tmux checks it: read with lstat,
//     not a symlink, a directory, owned by the real uid, and no permission
//     bits for others; group bits and a setgid bit inherited from /tmp pass
//     (R4a to R4c, R5). The socket is <real path of that directory>/default.
//
// Without create nothing is ever created (LFR H1): a missing per-user
// directory is no error and its would-be path is returned (the lookup then
// gets the no-socket reply, SR-2.5). Every refusal is the same under both:
// a base that is a regular file reads "couldn't create directory …
// (Not a directory)" either way, as tmux's own creation would.
//
// Every refusal is a *SocketDirError (ErrTmuxNotAvailable). Launches pass
// create true; lookups of a row with no recorded socket pass false. The
// tmux binary is never run.
func ResolveSocket(create bool) (string, error) {
	return productionSocketEnv().resolveSocket(create)
}

func (e socketEnv) resolveSocket(create bool) (string, error) {
	if v, ok := e.lookupEnv("TMUX"); ok {
		if field, _, _ := strings.Cut(v, ","); field != "" {
			return e.absoluteTMUXSocket(field)
		}
	}
	uid := e.getuid()
	dir := filepath.Join(e.socketBase(), "tmux-"+strconv.Itoa(uid))
	socket := filepath.Join(dir, defaultSocketName)
	exists, err := checkUserDir(dir, socket, uid, create)
	if err != nil {
		return "", err
	}
	if exists {
		if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
			socket = filepath.Join(resolved, defaultSocketName)
		}
	}
	return socket, nil
}

// absoluteTMUXSocket returns TMUX's first field, made absolute by text when
// relative (see ResolveSocket step 1).
func (e socketEnv) absoluteTMUXSocket(field string) (string, error) {
	if filepath.IsAbs(field) {
		return field, nil
	}
	wd, err := e.getwd()
	if err != nil {
		return "", &SocketDirError{Socket: field, Dir: ".", Reason: SocketDirUnreadable, Err: err}
	}
	return joinText(wd, field), nil
}

// socketBase is step 2 of ResolveSocket: TMUX_TMPDIR's real path when it can
// be taken, else the default base's real path, else the default as written.
func (e socketEnv) socketBase() string {
	if v, ok := e.lookupEnv("TMUX_TMPDIR"); ok && v != "" {
		if resolved, ok := e.realPath(v); ok {
			return resolved
		}
	}
	if resolved, ok := e.realPath(e.defaultBase); ok {
		return resolved
	}
	return e.defaultBase
}

// realPath is realpath(3): p resolved against the working directory when
// relative, every symlink followed, every component required to exist, and
// only the last allowed to be a non-directory (a trailing slash makes it a
// directory component).
func (e socketEnv) realPath(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		wd, err := e.getwd()
		if err != nil {
			return "", false
		}
		p = joinText(wd, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// joinText joins a relative path to dir without cleaning it, so "..", "."
// and symlinks are left for the kernel to resolve exactly as it would from
// dir.
func joinText(dir, rel string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + rel
	}
	return dir + "/" + rel
}

// EnsureSocketDir makes a recorded socket's vanished per-user directory
// again before a resume or reuse launches on that socket (SRD SR-3.3 "A
// recorded socket whose directory has gone"; Appendix E F9e, typically after
// a reboot). A lookup never calls it and never creates anything (LFR H1).
//
//   - The socket's directory exists (os.Stat succeeds, whatever it is): nil,
//     with nothing created, changed or checked. The recorded socket may be
//     another server's TMUX path such as /tmp/x.sock, and tmux's -S does not
//     check its directory either.
//   - It is missing, the socket path is absolute, the directory's base name
//     is exactly tmux-<real uid> and its parent exists: it is created with
//     mode 0700 (a concurrent creation is not an error) and checked as
//     ResolveSocket checks it. A failed creation or check is a
//     *SocketDirError with tmux's reason.
//   - It is missing and not of that form (a relative socket path, another
//     base name or uid, or a missing parent): a *SocketDirError with reason
//     SocketDirNotCreatable naming the socket and the directory. No parent
//     and no other directory is ever created.
//
// Every refusal matches ErrTmuxNotAvailable and no other sentinel.
func EnsureSocketDir(socket string) error {
	return productionSocketEnv().ensureSocketDir(socket)
}

func (e socketEnv) ensureSocketDir(socket string) error {
	dir := filepath.Dir(socket)
	missing := statErr(dir)
	if missing == nil {
		return nil
	}
	uid := e.getuid()
	notCreatable := func(err error) error {
		return &SocketDirError{Socket: socket, Dir: dir, Reason: SocketDirNotCreatable, Err: err}
	}
	if !filepath.IsAbs(socket) || filepath.Base(dir) != "tmux-"+strconv.Itoa(uid) {
		return notCreatable(missing)
	}
	if err := statErr(filepath.Dir(dir)); err != nil {
		return notCreatable(err)
	}
	_, err := checkUserDir(dir, socket, uid, true)
	return err
}

// statErr is os.Stat's error for p, nil when p exists.
func statErr(p string) error {
	_, err := os.Stat(p)
	return err
}

// checkUserDir is the one creation and check of a per-user directory, shared
// by ResolveSocket and EnsureSocketDir so they cannot drift (tmux 3.3a
// make_label). With create, a missing dir is made with mode 0700 (EEXIST is
// not an error); a failed creation is SocketDirCreateFailed. Without create,
// a missing dir (ENOENT) returns exists false and no error, and any other
// lstat failure is reported as the creation would fail, with the same errno.
// An existing dir is checked with lstat: a symlink, not a directory, not
// owned by uid, or with bits for others is refused. Group bits and setgid
// pass.
func checkUserDir(dir, socket string, uid int, create bool) (exists bool, err error) {
	refuse := func(reason SocketDirReason, err error) error {
		return &SocketDirError{Socket: socket, Dir: dir, Reason: reason, Err: err}
	}
	if create {
		if err := os.Mkdir(dir, userDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return false, refuse(SocketDirCreateFailed, err)
		}
	}
	fi, err := os.Lstat(dir)
	switch {
	case err == nil:
	case !create && errors.Is(err, syscall.ENOENT):
		return false, nil
	case !create:
		return false, refuse(SocketDirCreateFailed, err)
	default:
		return false, refuse(SocketDirUnreadable, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return true, refuse(SocketDirSymlink, nil)
	}
	if !fi.IsDir() {
		return true, refuse(SocketDirNotDirectory, nil)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int64(st.Uid) != int64(uid) {
		return true, refuse(SocketDirNotOwned, nil)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		return true, refuse(SocketDirUnsafePermissions, nil)
	}
	return true, nil
}
