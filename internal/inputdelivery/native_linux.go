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
	term, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return 0, err
	}
	if term.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		return 0, fmt.Errorf("terminal is not raw")
	}
	fg, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return 0, err
	}
	pg, err := unix.Getpgid(id.PID)
	if err != nil {
		return 0, err
	}
	if fg != pg {
		return 0, fmt.Errorf("agent is not the terminal foreground process group")
	}
	return unix.IoctlGetInt(int(f.Fd()), unix.TIOCINQ)
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
