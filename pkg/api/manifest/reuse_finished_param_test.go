package manifest_test

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// paramShape is one verb param's name, type, required flag and description.
type paramShape struct {
	name, typ, desc string
	required        bool
}

// paramShapesBySource returns verb's params from the manifest and from the
// committed surface.json, keyed by source ("manifest", "surface.json").
func paramShapesBySource(t *testing.T, verb string) map[string][]paramShape {
	t.Helper()
	v, ok := manifest.Lookup(verb)
	if !ok {
		t.Fatalf("%s not in manifest", verb)
	}
	out := map[string][]paramShape{"manifest": {}, "surface.json": {}}
	for _, p := range v.Params {
		out["manifest"] = append(out["manifest"], paramShape{p.Name, p.Type, p.Description, p.Required})
	}
	raw, _ := readSurfaceJSON(t)
	var doc struct {
		Verbs []struct {
			Name   string `json:"name"`
			Params []struct {
				Name        string `json:"name"`
				Type        string `json:"type"`
				Description string `json:"description"`
				Required    bool   `json:"required"`
			} `json:"params"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal surface.json params: %v", err)
	}
	for _, sv := range doc.Verbs {
		if sv.Name != verb {
			continue
		}
		for _, p := range sv.Params {
			out["surface.json"] = append(out["surface.json"], paramShape{p.Name, p.Type, p.Description, p.Required})
		}
	}
	return out
}

// TestSpawnHasReuseFinishedParam pins spawn's reuse-finished entry (SR-10.1,
// AC-REUSE-13): exactly one, bool, optional, described; the text is Task 4's.
func TestSpawnHasReuseFinishedParam(t *testing.T) {
	for source, params := range paramShapesBySource(t, "spawn") {
		var found []paramShape
		for _, p := range params {
			if p.name == "reuse-finished" {
				found = append(found, p)
			}
		}
		if len(found) != 1 {
			t.Errorf("%s: spawn has %d reuse-finished params; want exactly 1", source, len(found))
			continue
		}
		p := found[0]
		if p.typ != "bool" || p.required || p.desc == "" {
			t.Errorf("%s: spawn reuse-finished type %q required %v description %q; want bool, optional, non-empty",
				source, p.typ, p.required, p.desc)
		}
	}
}

// TestMakeTemplateHasNoReuseParam pins that make-template takes no reuse
// parameter under either spelling (a per-call opt-in no template records).
func TestMakeTemplateHasNoReuseParam(t *testing.T) {
	for source, params := range paramShapesBySource(t, "make-template") {
		for _, p := range params {
			if p.name == "reuse-finished" || p.name == "reuse_finished" {
				t.Errorf("%s: make-template has param %q; want no reuse param", source, p.name)
			}
		}
	}
}
