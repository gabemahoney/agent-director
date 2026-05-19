//go:build !linux && !darwin

package probe

import "context"

type unsupportedProber struct{}

func newProber() Prober { return unsupportedProber{} }

func (unsupportedProber) Probe(_ context.Context) (map[string]struct{}, error) {
	return nil, ErrProbeUnsupported
}
