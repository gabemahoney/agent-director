package store

import (
	"testing"
	"time"
)

// BeforeCursorTrailExecForTest reuses the transaction observation seam.
func BeforeCursorTrailExecForTest(t *testing.T, s *Store, path string, observe func(string)) {
	beforeEachExec(t, s, path, observe)
}

// RotateCursorSessionBeforeAcquireForTest exposes the per-call boundary on the real rotation path.
// Future rotation acquisition-boundary tests MUST reuse this bridge.
func RotateCursorSessionBeforeAcquireForTest(s *Store, examined Spawn, gate HookGate, next, path string, until time.Time, check CursorCheck, beforeAcquire func()) (bool, error) {
	return s.rotateCursorSession(examined, gate, next, path, until, check, beforeAcquire)
}
