//go:build darwin

package probe

import (
	"bytes"
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// darwinProber uses sysctl to enumerate live PIDs, then sysctl(KERN_PROCARGS2)
// to read each process's argv + env blob. The KERN_PROCARGS2 format
// is (per the XNU sources):
//
//   uint32  argc                            // 4 bytes, native byte order
//   string  exec_path '\0'                  // null-terminated
//   <pad>                                   // pad to word alignment
//   string  argv[0..argc-1], each '\0'-terminated
//   string  envp[0..], each '\0'-terminated, list ends at empty string
//
// We skip past argc + exec + argv to reach envp, then scan for
// AGENT_DIRECTOR_INSTANCE_ID=... entries.
type darwinProber struct{}

func newProber() Prober { return darwinProber{} }

func (darwinProber) Probe(ctx context.Context) (map[string]struct{}, error) {
	pids, err := listPIDs()
	if err != nil {
		return nil, fmt.Errorf("probe: list pids: %w", err)
	}

	out := make(map[string]struct{})
	keyPrefix := []byte(EnvKey + "=")

	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blob, err := procArgs(pid)
		if err != nil {
			// permission denied / process exited between listing
			// and reading — both routine. Skip.
			continue
		}
		if env, ok := envFromProcArgs2(blob); ok {
			for _, kv := range bytes.Split(env, []byte{0}) {
				if !bytes.HasPrefix(kv, keyPrefix) {
					continue
				}
				val := string(kv[len(keyPrefix):])
				if val != "" {
					out[val] = struct{}{}
				}
			}
		}
	}
	return out, nil
}

// listPIDs uses sysctl(kern.proc.all) to retrieve the full
// kinfo_proc array. The dotted-name form maps to MIB
// (CTL_KERN, KERN_PROC, KERN_PROC_ALL) which is the stable XNU
// surface; SysctlRaw under the hood double-calls (once to get the
// required size, once with a sized buffer).
//
// The byte-level parsing + plausibility sanity check lives in
// parse_darwin.go (build-tag-free) so the regression-test surface is
// callable from any platform. ErrProbeUnsupported flows back to
// find-missing on a struct-layout drift.
func listPIDs() ([]int, error) {
	buf, err := unix.SysctlRaw("kern.proc.all")
	if err != nil {
		return nil, err
	}
	return parsePIDsFromSysctlBuf(buf)
}

// procArgs reads KERN_PROCARGS2 for a single PID. Permission-denied
// (EPERM) and process-gone (ESRCH) are signaled as errors and the
// caller skips that PID.
func procArgs(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}

