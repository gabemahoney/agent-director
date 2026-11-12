//go:build darwin

package adminapi

import (
	"bytes"
	"io/fs"
	"runtime"
	"slices"
	"unsafe"

	"golang.org/x/sys/unix"
)

// journalKernelPath is the kernel's forms of path (SRD SR-2.15's "a form the
// system resolves that path to"): F_GETPATH and F_GETPATH_NOFIRMLINK (the
// firmlink-free form, such as /System/Volumes/Data/…) read on one descriptor
// of it, both when they differ and one when they are equal. Whichever form
// lsof reports is then matched. It opens path without following a link or
// blocking on a FIFO, and needs no cgo: x/sys's darwin fcntl goes through
// libSystem's trampoline.
func journalKernelPath(path string) ([]string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd)
	var forms []string
	for _, cmd := range []int{unix.F_GETPATH, unix.F_GETPATH_NOFIRMLINK} {
		form, err := journalFcntlPath(fd, cmd)
		if err != nil {
			return nil, &fs.PathError{Op: "fcntl", Path: path, Err: err}
		}
		if !slices.Contains(forms, form) {
			forms = append(forms, form)
		}
	}
	return forms, nil
}

// journalFcntlPath reads one path form of fd into its own MAXPATHLEN buffer.
// The kernel writes through the buffer's address, passed as an integer, as
// x/sys's own FcntlFlock passes its struct; the pin keeps that address valid
// and the buffer alive until the call returns.
func journalFcntlPath(fd, cmd int) (string, error) {
	buf := new([unix.PathMax]byte)
	var pin runtime.Pinner
	pin.Pin(buf)
	defer pin.Unpin()
	if _, err := unix.FcntlInt(uintptr(fd), cmd, int(uintptr(unsafe.Pointer(&buf[0])))); err != nil {
		return "", err
	}
	runtime.KeepAlive(buf)
	n := bytes.IndexByte(buf[:], 0)
	if n < 0 {
		n = len(buf)
	}
	return string(buf[:n]), nil
}
