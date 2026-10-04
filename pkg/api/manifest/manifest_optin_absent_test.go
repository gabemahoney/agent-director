package manifest_test

// manifest_optin_absent_test.go pins SR-6.8 and b.vqr on the agent-facing
// manifest and its committed surface.json: no delete verb, and no string naming
// kill's former finished-row opt-in, the off-PATH admin binary
// agent-director-admin or its kill-finished verb, while kill keeps only its
// claude_instance_id parameter and its kill_sent result.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// manifestStrings returns every string manifest.Verbs carries, keyed by its
// path (verb, field, index), walking every struct field, slice and map so a
// text field added later is covered too.
func manifestStrings() map[string]string {
	out := map[string]string{}
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			out[path] = v.String()
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(path+"."+v.Type().Field(i).Name, v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(fmt.Sprintf("%s[%v]", path, k), v.MapIndex(k))
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(path, v.Elem())
			}
		}
	}
	for _, v := range manifest.Verbs {
		walk(v.Name, reflect.ValueOf(v))
	}
	return out
}

// TestManifestHasNoOperatorActions: the manifest has no delete verb and no
// string (verb, parameter or result-field name, description, allowed value)
// naming an operator action, while kill has only claude_instance_id and still
// returns kill_sent.
func TestManifestHasNoOperatorActions(t *testing.T) {
	strs := manifestStrings()
	if len(strs) == 0 {
		t.Fatal("manifest walk found no strings (precondition)")
	}
	for path, s := range strs {
		if m := apitest.OperatorActionNames.FindString(s); m != "" {
			t.Errorf("SR-6.8: manifest %s names %q; nothing shown to agents may: %q", path, m, s)
		}
	}
	if _, ok := manifest.Lookup("delete"); ok {
		t.Error("manifest has a delete verb; delete is an agent-director-admin verb only")
	}

	kill, ok := manifest.Lookup("kill")
	if !ok {
		t.Fatal("kill not in manifest; the checks above would pass vacuously")
	}
	var params []string
	for _, p := range kill.Params {
		params = append(params, p.Name)
	}
	if !reflect.DeepEqual(params, []string{"claude_instance_id"}) {
		t.Errorf("SR-6.8: kill params = %v; want only claude_instance_id (the opt-in is not a manifest parameter)", params)
	}
	if !hasField(kill.ResultFields, "kill_sent") {
		t.Errorf("kill result fields lack kill_sent; the SR-6.8 absence check above would pass vacuously")
	}
}

// TestSurfaceJSONHasNoOperatorActions is the golden-side twin: the committed
// surface.json has no delete verb and names no operator action anywhere, while
// kill still returns kill_sent.
func TestSurfaceJSONHasNoOperatorActions(t *testing.T) {
	raw, surface := readSurfaceJSON(t)
	for _, loc := range apitest.OperatorActionNames.FindAllIndex(raw, -1) {
		t.Errorf("SR-6.8: surface.json names %q at byte offset %d", raw[loc[0]:loc[1]], loc[0])
	}
	var killSent bool
	for _, v := range surface.Verbs {
		if v.Name == "delete" {
			t.Error("surface.json has a delete verb; delete is an agent-director-admin verb only")
		}
		if v.Name != "kill" {
			continue
		}
		for _, f := range v.ResultFields {
			killSent = killSent || f.Name == "kill_sent"
		}
	}
	if !killSent {
		t.Error("surface.json kill lacks result field kill_sent; the checks above would pass vacuously")
	}
}

// hasField reports whether fields holds one named name.
func hasField(fields []manifest.FieldDef, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}
