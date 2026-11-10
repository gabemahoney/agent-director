//go:build linux

package inputdelivery

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/probe"
	"golang.org/x/sys/unix"
)

const nativeTerminalSupported = true

func (s *nativeSession) Queue(id Identity) (int, error) {
	owner, err := probe.ReadTerminalProcess(id.PID)
	if err != nil {
		return 0, err
	}
	if id.Starttime == "" || owner.Starttime != id.Starttime || owner.PGRP != owner.Foreground {
		return 0, fmt.Errorf("agent identity or terminal foreground ownership changed")
	}
	path := filepath.Join("/proc", strconv.Itoa(id.PID), "fd", "0")
	tty, err := os.Readlink(path)
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(tty, "/dev/pts/") {
		return 0, fmt.Errorf("agent has no owned terminal")
	}
	f, err := os.OpenFile(tty, os.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var opened, stdin unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &opened); err != nil {
		return 0, err
	}
	if err := unix.Stat(path, &stdin); err != nil {
		return 0, err
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFCHR || opened.Rdev != owner.TTY ||
		opened.Dev != stdin.Dev || opened.Ino != stdin.Ino || opened.Rdev != stdin.Rdev {
		return 0, fmt.Errorf("agent stdin is not its observed controlling terminal")
	}
	term, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return 0, err
	}
	if term.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		return 0, fmt.Errorf("terminal is not raw")
	}
	q, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCINQ)
	if err != nil {
		return 0, err
	}
	// Do not publish a queue sample observed across an exit, PID reuse,
	// foreground change or descriptor replacement. These observations do not
	// claim to lock the physical target against changes after this call.
	after, err := probe.ReadTerminalProcess(id.PID)
	if err != nil {
		return 0, err
	}
	if err := unix.Stat(path, &stdin); err != nil {
		return 0, err
	}
	if after != owner || opened.Dev != stdin.Dev || opened.Ino != stdin.Ino || opened.Rdev != stdin.Rdev {
		return 0, fmt.Errorf("agent terminal ownership changed during queue observation")
	}
	return q, nil
}

func ownedTranscripts(id Identity) ([]string, error) {
	if err := verifyTranscriptProcess(id); err != nil {
		return nil, err
	}
	dir := filepath.Join("/proc", strconv.Itoa(id.PID), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, e := range entries {
		p, err := os.Readlink(filepath.Join(dir, e.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if strings.Contains(p, "/sessions/") && strings.HasSuffix(p, ".jsonl") && !seen[p] {
			paths = append(paths, p)
			seen[p] = true
		}
	}
	// Never publish descriptors observed across an exit or PID reuse. Already
	// validated journal paths remain available independently for reconciliation.
	if err := verifyTranscriptProcess(id); err != nil {
		return nil, err
	}
	return paths, nil
}

func verifyTranscriptProcess(id Identity) error {
	start, alive, known := probe.NewProcChecker().StartTime(id.PID)
	if !known {
		return fmt.Errorf("rollout owner process identity is unavailable")
	}
	if !alive || id.Starttime == "" || start != id.Starttime {
		return fmt.Errorf("rollout owner process exited or changed identity")
	}
	return nil
}
