//go:build !linux && !darwin

package probe

import "context"

type unsupportedProber struct{}

func newProber() Prober { return unsupportedProber{} }

func (unsupportedProber) Probe(_ context.Context) (map[string]struct{}, error) {
	return nil, ErrProbeUnsupported
}

type unsupportedChecker struct{}

func newChecker() LivenessChecker { return unsupportedChecker{} }

// CheckLiveness on an unsupported OS always returns VerdictUnknown. Unlike
// Prober (fail-CLOSED via ErrProbeUnsupported), the per-row liveness seam is
// fail-OPEN by construction — there is no error channel and unknown maps
// cleanly to "skip this row": find-missing leaves the row's state untouched
// rather than marking it missing on a platform it cannot evidence. This is the
// posture that maps cleanly to fail-open for the per-row model.
func (unsupportedChecker) CheckLiveness(_ int, _, _ string) LivenessVerdict {
	return VerdictUnknown
}

// newProcChecker on an unsupported OS returns the always-unreadable start-time
// reader (starttime.go): known false for every pid, never gone.
func newProcChecker() ProcChecker { return unsupportedProcChecker{} }
