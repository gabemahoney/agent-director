package manifest_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// notProofSite is one manifest text that describes missing (SR-18.2): a verb's
// Description (field empty) or one of its result fields; short marks a site
// that carries the short form (decision-0930e) instead of the full sentence.
type notProofSite struct {
	verb, field string
	short       bool
}

// notProofSites lists SR-18.2's manifest sites, plus find-missing's ids field,
// which carries the full sentence.
var notProofSites = []notProofSite{
	{verb: "status", field: "state"},
	{verb: "get", field: "state"},
	{verb: "list", field: "spawns"},
	{verb: "kill", short: true},
	{verb: "resume", short: true},
	{verb: "pause", short: true},
	{verb: "find-missing"},
	{verb: "find-missing", field: "ids"},
	{verb: "expire", short: true},
	{verb: "delete", short: true},
}

// notProofTexts returns the site's text keyed by source ("manifest",
// "surface.json"), failing the test unless both sources carry it.
func notProofTexts(t *testing.T, surface surfaceDoc, s notProofSite) map[string]string {
	t.Helper()
	texts := map[string]string{}
	if s.field == "" {
		texts = verbDescriptions(t, surface, s.verb)
	} else {
		for source, f := range resultFieldSources(t, surface, s.verb, s.field) {
			texts[source] = f.desc
		}
	}
	if len(texts) != 2 {
		t.Fatalf("%s %s found in %d of manifest and surface.json", s.verb, s.field, len(texts))
	}
	return texts
}

// TestManifestMissingNotProof pins SR-18.2 (AC-DOC-02) on every manifest site in
// both surfaces: the full sentence or, at the short sites, the short form only.
func TestManifestMissingNotProof(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, s := range notProofSites {
		name := strings.TrimSpace(s.verb + " " + s.field)
		what := "description"
		if s.field != "" {
			what = "result field " + s.field
		}
		c := apitest.DescMissingNotProof()
		if s.short {
			c = apitest.DescMissingNotProofShort()
		}
		t.Run(name, func(t *testing.T) {
			for source, text := range notProofTexts(t, surface, s) {
				apitest.AssertAgentTextCase(t, source+": "+s.verb+" "+what, text, c)
			}
		})
	}
}

// TestGoDocMissingNotProof pins SR-18.2 on the Go docs of the find-missing,
// expire, kill and resume entry points, line wrapping rejoined.
func TestGoDocMissingNotProof(t *testing.T) {
	for _, method := range []string{"FindMissing", "Expire", "Kill", "Resume"} {
		t.Run(method, func(t *testing.T) {
			doc := strings.Join(strings.Fields(clientMethodDoc(t, method)), " ")
			apitest.AssertAgentTextCase(t, "Go doc of (*Client)."+method, doc, apitest.DescMissingNotProof())
		})
	}
}
