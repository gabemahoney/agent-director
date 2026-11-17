//go:build linux || darwin

package installversion

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

// All managed children must be owned and unwriteable by another user. Never
// follow a resource/evidence symlink or block opening a substituted FIFO.
func openRegular(path string, flags int, mode os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK, mode)
	if errors.Is(err, unix.ELOOP) {
		return nil, invalid("symlink in installation metadata or resources")
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || !owned(info) || info.Mode().Perm()&0022 != 0) {
		err = invalid("installation file has unsafe type, owner or permissions")
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func checkDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm()&0022 != 0 {
		return invalid("installation directory has unsafe type, owner or permissions")
	}
	return nil
}

func openDir(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.IsDir() || !owned(info) || info.Mode().Perm()&0022 != 0) {
		err = invalid("unsafe installation directory")
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}
