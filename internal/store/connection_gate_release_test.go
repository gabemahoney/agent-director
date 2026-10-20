package store

import (
	"context"
	"errors"
	"testing"
)

// The operation gate is free by the time the caller is told the operation is
// done, so a nonwaiting operation started at once (a 1 ms admission) never
// loses to a release that has not happened yet.
func TestConnectionOperationReleasesGateBeforeReturning(t *testing.T) {
	s, _ := newRelayRow(t, "on", StateWorking)
	failure := errors.New("operation failed")
	for _, tc := range []struct {
		name string
		fn   func(*connectionOperation) error
		want error
	}{
		{"success", func(*connectionOperation) error { return nil }, nil},
		{"failure", func(*connectionOperation) error { return failure }, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Repeated: the gate is released by another goroutine, so one
			// iteration could pass by chance if the order regressed.
			for i := 0; i < 500; i++ {
				if err := s.runConnectionOperation(context.Background(), DefaultLockWait, tc.fn); !errors.Is(err, tc.want) {
					t.Fatalf("iteration %d: operation=%v; want %v", i, err, tc.want)
				}
				if held := len(s.cleanup.operationGate); held != 0 {
					t.Fatalf("iteration %d: the operation returned with %d gate slot(s) still held", i, held)
				}
				// A nonwaiting operation follows at once and is admitted.
				if err := s.runConnectionOperation(context.Background(), 0, tc.fn); !errors.Is(err, tc.want) || errors.Is(err, ErrStoreBusy) {
					t.Fatalf("iteration %d: nonwaiting operation after return=%v; want %v", i, err, tc.want)
				}
			}
		})
	}
}
