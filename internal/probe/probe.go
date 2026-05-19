// Package probe discovers live Claude processes by their
// CLAUDE_DIRECTOR_INSTANCE_ID env var. The set returned by Probe is
// the ground truth find-missing diffs against the DB's live-state
// rows (SRD §4.4).
//
// Build-tag dispatch picks a per-OS implementation:
//
//   - Linux:   walk /proc/<pid>/environ entries (probe_linux.go).
//   - macOS:   sysctl(KERN_PROC, KERN_PROC_ALL) + sysctl(KERN_PROCARGS2)
//              per PID (probe_darwin.go).
//   - Other:   the fallback returns an explicit ErrProbeUnsupported so
//              find-missing fails closed rather than silently
//              under-reporting (probe_unsupported.go).
//
// Cron user invariant (SRD §14.6): the prober only sees processes the
// invoking user has permission to read. Running find-missing as a
// different user from the one that launched the Spawns produces an
// empty (or partial) probe set; the degraded-mode guard in
// internal/api.FindMissing catches the worst case (0 readable IDs +
// ≥1 live row).
package probe

import (
	"context"
	"errors"
)

// ErrProbeUnsupported is returned by the platform fallback when no
// per-OS implementation is registered. find-missing surfaces this as
// a hard failure rather than treating it as "no processes alive".
var ErrProbeUnsupported = errors.New("ErrProbeUnsupported")

// Prober is the narrow interface internal/api.FindMissing depends on.
// Implementations return the set of CLAUDE_DIRECTOR_INSTANCE_ID
// values currently observable in process env blocks.
//
// The set form (map[string]struct{}) is the diff-friendly shape — a
// caller asks `_, ok := set[id]` and doesn't care about iteration
// order or duplicates.
type Prober interface {
	Probe(ctx context.Context) (map[string]struct{}, error)
}

// EnvKey is the env-var name that identifies a tracked Spawn. Held
// as a constant here (rather than re-typing the string in the linux
// + darwin walkers) so a future rename has one point of truth.
const EnvKey = "CLAUDE_DIRECTOR_INSTANCE_ID"

// New returns the per-OS Prober. The implementation is selected by
// build tags at compile time; the factory exists so callers can swap
// a fake in tests without touching the production path.
func New() Prober {
	return newProber()
}
