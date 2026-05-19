package probe

import (
	"encoding/binary"
	"errors"
	"testing"
)

// TestParsePIDsFromSysctlBufRejectsGarbage feeds a deliberately
// corrupt sysctl(kern.proc.all) blob through the parser and confirms
// it returns ErrProbeUnsupported rather than emitting a garbage PID
// set that would silently poison find-missing.
//
// The blob is sized as an exact multiple of kinfoProcSize and filled
// with 0xFF so every "PID slot" decodes to 0xFFFFFFFF (4_294_967_295)
// — well above maxPlausiblePID. With >10% of slots failing the
// plausibility check the parser must surface ErrProbeUnsupported.
//
// The test is build-tag-free so the Linux CI lane (and any future OS)
// exercises the regression net even when the per-OS prober itself is
// not compiled in.
func TestParsePIDsFromSysctlBufRejectsGarbage(t *testing.T) {
	const slots = 20
	buf := make([]byte, slots*kinfoProcSize)
	for i := range buf {
		buf[i] = 0xFF
	}

	got, err := parsePIDsFromSysctlBuf(buf)
	if !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("err = %v; want ErrProbeUnsupported wrapped chain", err)
	}
	if got != nil {
		t.Errorf("got = %v; want nil PID slice on hard failure", got)
	}
}

// TestParsePIDsFromSysctlBufAcceptsPlausibleSet pins the happy path:
// a buffer where every slot's PID offset carries a small positive
// int32 parses without error and returns the parsed PIDs verbatim.
// This is the "constants still match real XNU" signal.
func TestParsePIDsFromSysctlBufAcceptsPlausibleSet(t *testing.T) {
	const slots = 5
	buf := make([]byte, slots*kinfoProcSize)
	want := []int{101, 202, 303, 404, 505}
	for i, pid := range want {
		off := i*kinfoProcSize + kinfoProcPIDOffset
		binary.LittleEndian.PutUint32(buf[off:off+4], uint32(pid))
	}

	got, err := parsePIDsFromSysctlBuf(buf)
	if err != nil {
		t.Fatalf("parsePIDsFromSysctlBuf: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d; want %d (parsed=%v)", len(got), len(want), got)
	}
	for i, p := range got {
		if p != want[i] {
			t.Errorf("got[%d] = %d; want %d", i, p, want[i])
		}
	}
}

// TestParsePIDsFromSysctlBufTolerates10PercentBogus pins the
// threshold: up to (but not exceeding) 10% bogus PIDs is treated as
// transient noise — a process can be in a bad state mid-sample. Only
// >10% trips the structural-drift guard.
func TestParsePIDsFromSysctlBufTolerates10PercentBogus(t *testing.T) {
	// 20 slots, 2 bogus (10%) → must NOT trip ErrProbeUnsupported.
	const slots = 20
	buf := make([]byte, slots*kinfoProcSize)
	// Plausible PIDs in 18 slots, bogus 0xFFFFFFFF in 2 slots.
	for i := 0; i < slots; i++ {
		off := i*kinfoProcSize + kinfoProcPIDOffset
		if i < 2 {
			binary.LittleEndian.PutUint32(buf[off:off+4], 0xFFFFFFFF)
		} else {
			binary.LittleEndian.PutUint32(buf[off:off+4], uint32(1000+i))
		}
	}

	got, err := parsePIDsFromSysctlBuf(buf)
	if err != nil {
		t.Fatalf("10%% bogus tripped guard prematurely: %v", err)
	}
	// The 2 bogus slots are skipped; 18 plausible PIDs remain.
	if len(got) != 18 {
		t.Errorf("len(got) = %d; want 18 (bogus PIDs should be dropped, not error)", len(got))
	}
}

// TestParsePIDsFromSysctlBufShortBufferReturnsNil pins the "too small
// for one entry" path: returns (nil, nil) rather than erroring. A
// sysctl that returned a short buffer is a legitimate "no live
// processes" answer in practice (the prober's caller treats nil as
// empty set).
func TestParsePIDsFromSysctlBufShortBufferReturnsNil(t *testing.T) {
	got, err := parsePIDsFromSysctlBuf(make([]byte, kinfoProcSize-1))
	if err != nil {
		t.Fatalf("short buffer should not error; got %v", err)
	}
	if got != nil {
		t.Errorf("got = %v; want nil for short buffer", got)
	}
}
