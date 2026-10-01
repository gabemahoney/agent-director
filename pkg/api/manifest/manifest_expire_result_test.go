package manifest_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestExpireResultFields pins expire's result fields (SR-12.4) on the manifest
// and in surface.json: exactly count, ids, kept, kept_ids, never null, and no error names.
func TestExpireResultFields(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	want := []struct{ name, typ string }{
		{"count", "int"},
		{"ids", "[]string"},
		{"kept", "int"},
		{"kept_ids", "[]string"},
	}

	v, ok := manifest.Lookup("expire")
	if !ok {
		t.Fatal("expire not in manifest")
	}
	names := map[string][]string{}
	errorNames := map[string][]string{"manifest": v.ErrorNames}
	for _, f := range v.ResultFields {
		names["manifest"] = append(names["manifest"], f.Name)
	}
	for _, sv := range surface.Verbs {
		if sv.Name != "expire" {
			continue
		}
		errorNames["surface.json"] = sv.ErrorNames
		for _, f := range sv.ResultFields {
			names["surface.json"] = append(names["surface.json"], f.Name)
		}
	}

	var wantNames []string
	for _, w := range want {
		wantNames = append(wantNames, w.name)
	}
	for _, source := range []string{"manifest", "surface.json"} {
		if got := names[source]; !reflect.DeepEqual(got, wantNames) {
			t.Errorf("%s: expire result fields = %v; want %v", source, got, wantNames)
		}
		if got, present := errorNames[source]; !present || len(got) != 0 {
			t.Errorf("%s: expire error names = %v (present %v); want an empty list", source, got, present)
		}
	}

	for _, w := range want {
		t.Run(w.name, func(t *testing.T) {
			sources := resultFieldSources(t, surface, "expire", w.name)
			for _, source := range []string{"manifest", "surface.json"} {
				f, ok := sources[source]
				if !ok {
					t.Errorf("%s: expire has no %q result field", source, w.name)
					continue
				}
				if f.typ != w.typ || f.nullable {
					t.Errorf("%s: expire.%s type %q nullable %v; want %q false", source, w.name, f.typ, f.nullable, w.typ)
				}
				if f.desc == "" {
					t.Errorf("%s: expire.%s has an empty description", source, w.name)
				}
			}
		})
	}
}
