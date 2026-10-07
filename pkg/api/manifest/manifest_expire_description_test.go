package manifest_test

// manifest_expire_description_test.go pins expire's texts and SR-18.7's
// cleanup guidance on the manifest (TestSurfaceJSONMirrorsManifest carries
// them to surface.json); expire's result field shapes are TestFieldShapes'.

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestExpireDescription pins SR-18.9 and SR-18.7 on expire's Description,
// under agent-text rules.
func TestExpireDescription(t *testing.T) {
	apitest.AssertAgentTextCase(t, "expire description", siteText(t, "expire", "", ""), apitest.DescExpireManifest())
}

// TestExpireResultFieldTexts pins the count, ids, kept and kept_ids texts
// (SR-12.4, SR-18.11).
func TestExpireResultFieldTexts(t *testing.T) {
	for _, field := range []apitest.ExpireField{apitest.ExpireCount, apitest.ExpireIDs, apitest.ExpireKept, apitest.ExpireKeptIDs} {
		t.Run(string(field), func(t *testing.T) {
			apitest.AssertAgentTextCase(t, "expire result field "+string(field), siteText(t, "expire", "", string(field)),
				apitest.DescExpireField(field))
		})
	}
}

// TestCleanupGuidanceInDescriptions pins SR-18.7's cleanup guidance: in full
// at expire, as the short pointer at kill and find-missing.
func TestCleanupGuidanceInDescriptions(t *testing.T) {
	cases := []struct {
		verb string
		own  bool // find-missing: the description before the live-row pointer
		c    apitest.DescCase
	}{
		{"expire", false, apitest.DescCleanupGuidance()},
		{"kill", false, apitest.DescCleanupPointer()},
		{"find-missing", true, apitest.DescCleanupPointer()},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			desc := siteText(t, tc.verb, "", "")
			if tc.own {
				desc = apitest.FindMissingOwnText(desc)
			}
			apitest.AssertAgentTextCase(t, tc.verb+" description", desc, tc.c)
		})
	}
}
