package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestFindMissingDescriptionStatesPendingGrace pins SR-18.9's grace statement
// before find-missing's live-row sequence in both surfaces, under agent-text rules.
func TestFindMissingDescriptionStatesPendingGrace(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	descs := verbDescriptions(t, surface, "find-missing")
	if len(descs) != 2 {
		t.Fatalf("find-missing description found in %d of manifest and surface.json", len(descs))
	}
	for source, desc := range descs {
		apitest.AssertAgentTextCase(t, source+": find-missing description before the live-row sequence",
			apitest.FindMissingOwnText(desc), apitest.DescFindMissingGrace())
		apitest.AssertAgentText(t, source+": find-missing description", desc)
	}
}
