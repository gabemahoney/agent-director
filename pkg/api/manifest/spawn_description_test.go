package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// spawnDescriptions returns spawn's description from the manifest source of
// truth and from the committed surface.json, keyed by source.
func spawnDescriptions(t *testing.T) map[string]string {
	t.Helper()
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	descs := map[string]string{"manifest": v.Description}
	_, surface := readSurfaceJSON(t)
	for _, vv := range surface.Verbs {
		if vv.Name == "spawn" {
			descs["surface.json"] = vv.Description
		}
	}
	if _, ok := descs["surface.json"]; !ok {
		t.Fatal("surface.json has no spawn verb")
	}
	return descs
}

// TestSpawnDescriptionStatesLaunchRules pins SR-18.1's launch-timeout rule and
// SR-9.3's scan refusal in spawn's description, with no session-ending command.
func TestSpawnDescriptionStatesLaunchRules(t *testing.T) {
	descs := spawnDescriptions(t)
	for _, c := range []apitest.DescCase{apitest.DescSpawnLaunchTimeoutRule(), apitest.DescSpawnScanRefusal()} {
		t.Run(c.Name, func(t *testing.T) {
			for source, desc := range descs {
				apitest.AssertAgentTextCase(t, source+": spawn description", desc, c)
			}
		})
	}
}

// TestManifestTextsNameNoSessionEndingCommand runs the helper's forbidden-only
// check over every verb, param and result-field description (SR-1.4, SR-6.8).
func TestManifestTextsNameNoSessionEndingCommand(t *testing.T) {
	for _, v := range manifest.Verbs {
		apitest.AssertAgentText(t, v.Name+" description", v.Description)
		for _, p := range v.Params {
			apitest.AssertAgentText(t, v.Name+" param "+p.Name, p.Description)
		}
		for _, f := range v.ResultFields {
			apitest.AssertAgentText(t, v.Name+" result field "+f.Name, f.Description)
		}
	}
}
