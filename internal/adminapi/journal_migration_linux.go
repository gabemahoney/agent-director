//go:build linux

package adminapi

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// journalProcRoot is where the verifier lists processes; tests point it at
// real temporary files to exercise unreadable entries and inventories. It is the
// one seam for the listing: a test uses it, never a second one.
var journalProcRoot = "/proc"

// verifyJournalQuiescence on Linux uses /proc as its evidence basis: each
// same-user process's start time, executable and descriptor links.
// Only readable program names and store/root links identify mutators;
// incomplete evidence about one process adds no finding.
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
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		// Private store/root permissions below establish the same-user scope.
		// Privileged administrators are controlled by the maintained boundary.
		if stat.Uid != uint32(os.Geteuid()) {
			continue
		}
		start, alive, known := pc.StartTime(pid)
		if !known {
			start = ""
		}
		if known && !alive {
			continue
		}
		exe, err := os.Readlink(filepath.Join(base, "exe"))
		mutator := err == nil && strings.Contains(filepath.Base(exe), "agent-director")
		fds, err := os.ReadDir(filepath.Join(base, "fd"))
		if err != nil {
			fds = nil
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
			if err != nil {
				continue
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
		_, alive, known = pc.StartTime(pid)
		if known && !alive {
			continue
		}
		if mutator {
			blockers = append(blockers, JournalProcess{PID: pid, Starttime: start, Role: "agent_director_mutator", Action: "stop_and_restart_after_cutover"})
		}
	}
	if len(blockers) > 0 {
		return journalRefusal("old_or_unverified_mutators", blockers...)
	}
	return nil
}
