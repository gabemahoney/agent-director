// Package procstarttimefix holds canonical per-OS proc_starttime fixture
// values for schema-v3 liveness/identity tests.
//
// This is a LEAF test-support package: it imports NOTHING from internal/store
// (or any other agent-director package) so it can be pulled into any test
// without dragging in the store graph. The values live here exactly once as
// named constants; tests (and pkg/api/apitest, which re-exports them) reference
// the constants rather than duplicating starttime literals.
//
// Cross-reference (per SR-6.3 / SR-6.4): the strings below are fixture VALUES
// in the canonical proc_starttime form for each OS. The PRODUCTION formatting
// code that must agree with these values — i.e. that produces a proc_starttime
// string from a live process — lands in Epic t1.93m.is. When that code is
// written, its output for the fixture process must be byte-identical to these
// constants; keep the two in sync.
package procstarttimefix

const (
	// LinuxProcStarttime is the canonical Linux proc_starttime fixture value:
	// the process start time expressed in decimal clock-ticks since boot
	// (field 22 of /proc/<pid>/stat, per SR-6.3). It is an opaque decimal
	// string — no unit conversion is applied at the storage/fixture layer.
	LinuxProcStarttime = "12345678"

	// DarwinProcStarttime is the canonical macOS proc_starttime fixture value:
	// the process start time in "sec.usec" form (whole seconds, a dot, then
	// microseconds) as derived from kinfo_proc's start time (per SR-6.4).
	DarwinProcStarttime = "1700000000.123456"
)
