//go:build linux

package probe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
)

// linuxProber walks /proc/<pid>/environ for every numeric PID dir.
// `environ` is the NUL-separated KEY=VAL block, kernel-default
// readable only by the file's owner. Unreadable (foreign-uid)
// processes are skipped silently — the degraded-mode guard upstream
// catches the worst case of "I can't read anything but the DB has
// live rows".
type linuxProber struct{}

func newProber() Prober { return linuxProber{} }

func (linuxProber) Probe(ctx context.Context) (map[string]struct{}, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("probe: read /proc: %w", err)
	}

	out := make(map[string]struct{})
	keyPrefix := []byte(EnvKey + "=")

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// /proc has many non-PID entries (uptime, meminfo, …). The
		// PID dirs are exactly the ones whose name parses as an int.
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			// Permission denied / process gone between ReadDir and
			// the file read are both routine. Skip; don't fail the
			// whole probe.
			continue
		}
		for _, kv := range bytes.Split(data, []byte{0}) {
			if !bytes.HasPrefix(kv, keyPrefix) {
				continue
			}
			val := string(kv[len(keyPrefix):])
			if val != "" {
				out[val] = struct{}{}
			}
		}
	}
	return out, nil
}
