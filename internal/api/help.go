package api

import "github.com/gabemahoney/agent-director/internal/api/manifest"

// Help returns the manifest-derived list of verbs as a slice of VerbSummary.
//
// The error return is always nil — manifest.Verbs is compile-time data with
// no I/O. The signature keeps an error in the return so future verbs that
// share Help's call shape have a single uniform pattern, and so callers can
// treat all api verbs identically.
func Help() ([]VerbSummary, error) {
	out := make([]VerbSummary, 0, len(manifest.Verbs))
	for _, v := range manifest.Verbs {
		out = append(out, VerbSummary{
			Name:        v.Name,
			Description: v.Description,
		})
	}
	return out, nil
}
