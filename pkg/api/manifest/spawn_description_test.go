package manifest_test

import (
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// spawnTexts returns spawn's description (param "") or the named param's
// description from the manifest source of truth and from the committed
// surface.json, keyed by source.
func spawnTexts(t *testing.T, param string) map[string]string {
	t.Helper()
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	texts := map[string]string{}
	if param == "" {
		texts["manifest"] = v.Description
	}
	for _, p := range v.Params {
		if param != "" && p.Name == param {
			texts["manifest"] = p.Description
		}
	}
	_, surface := readSurfaceJSON(t)
	for _, vv := range surface.Verbs {
		if vv.Name != "spawn" {
			continue
		}
		if param == "" {
			texts["surface.json"] = vv.Description
		}
		for _, p := range vv.Params {
			if param != "" && p.Name == param {
				texts["surface.json"] = p.Description
			}
		}
	}
	for _, source := range []string{"manifest", "surface.json"} {
		if _, ok := texts[source]; !ok {
			t.Fatalf("%s has no spawn text for param %q", source, param)
		}
	}
	return texts
}

// TestSpawnDescriptionStatesLaunchRules pins SR-18.1's launch-timeout rule,
// SR-9.3's scan refusal and SR-9.4's held-name contract in spawn's texts.
func TestSpawnDescriptionStatesLaunchRules(t *testing.T) {
	for _, tc := range []struct {
		param string
		c     apitest.DescCase
	}{
		{"", apitest.DescSpawnLaunchTimeoutRule()},
		{"", apitest.DescSpawnScanRefusal()},
		{"", apitest.DescSpawnHeldName()},
		{"tmux-session-name", apitest.DescSpawnSessionNameParam()},
	} {
		t.Run(tc.c.Name, func(t *testing.T) {
			for source, text := range spawnTexts(t, tc.param) {
				apitest.AssertAgentTextCase(t, source+": spawn text "+strconv.Quote(tc.param), text, tc.c)
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
