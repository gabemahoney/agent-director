//go:build darwin && arm64

package adminapi

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// The verifier's two helpers, at their fixed system paths, never found
// through PATH. runFixedHelper gives each only LC_ALL=C as its environment.
const (
	journalLsofPath = "/usr/sbin/lsof"
	journalLsPath   = "/bin/ls"
)

// journalLsofArgs is the one bulk listing of uid's processes:
//   - -n, -P: no host or port lookups;
//   - +w: warnings forced on, so a mount or descriptor lsof cannot read
//     always reaches stderr and refuses;
//   - -u: the verifier's effective uid;
//   - -F0pftDin: the fields the core parses, and no command or user field.
func journalLsofArgs(uid int) []string {
	return []string{"-nP", "+w", "-u", strconv.Itoa(uid), "-F0pftDin"}
}

// journalLsArgs reads one path's access-control entries: a long listing (-l)
// with its entries (-e) of the path itself, not a directory's contents (-d).
func journalLsArgs(path string) []string {
	return []string{"-led", path}
}

// verifyJournalQuiescence on Darwin/arm64 uses SR-2.15's evidence basis: the
// kernel's kern.proc.uid selection, one lsof listing and /bin/ls -led, judged
// by darwinVerifier. This file only binds the real seams.
func verifyJournalQuiescence(ctx context.Context, dbPath string, roots []string) error {
	v := darwinVerifier{
		selectUID: probe.NewUIDProcessReader().PIDs,
		procs:     probe.NewProcChecker(),
		ppids:     probe.NewParentPIDReader(),
		listFiles: func(ctx context.Context, uid int, started func(pid int)) ([]byte, []byte, error) {
			return runFixedHelper(ctx, journalLsofPath, journalLsofArgs(uid), started)
		},
		readACL: func(ctx context.Context, path string, started func(pid int)) ([]byte, []byte, error) {
			return runFixedHelper(ctx, journalLsPath, journalLsArgs(path), started)
		},
		fs:      osJournalFS{},
		now:     time.Now,
		selfPID: os.Getpid(),
		euid:    os.Geteuid(),
	}
	return v.verify(ctx, dbPath, roots)
}
