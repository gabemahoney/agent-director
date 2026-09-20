//go:build darwin

package probe

import (
	"bytes"
	"os"

	"golang.org/x/sys/unix"
)

// darwinResolver walks the parent chain over a single kern.proc.all snapshot
// (PM decision Q3: ONE snapshot per walk, the existing SR-6.4 mechanism). It
// holds no byte-parsing itself: it wires the real sysctl snapshot + the real
// KERN_PROCARGS2 env reader into the build-tag-free darwinSnapshotReader, which
// drives the shared walkAncestors core. This follows the parse_kinfo.go
// discipline — syscalls here, byte parsing in the tag-free core — so the darwin
// walk logic is unit-testable off-darwin against synthetic snapshot bytes.
//
// Epic hp (SR-7.2) reuses the factored per-entry kinfo reads for its liveness
// checker.
type darwinResolver struct{}

func newResolver() Resolver { return darwinResolver{} }

func (darwinResolver) Resolve(id string) (int, string, error) {
	snapshot, err := listKinfoSnapshot()
	if err != nil {
		return 0, "", err
	}
	reader := &darwinSnapshotReader{
		snapshot: snapshot,
		self:     os.Getpid(),
		envID:    darwinEnvID,
	}
	return walkAncestors(reader, id)
}

// listKinfoSnapshot returns the raw kern.proc.all blob (the packed kinfo_proc
// array) via the same sysctl surface as the Prober's listPIDs. Unlike listPIDs
// it returns the RAW bytes (no parsePIDsFromSysctlBuf plausibility pass): the
// per-entry parsers apply the fail-open drift guards during the walk.
func listKinfoSnapshot() ([]byte, error) {
	return unix.SysctlRaw("kern.proc.all")
}

// darwinEnvID reads a single pid's KERN_PROCARGS2 blob and extracts its EnvKey
// value. A permission-denied / process-gone read is a routine non-match
// (\"\", false, nil) so the walk continues past a foreign-uid or vanished
// ancestor.
func darwinEnvID(pid int) (string, bool, error) {
	blob, err := procArgs(pid)
	if err != nil {
		return "", false, nil
	}
	env, ok := envFromProcArgs2(blob)
	if !ok {
		return "", false, nil
	}
	keyPrefix := []byte(EnvKey + "=")
	for _, kv := range bytes.Split(env, []byte{0}) {
		if !bytes.HasPrefix(kv, keyPrefix) {
			continue
		}
		return string(kv[len(keyPrefix):]), true, nil
	}
	return "", false, nil
}
