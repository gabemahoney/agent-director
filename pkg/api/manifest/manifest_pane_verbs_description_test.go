package manifest_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// paneParamCases maps each pane verb to the parameters whose text a shared
// case pins (SR-18.14); every other parameter gets the agent-text check only.
var paneParamCases = map[apitest.PaneVerb]map[string]apitest.DescCase{
	apitest.PaneReadPane: {},
	apitest.PaneSendKeys: {"allow_pending": apitest.DescAllowPending(apitest.AllowPendingFlag)},
	apitest.PanePause:    {},
}

// paneVerbTexts is one source's copy of a pane verb's manifest texts, its
// parameters in declared order.
type paneVerbTexts struct {
	source      string
	description string
	errorNames  []string
	paramNames  []string
	paramDescs  []string
}

// TestReadPaneManifestTexts pins SR-18.1's tmux error classes on read-pane's
// Description and the agent-text rules on its parameter texts.
func TestReadPaneManifestTexts(t *testing.T) { checkPaneManifestTexts(t, apitest.PaneReadPane) }

// TestSendKeysManifestTexts pins SR-18.1's tmux error classes on send-keys'
// Description, SR-18.14 on allow_pending and the agent-text rules on its params.
func TestSendKeysManifestTexts(t *testing.T) { checkPaneManifestTexts(t, apitest.PaneSendKeys) }

// TestPauseManifestTexts pins SR-18.1's tmux error classes on pause's
// Description and the agent-text rules on its parameter texts.
func TestPauseManifestTexts(t *testing.T) { checkPaneManifestTexts(t, apitest.PanePause) }

// checkPaneManifestTexts asserts v's Description and parameter texts, in the
// manifest and in surface.json, through the shared description helper.
func checkPaneManifestTexts(t *testing.T, v apitest.PaneVerb) {
	t.Helper()
	cases, ok := paneParamCases[v]
	if !ok {
		t.Fatalf("no paneParamCases entry for %s", v)
	}
	for _, src := range paneVerbSources(t, string(v)) {
		t.Run(src.source+" description", func(t *testing.T) {
			apitest.AssertAgentTextCase(t, src.source+": "+string(v)+" description",
				src.description, apitest.DescPaneManifest(v, src.errorNames))
		})
		for name := range cases {
			if !slices.Contains(src.paramNames, name) {
				t.Errorf("%s: %s has no param %q", src.source, v, name)
			}
		}
		for i, name := range src.paramNames {
			desc := src.paramDescs[i]
			t.Run(src.source+" param "+name, func(t *testing.T) {
				what := src.source + ": " + string(v) + " param " + name
				if c, ok := cases[name]; ok {
					apitest.AssertAgentTextCase(t, what, desc, c)
					return
				}
				apitest.AssertAgentText(t, what, desc)
			})
		}
	}
}

// paneVerbSources returns verb's texts from the manifest and from
// surface.json, failing the test when either lacks the verb.
func paneVerbSources(t *testing.T, verb string) []paneVerbTexts {
	t.Helper()
	v, ok := manifest.Lookup(verb)
	if !ok {
		t.Fatalf("verb %q not in manifest", verb)
	}
	fromManifest := paneVerbTexts{source: "manifest", description: v.Description, errorNames: v.ErrorNames}
	for _, p := range v.Params {
		fromManifest.paramNames = append(fromManifest.paramNames, p.Name)
		fromManifest.paramDescs = append(fromManifest.paramDescs, p.Description)
	}
	_, surface := readSurfaceJSON(t)
	for _, sv := range surface.Verbs {
		if sv.Name != verb {
			continue
		}
		fromSurface := paneVerbTexts{source: "surface.json", description: sv.Description, errorNames: sv.ErrorNames}
		for _, p := range sv.Params {
			fromSurface.paramNames = append(fromSurface.paramNames, p.Name)
			fromSurface.paramDescs = append(fromSurface.paramDescs, p.Description)
		}
		return []paneVerbTexts{fromManifest, fromSurface}
	}
	t.Fatalf("verb %q not in surface.json", verb)
	return nil
}
