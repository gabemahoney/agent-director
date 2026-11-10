package probe

import (
	"errors"
	"fmt"
	"syscall"
)

// UIDProcessReader selects the processes of one user (SR-2.15 "Processes":
// the kernel's own effective-uid selection, never a uid parsed from a pinned
// offset). The Darwin journal verifier is its only caller.
//
// Contract of PIDs(uid): the pids of every process the kernel selects for
// uid, in the kernel's order, or an error when the selection cannot be read
// whole. An empty selection is an error, because the caller's own process
// always has the caller's uid.
//
// The reader never reads a process environment or arguments and never reads
// the clock.
type UIDProcessReader interface {
	PIDs(uid int) ([]int, error)
}

// NewUIDProcessReader returns the per-OS uid selection reader, selected by
// build tags at compile time:
//
//   - darwin: darwinUIDProcessReader over the real KERN_PROC_UID fetch.
//   - other:  unsupportedUIDProcessReader, which errors for every uid. Linux
//     has no use for it: its journal verifier reads /proc.
func NewUIDProcessReader() UIDProcessReader {
	return newUIDProcessReader()
}

// uidFetchRetries bounds the retries of a KERN_PROC_UID fetch that fails with
// ENOMEM: the kernel sizes the buffer and then fills it, so a process that
// appears in between can outgrow it.
const uidFetchRetries = 3

// darwinUIDProcessReader is the darwin uid selection reader core. Its ONLY
// injected seam is the KERN_PROC_UID kinfo fetch (fetchKinfoUID, wired by
// newUIDProcessReader on darwin — see uidprocs_darwin.go), so the answer
// logic is build-tag-free and unit-testable off-darwin against synthetic
// kinfo_proc entries (the darwinStartTimeReader precedent). The uid is the
// kernel's filter argument; no uid is read from the returned bytes, only each
// entry's p_pid (and p_stat as a drift guard). It carries no KERN_PROCARGS2
// seam, so it structurally cannot read a process's arguments or environment.
type darwinUIDProcessReader struct {
	fetchKinfoUID func(uid int) ([]byte, error)
}

// PIDs fetches uid's selection, retrying up to uidFetchRetries times while
// the fetch fails with ENOMEM, and parses it with parseKinfoPIDs.
func (r darwinUIDProcessReader) PIDs(uid int) ([]int, error) {
	buf, err := r.fetchKinfoUID(uid)
	for retry := 0; retry < uidFetchRetries && errors.Is(err, syscall.ENOMEM); retry++ {
		buf, err = r.fetchKinfoUID(uid)
	}
	if err != nil {
		return nil, fmt.Errorf("read the process selection of uid %d: %w", uid, err)
	}
	pids, err := parseKinfoPIDs(buf)
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("the process selection of uid %d is empty", uid)
	}
	return pids, nil
}

// errUIDSelectionUnsupported is every answer of the unsupported reader.
var errUIDSelectionUnsupported = errors.New("no uid process selection on this OS")

// unsupportedUIDProcessReader is the uid selection reader on an OS without
// one: an error for every uid, never an empty selection. Build-tag-free so
// its contract is unit-testable on any OS; newUIDProcessReader returns it
// only off darwin (uidprocs_unsupported.go).
type unsupportedUIDProcessReader struct{}

// PIDs always errors.
func (unsupportedUIDProcessReader) PIDs(int) ([]int, error) {
	return nil, errUIDSelectionUnsupported
}
