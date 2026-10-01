package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDeleteDescriptionDeprecated pins SR-18.8's notice (AC-DOC-07) on
// delete's Description, manifest and surface.json, under agent-text rules.
func TestDeleteDescriptionDeprecated(t *testing.T) {
	for source, desc := range verbDescriptionsBoth(t, "delete") {
		apitest.AssertAgentTextCase(t, source+": delete description", desc, apitest.DescDeleteDeprecated())
	}
}
