//go:build darwin

package probe

import (
	"bytes"
	"context"
	"encoding/binary"
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
// CLAUDE_DIRECTOR_INSTANCE_ID=... entries.
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
func listPIDs() ([]int, error) {
	buf, err := unix.SysctlRaw("kern.proc.all")
	if err != nil {
		return nil, err
	}
	const kinfoProcSize = 648 // sizeof(struct kinfo_proc) on macOS
	if len(buf) < kinfoProcSize {
		return nil, nil
	}
	n := len(buf) / kinfoProcSize
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		// kp_proc.p_pid lives at offset 40 in struct kinfo_proc
		// (extern_proc.p_pid offset; verified against XNU 11.x).
		pidOff := i*kinfoProcSize + 40
		if pidOff+4 > len(buf) {
			break
		}
		pid := int(binary.LittleEndian.Uint32(buf[pidOff : pidOff+4]))
		if pid > 0 {
			out = append(out, pid)
		}
	}
	return out, nil
}

// procArgs reads KERN_PROCARGS2 for a single PID. Permission-denied
// (EPERM) and process-gone (ESRCH) are signaled as errors and the
// caller skips that PID.
func procArgs(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}

// envFromProcArgs2 parses the KERN_PROCARGS2 blob and returns just the
// env section (NUL-separated KEY=VAL entries). Returns (nil, false)
// on a too-short blob.
func envFromProcArgs2(blob []byte) ([]byte, bool) {
	if len(blob) < 4 {
		return nil, false
	}
	argc := int(binary.LittleEndian.Uint32(blob[:4]))
	if argc < 0 {
		return nil, false
	}

	// Skip past the 4-byte argc, the exec_path (null-terminated), any
	// padding (the exec_path is followed by enough NULs to align to
	// argv start, but in practice the parser just hops to the next
	// non-NUL byte), and argc argv entries.
	i := 4

	// Walk the exec_path until the first NUL. Then skip any additional
	// NULs (alignment padding before argv[0]).
	for i < len(blob) && blob[i] != 0 {
		i++
	}
	for i < len(blob) && blob[i] == 0 {
		i++
	}

	// Skip argc argv strings.
	for j := 0; j < argc && i < len(blob); j++ {
		for i < len(blob) && blob[i] != 0 {
			i++
		}
		// consume the NUL
		if i < len(blob) {
			i++
		}
	}
	if i >= len(blob) {
		return nil, false
	}
	return blob[i:], true
}
