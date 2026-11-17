//go:build linux || darwin

package installversion

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func takeLock(path string) (*os.File, error) {
	f, err := openRegular(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.Join(problem{"lock_busy", "another administrator holds the installation lock; retry later"}, closeErr)
		}
		return nil, errors.Join(err, closeErr)
	}
	return f, nil
}
