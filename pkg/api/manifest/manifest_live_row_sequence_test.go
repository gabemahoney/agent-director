package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// liveRowVerbs are the verbs whose descriptions state SR-18.6's live-row
// sequence.
var liveRowVerbs = []string{"kill", "find-missing", "spawn"}

// TestLiveRowSequenceInKillFindMissingSpawn pins SR-18.6 (AC-DOC-05): the kill,
// find-missing and spawn descriptions each state the live-row sequence once.
func TestLiveRowSequenceInKillFindMissingSpawn(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, verb := range liveRowVerbs {
		t.Run(verb, func(t *testing.T) {
			descs := verbDescriptions(t, surface, verb)
			if len(descs) != 2 {
				t.Fatalf("%s description found in %d of manifest and surface.json", verb, len(descs))
			}
			for source, desc := range descs {
				apitest.AssertAgentTextCase(t, source+": "+verb+" description", desc, apitest.DescLiveRowSequence())
				if n := len(apitest.LiveRowSequenceSpans(desc)); n != 1 {
					t.Errorf("%s: %s description states the live-row sequence %d times; want 1", source, verb, n)
				}
			}
		})
	}
}

// TestLiveRowSequenceAgreesAcrossVerbs pins that the three descriptions state the
// same sequence (grace default, find-missing runs, wait, final steps) word for word.
func TestLiveRowSequenceAgreesAcrossVerbs(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, source := range []string{"manifest", "surface.json"} {
		var want, wantVerb string
		for _, verb := range liveRowVerbs {
			spans := apitest.LiveRowSequenceSpans(verbDescriptions(t, surface, verb)[source])
			if len(spans) == 0 {
				t.Errorf("%s: %s description states no live-row sequence", source, verb)
				continue
			}
			if want == "" {
				want, wantVerb = spans[0], verb
			}
			for _, got := range spans {
				if got != want {
					t.Errorf("%s: %s description's live-row sequence differs from %s's:\n got %q\nwant %q",
						source, verb, wantVerb, got, want)
				}
			}
		}
	}
}
