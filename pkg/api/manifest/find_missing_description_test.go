package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// findMissingDescriptions returns find-missing's Description keyed by source,
// failing the test unless both the manifest and surface.json carry it.
func findMissingDescriptions(t *testing.T) map[string]string {
	t.Helper()
	_, surface := readSurfaceJSON(t)
	descs := verbDescriptions(t, surface, "find-missing")
	if len(descs) != 2 {
		t.Fatalf("find-missing description found in %d of manifest and surface.json", len(descs))
	}
	return descs
}

// TestFindMissingDescriptionStatesPendingGrace pins SR-18.9's grace statement
// before find-missing's live-row pointer in both surfaces, under agent-text rules.
func TestFindMissingDescriptionStatesPendingGrace(t *testing.T) {
	for source, desc := range findMissingDescriptions(t) {
		apitest.AssertAgentTextCase(t, source+": find-missing description before the live-row pointer",
			apitest.FindMissingOwnText(desc), apitest.DescFindMissingGrace())
		apitest.AssertAgentText(t, source+": find-missing description", desc)
	}
}

// TestFindMissingDescriptionStatesLivenessAndEnvironment pins SR-18.9, SR-18.7
// and SR-3.8 (process-only liveness, same user and tmux environment) in both surfaces.
func TestFindMissingDescriptionStatesLivenessAndEnvironment(t *testing.T) {
	for source, desc := range findMissingDescriptions(t) {
		apitest.AssertAgentTextCase(t, source+": find-missing description before the live-row pointer",
			apitest.FindMissingOwnText(desc), apitest.DescFindMissingManifest())
	}
}

// TestFindMissingIDListFields pins the ids and unverified_ids result fields
// (SR-11.7, SR-18.3, SR-18.11) on the manifest and in surface.json.
func TestFindMissingIDListFields(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, field := range []apitest.FindMissingField{apitest.FindMissingIDs, apitest.FindMissingUnverifiedIDs} {
		t.Run(string(field), func(t *testing.T) {
			sources := resultFieldSources(t, surface, "find-missing", string(field))
			for _, source := range []string{"manifest", "surface.json"} {
				f, ok := sources[source]
				if !ok {
					t.Errorf("%s: find-missing has no %q result field", source, field)
					continue
				}
				apitest.AssertAgentTextCase(t, source+": find-missing result field "+string(field), f.desc,
					apitest.DescFindMissingField(field))
			}
		})
	}
}

// TestFindMissingResultFieldsAgentText runs the forbidden-forms check over
// every find-missing result field description in both surfaces.
func TestFindMissingResultFieldsAgentText(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	v, ok := manifest.Lookup("find-missing")
	if !ok {
		t.Fatal("find-missing not in manifest")
	}
	for _, mf := range v.ResultFields {
		sources := resultFieldSources(t, surface, "find-missing", mf.Name)
		if len(sources) != 2 {
			t.Errorf("find-missing result field %q found in %d of manifest and surface.json", mf.Name, len(sources))
		}
		for source, f := range sources {
			apitest.AssertAgentText(t, source+": find-missing result field "+mf.Name, f.desc)
		}
	}
}
