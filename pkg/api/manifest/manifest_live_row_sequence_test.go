package manifest_test

import (
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// liveRowWant is what one verb's description carries of SR-18.6's live-row
// sequence: statements of it, pointers to it, and the case it passes (nil: none).
type liveRowWant struct {
	sequences, pointers int
	desc                func() apitest.DescCase
}

// liveRowWants lists the verbs that carry the sequence or its pointer
// (decision-0930b Q6); every other verb carries neither.
var liveRowWants = map[string]liveRowWant{
	"kill":         {sequences: 1, desc: apitest.DescLiveRowSequence},
	"find-missing": {pointers: 1, desc: apitest.DescLiveRowPointer},
	"spawn":        {pointers: 1, desc: apitest.DescLiveRowPointer},
}

// TestLiveRowSequencePerVerb pins SR-18.6 (AC-DOC-05) on every verb in both surfaces:
// only kill states the short form, and find-missing and spawn only point to it.
func TestLiveRowSequencePerVerb(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	seen := map[string]bool{}
	for _, v := range manifest.Verbs {
		seen[v.Name] = true
	}
	for _, v := range surface.Verbs {
		seen[v.Name] = true
	}
	for verb := range liveRowWants {
		seen[verb] = true
	}
	verbs := make([]string, 0, len(seen))
	for verb := range seen {
		verbs = append(verbs, verb)
	}
	sort.Strings(verbs)

	for _, verb := range verbs {
		t.Run(verb, func(t *testing.T) {
			want := liveRowWants[verb]
			descs := verbDescriptions(t, surface, verb)
			if len(descs) != 2 {
				t.Fatalf("%s description found in %d of manifest and surface.json", verb, len(descs))
			}
			for source, desc := range descs {
				if n := apitest.LiveRowSequenceCount(desc); n != want.sequences {
					t.Errorf("%s: %s description states the live-row sequence %d times; want %d",
						source, verb, n, want.sequences)
				}
				if n := apitest.LiveRowPointerCount(desc); n != want.pointers {
					t.Errorf("%s: %s description carries the live-row pointer %d times; want %d",
						source, verb, n, want.pointers)
				}
				if want.desc != nil {
					apitest.AssertAgentTextCase(t, source+": "+verb+" description", desc, want.desc())
				}
			}
		})
	}
}
