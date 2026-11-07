package spawn

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"golang.org/x/sys/unix"
)

// LaunchFileLogger is the existing verb logger's narrow warning surface.
type LaunchFileLogger interface{ Printf(string, ...any) }

func launchFileWarning(lg LaunchFileLogger, format string, args ...any) {
	if logger, ok := lg.(*log.Logger); ok && logger == nil {
		return
	}
	if lg != nil {
		lg.Printf(format, args...)
	}
}

// launchDirectories holds directory descriptors so neither creation nor removal
// follows a replaced path. Only directories this invocation created roll back.
type launchDirectories struct {
	fds     []int
	names   []string
	created []bool
}

// Per-call operations keep write/flush/close and rollback failures testable
// without replacing process-global filesystem functions.
type launchFileOps struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	close    func(*os.File) error
	unlinkat func(int, string, int) error
	closeDir func(int) error
}

func defaultLaunchFileOps() launchFileOps {
	return launchFileOps{(*os.File).Write, (*os.File).Sync, (*os.File).Close, unix.Unlinkat, unix.Close}
}

// Keep the original cause available to errors.Is/As without printing paths or
// other values carried by an os.PathError (or an injected failure).
type launchFileStepError struct {
	step  string
	cause error
}

func (e *launchFileStepError) Error() string { return e.step + " failed" }
func (e *launchFileStepError) Unwrap() error { return e.cause }

func launchFileFailure(step string, cause error) error {
	if cause == nil {
		return nil
	}
	return &launchFileStepError{step: step, cause: cause}
}

func (d *launchDirectories) close(rollback bool, ops launchFileOps) error {
	var failures []error
	for i := len(d.fds) - 1; i >= 0; i-- {
		if rollback && d.created[i] {
			failures = append(failures, launchFileFailure("remove created directory", ops.unlinkat(d.fds[i-1], d.names[i], unix.AT_REMOVEDIR)))
		}
		if d.fds[i] >= 0 {
			failures = append(failures, launchFileFailure("close directory", ops.closeDir(d.fds[i])))
		}
	}
	return errors.Join(failures...)
}

func openLaunchDirectories(storeID, token string, create bool) (*launchDirectories, error) {
	d := &launchDirectories{}
	if _, err := LaunchDirectory(storeID, token); err != nil {
		return d, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return d, err
	}
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return d, err
	}
	d.fds, d.names, d.created = []int{fd}, []string{""}, []bool{false}
	parts := []string{".config", "agent-director", "launch", storeID, token}
	for i, part := range parts {
		parent := d.fds[len(d.fds)-1]
		created := false
		if create {
			err = unix.Mkdirat(parent, part, 0700)
			if err == nil {
				created = true
			} else if !errors.Is(err, unix.EEXIST) || i == len(parts)-1 {
				return d, err
			}
		}
		next, err := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if created {
				// Keep even an unopened directory in the rollback ledger so
				// its unlink failure is reported along with every other one.
				d.fds = append(d.fds, -1)
				d.names = append(d.names, part)
				d.created = append(d.created, true)
			}
			return d, err
		}
		d.fds = append(d.fds, next)
		d.names = append(d.names, part)
		d.created = append(d.created, created)
		if created {
			if err = unix.Fchmod(next, 0700); err != nil {
				return d, err
			}
		}
		var st unix.Stat_t
		if err = unix.Fstat(next, &st); err != nil {
			return d, err
		}
		if st.Uid != uint32(os.Geteuid()) || st.Mode&0022 != 0 || (i >= 2 && st.Mode&07777 != 0700) {
			return d, fmt.Errorf("launch directory ownership or mode is unsafe")
		}
	}
	return d, nil
}

// PrepareLaunchFiles creates only the adapter's nonsecret files, exclusively and
// privately. Call it after composition and before insert/reset, never after the
// row write. A failure rolls back this invocation's files and empty directories.
func PrepareLaunchFiles(c ComposedLaunch, storeID, token string, lg LaunchFileLogger) error {
	return prepareLaunchFiles(c, storeID, token, lg, defaultLaunchFileOps())
}

func prepareLaunchFiles(c ComposedLaunch, storeID, token string, lg LaunchFileLogger, ops launchFileOps) (err error) {
	if len(c.Files) == 0 {
		return nil
	}
	dir, err := LaunchDirectory(storeID, token)
	if err != nil {
		return launchPreparationError("resolve directory", err)
	}
	allowed := generatedLaunchFileNames()
	for _, file := range c.Files {
		if filepath.Dir(file.Path) != dir || !slices.Contains(allowed, filepath.Base(file.Path)) {
			return launchPreparationError("validate generated path", errors.New("path is not an owned generated file"))
		}
	}
	d, openErr := openLaunchDirectories(storeID, token, true)
	var created []string
	defer func() {
		var cleanup []error
		step := "close directories"
		if err != nil {
			step = "rollback preparation"
			for _, name := range created {
				fd := d.fds[len(d.fds)-1]
				cleanup = append(cleanup, launchFileFailure("remove created file", ops.unlinkat(fd, name, 0)))
			}
		}
		cleanup = append(cleanup, d.close(err != nil, ops))
		if cleanupErr := errors.Join(cleanup...); cleanupErr != nil {
			launchFileWarning(lg, "WARN: launch file cleanup incomplete: store_id=%q launch_token=%q step=%q: %v", storeID, token, step, cleanupErr)
			if err != nil {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	if openErr != nil {
		return launchPreparationError("create private directory", openErr)
	}
	fd := d.fds[len(d.fds)-1]
	for _, file := range c.Files {
		name := filepath.Base(file.Path)
		raw, openErr := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if openErr != nil {
			return launchPreparationError("create settings file", openErr)
		}
		created = append(created, name)
		f := os.NewFile(uintptr(raw), name)
		step := "chmod settings file"
		writeErr := f.Chmod(0600)
		var st unix.Stat_t
		if writeErr == nil {
			step = "stat settings file"
			writeErr = unix.Fstat(raw, &st)
		}
		if writeErr == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid())) {
			step = "validate settings file"
			writeErr = fmt.Errorf("unsafe settings file")
		}
		if writeErr == nil {
			step = "write settings file"
			var n int
			n, writeErr = ops.write(f, file.Content)
			if writeErr == nil && n != len(file.Content) {
				writeErr = io.ErrShortWrite
			}
		}
		if writeErr == nil {
			step = "sync settings file"
			writeErr = ops.sync(f)
		}
		closeErr := ops.close(f)
		if writeErr != nil || closeErr != nil {
			if writeErr == nil {
				step = "close settings file"
			}
			return launchPreparationError(step, errors.Join(writeErr, closeErr))
		}
	}
	return nil
}

func launchPreparationError(step string, cause error) error {
	return fmt.Errorf("%w: prepare launch files: %w; nothing was launched or written to the store", tmux.ErrTmuxSessionCreate, launchFileFailure(step, cause))
}

func generatedLaunchFileNames() []string {
	var names []string
	for _, p := range paneOnlyAgents {
		names = append(names, p.files...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// ReclaimLaunchFiles removes only named, regular, current-user generated files
// and a then-empty directory. Callers must first establish no-launch or satisfy
// their existing conditional finish and liveness guards. Never use recursive
// deletion: native session data in a lane's private directory must survive.
func ReclaimLaunchFiles(storeID, token string, lg LaunchFileLogger) {
	if token == "" {
		return
	}
	if err := reclaimLaunchFiles(storeID, token); err != nil {
		launchFileWarning(lg, "WARN: launch file reclaim: store_id=%q launch_token=%q: %v", storeID, token, err)
	}
}

func reclaimLaunchFiles(storeID, token string) (err error) {
	d, err := openLaunchDirectories(storeID, token, false)
	defer func() { err = errors.Join(err, d.close(false, defaultLaunchFileOps())) }()
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return launchFileFailure("open reclaim directory", err)
	}
	fd := d.fds[len(d.fds)-1]
	var failures []error
	for _, name := range generatedLaunchFileNames() {
		var st unix.Stat_t
		err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid())) {
			err = fmt.Errorf("entry is not a regular file owned by the current user")
		}
		if err == nil {
			err = unix.Unlinkat(fd, name, 0)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s retained: %w", name, err))
		}
	}
	if err := unix.Unlinkat(d.fds[len(d.fds)-2], token, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOTEMPTY) && !errors.Is(err, unix.EEXIST) && !errors.Is(err, unix.ENOENT) {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// DefinitelyNotStarted permits cleanup only for classified no-launch evidence.
// An unrecognized failure and even a killed unlabelled session retain files.
func (out CreateOutcome) DefinitelyNotStarted() bool {
	if out.BeforeCreate || out.Kind == CreateUnavailable || out.Kind == CreateDuplicate {
		return true
	}
	return out.Kind == CreateFailed && out.Cause != nil && (out.Cause.Failure == tmux.FailNoServer || out.Cause.Failure == tmux.FailNoSocket)
}

// ReportRetainedLaunchFiles reports exceptional, possibly unreachable files with
// ownership metadata only. It does not scan or attempt additional cleanup.
func ReportRetainedLaunchFiles(storeID, token, rowState string, lg LaunchFileLogger) {
	if lg == nil {
		return
	}
	path, err := LaunchDirectory(storeID, token)
	if err != nil {
		launchFileWarning(lg, "WARN: retained launch files: store_id=%q launch_token=%q step=%q", storeID, token, "resolve directory")
		return
	}
	launchFileWarning(lg, "WARN: retained launch files may be unreachable after failed reuse: store_id=%q launch_token=%q row_state=%q owning_path=%q", storeID, token, rowState, path)
}
