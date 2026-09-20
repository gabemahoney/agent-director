package apitest

import (
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// Canonical per-OS proc_starttime fixture values, re-exported from the leaf
// procstarttimefix package so apitest callers have a single import surface.
// These are fixture VALUES in each OS's canonical proc_starttime form; the
// production formatting code that must agree with them lands in Epic t1.93m.is.
// See internal/testsupport/procstarttimefix for the authoritative definitions.
const (
	LinuxProcStarttime  = procstarttimefix.LinuxProcStarttime
	DarwinProcStarttime = procstarttimefix.DarwinProcStarttime
)

// spawnOpts accumulates the schema-v3 column overrides requested via
// SeedSpawn's variadic SpawnOption arguments. Each field is a pointer so an
// unset option is distinguishable from an explicit zero value: only fields
// with a non-nil pointer are written by the post-insert SQL UPDATE, and the
// nullable columns therefore stay NULL unless explicitly seeded.
type spawnOpts struct {
	pid                     *int
	procStarttime           *string
	jsonlPath               *string
	extraEnv                map[string]string
	livenessUnverifiedSince *string
	livenessNote            *string
}

// SpawnOption configures optional schema-v3 columns on a SeedSpawn row. Options
// are variadic and trailing, so every pre-existing 7-arg SeedSpawn call site
// compiles unchanged; supplying no options preserves the original defaults.
type SpawnOption func(*spawnOpts)

// WithPID seeds the pid column. A real pid is ≥1; pass a positive value.
func WithPID(pid int) SpawnOption {
	return func(o *spawnOpts) { o.pid = &pid }
}

// WithProcStarttime seeds the proc_starttime column. Use the canonical per-OS
// fixture constants (LinuxProcStarttime / DarwinProcStarttime) rather than
// inline literals.
func WithProcStarttime(procStarttime string) SpawnOption {
	return func(o *spawnOpts) { o.procStarttime = &procStarttime }
}

// WithJsonlPath seeds the jsonl_path column.
func WithJsonlPath(jsonlPath string) SpawnOption {
	return func(o *spawnOpts) { o.jsonlPath = &jsonlPath }
}

// WithExtraEnv seeds the extra_env column (stored as a JSON object). A nil or
// empty map is written as the canonical empty object "{}".
func WithExtraEnv(extraEnv map[string]string) SpawnOption {
	return func(o *spawnOpts) {
		if extraEnv == nil {
			extraEnv = map[string]string{}
		}
		o.extraEnv = extraEnv
	}
}

// WithLivenessUnverifiedSince seeds the liveness_unverified_since column.
func WithLivenessUnverifiedSince(ts string) SpawnOption {
	return func(o *spawnOpts) { o.livenessUnverifiedSince = &ts }
}

// WithLivenessNote seeds the liveness_note column.
func WithLivenessNote(note string) SpawnOption {
	return func(o *spawnOpts) { o.livenessNote = &note }
}
