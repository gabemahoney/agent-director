package manifest_test

// param_names_test.go pins b.c4u's and b.7or's manifest side: every param has
// one name, with no "-" (the name MCP tools/list, surface.json and the
// references publish; CLI flags stay dashed), every verb Description, which
// every surface shows, names the reuse opt-in in one spelling only, and a
// param text naming another param gives its manifest name with the CLI flag.
// b.ro3 adds that no verb or param text names a param by its CLI flag alone;
// b.pti that a param spawn and make-template share has one Type on both.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestManifestParamNamesHaveNoDash: no param of any verb keeps a "-", on the
// manifest and in surface.json (b.c4u, b.7or).
func TestManifestParamNamesHaveNoDash(t *testing.T) {
	for _, v := range manifest.Verbs {
		for source, params := range paramShapesBySource(t, v.Name) {
			for _, p := range params {
				if strings.Contains(p.name, "-") {
					t.Errorf("%s: %s param %q has a dash; want underscores (CLI flags stay dashed)", source, v.Name, p.name)
				}
			}
		}
	}
}

// TestManifestSpawnTemplateParamsOneType: every param spawn and make-template
// both take has the same Type on both, as a template supplies spawn's values
// (b.pti: spawn's extra_env was "[]string (K=V)", make-template's a map).
func TestManifestSpawnTemplateParamsOneType(t *testing.T) {
	spawn, _ := manifest.Lookup("spawn")
	tpl, _ := manifest.Lookup("make-template")
	spawnTypes := map[string]string{}
	for _, p := range spawn.Params {
		spawnTypes[p.Name] = p.Type
	}
	shared := 0
	for _, p := range tpl.Params {
		typ, ok := spawnTypes[p.Name]
		if !ok {
			continue
		}
		shared++
		if typ != p.Type {
			t.Errorf("%s: spawn Type %q, make-template Type %q; want one Type", p.Name, typ, p.Type)
		}
	}
	if shared == 0 {
		t.Fatal("spawn and make-template share no param")
	}
}

// TestManifestParamOlderServeSentences: the spawn and list param texts b.c4u
// renamed or b.7or made MCP decode state what a serve process from before the
// change does with them, on the manifest and in surface.json (reuse_finished's
// sentence is in apitest.DescReuseFinishedParam).
func TestManifestParamOlderServeSentences(t *testing.T) {
	for _, tc := range []struct{ verb, param string }{
		{"spawn", "relay_mode"}, {"spawn", "extra_env"}, {"spawn", "no_pre_trust"},
		{"spawn", "tmux_session_name"}, {"list", "tmux_session_name"},
	} {
		t.Run(tc.verb+"/"+tc.param, func(t *testing.T) {
			c := apitest.DescOlderServeParam(tc.verb, tc.param)
			for source, params := range paramShapesBySource(t, tc.verb) {
				found := false
				for _, p := range params {
					if p.name == tc.param {
						found = true
						apitest.AssertAgentTextCase(t, source+": "+tc.verb+" param "+tc.param, p.desc, c)
					}
				}
				if !found {
					t.Errorf("%s: %s has no param %q", source, tc.verb, tc.param)
				}
			}
		})
	}
}

// TestManifestParamMakeTemplatePerCallSpelling: make-template's relay_mode and
// extra_env texts name spawn's per-call param by its manifest name, with the
// CLI flag beside it, so an MCP caller is not told a CLI-only spelling (b.c4u),
// on the manifest and in surface.json.
func TestManifestParamMakeTemplatePerCallSpelling(t *testing.T) {
	for param, want := range map[string]string{
		"relay_mode": "Per-call relay_mode (--relay-mode on the CLI) overrides.",
		"extra_env":  "Per-call extra_env (--extra-env on the CLI) merges by key; per-call wins on collision.",
	} {
		c := apitest.DescCase{Name: "make-template " + param + " per-call spelling", Require: []string{want}}
		for source, params := range paramShapesBySource(t, "make-template") {
			found := false
			for _, p := range params {
				if p.name == param {
					found = true
					apitest.AssertAgentTextCase(t, source+": make-template param "+param, p.desc, c)
				}
			}
			if !found {
				t.Errorf("%s: make-template has no param %q", source, param)
			}
		}
	}
}

// TestManifestParamReuseOptInOneSpelling: no verb Description names the reuse
// opt-in in any spelling other than "reuse_finished (--reuse-finished on the
// CLI)" (make-template's list of refused param names aside). The advice that
// uses it (spawn, kill's live-row step 6, delete) is pinned word for word by
// its Desc* cases and advice-follow tests.
func TestManifestParamReuseOptInOneSpelling(t *testing.T) {
	const spelling = "reuse_finished (--reuse-finished on the CLI)"
	for _, v := range manifest.Verbs {
		for source, desc := range verbDescriptionsBoth(t, v.Name) {
			rest := strings.ReplaceAll(desc, spelling, "")
			others := []string{"reuse-finished", "ReuseFinished"} // the first covers --reuse-finished
			if v.Name != "make-template" {
				others = append(others, "reuse_finished")
			}
			for _, other := range others {
				if strings.Contains(rest, other) {
					t.Errorf("%s: %s description names the reuse opt-in as %q outside %q:\n%s", source, v.Name, other, spelling, desc)
				}
			}
		}
	}
}

// TestManifestTextsNameParamsNotFlags: no verb or param Description names a
// param by its CLI flag alone; a flag that is a param's dashed name appears
// only as "<param> (--<flag> on the CLI)" (b.ro3), on the manifest and in
// surface.json. Other programs' flags (claude's --settings) are not params.
func TestManifestTextsNameParamsNotFlags(t *testing.T) {
	params := map[string]bool{}
	for _, v := range manifest.Verbs {
		for _, p := range v.Params {
			params[p.Name] = true
		}
	}
	flag := regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	for _, v := range manifest.Verbs {
		texts := verbDescriptionsBoth(t, v.Name)
		for source, ps := range paramShapesBySource(t, v.Name) {
			for _, p := range ps {
				texts[source+" param "+p.name] = p.desc
			}
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
					t.Errorf("%s: %s names param %s as %q alone; want %q:\n%s", where, v.Name, name, f, aside, text)
				}
			}
		}
	}
}
