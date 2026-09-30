package manifest_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// verbDescriptions returns verb's Description keyed by source ("manifest",
// "surface.json"); a source lacking the verb has no key.
func verbDescriptions(t *testing.T, surface surfaceDoc, verb string) map[string]string {
	t.Helper()
	descs := map[string]string{}
	if v, ok := manifest.Lookup(verb); ok {
		descs["manifest"] = v.Description
	}
	for _, sv := range surface.Verbs {
		if sv.Name == verb {
			descs["surface.json"] = sv.Description
		}
	}
	return descs
}

// TestTmuxSocketResultField pins get's tmux_socket (SR-3.3, SR-16.1,
// AC-LKP-22) on the manifest and in surface.json; status and list lack it.
func TestTmuxSocketResultField(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	sources := []string{"manifest", "surface.json"}

	t.Run("get", func(t *testing.T) {
		fields := resultFieldSources(t, surface, "get", "tmux_socket")
		descs := verbDescriptions(t, surface, "get")
		for _, source := range sources {
			f, ok := fields[source]
			if !ok {
				t.Errorf("%s: get has no tmux_socket result field", source)
			} else {
				if f.typ != "string?" || !f.nullable || f.allowEmpty {
					t.Errorf("%s: get.tmux_socket type %q nullable %v allow_empty %v; want \"string?\" true false",
						source, f.typ, f.nullable, f.allowEmpty)
				}
				if f.enum != nil {
					t.Errorf("%s: get.tmux_socket allowed values %v; want none", source, f.enum)
				}
				apitest.AssertAgentTextCase(t, source+": get result field tmux_socket", f.desc,
					apitest.DescTmuxSocketField())
			}
			if !strings.Contains(descs[source], "tmux socket") {
				t.Errorf("%s: get description does not name the tmux socket; got %q", source, descs[source])
			}
		}
	})

	for _, verb := range []string{"status", "list"} {
		t.Run(verb+" lacks it", func(t *testing.T) {
			fields := resultFieldSources(t, surface, verb, "tmux_socket")
			for _, source := range sources {
				if _, ok := fields[source]; ok {
					t.Errorf("%s: %s has a tmux_socket result field; want none", source, verb)
				}
			}
		})
	}

	t.Run("list spawns text omits it", func(t *testing.T) {
		fields := resultFieldSources(t, surface, "list", "spawns")
		for _, source := range sources {
			f, ok := fields[source]
			if !ok {
				t.Errorf("%s: list has no spawns result field", source)
				continue
			}
			if strings.Contains(f.desc, "tmux_socket") {
				t.Errorf("%s: list spawns text names tmux_socket; got %q", source, f.desc)
			}
		}
	})
}
