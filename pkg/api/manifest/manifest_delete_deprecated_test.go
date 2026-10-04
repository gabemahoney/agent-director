package manifest_test

import (
	"strings"
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

// TestDeleteResultsFieldText pins delete's results text, manifest and
// surface.json: ok or an err_name per id, and a partial failure never aborts
// the batch (moved there from the Description, b.c4u).
func TestDeleteResultsFieldText(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	sources := resultFieldSources(t, surface, "delete", "results")
	for _, source := range []string{"manifest", "surface.json"} {
		f, ok := sources[source]
		if !ok {
			t.Errorf("%s: delete has no results field", source)
			continue
		}
		for _, tok := range []string{`"ok" on success`, "an err_name string on failure", "a partial failure never aborts the batch"} {
			if !strings.Contains(f.desc, tok) {
				t.Errorf("%s: delete results description does not contain %q; got %q", source, tok, f.desc)
			}
		}
	}
}
