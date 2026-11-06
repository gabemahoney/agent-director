//go:build linux

package adminapi

import (
	"context"
	"debug/buildinfo"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// journalProcRoot is where the verifier lists processes; tests point it at
// real temporary files to reach its unreadable-inventory refusals. It is the
// one seam for the listing: a test uses it, never a second one.
var journalProcRoot = "/proc"

// verifyJournalQuiescence on Linux uses /proc as its evidence basis: each
// same-user process's start time, executable and descriptor links.
func verifyJournalQuiescence(ctx context.Context, dbPath string, roots []string) error {
	for _, path := range append([]string{dbPath}, roots...) {
		if err := verifyJournalPath(path); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(journalProcRoot)
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
		base := filepath.Join(journalProcRoot, entry.Name())
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
		// Read build metadata only.
		if bi, err := buildinfo.ReadFile(exePath); err == nil {
			if buildInfoNamesModule(bi) {
				mutator = true
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
