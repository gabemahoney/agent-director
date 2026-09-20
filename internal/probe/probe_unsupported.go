//go:build !linux && !darwin

package probe

import "context"

type unsupportedProber struct{}

func newProber() Prober { return unsupportedProber{} }

func (unsupportedProber) Probe(_ context.Context) (map[string]struct{}, error) {
	return nil, ErrProbeUnsupported
}

type unsupportedResolver struct{}

func newResolver() Resolver { return unsupportedResolver{} }

// Resolve on an unsupported OS returns ErrProbeUnsupported. Resolve's callers
// are fail-open, so the hook maps this (like every other Resolve error) to a
// NULL process identity — it does NOT carry Prober's fail-closed meaning here.
func (unsupportedResolver) Resolve(_ string) (int, string, error) {
	return 0, "", ErrProbeUnsupported
}
