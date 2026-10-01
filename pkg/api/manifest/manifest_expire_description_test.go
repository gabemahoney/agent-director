package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// verbDescriptionsBoth returns verb's Description keyed by source, failing the
// test unless both the manifest and surface.json carry it.
func verbDescriptionsBoth(t *testing.T, verb string) map[string]string {
	t.Helper()
	_, surface := readSurfaceJSON(t)
	descs := verbDescriptions(t, surface, verb)
	if len(descs) != 2 {
		t.Fatalf("%s description found in %d of manifest and surface.json", verb, len(descs))
	}
	return descs
}

// TestExpireDescription pins SR-18.9 and SR-18.7 on expire's Description in
// both surfaces, under agent-text rules.
func TestExpireDescription(t *testing.T) {
	for source, desc := range verbDescriptionsBoth(t, "expire") {
		apitest.AssertAgentTextCase(t, source+": expire description", desc, apitest.DescExpireManifest())
	}
}

// TestExpireResultFieldTexts pins the count, ids, kept and kept_ids texts
// (SR-12.4, SR-18.11) on the manifest and in surface.json.
func TestExpireResultFieldTexts(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, field := range []apitest.ExpireField{apitest.ExpireCount, apitest.ExpireIDs, apitest.ExpireKept, apitest.ExpireKeptIDs} {
		t.Run(string(field), func(t *testing.T) {
			sources := resultFieldSources(t, surface, "expire", string(field))
			for _, source := range []string{"manifest", "surface.json"} {
				f, ok := sources[source]
				if !ok {
					t.Errorf("%s: expire has no %q result field", source, field)
					continue
				}
				apitest.AssertAgentTextCase(t, source+": expire result field "+string(field), f.desc,
					apitest.DescExpireField(field))
			}
		})
	}
}

// TestExpireParamAndResultTextsAgentText runs the forbidden-forms check over
// every expire parameter and result field description in both surfaces.
func TestExpireParamAndResultTextsAgentText(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	v, ok := manifest.Lookup("expire")
	if !ok {
		t.Fatal("expire not in manifest")
	}
	params := map[string]map[string]string{}
	for _, p := range v.Params {
		params[p.Name] = map[string]string{"manifest": p.Description}
	}
	for _, sv := range surface.Verbs {
		if sv.Name != "expire" {
			continue
		}
		for _, p := range sv.Params {
			if params[p.Name] == nil {
				params[p.Name] = map[string]string{}
			}
			params[p.Name]["surface.json"] = p.Description
		}
	}
	for name, sources := range params {
		if len(sources) != 2 {
			t.Errorf("expire param %q found in %d of manifest and surface.json", name, len(sources))
		}
		for source, desc := range sources {
			apitest.AssertAgentText(t, source+": expire param "+name, desc)
		}
	}
	for _, mf := range v.ResultFields {
		sources := resultFieldSources(t, surface, "expire", mf.Name)
		if len(sources) != 2 {
			t.Errorf("expire result field %q found in %d of manifest and surface.json", mf.Name, len(sources))
		}
		for source, f := range sources {
			apitest.AssertAgentText(t, source+": expire result field "+mf.Name, f.desc)
		}
	}
}

// TestCleanupGuidanceInDescriptions pins SR-18.7's cleanup guidance in both
// surfaces: in full at expire, as the short pointer at kill and find-missing.
func TestCleanupGuidanceInDescriptions(t *testing.T) {
	whole := func(desc string) string { return desc }
	cases := []struct {
		verb string
		text func(string) string
		c    apitest.DescCase
	}{
		{"expire", whole, apitest.DescCleanupGuidance()},
		{"kill", whole, apitest.DescCleanupPointer()},
		{"find-missing", apitest.FindMissingOwnText, apitest.DescCleanupPointer()},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			for source, desc := range verbDescriptionsBoth(t, tc.verb) {
				apitest.AssertAgentTextCase(t, source+": "+tc.verb+" description", tc.text(desc), tc.c)
			}
		})
	}
}
