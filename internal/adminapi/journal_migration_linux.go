//go:build linux

package adminapi

import (
	"context"
	"debug/buildinfo"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gabemahoney/agent-director/internal/probe"
)

func verifyJournalQuiescence(ctx context.Context, dbPath string, roots []string) error {
	for _, path := range append([]string{dbPath}, roots...) {
		if err := verifyJournalPath(path); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return journalRefusal("process_inventory_unreadable")
	}
	pc := probe.NewProcChecker()
	var blockers []JournalProcess
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return journalRefusal("inventory_deadline")
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		base := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return journalRefusal("process_identity_unreadable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return journalRefusal("process_identity_unreadable")
		}
		// Private store/root permissions below establish the same-user scope.
		// Privileged administrators are controlled by the maintained boundary.
		if stat.Uid != uint32(os.Geteuid()) {
			continue
		}
		start, alive, known := pc.StartTime(pid)
		p := JournalProcess{PID: pid, Starttime: start, Role: "unverified_process", Action: "stop_or_make_inventory_readable"}
		if !known {
			blockers = append(blockers, p)
			continue
		}
		if !alive {
			continue
		}
		exePath := filepath.Join(base, "exe")
		exe, err := os.Readlink(exePath)
		if err != nil {
			_, alive, known = pc.StartTime(pid)
			if known && !alive {
				continue
			}
			blockers = append(blockers, p)
			continue
		}
		mutator := strings.Contains(filepath.Base(exe), "agent-director")
		// Detect renamed binaries and Go callers embedding this module, even
		// while their SQLite lease is closed. Read build metadata only.
		if bi, err := buildinfo.ReadFile(exePath); err == nil {
			if strings.Contains(bi.Path, "github.com/gabemahoney/agent-director") || bi.Main.Path == "github.com/gabemahoney/agent-director" {
				mutator = true
			}
			for _, dep := range bi.Deps {
				if dep.Path == "github.com/gabemahoney/agent-director" {
					mutator = true
				}
			}
		} else if f, err := os.Open(exePath); err != nil {
			blockers = append(blockers, p)
			continue
		} else {
			f.Close()
		}
		fds, err := os.ReadDir(filepath.Join(base, "fd"))
		if err != nil {
			_, alive, known = pc.StartTime(pid)
			if known && !alive {
				continue
			}
			blockers = append(blockers, p)
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				blockers = append(blockers, p)
				mutator = false
				break
			}
			target = strings.TrimSuffix(target, " (deleted)")
			if target == dbPath || target == dbPath+"-wal" || target == dbPath+"-shm" {
				mutator = true
			}
			for _, root := range roots {
				if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
					mutator = true
				}
			}
		}
		after, alive, known := pc.StartTime(pid)
		if !known || alive && after != start {
			blockers = append(blockers, p)
			continue
		}
		if alive && mutator {
			p.Role, p.Action = "agent_director_mutator", "stop_and_restart_after_cutover"
			blockers = append(blockers, p)
		}
	}
	if len(blockers) > 0 {
		return journalRefusal("old_or_unverified_mutators", blockers...)
	}
	return nil
}

// Verify every existing path component without following a link. An absent
// root is known absent only after its existing parent was readable. Private
// roots establish which unprivileged user can hold operational authority.
func verifyJournalPath(path string) error {
	if !filepath.IsAbs(path) {
		return journalRefusal("legacy_root_not_absolute")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return journalRefusal("legacy_root_unreadable")
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return journalRefusal("legacy_root_symlink")
			}
			if current == path {
				st, ok := info.Sys().(*syscall.Stat_t)
				if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
					return journalRefusal("legacy_root_access_unestablished")
				}
			}
			if info.IsDir() {
				f, err := os.Open(current)
				if err != nil {
					return journalRefusal("legacy_root_unreadable")
				}
				_, err = f.Readdirnames(1)
				f.Close()
				if err != nil && !errors.Is(err, io.EOF) {
					return journalRefusal("legacy_root_unreadable")
				}
			}
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}
