package manifest_test

// param_names_test.go pins b.c4u's and b.7or's manifest side: every param has
// one name, with no "-" (the name MCP tools/list, surface.json and the
// references publish; CLI flags stay dashed), every verb Description, which
// every surface shows, names the reuse opt-in in one spelling only, and a
// param text naming another param gives its manifest name with the CLI flag.
// b.ro3 adds that no verb or param text names a param by its CLI flag alone
// (b.ia3: nor any result-field text); b.pti that a param spawn and make-template share has one Type on both.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestManifestParamNamesHaveNoDash: no param of any verb keeps a "-" (b.c4u, b.7or).
func TestManifestParamNamesHaveNoDash(t *testing.T) {
	for _, v := range manifest.Verbs {
		for _, p := range v.Params {
			if strings.Contains(p.Name, "-") {
				t.Errorf("%s param %q has a dash; want underscores (CLI flags stay dashed)", v.Name, p.Name)
			}
		}
	}
}

// TestManifestSpawnTemplateParamsOneType: every param spawn and make-template
// both take has the same Type on both, as a template supplies spawn's values
// (b.pti: spawn's extra_env was "[]string (K=V)", make-template's a map).
func TestManifestSpawnTemplateParamsOneType(t *testing.T) {
	spawnTypes := map[string]string{}
	for _, p := range verbOf(t, "spawn").Params {
		spawnTypes[p.Name] = p.Type
	}
	shared := 0
	for _, p := range verbOf(t, "make-template").Params {
		if typ, ok := spawnTypes[p.Name]; ok {
			shared++
			if typ != p.Type {
				t.Errorf("%s: spawn Type %q, make-template Type %q; want one Type", p.Name, typ, p.Type)
			}
		}
	}
	if shared == 0 {
		t.Fatal("spawn and make-template share no param")
	}
}

// TestManifestParamOlderServeSentences: the spawn and list param texts b.c4u
// renamed or b.7or made MCP decode state what a serve process from before the
// change does with them (reuse_finished's sentence is in
// apitest.DescReuseFinishedParam).
func TestManifestParamOlderServeSentences(t *testing.T) {
	for _, tc := range []struct{ verb, param string }{
		{"spawn", "relay_mode"}, {"spawn", "extra_env"}, {"spawn", "no_pre_trust"},
		{"spawn", "tmux_session_name"}, {"list", "tmux_session_name"},
	} {
		apitest.AssertAgentTextCase(t, tc.verb+" param "+tc.param, siteText(t, tc.verb, tc.param, ""),
			apitest.DescOlderServeParam(tc.verb, tc.param))
	}
}

// TestManifestParamReuseOptInOneSpelling: no verb Description names the reuse
// opt-in in any spelling other than "reuse_finished (--reuse-finished on the
// CLI)" (make-template's list of refused param names aside). The advice that
// uses it (spawn, kill's live-row step 6) is pinned word for word by its Desc*
// cases and advice-follow tests.
func TestManifestParamReuseOptInOneSpelling(t *testing.T) {
	const spelling = "reuse_finished (--reuse-finished on the CLI)"
	for _, v := range manifest.Verbs {
		rest := strings.ReplaceAll(v.Description, spelling, "")
		others := []string{"reuse-finished", "ReuseFinished"} // the first covers --reuse-finished
		if v.Name != "make-template" {
			others = append(others, "reuse_finished")
		}
		for _, other := range others {
			if strings.Contains(rest, other) {
				t.Errorf("%s description names the reuse opt-in as %q outside %q:\n%s", v.Name, other, spelling, v.Description)
			}
		}
	}
}

// TestManifestTextsNameParamsNotFlags: no verb, param or result-field
// Description names a param by its CLI flag alone; a flag that is a param's
// dashed name appears only as "<param> (--<flag> on the CLI)" (b.ro3, b.ia3).
// Other programs' flags (claude's --settings) are not params.
func TestManifestTextsNameParamsNotFlags(t *testing.T) {
	params := map[string]bool{}
	for _, v := range manifest.Verbs {
		for _, p := range v.Params {
			params[p.Name] = true
		}
	}
	flag := regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	for _, v := range manifest.Verbs {
		texts := map[string]string{"description": v.Description}
		for _, p := range v.Params {
			texts["param "+p.Name] = p.Description
		}
		for _, f := range v.ResultFields {
			texts["result field "+f.Name] = f.Description
		}
		for where, text := range texts {
			for _, loc := range flag.FindAllStringIndex(text, -1) {
				f := text[loc[0]:loc[1]]
				name := strings.ReplaceAll(f[2:], "-", "_")
				if !params[name] {
					continue
				}
				aside := name + " (" + f + " on the CLI)"
				if start := loc[0] - len(name+" ("); start < 0 || !strings.HasPrefix(text[start:], aside) {
					t.Errorf("%s %s names param %s as %q alone; want %q:\n%s", v.Name, where, name, f, aside, text)
				}
			}
		}
	}
}
