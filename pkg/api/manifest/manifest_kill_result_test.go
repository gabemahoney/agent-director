package manifest_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestKillResultFieldKillSent pins SR-6.6 on the manifest and in surface.json:
// kill's only result field is a non-null boolean kill_sent with agent-safe text.
func TestKillResultFieldKillSent(t *testing.T) {
	_, surface := readSurfaceJSON(t)

	names := map[string][]string{"manifest": {}, "surface.json": {}}
	v, ok := manifest.Lookup("kill")
	if !ok {
		t.Fatal("kill not in manifest")
	}
	for _, f := range v.ResultFields {
		names["manifest"] = append(names["manifest"], f.Name)
	}
	for _, sv := range surface.Verbs {
		if sv.Name == "kill" {
			for _, f := range sv.ResultFields {
				names["surface.json"] = append(names["surface.json"], f.Name)
			}
		}
	}

	fields := resultFieldSources(t, surface, "kill", "kill_sent")
	for _, source := range []string{"manifest", "surface.json"} {
		if got, want := names[source], []string{"kill_sent"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: kill result fields = %v; want %v", source, got, want)
		}
		f, ok := fields[source]
		if !ok {
			continue
		}
		if f.typ != "bool" || f.nullable || f.allowEmpty || f.enum != nil {
			t.Errorf("%s: kill.kill_sent type %q nullable %v allow_empty %v allowed values %v; want \"bool\" false false none",
				source, f.typ, f.nullable, f.allowEmpty, f.enum)
		}
		if f.desc == "" {
			t.Errorf("%s: kill.kill_sent has an empty description", source)
		}
		apitest.AssertAgentText(t, source+": kill result field kill_sent", f.desc)
	}
}
