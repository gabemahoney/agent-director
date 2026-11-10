//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// These are the default Unix SQLite VFS's documented lock ranges, not flock
// locks. https://www.sqlite.org/lockingv3.html and
// https://www.sqlite.org/walformat.html#locking_matrix describe their protocol.
// Read-locking pending/reserved/shared prevents rollback writes, exclusive WAL
// access, mode changes and sidecar deletion. Read-locking all WAL lock bytes and
// the dead-man switch prevents WAL writes/checkpoints/recovery/SHM truncation.
// All source descriptors are O_RDONLY; no lock needs a writable descriptor.
const (
	snapshotDBLockStart   = 0x40000000
	snapshotDBLockLength  = 512
	snapshotSHMLockStart  = 120
	snapshotSHMLockLength = 9
)

var errSnapshotChanged = errors.New("SQLite sidecars changed during readonly snapshot")

func snapshotWait(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("readonly SQLite snapshot busy or changed past its timeout: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func snapshotReadLock(ctx context.Context, f *os.File, start, length int64) error {
	lock := unix.Flock_t{Type: unix.F_RDLCK, Whence: io.SeekStart, Start: start, Len: length}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("readonly SQLite snapshot lock timeout: %w", err)
		}
		err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("readonly SQLite snapshot lock: %w", err)
		}
		if err := snapshotWait(ctx); err != nil {
			return err
		}
	}
}

// Opening once and copying from that descriptor matters: a second open/close
// of the same source inode would release the process's POSIX locks.
func openSnapshotSource(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("readonly SQLite snapshot source is not a readable regular file: %s", path)
	}
	return f, nil
}

func snapshotSourceInfo(f *os.File) (os.FileInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(f.Name())
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		return nil, errSnapshotChanged
	}
	return info, nil
}

func copyReadSnapshot(ctx context.Context, source, destination string) error {
	for {
		err := copyReadSnapshotAttempt(ctx, source, destination)
		if !errors.Is(err, errSnapshotChanged) {
			return err
		}
		if err := snapshotWait(ctx); err != nil {
			return err
		}
	}
}

func copyReadSnapshotAttempt(ctx context.Context, source, destination string) (resultErr error) {
	db, err := openSnapshotSource(source)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	if err := snapshotReadLock(ctx, db, snapshotDBLockStart, snapshotDBLockLength); err != nil {
		return err
	}
	if _, err := snapshotSourceInfo(db); err != nil {
		return err
	}

	shm, err := openSnapshotSource(source + "-shm")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if shm != nil {
		defer func() { resultErr = errors.Join(resultErr, shm.Close()) }()
		if err := snapshotReadLock(ctx, shm, snapshotSHMLockStart, snapshotSHMLockLength); err != nil {
			return err
		}
		if _, err := snapshotSourceInfo(shm); err != nil {
			return err
		}
	}

	// A hot rollback journal requires recovery, which a readonly source read
	// cannot perform. A zeroed/PERSIST journal is harmless. Never recover a
	// private copy using a journal that can name a live super-journal elsewhere.
	journal, err := openSnapshotSource(source + "-journal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if journal != nil {
		defer func() { resultErr = errors.Join(resultErr, journal.Close()) }()
		var header [1]byte
		n, err := journal.Read(header[:])
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n != 0 && header[0] != 0 {
			return fmt.Errorf("readonly SQLite snapshot refuses a rollback journal requiring recovery")
		}
	}

	wal, err := openSnapshotSource(source + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if wal != nil {
		defer func() { resultErr = errors.Join(resultErr, wal.Close()) }()
	}
	files := []*os.File{db, wal, shm, journal}
	infos := make([]os.FileInfo, len(files))
	for i, f := range files {
		if f == nil {
			continue
		}
		infos[i], err = snapshotSourceInfo(f)
		if err != nil {
			return err
		}
		for j := 0; j < i; j++ {
			if infos[j] != nil && os.SameFile(infos[i], infos[j]) {
				return fmt.Errorf("readonly SQLite snapshot refuses aliased database/sidecar inodes")
			}
		}
	}
	var created []string
	defer func() {
		if resultErr != nil {
			for _, path := range created {
				if err := os.Remove(path); err != nil {
					resultErr = errors.Join(resultErr, err)
				}
			}
		}
	}()
	for i, f := range []*os.File{db, wal} {
		if f == nil {
			continue
		}
		path := destination
		if i == 1 {
			path += "-wal"
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		created = append(created, path)
		_, copyErr := io.Copy(out, &snapshotReader{ctx: ctx, reader: f})
		if err := errors.Join(copyErr, out.Close()); err != nil {
			return err
		}
	}
	for i, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if files[i] == nil {
			if _, err := os.Lstat(source + suffix); !errors.Is(err, os.ErrNotExist) {
				return errSnapshotChanged
			}
			continue
		}
		after, err := snapshotSourceInfo(files[i])
		if err != nil {
			return err
		}
		if after.Size() != infos[i].Size() || !after.ModTime().Equal(infos[i].ModTime()) {
			return errSnapshotChanged
		}
	}
	// If SHM was absent, an ordinary WAL writer would have to create it, and
	// cannot unlink it while our database shared lock remains held. Its
	// appearance above rejects the copy. An exclusive WAL writer is blocked
	// by the database lock itself. Thus an absent SHM is not an unlocked copy.
	return ctx.Err()
}

type snapshotReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *snapshotReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
