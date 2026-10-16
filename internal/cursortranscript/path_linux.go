//go:build linux

// Package cursortranscript owns protected gateway transcript access shared by
// public resume preflight and the private Cursor recorder.
package cursortranscript

import (
	"encoding/hex"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

// Verify checks an existing protected transcript without creating it.
func Verify(path string) error {
	f, err := Open(path, unix.O_RDONLY)
	if err != nil {
		return err
	}
	return f.Close()
}

// VerifyAppend preflights continuation without creating or writing the file.
func VerifyAppend(path string) error {
	f, err := Open(path, unix.O_RDWR|unix.O_APPEND)
	if err != nil {
		return err
	}
	return f.Close()
}

// Open refuses symlink components, non-private files and file creation.
func Open(path string, flags int) (*os.File, error) {
	if flags != unix.O_RDONLY && flags != unix.O_RDWR|unix.O_APPEND {
		return nil, fmt.Errorf("invalid Cursor transcript open mode")
	}
	if !filepath.IsAbs(os.Getenv("HOME")) {
		return nil, fmt.Errorf("Cursor transcript home unavailable")
	}
	base := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "cursor-transcripts")
	name := filepath.Base(path)
	if filepath.Dir(path) != base || len(name) != 38 || name[32:] != ".jsonl" {
		return nil, fmt.Errorf("invalid Cursor transcript reference")
	}
	if _, err := hex.DecodeString(name[:32]); err != nil {
		return nil, fmt.Errorf("invalid Cursor transcript reference")
	}
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("Cursor transcript unavailable")
	}
	parts := strings.Split(strings.TrimPrefix(base, "/"), "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(dir)
			return nil, fmt.Errorf("unsafe Cursor transcript path")
		}
		next, e := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dir)
		if e != nil {
			return nil, fmt.Errorf("protected Cursor transcript unavailable")
		}
		dir = next
		if i >= len(parts)-2 {
			var st unix.Stat_t
			if e = unix.Fstat(dir, &st); e != nil || st.Uid != uint32(os.Getuid()) || st.Mode&0022 != 0 {
				unix.Close(dir)
				return nil, fmt.Errorf("Cursor transcript parent is not protected")
			}
		}
	}
	defer unix.Close(dir)
	var ds unix.Stat_t
	if err = unix.Fstat(dir, &ds); err != nil || ds.Uid != uint32(os.Getuid()) || ds.Mode&0077 != 0 {
		return nil, fmt.Errorf("Cursor transcript directory changed")
	}
	fd, err := unix.Openat(dir, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("Cursor transcript unavailable")
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0077 != 0 || st.Nlink != 1 {
		unix.Close(fd)
		return nil, fmt.Errorf("Cursor transcript is not protected")
	}
	return os.NewFile(uintptr(fd), name), nil
}
